package collector

import (
	"context"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// GPU probing can be expensive (subprocesses, driver queries) and changes
// slowly compared with CPU/memory, so it runs on its own cadence.
func (c *Collector) sampleGPUs(ctx context.Context) {
	c.refreshGPUs()

	ticker := time.NewTicker(c.gpuEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refreshGPUs()
		}
	}
}

func (c *Collector) refreshGPUs() {
	gpus, err := readGPUs()
	if err != nil {
		return // keep the previous reading on transient driver errors
	}
	c.gpuMu.Lock()
	c.gpuCache = gpus
	c.gpuMu.Unlock()
}

func (c *Collector) fillGPUs(snap *Snapshot) {
	c.gpuMu.RLock()
	snap.GPUs = c.gpuCache
	c.gpuMu.RUnlock()
}

var nvidiaQueryArgs = []string{
	"--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,fan.speed",
	"--format=csv,noheader,nounits",
}

// parseNvidiaSMI parses the CSV emitted by nvidia-smi. Memory values are MiB.
func parseNvidiaSMI(out string) []GPU {
	var gpus []GPU
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 8 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}

		g := GPU{Name: fields[1], Vendor: "NVIDIA"}
		if idx, err := strconv.Atoi(fields[0]); err == nil {
			g.Index = idx
		} else {
			g.Index = len(gpus)
		}
		if g.Name == "" {
			g.Name = "NVIDIA GPU"
		}
		if v, ok := parseMetric(fields[2]); ok {
			g.Utilization = f64ptr(clamp(v, 0, 100))
		}
		if v, ok := parseMetric(fields[3]); ok {
			g.MemoryUsed = u64ptr(uint64(v * 1024 * 1024))
		}
		if v, ok := parseMetric(fields[4]); ok {
			g.MemoryTotal = u64ptr(uint64(v * 1024 * 1024))
		}
		if v, ok := parseMetric(fields[5]); ok {
			g.Temperature = f64ptr(v)
		}
		if v, ok := parseMetric(fields[6]); ok {
			g.Power = f64ptr(v)
		}
		if v, ok := parseMetric(fields[7]); ok {
			g.FanSpeed = f64ptr(v)
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func parseMetric(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.EqualFold(s, "N/A") || strings.EqualFold(s, "[N/A]") ||
		strings.EqualFold(s, "[Not Supported]") {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func parseUint(raw string) (uint64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func f64ptr(v float64) *float64 {
	return &v
}

func u64ptr(v uint64) *uint64 {
	return &v
}

func readSmallFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func gpuVendor(name string) string {
	upper := strings.ToUpper(name)
	switch {
	case strings.Contains(upper, "APPLE"):
		return "Apple"
	case strings.Contains(upper, "NVIDIA") || strings.Contains(upper, "GEFORCE") || strings.Contains(upper, "QUADRO"):
		return "NVIDIA"
	case strings.Contains(upper, "AMD") || strings.Contains(upper, "RADEON"):
		return "AMD"
	case strings.Contains(upper, "INTEL"):
		return "Intel"
	}
	return ""
}
