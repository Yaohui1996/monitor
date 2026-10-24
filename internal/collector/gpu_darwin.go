//go:build darwin

package collector

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ioregModelRe  = regexp.MustCompile(`"model" = "([^"]*)"`)
	ioregCoresRe  = regexp.MustCompile(`"gpu-core-count" = ([0-9]+)`)
	ioregStatRe   = regexp.MustCompile(`"([^"]+)"=([^,}]+)`)
	ioregStatuses = []string{"Device Utilization %", "Renderer Utilization %"}
)

func readGPUs() ([]GPU, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bin, err := exec.LookPath("ioreg")
	if err != nil {
		bin = "/usr/sbin/ioreg"
	}
	out, err := exec.CommandContext(ctx, bin, "-r", "-d", "1", "-c", "IOAccelerator", "-w", "0").Output()
	if err != nil {
		return nil, err
	}
	return parseIOAccelerator(string(out)), nil
}

// parseIOAccelerator extracts GPUs and their driver statistics from
// `ioreg -c IOAccelerator` output. Apple Silicon and AMD/Intel GPUs expose a
// flat "PerformanceStatistics" dictionary on each accelerator node.
func parseIOAccelerator(out string) []GPU {
	var gpus []GPU
	for _, block := range strings.Split(out, "+-o ") {
		stats := parseIOAcceleratorStats(block)
		if len(stats) == 0 {
			continue
		}

		name := ""
		if m := ioregModelRe.FindStringSubmatch(block); len(m) == 2 {
			name = strings.TrimSpace(m[1])
		}
		if name == "" {
			first := strings.TrimSpace(strings.SplitN(block, "\n", 2)[0])
			if fields := strings.Fields(first); len(fields) > 0 {
				name = fields[0]
			}
		}
		if name == "" {
			continue
		}

		g := GPU{Index: len(gpus), Name: name, Vendor: gpuVendor(name)}
		if m := ioregCoresRe.FindStringSubmatch(block); len(m) == 2 {
			if n, err := strconv.Atoi(m[1]); err == nil {
				g.Cores = n
			}
		}
		for _, key := range ioregStatuses {
			if v, ok := parseMetric(stats[key]); ok {
				g.Utilization = f64ptr(clamp(v, 0, 100))
				break
			}
		}
		if v, ok := parseUint(stats["In use system memory"]); ok {
			g.MemoryUsed = u64ptr(v)
		} else if v, ok := parseUint(stats["vramUsedBytes"]); ok {
			g.MemoryUsed = u64ptr(v)
			if free, ok := parseUint(stats["vramFreeBytes"]); ok && free > 0 {
				g.MemoryTotal = u64ptr(v + free)
			}
		}
		if v, ok := parseUint(stats["vramTotalBytes"]); ok && v > 0 {
			g.MemoryTotal = u64ptr(v)
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func parseIOAcceleratorStats(block string) map[string]string {
	idx := strings.Index(block, `"PerformanceStatistics" = {`)
	if idx < 0 {
		return nil
	}
	line := block[idx:]
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	stats := make(map[string]string)
	for _, m := range ioregStatRe.FindAllStringSubmatch(line, -1) {
		key := strings.TrimSpace(m[1])
		value := strings.Trim(strings.TrimSpace(m[2]), `"`)
		stats[key] = value
	}
	return stats
}
