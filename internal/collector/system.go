package collector

import (
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

type hostStatic struct {
	hostname        string
	osName          string
	platform        string
	platformVersion string
	kernelVersion   string
	kernelArch      string
	cpuModel        string
	cpuMhz          float64
	logicalCores    int
	physicalCores   int
	bootTime        int64
}

func readHostStatic() hostStatic {
	hs := hostStatic{osName: runtime.GOOS, kernelArch: runtime.GOARCH}
	if name, err := os.Hostname(); err == nil {
		hs.hostname = name
	}

	if info, err := host.Info(); err == nil {
		if info.Hostname != "" {
			hs.hostname = info.Hostname
		}
		hs.platform = info.Platform
		hs.platformVersion = info.PlatformVersion
		hs.kernelVersion = info.KernelVersion
		if info.KernelArch != "" {
			hs.kernelArch = info.KernelArch
		}
		hs.bootTime = int64(info.BootTime)
	}
	if hs.platform == "" {
		hs.platform = runtime.GOOS
	}
	if runtime.GOOS == "darwin" && strings.EqualFold(hs.platform, "darwin") {
		hs.platform = "macOS"
	}

	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		hs.cpuModel = strings.TrimSpace(infos[0].ModelName)
		hs.cpuMhz = infos[0].Mhz
	}
	if n, err := cpu.Counts(true); err == nil {
		hs.logicalCores = n
	}
	if n, err := cpu.Counts(false); err == nil {
		hs.physicalCores = n
	}
	if hs.logicalCores == 0 {
		hs.logicalCores = runtime.NumCPU()
	}
	if hs.physicalCores == 0 {
		hs.physicalCores = hs.logicalCores
	}
	return hs
}

func (h hostStatic) snapshot(now time.Time) Host {
	var uptime int64
	if h.bootTime > 0 {
		uptime = now.Unix() - h.bootTime
		if uptime < 0 {
			uptime = 0
		}
	}
	return Host{
		Hostname:        h.hostname,
		OS:              h.osName,
		Platform:        h.platform,
		PlatformVersion: h.platformVersion,
		KernelVersion:   h.kernelVersion,
		KernelArch:      h.kernelArch,
		CPUModel:        h.cpuModel,
		CPUMhz:          round(h.cpuMhz, 0),
		CPUCores:        h.logicalCores,
		CPUPhysical:     h.physicalCores,
		BootTime:        h.bootTime,
		Uptime:          uptime,
	}
}

func (c *Collector) readCPU() CPU {
	percents, err := cpu.Percent(0, true)
	if err != nil || len(percents) == 0 {
		return c.last.cpu
	}

	cores := make([]float64, len(percents))
	total := 0.0
	for i, p := range percents {
		p = clamp(p, 0, 100)
		total += p
		cores[i] = round(p, 1)
	}

	out := CPU{
		Percent: round(total/float64(len(percents)), 1),
		PerCore: cores,
		Cores:   len(percents),
	}
	if c.static.logicalCores > 0 {
		out.Cores = c.static.logicalCores
	}
	c.addCPUBreakdown(&out)
	c.last.cpu = out
	return out
}

// addCPUBreakdown derives user/system/idle/iowait shares from cumulative CPU
// times, using the delta against the previous sample.
func (c *Collector) addCPUBreakdown(out *CPU) {
	stats, err := cpu.Times(false)
	if err != nil || len(stats) == 0 {
		return
	}
	cur := stats[0]
	if c.prev.hasCPUTimes {
		total := timesTotal(cur) - timesTotal(c.prev.cpuTimes)
		if total > 0 {
			prev := c.prev.cpuTimes
			out.User = round(percentOf((cur.User+cur.Nice)-(prev.User+prev.Nice), total), 1)
			out.System = round(percentOf((cur.System+cur.Irq+cur.Softirq)-(prev.System+prev.Irq+prev.Softirq), total), 1)
			out.Iowait = round(percentOf(cur.Iowait-prev.Iowait, total), 1)
			out.Idle = round(percentOf(cur.Idle-prev.Idle, total), 1)
			out.Steal = round(percentOf(cur.Steal-prev.Steal, total), 1)
		}
	}
	c.prev.cpuTimes = cur
	c.prev.hasCPUTimes = true
}

func timesTotal(t cpu.TimesStat) float64 {
	// Guest time is already accounted for in User on Linux, so it is omitted
	// here to avoid double counting.
	return t.User + t.Nice + t.System + t.Idle + t.Iowait + t.Irq + t.Softirq + t.Steal
}

func (c *Collector) readMemory() Memory {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return c.last.memory
	}
	out := Memory{
		Total:     vm.Total,
		Used:      vm.Used,
		Available: vm.Available,
		Free:      vm.Free,
		Cached:    vm.Cached,
		Buffers:   vm.Buffers,
		Percent:   round(vm.UsedPercent, 1),
	}
	if out.Available == 0 {
		out.Available = vm.Free
	}
	if sw, err := mem.SwapMemory(); err == nil {
		out.SwapTotal = sw.Total
		out.SwapUsed = sw.Used
		out.SwapPercent = round(sw.UsedPercent, 1)
	}
	c.last.memory = out
	return out
}

func readLoad() *Load {
	avg, err := load.Avg()
	if err != nil {
		return nil // not supported on Windows
	}
	return &Load{
		Load1:  round(avg.Load1, 2),
		Load5:  round(avg.Load5, 2),
		Load15: round(avg.Load15, 2),
	}
}
