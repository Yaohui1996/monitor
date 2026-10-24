package historydb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"monitor/internal/collector"
)

const (
	writeBatchSize  = 8
	writeFlushEvery = 2 * time.Second
	diskWriteEvery  = 60 * time.Second
)

func (d *DB) writerLoop() {
	defer d.wg.Done()

	ticker := time.NewTicker(writeFlushEvery)
	defer ticker.Stop()

	batch := make([]*collector.Snapshot, 0, writeBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		err := d.writeBatch(batch)
		d.recordError(err)
		batch = batch[:0]
	}

	for {
		select {
		case snap, ok := <-d.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, snap)
			if len(batch) >= writeBatchSize {
				flush()
			}
		case ack := <-d.flushCh:
			// A Flush must include snapshots that are still sitting in the
			// channel, otherwise it can overtake them and report success early.
		drain:
			for {
				select {
				case snap, ok := <-d.queue:
					if !ok {
						break drain
					}
					batch = append(batch, snap)
				default:
					break drain
				}
			}
			flush()
			ack <- d.lastError()
		case <-ticker.C:
			flush()
		}
	}
}

func (d *DB) lastError() error {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	if d.lastErr == "" {
		return nil
	}
	return errors.New(d.lastErr)
}

func (d *DB) writeBatch(batch []*collector.Snapshot) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	for _, snap := range batch {
		if err := d.writeSnapshot(tx, snap); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) writeSnapshot(tx *sql.Tx, snap *collector.Snapshot) error {
	ts := snap.Timestamp
	if ts <= 0 {
		ts = time.Now().UnixMilli()
	}

	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO samples
			(ts, cpu, mem, swap, gpu, load, rx, tx, disk_read, disk_write)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ts,
		snap.CPU.Percent,
		snap.Memory.Percent,
		snap.Memory.SwapPercent,
		gpuUtilization(snap.GPUs),
		loadValue(snap.Load),
		snap.Network.RxBytesPerSec,
		snap.Network.TxBytesPerSec,
		snap.DiskIO.ReadBytesPerSec,
		snap.DiskIO.WriteBytesPerSec,
	); err != nil {
		return err
	}

	cores := marshalJSON(snap.CPU.PerCore)
	gpus := marshalJSON(snap.GPUs)
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO details
			(ts, cores, gpus, mem_used, mem_total, swap_used, swap_total)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts, cores, gpus,
		int64(snap.Memory.Used), int64(snap.Memory.Total),
		int64(snap.Memory.SwapUsed), int64(snap.Memory.SwapTotal),
	); err != nil {
		return err
	}

	sampleTime := time.UnixMilli(ts)
	if len(snap.Disks) > 0 &&
		(d.lastDiskWrite.IsZero() || sampleTime.Sub(d.lastDiskWrite) >= diskWriteEvery || sampleTime.Before(d.lastDiskWrite)) {
		for _, disk := range snap.Disks {
			if _, err := tx.Exec(`
				INSERT OR REPLACE INTO disk_usage
					(ts, mount, device, fstype, total, used, free, percent)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				ts, disk.Mountpoint, disk.Device, disk.Fstype,
				int64(disk.Total), int64(disk.Used), int64(disk.Free), disk.Percent,
			); err != nil {
				return err
			}
		}
		d.lastDiskWrite = sampleTime
	}

	for _, proc := range snap.Processes {
		if err := d.writeProcess(tx, ts, proc); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) writeProcess(tx *sql.Tx, ts int64, proc collector.Process) error {
	id, err := d.processID(tx, proc.PID, proc.Name)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
		INSERT OR REPLACE INTO process_samples (ts, process_id, cpu, mem, rss)
		VALUES (?, ?, ?, ?, ?)`,
		ts, id, proc.CPUPercent, proc.MemPercent, int64(proc.RSS),
	)
	return err
}

func (d *DB) processID(tx *sql.Tx, pid int32, name string) (int64, error) {
	key := strconv.FormatInt(int64(pid), 10) + "\x00" + name
	if id, ok := d.procIDs[key]; ok {
		return id, nil
	}
	if _, err := tx.Exec(`
		INSERT INTO processes (pid, name) VALUES (?, ?)
		ON CONFLICT(pid, name) DO NOTHING`, pid, name); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM processes WHERE pid = ? AND name = ?`, pid, name).Scan(&id); err != nil {
		return 0, err
	}
	d.procIDs[key] = id
	return id, nil
}

func marshalJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil || len(data) == 0 || string(data) == "null" {
		return "[]"
	}
	return string(data)
}

func gpuUtilization(gpus []collector.GPU) float64 {
	best := 0.0
	for _, gpu := range gpus {
		if gpu.Utilization != nil && *gpu.Utilization > best {
			best = *gpu.Utilization
		}
	}
	return best
}

func loadValue(load *collector.Load) float64 {
	if load == nil {
		return 0
	}
	return load.Load1
}
