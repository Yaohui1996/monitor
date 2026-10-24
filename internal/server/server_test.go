package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"monitor/internal/collector"
	"monitor/internal/historydb"
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

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/theme.js", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "monitor.theme") {
		t.Fatalf("theme bootstrap script is not served: %d %s", rec.Code, rec.Body.String())
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

func TestPersistentHistoryEndpoints(t *testing.T) {
	db, err := historydb.Open(filepath.Join(t.TempDir(), "monitor.db"), historydb.Options{MaxPoints: 100})
	if err != nil {
		t.Fatalf("open history db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ts := time.Now().Add(-2 * time.Second).UnixMilli()
	util := 42.5
	db.Append(&collector.Snapshot{
		Timestamp: ts,
		CPU:       collector.CPU{Percent: 21, PerCore: []float64{10, 30, 20, 25}, Cores: 4},
		Memory:    collector.Memory{Total: 4096, Used: 2048, Percent: 50, SwapTotal: 1024, SwapUsed: 128},
		Disks:     []collector.Disk{{Device: "/dev/test", Mountpoint: "/", Fstype: "testfs", Total: 100, Used: 40, Free: 60, Percent: 40}},
		GPUs:      []collector.GPU{{Index: 0, Name: "Test GPU", Utilization: &util}},
		Processes: []collector.Process{{PID: 42, Name: "history-test", CPUPercent: 7, MemPercent: 3, RSS: 2048}},
	})
	if err := db.Flush(context.Background()); err != nil {
		t.Fatalf("flush history: %v", err)
	}

	handler := newHandler(t, server.Options{History: db})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/history?seconds=300", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("history status %d: %s", rec.Code, rec.Body.String())
	}
	var hist struct {
		Resolution int64     `json:"resolution"`
		Step       int64     `json:"step"`
		T          []int64   `json:"t"`
		CPU        []float64 `json:"cpu"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if hist.Resolution != 1 || hist.Step != 1 || len(hist.T) == 0 {
		t.Fatalf("unexpected persistent history: %+v", hist)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/history/details?ts="+strconv.FormatInt(ts, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("details status %d: %s", rec.Code, rec.Body.String())
	}
	var details historydb.Details
	if err := json.Unmarshal(rec.Body.Bytes(), &details); err != nil {
		t.Fatalf("decode details: %v", err)
	}
	if len(details.PerCore) != 4 || len(details.GPUs) != 1 || len(details.Processes) != 1 || len(details.Disks) != 1 {
		t.Fatalf("missing persistent details: %+v", details)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
	var meta struct {
		Storage historydb.Info `json:"storage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if !meta.Storage.Enabled {
		t.Fatal("expected storage to be enabled in meta")
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
		GPU      []float64 `json:"gpu"`
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
	if len(hist.GPU) != len(hist.T) {
		t.Fatalf("GPU column is not aligned: %d/%d", len(hist.GPU), len(hist.T))
	}
}
