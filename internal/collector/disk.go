package collector

import (
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v4/disk"
)

const maxDisks = 12

func (c *Collector) readDisks() []Disk {
	parts, err := disk.Partitions(false)
	if err != nil || len(parts) == 0 {
		return c.last.disks
	}

	seen := make(map[string]struct{}, len(parts))
	out := make([]Disk, 0, len(parts))
	for _, p := range parts {
		if skipPartition(p) {
			continue
		}
		key := p.Device
		if key == "" {
			key = p.Mountpoint
		}
		if _, ok := seen[key]; ok {
			continue
		}
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil || usage.Total == 0 {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Disk{
			Device:     p.Device,
			Mountpoint: p.Mountpoint,
			Fstype:     p.Fstype,
			Total:      usage.Total,
			Used:       usage.Used,
			Free:       usage.Free,
			Percent:    round(usage.UsedPercent, 1),
		})
	}
	if len(out) == 0 {
		return c.last.disks
	}

	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := isRootMount(out[i].Mountpoint), isRootMount(out[j].Mountpoint)
		if ri != rj {
			return ri
		}
		return out[i].Mountpoint < out[j].Mountpoint
	})
	if len(out) > maxDisks {
		out = out[:maxDisks]
	}
	c.last.disks = out
	return out
}

func skipPartition(p disk.PartitionStat) bool {
	switch p.Fstype {
	case "devfs", "devtmpfs", "tmpfs", "ramfs", "proc", "sysfs", "cgroup", "cgroup2", "autofs", "devicefs":
		return true
	}

	switch runtime.GOOS {
	case "darwin":
		// APFS exposes many synthetic volumes (system snapshot, simulator
		// runtimes, recovery); keep only the root and user-mounted volumes.
		if p.Mountpoint != "/" && !strings.HasPrefix(p.Mountpoint, "/Volumes/") {
			return true
		}
		return p.Mountpoint == "/Volumes/Recovery"
	case "linux":
		return strings.HasPrefix(p.Device, "/dev/loop")
	}
	return false
}

func isRootMount(mountpoint string) bool {
	if mountpoint == "/" {
		return true
	}
	if len(mountpoint) == 2 && mountpoint[1] == ':' {
		return true
	}
	return len(mountpoint) == 3 && mountpoint[1] == ':' && (mountpoint[2] == '\\' || mountpoint[2] == '/')
}

func (c *Collector) readDiskIO(dt float64) DiskIO {
	stats, err := disk.IOCounters()
	if err != nil || len(stats) == 0 {
		return c.last.diskIO
	}

	allowed := allowedDiskDevices()
	var readBytes, writeBytes, readOps, writeOps uint64
	for name, s := range stats {
		if !allowed(name) {
			continue
		}
		readBytes += s.ReadBytes
		writeBytes += s.WriteBytes
		readOps += s.ReadCount
		writeOps += s.WriteCount
	}

	out := DiskIO{
		ReadBytesTotal:  readBytes,
		WriteBytesTotal: writeBytes,
	}
	if c.prev.hasDisk && dt > 0 {
		out.ReadBytesPerSec = round(rate(readBytes, c.prev.diskRead, dt), 0)
		out.WriteBytesPerSec = round(rate(writeBytes, c.prev.diskWrite, dt), 0)
		out.ReadOpsPerSec = round(rate(readOps, c.prev.diskReadOps, dt), 1)
		out.WriteOpsPerSec = round(rate(writeOps, c.prev.diskWriteOps, dt), 1)
	}
	c.prev.diskRead = readBytes
	c.prev.diskWrite = writeBytes
	c.prev.diskReadOps = readOps
	c.prev.diskWriteOps = writeOps
	c.prev.hasDisk = true
	c.last.diskIO = out
	return out
}

// allowedDiskDevices returns a filter that keeps whole physical disks only.
// Linux exposes both whole disks and their partitions in /proc/diskstats, so
// summing every entry would double count; /sys/block lists whole devices only.
// Device-mapper and software RAID layers are excluded because the traffic is
// already accounted for at the physical disk layer.
func allowedDiskDevices() func(string) bool {
	if runtime.GOOS != "linux" {
		return func(name string) bool { return !skipDevice(name) }
	}

	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return func(name string) bool { return !skipDevice(name) }
	}
	keep := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "md") ||
			strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "zram") {
			continue
		}
		keep[name] = struct{}{}
	}
	if len(keep) == 0 {
		return func(name string) bool { return !skipDevice(name) }
	}
	return func(name string) bool {
		_, ok := keep[name]
		return ok
	}
}

func skipDevice(name string) bool {
	switch runtime.GOOS {
	case "linux":
		return strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram")
	}
	return false
}
