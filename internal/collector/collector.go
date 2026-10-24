package collector

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

// Options configures a Collector.
type Options struct {
	// Interval between samples. Defaults to one second.
	Interval time.Duration
	// TopProcesses is how many processes are kept per ranking (CPU and memory).
	TopProcesses int
	// HistorySize is the number of samples kept in memory.
	HistorySize int
}

// Collector periodically samples the machine and publishes immutable snapshots.
type Collector struct {
	interval  time.Duration
	topN      int
	procEvery time.Duration
	gpuEvery  time.Duration

	history *history
	hub     *hub

	collectMu sync.Mutex // serializes sampling and guards prev/last
	mu        sync.RWMutex
	latest    *Snapshot

	// Process enumeration is expensive on some platforms, so it runs on its
	// own cadence and only the cached result is attached to each snapshot.
	procMu    sync.RWMutex
	procCache []Process
	procCount int
	procCPU   map[int32]float64
	procAt    time.Time

	// GPU probing can spawn driver utilities, so it is sampled asynchronously
	// and cached as well.
	gpuMu    sync.RWMutex
	gpuCache []GPU

	first     chan struct{}
	firstOnce sync.Once

	static hostStatic
	prev   prevState
	last   lastGood
}

type netCounters struct {
	rx uint64
	tx uint64
}

type prevState struct {
	at time.Time

	cpuTimes    cpu.TimesStat
	hasCPUTimes bool

	net    map[string]netCounters
	netRx  uint64
	netTx  uint64
	hasNet bool

	diskRead     uint64
	diskWrite    uint64
	diskReadOps  uint64
	diskWriteOps uint64
	hasDisk      bool
}

// lastGood keeps the most recent successful reading of each metric so a
// transient platform error does not flash zeros on the dashboard.
type lastGood struct {
	cpu     CPU
	memory  Memory
	disks   []Disk
	diskIO  DiskIO
	network Network
}

// New creates a Collector with sensible defaults.
func New(opts Options) *Collector {
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.TopProcesses <= 0 {
		opts.TopProcesses = 12
	}
	if opts.HistorySize <= 0 {
		opts.HistorySize = 3600
	}
	return &Collector{
		interval:  opts.Interval,
		topN:      opts.TopProcesses,
		procEvery: max(opts.Interval, 2*time.Second),
		gpuEvery:  max(opts.Interval, 3*time.Second),
		history:   newHistory(opts.HistorySize),
		hub:       newHub(),
		first:     make(chan struct{}),
		static:    readHostStatic(),
		procCPU:   make(map[int32]float64),
	}
}

// Start blocks until ctx is cancelled, sampling at the configured interval.
func (c *Collector) Start(ctx context.Context) {
	go c.sampleProcesses(ctx)
	go c.sampleGPUs(ctx)

	// Prime delta counters (CPU, network, disk) so the first published
	// snapshot already carries meaningful rates.
	c.collect(time.Now(), false)

	timer := time.NewTimer(min(c.interval, 500*time.Millisecond))
	select {
	case <-ctx.Done():
		timer.Stop()
		return
	case <-timer.C:
	}
	c.collect(time.Now(), true)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.collect(now, true)
		}
	}
}

func (c *Collector) collect(now time.Time, publish bool) {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	dt := 0.0
	if !c.prev.at.IsZero() {
		dt = now.Sub(c.prev.at).Seconds()
	}

	snap := &Snapshot{
		Timestamp: now.UnixMilli(),
		Host:      c.static.snapshot(now),
		CPU:       c.readCPU(),
		Memory:    c.readMemory(),
		Disks:     c.readDisks(),
		DiskIO:    c.readDiskIO(dt),
		Network:   c.readNetwork(dt),
		Load:      readLoad(),
	}
	c.fillGPUs(snap)
	c.fillProcesses(snap)
	c.prev.at = now

	if !publish {
		return
	}

	c.mu.Lock()
	c.latest = snap
	c.mu.Unlock()

	c.history.add(pointFrom(snap))
	c.hub.publish(snap)
	c.firstOnce.Do(func() { close(c.first) })
}

// Latest returns the most recent snapshot, if any.
func (c *Collector) Latest() (*Snapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest, c.latest != nil
}

// Wait blocks until a snapshot is available or ctx expires.
func (c *Collector) Wait(ctx context.Context) (*Snapshot, bool) {
	if snap, ok := c.Latest(); ok {
		return snap, true
	}
	select {
	case <-c.first:
		return c.Latest()
	case <-ctx.Done():
		return nil, false
	}
}

// Subscribe registers a live snapshot subscriber.
func (c *Collector) Subscribe() (<-chan *Snapshot, func()) {
	return c.hub.subscribe(4)
}

// History returns samples from the last d, oldest first.
func (c *Collector) History(d time.Duration) []Point {
	return c.history.since(d)
}

// HistorySpan reports how much time the ring buffer can hold.
func (c *Collector) HistorySpan() time.Duration {
	return c.history.span(c.interval)
}

// Interval returns the sampling interval.
func (c *Collector) Interval() time.Duration {
	return c.interval
}

func pointFrom(s *Snapshot) Point {
	p := Point{
		Time:  s.Timestamp,
		CPU:   s.CPU.Percent,
		Mem:   s.Memory.Percent,
		Swap:  s.Memory.SwapPercent,
		Rx:    s.Network.RxBytesPerSec,
		Tx:    s.Network.TxBytesPerSec,
		Read:  s.DiskIO.ReadBytesPerSec,
		Write: s.DiskIO.WriteBytesPerSec,
	}
	for i := range s.GPUs {
		if u := s.GPUs[i].Utilization; u != nil && *u > p.GPU {
			p.GPU = *u
		}
	}
	if s.Load != nil {
		p.Load = s.Load.Load1
	}
	return p
}

// rate converts a monotonically increasing counter into units per second.
func rate(cur, prev uint64, seconds float64) float64 {
	if seconds <= 0 || cur < prev {
		return 0
	}
	return float64(cur-prev) / seconds
}

func round(v float64, digits int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	p := math.Pow10(digits)
	return math.Round(v*p) / p
}

func clamp(v, lo, hi float64) float64 {
	return min(max(v, lo), hi)
}

func percentOf(delta, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return clamp(delta/total*100, 0, 100)
}
