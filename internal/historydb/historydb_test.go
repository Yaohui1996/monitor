package historydb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"monitor/internal/collector"
)

func testSnapshot(ts int64, offset float64) *collector.Snapshot {
	util := 25.0 + offset
	gpuMem := uint64(512)
	return &collector.Snapshot{
		Timestamp: ts,
		CPU: collector.CPU{
			Percent: 10 + offset,
			PerCore: []float64{10 + offset, 20 + offset},
			Cores:   2,
		},
		Memory: collector.Memory{
			Total: 2000, Used: 1000, Available: 1000, Percent: 50,
			SwapTotal: 500, SwapUsed: 100, SwapPercent: 20,
		},
		Disks: []collector.Disk{{
			Device: "/dev/test", Mountpoint: "/", Fstype: "testfs",
			Total: 1000, Used: 400, Free: 600, Percent: 40,
		}},
		DiskIO:  collector.DiskIO{ReadBytesPerSec: 100, WriteBytesPerSec: 200},
		Network: collector.Network{RxBytesPerSec: 300, TxBytesPerSec: 400},
		GPUs: []collector.GPU{{
			Index: 0, Name: "Test GPU", Vendor: "Test", Utilization: &util, MemoryUsed: &gpuMem,
		}},
		Load: &collector.Load{Load1: 1.5},
		Processes: []collector.Process{{
			PID: 100, Name: "test-process", CPUPercent: 5 + offset,
			MemPercent: 10, RSS: 1024,
		}},
	}
}

func newTestDB(t *testing.T, opts Options) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "monitor.db"), opts)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return db
}

func TestAppendQueryAndDetails(t *testing.T) {
	db := newTestDB(t, Options{MaxPoints: 100})
	ctx := context.Background()
	ts := time.Now().Add(-5 * time.Second).UnixMilli()

	db.Append(testSnapshot(ts, 0))
	if err := db.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	series, err := db.Query(ctx, 300)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(series.T) != 1 || series.T[0] != ts {
		t.Fatalf("unexpected series: %+v", series)
	}
	if series.CPU[0] != 10 || series.GPU[0] != 25 {
		t.Fatalf("unexpected values: %+v", series)
	}
	if series.Resolution != 1 || series.Step != 1 {
		t.Fatalf("unexpected resolution: %d/%d", series.Resolution, series.Step)
	}

	details, err := db.Details(ctx, ts)
	if err != nil {
		t.Fatalf("details: %v", err)
	}
	if details == nil {
		t.Fatal("expected details")
	}
	if details.Approximate || details.Resolution != 1 {
		t.Fatalf("expected exact raw details: %+v", details)
	}
	if len(details.PerCore) != 2 || len(details.GPUs) != 1 {
		t.Fatalf("missing details: %+v", details)
	}
	if len(details.Processes) != 1 || details.Processes[0].Name != "test-process" {
		t.Fatalf("missing processes: %+v", details.Processes)
	}
	if len(details.Disks) != 1 || details.Disks[0].Mountpoint != "/" {
		t.Fatalf("missing disks: %+v", details.Disks)
	}
	if details.Memory.Total != 2000 || details.Memory.Used != 1000 {
		t.Fatalf("unexpected memory: %+v", details.Memory)
	}
}

func TestMinuteRollupAndRollupDetails(t *testing.T) {
	db := newTestDB(t, Options{})
	ctx := context.Background()

	bucket := time.Now().Truncate(time.Minute).Add(-time.Minute).UnixMilli()
	for i := 0; i < 60; i++ {
		db.Append(testSnapshot(bucket+int64(i)*1000, float64(i)))
	}
	if err := db.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := db.aggregateMinutes(ctx); err != nil {
		t.Fatalf("aggregate minutes: %v", err)
	}

	var count int
	if err := db.sql.QueryRow(
		`SELECT COUNT(*) FROM rollups WHERE resolution = 60 AND ts = ?`, bucket,
	).Scan(&count); err != nil {
		t.Fatalf("count rollups: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one rollup row, got %d", count)
	}

	var cpuAvg, cpuMax float64
	if err := db.sql.QueryRow(
		`SELECT cpu_avg, cpu_max FROM rollups WHERE resolution = 60 AND ts = ?`, bucket,
	).Scan(&cpuAvg, &cpuMax); err != nil {
		t.Fatalf("read rollup: %v", err)
	}
	if cpuAvg < 39 || cpuAvg > 40 || cpuMax < 69 || cpuMax > 70 {
		t.Fatalf("unexpected cpu avg/max: %v/%v", cpuAvg, cpuMax)
	}

	// Drop the raw rows so details must come from the rollup.
	if _, err := db.sql.Exec(`DELETE FROM samples WHERE ts >= ? AND ts < ?`, bucket, bucket+minuteMs); err != nil {
		t.Fatalf("delete raw: %v", err)
	}
	details, err := db.Details(ctx, bucket+30_000)
	if err != nil {
		t.Fatalf("rollup details: %v", err)
	}
	if details == nil || !details.Approximate || details.Resolution != 60 {
		t.Fatalf("expected approximate 1m details: %+v", details)
	}
	if len(details.PerCore) != 2 {
		t.Fatalf("missing rollup cores: %+v", details.PerCore)
	}
	if len(details.Processes) == 0 {
		t.Fatal("missing rollup process summary")
	}
}

