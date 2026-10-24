package historydb

import (
	"time"

	"monitor/internal/collector"
)

// Retention defaults approved for the dashboard.
const (
	DefaultRawRetention    = 14 * 24 * time.Hour
	DefaultMinuteRetention = 90 * 24 * time.Hour
	DefaultHourRetention   = 730 * 24 * time.Hour
	DefaultMaxPoints       = 4000
)

// Options configures the history database.
type Options struct {
	RawRetention    time.Duration // full-fidelity samples
	MinuteRetention time.Duration // 1-minute rollups
	HourRetention   time.Duration // 1-hour rollups
	MaxPoints       int           // upper bound for a chart response
}

func (o Options) withDefaults() Options {
	if o.RawRetention <= 0 {
		o.RawRetention = DefaultRawRetention
	}
	if o.MinuteRetention <= 0 {
		o.MinuteRetention = DefaultMinuteRetention
	}
	if o.HourRetention <= 0 {
		o.HourRetention = DefaultHourRetention
	}
	if o.MaxPoints <= 0 {
		o.MaxPoints = DefaultMaxPoints
	}
	if o.MinuteRetention < o.RawRetention {
		o.MinuteRetention = o.RawRetention
	}
	if o.HourRetention < o.MinuteRetention {
		o.HourRetention = o.MinuteRetention
	}
	return o
}

// Info describes the storage backend to the UI.
type Info struct {
	Enabled                bool   `json:"enabled"`
	Path                   string `json:"path,omitempty"`
	SizeBytes              int64  `json:"sizeBytes"`
	RawRetentionSeconds    int64  `json:"rawRetentionSeconds"`
	MinuteRetentionSeconds int64  `json:"minuteRetentionSeconds"`
	HourRetentionSeconds   int64  `json:"hourRetentionSeconds"`
	Oldest                 int64  `json:"oldest,omitempty"` // unix ms
	Newest                 int64  `json:"newest,omitempty"` // unix ms
	Dropped                uint64 `json:"dropped"`
	Error                  string `json:"error,omitempty"`
}

// Series is a columnar history payload for the charts.
type Series struct {
	Resolution int64     `json:"resolution"` // source resolution in seconds
	Step       int64     `json:"step"`       // bucket step in seconds
	T          []int64   `json:"t"`
	CPU        []float64 `json:"cpu"`
	Mem        []float64 `json:"mem"`
	Swap       []float64 `json:"swap"`
	GPU        []float64 `json:"gpu"`
	Load       []float64 `json:"load"`
	Rx         []float64 `json:"rx"`
	Tx         []float64 `json:"tx"`
	Read       []float64 `json:"read"`
	Write      []float64 `json:"write"`
}

// MemoryDetails is the memory fragment shown in the history detail panel.
type MemoryDetails struct {
	Used      uint64  `json:"used"`
	Total     uint64  `json:"total"`
	Percent   float64 `json:"percent"`
	SwapUsed  uint64  `json:"swapUsed"`
	SwapTotal uint64  `json:"swapTotal"`
}

// Details is a point-in-time detail payload reconstructed from raw samples or
// rollups.
type Details struct {
	Timestamp   int64               `json:"timestamp"`
	Resolution  int64               `json:"resolution"`
	Approximate bool                `json:"approximate"`
	CPUPercent  float64             `json:"cpuPercent"`
	PerCore     []float64           `json:"perCore"`
	Memory      MemoryDetails       `json:"memory"`
	GPUs        []collector.GPU     `json:"gpus,omitempty"`
	Disks       []collector.Disk    `json:"disks,omitempty"`
	Processes   []collector.Process `json:"processes,omitempty"`
}
