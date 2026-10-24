package collector

import (
	"testing"
	"time"
)

func TestHistoryTrimsOldest(t *testing.T) {
	h := newHistory(3)
	for i := int64(1); i <= 5; i++ {
		h.add(Point{Time: i})
	}
	if len(h.points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(h.points))
	}
	if h.points[0].Time != 3 || h.points[2].Time != 5 {
		t.Fatalf("unexpected window: %+v", h.points)
	}
}

func TestHistorySince(t *testing.T) {
	now := time.Now().UnixMilli()
	h := newHistory(10)
	h.add(Point{Time: now - 5000, CPU: 1})
	h.add(Point{Time: now - 2000, CPU: 2})
	h.add(Point{Time: now - 1000, CPU: 3})

	got := h.since(3 * time.Second)
	if len(got) != 2 {
		t.Fatalf("expected 2 points, got %d", len(got))
	}
	if got[0].CPU != 2 || got[1].CPU != 3 {
		t.Fatalf("unexpected points: %+v", got)
	}
}

func TestRate(t *testing.T) {
	if got := rate(200, 100, 2); got != 50 {
		t.Fatalf("expected 50, got %v", got)
	}
	if got := rate(200, 100, 0); got != 0 {
		t.Fatalf("zero interval must yield 0, got %v", got)
	}
	if got := rate(100, 200, 2); got != 0 {
		t.Fatalf("counter reset must yield 0, got %v", got)
	}
}

func TestPointFromUsesBusiestGPU(t *testing.T) {
	low := 12.0
	high := 63.5
	snap := &Snapshot{
		Timestamp: 1234,
		GPUs: []GPU{
			{Utilization: &low},
			{Utilization: &high},
		},
	}
	point := pointFrom(snap)
	if point.GPU != high {
		t.Fatalf("expected GPU utilization %v, got %v", high, point.GPU)
	}
}

func TestPickTopMergesRankings(t *testing.T) {
	procs := []Process{
		{PID: 1, CPUPercent: 5, MemPercent: 50},
		{PID: 2, CPUPercent: 90, MemPercent: 1},
		{PID: 3, CPUPercent: 20, MemPercent: 30},
		{PID: 4, CPUPercent: 1, MemPercent: 40},
	}

	top := pickTop(procs, 2)
	if len(top) != 4 {
		t.Fatalf("expected union of both rankings, got %d entries: %+v", len(top), top)
	}
	if top[0].PID != 2 {
		t.Fatalf("expected busiest CPU process first, got %+v", top[0])
	}
	seen := make(map[int32]bool, len(top))
	for _, p := range top {
		if seen[p.PID] {
			t.Fatalf("duplicate process in result: %d", p.PID)
		}
		seen[p.PID] = true
	}

	limited := pickTop(procs, 1)
	if len(limited) != 2 {
		t.Fatalf("expected 2 distinct entries, got %+v", limited)
	}
}
