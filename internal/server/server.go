// Package server exposes collected metrics over HTTP: JSON snapshots for
// initial rendering, server-sent events for live updates and a history window
// for the charts.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"monitor/internal/collector"
	"monitor/web"
)

// Options configures the HTTP handler.
type Options struct {
	// Token enables a shared-secret check on all API endpoints when non-empty.
	Token string
	// Version is reported to the UI.
	Version string
}

type server struct {
	collector *collector.Collector
	token     string
	version   string
	startedAt time.Time

	staticFS fs.FS
	files    http.Handler
}

// New builds the HTTP handler for the dashboard and API.
func New(c *collector.Collector, opts Options) http.Handler {
	sub, err := fs.Sub(web.Assets, "static")
	if err != nil {
		panic(err) // the embed directive guarantees this directory exists
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	s := &server{
		collector: c,
		token:     opts.Token,
		version:   opts.Version,
		startedAt: time.Now(),
		staticFS:  sub,
		files:     http.FileServer(http.FS(sub)),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/meta", s.auth(s.handleMeta))
	mux.HandleFunc("/api/snapshot", s.auth(s.handleSnapshot))
	mux.HandleFunc("/api/history", s.auth(s.handleHistory))
	mux.HandleFunc("/api/stream", s.auth(s.handleStream))
	mux.HandleFunc("/", s.handleStatic)
	return securityHeaders(mux)
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

type metaResponse struct {
	Version        string  `json:"version"`
	Interval       float64 `json:"interval"`
	HistorySeconds int     `json:"historySeconds"`
	Uptime         int64   `json:"uptime"`
}

func (s *server) handleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, metaResponse{
		Version:        s.version,
		Interval:       s.collector.Interval().Seconds(),
		HistorySeconds: int(s.collector.HistorySpan().Seconds()),
		Uptime:         int64(time.Since(s.startedAt).Seconds()),
	})
}

func (s *server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	snap, ok := s.collector.Wait(ctx)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "metrics are not ready yet"})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

type historyResponse struct {
	Interval float64   `json:"interval"`
	T        []int64   `json:"t"`
	CPU      []float64 `json:"cpu"`
	Mem      []float64 `json:"mem"`
	Swap     []float64 `json:"swap"`
	Load     []float64 `json:"load"`
	Rx       []float64 `json:"rx"`
	Tx       []float64 `json:"tx"`
	Read     []float64 `json:"read"`
	Write    []float64 `json:"write"`
}

func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	seconds := 3600
	if raw := r.URL.Query().Get("seconds"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "seconds must be a positive integer"})
			return
		}
		seconds = min(v, 86400)
	}

	points := s.collector.History(time.Duration(seconds) * time.Second)
	resp := historyResponse{
		Interval: s.collector.Interval().Seconds(),
		T:        make([]int64, 0, len(points)),
		CPU:      make([]float64, 0, len(points)),
		Mem:      make([]float64, 0, len(points)),
		Swap:     make([]float64, 0, len(points)),
		Load:     make([]float64, 0, len(points)),
		Rx:       make([]float64, 0, len(points)),
		Tx:       make([]float64, 0, len(points)),
		Read:     make([]float64, 0, len(points)),
		Write:    make([]float64, 0, len(points)),
	}
	for _, p := range points {
		resp.T = append(resp.T, p.Time)
		resp.CPU = append(resp.CPU, p.CPU)
		resp.Mem = append(resp.Mem, p.Mem)
		resp.Swap = append(resp.Swap, p.Swap)
		resp.Load = append(resp.Load, p.Load)
		resp.Rx = append(resp.Rx, p.Rx)
		resp.Tx = append(resp.Tx, p.Tx)
		resp.Read = append(resp.Read, p.Read)
		resp.Write = append(resp.Write, p.Write)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming is not supported"})
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.collector.Subscribe()
	defer unsubscribe()

	if snap, ok := s.collector.Latest(); ok {
		if err := writeEvent(w, flusher, snap); err != nil {
			return
		}
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case snap, ok := <-ch:
			if !ok {
				return
			}
			if err := writeEvent(w, flusher, snap); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w io.Writer, flusher http.Flusher, snap *collector.Snapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: tick\ndata: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if _, err := fs.Stat(s.staticFS, name); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	s.files.ServeHTTP(w, r)
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	if s.token == "" {
		return next
	}
	want := []byte(s.token)
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Monitor-Token")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="monitor"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing access token"})
			return
		}
		next(w, r)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data:; style-src 'self'; " +
		"script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; " +
		"frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