func TestHourRollup(t *testing.T) {
	db := newTestDB(t, Options{})
	ctx := context.Background()

	bucket := time.Now().Truncate(time.Hour).Add(-time.Hour).UnixMilli()
	for i := int64(0); i < 3; i++ {
		vals := rollupValues{
			cpuAvg: 10 + float64(i), cpuMax: 20 + float64(i),
			memAvg: 50, memMax: 60, swapAvg: 10, swapMax: 20,
			gpuAvg: 30, gpuMax: 40, loadAvg: 1, loadMax: 2,
			rxAvg: 100, txAvg: 200, readAvg: 300, writeAvg: 400,
			memUsed: 1000, memTotal: 2000, swapUsed: 100, swapTotal: 500,
			coresAvg: []float64{10, 20}, coresMax: []float64{30, 40},
			gpus:      []collector.GPU{{Index: 0, Name: "Test GPU"}},
			disks:     []collector.Disk{{Mountpoint: "/", Total: 100}},
			processes: []collector.Process{{PID: 1, Name: "p", CPUPercent: 5, MemPercent: 10}},
		}
		if err := db.insertRollup(ctx, 60, bucket+i*minuteMs, vals); err != nil {
			t.Fatalf("insert 1m rollup: %v", err)
		}
	}
	if err := db.aggregateHour(ctx, bucket); err != nil {
		t.Fatalf("aggregate hour: %v", err)
	}

	var cpuAvg, cpuMax float64
	if err := db.sql.QueryRow(
		`SELECT cpu_avg, cpu_max FROM rollups WHERE resolution = 3600 AND ts = ?`, bucket,
	).Scan(&cpuAvg, &cpuMax); err != nil {
		t.Fatalf("read hour rollup: %v", err)
	}
	if cpuAvg != 11 || cpuMax != 22 {
		t.Fatalf("unexpected hour avg/max: %v/%v", cpuAvg, cpuMax)
	}
}

func TestQueryUsesHourlyRollup(t *testing.T) {
	db := newTestDB(t, Options{})
	ctx := context.Background()
	ts := time.Now().Add(-100 * 24 * time.Hour).Truncate(time.Hour).UnixMilli()
	vals := rollupValues{
		cpuAvg: 30, cpuMax: 50, memAvg: 60, memMax: 70,
		coresAvg: []float64{20, 40}, coresMax: []float64{30, 50},
		processes: []collector.Process{{PID: 7, Name: "hourly", CPUPercent: 4, MemPercent: 2}},
	}
	if err := db.insertRollup(ctx, 3600, ts, vals); err != nil {
		t.Fatalf("insert hourly rollup: %v", err)
	}

	series, err := db.Query(ctx, 101*86400)
	if err != nil {
		t.Fatalf("query hourly: %v", err)
	}
	if series.Resolution != 3600 || len(series.T) != 1 || series.CPU[0] != 30 {
		t.Fatalf("unexpected hourly series: %+v", series)
	}

	details, err := db.Details(ctx, ts)
	if err != nil {
		t.Fatalf("hourly details: %v", err)
	}
	if details == nil || details.Resolution != 3600 || !details.Approximate || len(details.Processes) != 1 {
		t.Fatalf("unexpected hourly details: %+v", details)
	}
}

func TestPrune(t *testing.T) {
	db := newTestDB(t, Options{RawRetention: time.Hour, MinuteRetention: 2 * time.Hour, HourRetention: 3 * time.Hour})
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	recent := time.Now().Add(-10 * time.Second).UnixMilli()
	db.Append(testSnapshot(old, 0))
	db.Append(testSnapshot(recent, 1))
	if err := db.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	db.prune()

	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM samples`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected only the recent sample, got %d", count)
	}
}

func TestDownsampleCapsPoints(t *testing.T) {
	db := newTestDB(t, Options{MaxPoints: 10})
	now := time.Now().UnixMilli()
	points := make([]seriesPoint, 0, 100)
	for i := 0; i < 100; i++ {
		points = append(points, seriesPoint{
			t:   now - int64(100-i)*1000,
			cpu: float64(i), mem: 50,
		})
	}
	out, step := db.downsample(points, 1, now-100_000, now)
	if len(out) > 11 {
		t.Fatalf("expected at most 11 sampled points, got %d", len(out))
	}
	if step < 1 {
		t.Fatalf("unexpected step: %d", step)
	}
}

func TestInfo(t *testing.T) {
	db := newTestDB(t, Options{})
	ctx := context.Background()
	db.Append(testSnapshot(time.Now().UnixMilli(), 0))
	if err := db.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	info := db.Info()
	if !info.Enabled || info.SizeBytes <= 0 {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.RawRetentionSeconds != int64(DefaultRawRetention/time.Second) {
		t.Fatalf("unexpected raw retention: %d", info.RawRetentionSeconds)
	}
}
