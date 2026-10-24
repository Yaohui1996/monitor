// Package collector gathers a portable snapshot of the local machine's
// resource usage: CPU, memory, disks, network, load average and processes.
package collector

// Snapshot is a complete point-in-time view of the machine. Once published, a
// Snapshot is treated as immutable and may be shared with any number of readers.
type Snapshot struct {
	Timestamp    int64     `json:"timestamp"` // unix milliseconds
	Host         Host      `json:"host"`
	CPU          CPU       `json:"cpu"`
	Memory       Memory    `json:"memory"`
	Disks        []Disk    `json:"disks"`
	DiskIO       DiskIO    `json:"diskIO"`
	Network      Network   `json:"network"`
	GPUs         []GPU     `json:"gpus,omitempty"`
	Load         *Load     `json:"load,omitempty"`
	ProcessCount int       `json:"processCount"`
	Processes    []Process `json:"processes"`
}

// Host describes relatively static machine properties. Uptime is the only
// field that changes between snapshots.
type Host struct {
	Hostname        string  `json:"hostname"`
	OS              string  `json:"os"`
	Platform        string  `json:"platform"`
	PlatformVersion string  `json:"platformVersion"`
	KernelVersion   string  `json:"kernelVersion"`
	KernelArch      string  `json:"kernelArch"`
	CPUModel        string  `json:"cpuModel"`
	CPUMhz          float64 `json:"cpuMhz"`
	CPUCores        int     `json:"cpuCores"`
	CPUPhysical     int     `json:"cpuPhysical"`
	BootTime        int64   `json:"bootTime"` // unix seconds
	Uptime          int64   `json:"uptime"`   // seconds
}

// CPU holds utilization percentages. PerCore contains one entry per logical
// core; User/System/Idle/Iowait are averages over the sampling window.
type CPU struct {
	Percent float64   `json:"percent"`
	PerCore []float64 `json:"perCore"`
	Cores   int       `json:"cores"`
	User    float64   `json:"user"`
	System  float64   `json:"system"`
	Idle    float64   `json:"idle"`
	Iowait  float64   `json:"iowait"`
	Steal   float64   `json:"steal"`
}

// Memory holds byte counts for physical and swap memory.
type Memory struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Available   uint64  `json:"available"`
	Free        uint64  `json:"free"`
	Cached      uint64  `json:"cached"`
	Buffers     uint64  `json:"buffers"`
	Percent     float64 `json:"percent"`
	SwapTotal   uint64  `json:"swapTotal"`
	SwapUsed    uint64  `json:"swapUsed"`
	SwapPercent float64 `json:"swapPercent"`
}

// Disk describes usage of a mounted filesystem.
type Disk struct {
	Device     string  `json:"device"`
	Mountpoint string  `json:"mountpoint"`
	Fstype     string  `json:"fstype"`
	Total      uint64  `json:"total"`
	Used       uint64  `json:"used"`
	Free       uint64  `json:"free"`
	Percent    float64 `json:"percent"`
}

// DiskIO holds aggregate disk throughput. Rates are bytes per second.
type DiskIO struct {
	ReadBytesPerSec  float64 `json:"readBytesPerSec"`
	WriteBytesPerSec float64 `json:"writeBytesPerSec"`
	ReadOpsPerSec    float64 `json:"readOpsPerSec"`
	WriteOpsPerSec   float64 `json:"writeOpsPerSec"`
	ReadBytesTotal   uint64  `json:"readBytesTotal"`
	WriteBytesTotal  uint64  `json:"writeBytesTotal"`
}

// Network holds aggregate throughput and per-interface rates.
type Network struct {
	RxBytesPerSec float64     `json:"rxBytesPerSec"`
	TxBytesPerSec float64     `json:"txBytesPerSec"`
	RxBytesTotal  uint64      `json:"rxBytesTotal"`
	TxBytesTotal  uint64      `json:"txBytesTotal"`
	Interfaces    []Interface `json:"interfaces"`
}

// Interface describes the traffic of a single network interface.
type Interface struct {
	Name          string  `json:"name"`
	RxBytesPerSec float64 `json:"rxBytesPerSec"`
	TxBytesPerSec float64 `json:"txBytesPerSec"`
	RxBytesTotal  uint64  `json:"rxBytesTotal"`
	TxBytesTotal  uint64  `json:"txBytesTotal"`
}

// Load holds the 1/5/15 minute load averages. Not available on Windows.
type Load struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// GPU describes one graphics processor. Optional metrics are nil when the
// platform or driver cannot report them.
type GPU struct {
	Index       int      `json:"index"`
	Name        string   `json:"name"`
	Vendor      string   `json:"vendor,omitempty"`
	Cores       int      `json:"cores,omitempty"`
	Utilization *float64 `json:"utilization,omitempty"`
	MemoryUsed  *uint64  `json:"memoryUsed,omitempty"`
	MemoryTotal *uint64  `json:"memoryTotal,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Power       *float64 `json:"power,omitempty"`
	FanSpeed    *float64 `json:"fanSpeed,omitempty"`
}

// Process describes a single process. CPUPercent is relative to one core.
type Process struct {
	PID        int32   `json:"pid"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpuPercent"`
	MemPercent float64 `json:"memPercent"`
	RSS        uint64  `json:"rss"`
}

// Point is one compact history sample; byte rates are per second.
type Point struct {
	Time  int64   `json:"t"`
	CPU   float64 `json:"cpu"`
	Mem   float64 `json:"mem"`
	Swap  float64 `json:"swap"`
	GPU   float64 `json:"gpu"`
	Load  float64 `json:"load"`
	Rx    float64 `json:"rx"`
	Tx    float64 `json:"tx"`
	Read  float64 `json:"read"`
	Write float64 `json:"write"`
}
