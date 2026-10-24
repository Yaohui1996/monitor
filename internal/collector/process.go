package collector

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// sampleProcesses refreshes the process ranking on its own cadence so the
// expensive enumeration never delays the main sampling loop.
func (c *Collector) sampleProcesses(ctx context.Context) {
	c.refreshProcesses(time.Now())

	ticker := time.NewTicker(c.procEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.refreshProcesses(now)
		}
	}
}

// fillProcesses attaches the most recently collected ranking. The cached slice
// is immutable, so it is safe to share with published snapshots.
func (c *Collector) fillProcesses(snap *Snapshot) {
	c.procMu.RLock()
	snap.Processes = c.procCache
	snap.ProcessCount = c.procCount
	c.procMu.RUnlock()
}

func (c *Collector) refreshProcesses(now time.Time) {
	procs, err := process.Processes()
	if err != nil || len(procs) == 0 {
		return
	}

	elapsed := 0.0
	if !c.procAt.IsZero() {
		elapsed = now.Sub(c.procAt).Seconds()
	}
	coreLimit := float64(c.static.logicalCores) * 100

	next := make(map[int32]float64, len(procs))
	all := make([]Process, 0, len(procs))
	for _, p := range procs {
		times, err := p.Times()
		if err != nil {
			continue
		}
		cpuSeconds := times.User + times.System
		next[p.Pid] = cpuSeconds

		var cpuPercent float64
		if prev, ok := c.procCPU[p.Pid]; ok && elapsed > 0 && cpuSeconds >= prev {
			cpuPercent = (cpuSeconds - prev) / elapsed * 100
			if coreLimit > 0 {
				cpuPercent = min(cpuPercent, coreLimit)
			}
		}

		name, err := p.Name()
		if err != nil || strings.TrimSpace(name) == "" {
			name = "pid " + strconv.Itoa(int(p.Pid))
		}
		info, err := p.MemoryInfo()
		if err != nil {
			continue
		}
		memPercent, _ := p.MemoryPercent()

		all = append(all, Process{
			PID:        p.Pid,
			Name:       name,
			CPUPercent: round(cpuPercent, 1),
			MemPercent: round(float64(memPercent), 1),
			RSS:        info.RSS,
		})
	}

	c.procCPU = next
	c.procAt = now
	top := pickTop(all, c.topN)

	c.procMu.Lock()
	c.procCache = top
	c.procCount = len(procs)
	c.procMu.Unlock()
}

// pickTop returns the union of the busiest processes by CPU and by memory, so
// the UI can sort by either metric without the backend sending every process.
func pickTop(procs []Process, n int) []Process {
	if n <= 0 || len(procs) == 0 {
		return nil
	}

	byCPU := append([]Process(nil), procs...)
	sort.SliceStable(byCPU, func(i, j int) bool {
		return byCPU[i].CPUPercent > byCPU[j].CPUPercent
	})
	byMem := append([]Process(nil), procs...)
	sort.SliceStable(byMem, func(i, j int) bool {
		return byMem[i].MemPercent > byMem[j].MemPercent
	})

	seen := make(map[int32]struct{}, n*2)
	out := make([]Process, 0, n*2)
	for _, list := range [][]Process{byCPU, byMem} {
		for i := 0; i < len(list) && i < n; i++ {
			p := list[i]
			if _, ok := seen[p.PID]; ok {
				continue
			}
			seen[p.PID] = struct{}{}
			out = append(out, p)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CPUPercent != out[j].CPUPercent {
			return out[i].CPUPercent > out[j].CPUPercent
		}
		return out[i].MemPercent > out[j].MemPercent
	})
	return out
}
