// Package historydb persists metric history in an embedded SQLite database.
//
// Raw samples are kept for a short window, 1-minute rollups for longer trend
// queries and 1-hour rollups for long-term retention. The package is designed
// for a single writer and keeps all database access behind one connection so
// SQLite never has to arbitrate concurrent writers.
package historydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"monitor/internal/collector"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS samples (
  ts INTEGER PRIMARY KEY,
  cpu REAL NOT NULL,
  mem REAL NOT NULL,
  swap REAL NOT NULL,
  gpu REAL NOT NULL,
  load REAL NOT NULL,
  rx REAL NOT NULL,
  tx REAL NOT NULL,
  disk_read REAL NOT NULL,
  disk_write REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS details (
  ts INTEGER PRIMARY KEY,
  cores TEXT NOT NULL,
  gpus TEXT NOT NULL,
  mem_used INTEGER NOT NULL,
  mem_total INTEGER NOT NULL,
  swap_used INTEGER NOT NULL,
  swap_total INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS disk_usage (
  ts INTEGER NOT NULL,
  mount TEXT NOT NULL,
  device TEXT NOT NULL,
  fstype TEXT NOT NULL,
  total INTEGER NOT NULL,
  used INTEGER NOT NULL,
  free INTEGER NOT NULL,
  percent REAL NOT NULL,
  PRIMARY KEY (ts, mount)
);
CREATE INDEX IF NOT EXISTS idx_disk_usage_mount ON disk_usage(mount, ts);

CREATE TABLE IF NOT EXISTS processes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  pid INTEGER NOT NULL,
  name TEXT NOT NULL,
  UNIQUE(pid, name)
);

CREATE TABLE IF NOT EXISTS process_samples (
  ts INTEGER NOT NULL,
  process_id INTEGER NOT NULL,
  cpu REAL NOT NULL,
  mem REAL NOT NULL,
  rss INTEGER NOT NULL,
  PRIMARY KEY (ts, process_id)
);
CREATE INDEX IF NOT EXISTS idx_process_samples_pid ON process_samples(process_id, ts);

CREATE TABLE IF NOT EXISTS rollups (
  resolution INTEGER NOT NULL,
  ts INTEGER NOT NULL,
  cpu_avg REAL NOT NULL,
  cpu_max REAL NOT NULL,
  mem_avg REAL NOT NULL,
  mem_max REAL NOT NULL,
  swap_avg REAL NOT NULL,
  swap_max REAL NOT NULL,
  gpu_avg REAL NOT NULL,
  gpu_max REAL NOT NULL,
  load_avg REAL NOT NULL,
  load_max REAL NOT NULL,
  rx_avg REAL NOT NULL,
  tx_avg REAL NOT NULL,
  read_avg REAL NOT NULL,
  write_avg REAL NOT NULL,
  mem_used INTEGER NOT NULL,
  mem_total INTEGER NOT NULL,
  swap_used INTEGER NOT NULL,
  swap_total INTEGER NOT NULL,
  cores_avg TEXT NOT NULL,
  cores_max TEXT NOT NULL,
  gpus TEXT NOT NULL,
  disks TEXT NOT NULL,
  processes TEXT NOT NULL,
  PRIMARY KEY (resolution, ts)
);
`

// DB is a durable snapshot store with background rollups and retention.
type DB struct {
	sql  *sql.DB
	path string
	opts Options

	queue   chan *collector.Snapshot
	flushCh chan chan error
	done    chan struct{}

	mu     sync.RWMutex
	closed bool

	wg      sync.WaitGroup
	dropped atomic.Uint64

	// Owned by the writer goroutine.
	lastDiskWrite time.Time
	procIDs       map[string]int64

	errMu   sync.Mutex
	lastErr string
}

// Open opens or creates the database and starts its background workers.
func Open(path string, opts Options) (*DB, error) {
	if path == "" {
		return nil, errors.New("empty database path")
	}
	opts = opts.withDefaults()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	db := &DB{
		sql:     sqlDB,
		path:    path,
		opts:    opts,
		queue:   make(chan *collector.Snapshot, 64),
		flushCh: make(chan chan error, 2),
		done:    make(chan struct{}),
		procIDs: make(map[string]int64),
	}

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA auto_vacuum = INCREMENTAL",
		"PRAGMA temp_store = MEMORY",
	} {
		if _, err := sqlDB.Exec(pragma); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("apply %q: %w", pragma, err)
		}
	}
	if err := db.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)

	db.wg.Add(1)
	go db.writerLoop()
	db.wg.Add(1)
	go db.maintenanceLoop()
	return db, nil
}

func (d *DB) migrate() error {
	if _, err := d.sql.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	var version int
	if err := d.sql.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version < schemaVersion {
		if _, err := d.sql.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	return nil
}

// Append queues a snapshot for asynchronous persistence. It never blocks the
// sampling loop; if the queue is full the sample is dropped and counted.
func (d *DB) Append(snap *collector.Snapshot) {
	if snap == nil {
		return
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return
	}
	select {
	case d.queue <- snap:
	default:
		d.dropped.Add(1)
	}
}

// Flush waits until all currently queued snapshots have been written.
func (d *DB) Flush(ctx context.Context) error {
	d.mu.RLock()
	if d.closed {
		d.mu.RUnlock()
		return errors.New("database is closed")
	}
	ack := make(chan error, 1)
	select {
	case d.flushCh <- ack:
		d.mu.RUnlock()
	case <-ctx.Done():
		d.mu.RUnlock()
		return ctx.Err()
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-d.done:
		return errors.New("database is closed")
	}
}

// Close flushes pending samples and closes the database.
func (d *DB) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	close(d.done)
	close(d.queue)
	d.mu.Unlock()

	d.wg.Wait()
	_, _ = d.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return d.sql.Close()
}

// Info reports storage state for /api/meta.
func (d *DB) Info() Info {
	info := Info{
		Enabled:                true,
		Path:                   d.path,
		RawRetentionSeconds:    int64(d.opts.RawRetention / time.Second),
		MinuteRetentionSeconds: int64(d.opts.MinuteRetention / time.Second),
		HourRetentionSeconds:   int64(d.opts.HourRetention / time.Second),
		Dropped:                d.dropped.Load(),
	}
	if st, err := os.Stat(d.path); err == nil {
		info.SizeBytes = st.Size()
	}
	var oldest, newest sql.NullInt64
	if err := d.sql.QueryRow("SELECT MIN(ts), MAX(ts) FROM samples").Scan(&oldest, &newest); err == nil {
		if oldest.Valid {
			info.Oldest = oldest.Int64
		}
		if newest.Valid {
			info.Newest = newest.Int64
		}
	}
	d.errMu.Lock()
	info.Error = d.lastErr
	d.errMu.Unlock()
	return info
}

func (d *DB) recordError(err error) {
	d.errMu.Lock()
	changed := (err == nil) != (d.lastErr == "")
	if err != nil && d.lastErr != err.Error() {
		changed = true
	}
	if err != nil {
		d.lastErr = err.Error()
	} else {
		d.lastErr = ""
	}
	d.errMu.Unlock()

	if err != nil && changed {
		log.Printf("history database write failed: %v", err)
	}
}
