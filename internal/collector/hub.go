package collector

import "sync"

// hub fans snapshots out to live subscribers. Slow subscribers are skipped
// rather than allowed to block the sampling loop.
type hub struct {
	mu   sync.Mutex
	subs map[chan *Snapshot]struct{}
}

func newHub() *hub {
	return &hub{subs: make(map[chan *Snapshot]struct{})}
}

func (h *hub) subscribe(buffer int) (<-chan *Snapshot, func()) {
	ch := make(chan *Snapshot, buffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			close(ch)
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

func (h *hub) publish(s *Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- s:
		default:
		}
	}
}
