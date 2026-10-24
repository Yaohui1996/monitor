package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"monitor/internal/collector"
	"monitor/internal/server"
)

func newHandler(t *testing.T, opts server.Options) http.Handler {
	t.Helper()
	c := collector.New(collector.Options{
		Interval:     100 * time.Millisecond,
		HistorySize:  30,
		TopProcesses: 5,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go c.Start(ctx)
	t.Cleanup(cancel)
	return server.New(c, opts)
}

func TestSnapshotEndpoint(t *testing.T) {
	handler := newHandler(t, server.Options{Version: "test"})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/snapshot", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var snap collector.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if snap.Host.Hostname == "" {
		t.Error("hostname is empty")
	}
	if snap.Memory.Total == 0 {
		t.Error("memory total is zero")
	}
	if len(snap.CPU.PerCore) == 0 {
		t.Error("per-core CPU data is empty")
	}
}

func TestIndexPage(t *testing.T) {
	handler := newHandler(t, server.Options{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Monitor") {
		t.Fatal("index page does not look like the dashboard")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("missing CSP header")
	}
}

func TestTokenRequiredForAPI(t *testing.T) {
	handler := newHandler(t, server.Options{Token: "s3cret"})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/meta?token=s3cret", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health check must stay public, got %d", rec.Code)
	}
}

func TestHistoryEndpoint(t *testing.T) {
	handler := newHandler(t, server.Options{})
	// Give the collector time to record a few samples.
	time.Sleep(350 * time.Millisecond)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/history?seconds=60", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var hist struct {
		Interval float64   `json:"interval"`
		T        []int64   `json:"t"`
		CPU      []float64 `json:"cpu"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if hist.Interval <= 0 {
		t.Errorf("unexpected interval: %v", hist.Interval)
	}
	if len(hist.T) == 0 || len(hist.T) != len(hist.CPU) {
		t.Fatalf("history columns are not aligned: %d/%d", len(hist.T), len(hist.CPU))
	}
}
