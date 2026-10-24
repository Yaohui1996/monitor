package collector

import (
	"sort"
	"sync"
	"time"
)

// history is a bounded in-memory buffer of recent samples.
type history struct {
	mu     sync.Mutex
	max    int
	points []Point
}

func newHistory(max int) *history {
	if max < 1 {
		max = 1
	}
	return &history{max: max, points: make([]Point, 0, max)}
}

func (h *history) add(p Point) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.points) < h.max {
		h.points = append(h.points, p)
		return
	}
	copy(h.points, h.points[1:])
	h.points[len(h.points)-1] = p
}

// since returns a copy of all samples newer than d.
func (h *history) since(d time.Duration) []Point {
	h.mu.Lock()
	defer h.mu.Unlock()
	cutoff := time.Now().Add(-d).UnixMilli()
	idx := sort.Search(len(h.points), func(i int) bool {
		return h.points[i].Time >= cutoff
	})
	return append([]Point(nil), h.points[idx:]...)
}

func (h *history) span(interval time.Duration) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Duration(len(h.points)) * interval
}
