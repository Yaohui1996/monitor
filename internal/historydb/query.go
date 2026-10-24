package historydb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"monitor/internal/collector"
)

const (
	rawQueryWindow    = 6 * 3600
	minuteQueryWindow = 90 * 24 * 3600
)

type seriesPoint struct {
	t                         int64
	cpu, mem, swap, gpu, load float64
	rx, tx, read, write       float64
}

// Query returns a downsampled chart series for the requested time window.
func (d *DB) Query(ctx context.Context, seconds int) (*Series, error) {
	if seconds <= 0 {
		seconds = 3600
	}
	now := time.Now().UnixMilli()
	from := now - int64(seconds)*1000

	var (
		resolution int64
		points     []seriesPoint
		err        error
	)
	switch {
	case seconds <= rawQueryWindow:
		resolution = 1
		points, err = d.queryRaw(ctx, from)
	case seconds <= minuteQueryWindow:
		resolution = 60
		points, err = d.queryRollups(ctx, 60, from)
	default:
		resolution = 3600
		points, err = d.queryRollups(ctx, 3600, from)
	}
	if err != nil {
		return nil, err
	}

	points, step := d.downsample(points, resolution, from, now)
	out := &Series{
		Resolution: resolution,
		Step:       step,
		T:          make([]int64, 0, len(points)),
		CPU:        make([]float64, 0, len(points)),
		Mem:        make([]float64, 0, len(points)),
		Swap:       make([]float64, 0, len(points)),
		GPU:        make([]float64, 0, len(points)),
		Load:       make([]float64, 0, len(points)),
		Rx:         make([]float64, 0, len(points)),
		Tx:         make([]float64, 0, len(points)),
		Read:       make([]float64, 0, len(points)),
		Write:      make([]float64, 0, len(points)),
	}
	for _, p := range points {
		out.T = append(out.T, p.t)
		out.CPU = append(out.CPU, p.cpu)
		out.Mem = append(out.Mem, p.mem)
		out.Swap = append(out.Swap, p.swap)
		out.GPU = append(out.GPU, p.gpu)
		out.Load = append(out.Load, p.load)
		out.Rx = append(out.Rx, p.rx)
		out.Tx = append(out.Tx, p.tx)
		out.Read = append(out.Read, p.read)
		out.Write = append(out.Write, p.write)
	}
	return out, nil
}

func (d *DB) queryRaw(ctx context.Context, from int64) ([]seriesPoint, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT ts, cpu, mem, swap, gpu, load, rx, tx, disk_read, disk_write
		FROM samples WHERE ts >= ? ORDER BY ts`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []seriesPoint
	for rows.Next() {
		var p seriesPoint
		if err := rows.Scan(&p.t, &p.cpu, &p.mem, &p.swap, &p.gpu, &p.load, &p.rx, &p.tx, &p.read, &p.write); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

func (d *DB) queryRollups(ctx context.Context, resolution int64, from int64) ([]seriesPoint, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT ts, cpu_avg, mem_avg, swap_avg, gpu_avg, load_avg, rx_avg, tx_avg, read_avg, write_avg
		FROM rollups WHERE resolution = ? AND ts >= ? ORDER BY ts`, resolution, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []seriesPoint
	for rows.Next() {
		var p seriesPoint
		if err := rows.Scan(&p.t, &p.cpu, &p.mem, &p.swap, &p.gpu, &p.load, &p.rx, &p.tx, &p.read, &p.write); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

func (d *DB) downsample(points []seriesPoint, resolution, from, to int64) ([]seriesPoint, int64) {
	if len(points) <= d.opts.MaxPoints {
		return points, resolution
	}
	resMs := resolution * 1000
	if resMs <= 0 {
		resMs = 1000
	}
	span := to - from
	if span <= 0 {
		span = int64(len(points)) * resMs
	}
	bucketMs := int64(math.Ceil(float64(span) / float64(d.opts.MaxPoints)))
	if bucketMs < resMs {
		bucketMs = resMs
	}
	bucketMs = ((bucketMs + resMs - 1) / resMs) * resMs

	out := make([]seriesPoint, 0, d.opts.MaxPoints+1)
	var current seriesPoint
	var bucket int64
	count := 0
	flush := func() {
		if count == 0 {
			return
		}
		out = append(out, averagePoint(current, count))
		current = seriesPoint{}
		count = 0
	}
	for _, p := range points {
		b := p.t / bucketMs
		if count == 0 {
			current = p
			bucket = b
			count = 1
			continue
		}
		if b != bucket {
			flush()
			current = p
			bucket = b
			count = 1
			continue
		}
		current.cpu += p.cpu
		current.mem += p.mem
		current.swap += p.swap
		current.gpu += p.gpu
		current.load += p.load
		current.rx += p.rx
		current.tx += p.tx
		current.read += p.read
		current.write += p.write
		count++
	}
	flush()
	return out, bucketMs / 1000
}

func averagePoint(p seriesPoint, count int) seriesPoint {
	if count <= 1 {
		return p
	}
	f := float64(count)
	p.cpu /= f
	p.mem /= f
	p.swap /= f
	p.gpu /= f
	p.load /= f
	p.rx /= f
	p.tx /= f
	p.read /= f
	p.write /= f
	return p
}

type sampleRow struct {
	ts                        int64
	cpu, mem, swap, gpu, load float64
	rx, tx, read, write       float64
}

// Details returns per-core, GPU, disk and process details around ts. Raw
// samples are preferred; once they expire, 1-minute and 1-hour rollups are
// used and marked approximate.
func (d *DB) Details(ctx context.Context, ts int64) (*Details, error) {
	if ts <= 0 {
		return nil, nil
	}
	now := time.Now().UnixMilli()
	if ts > now {
		ts = now
	}

	var sample sampleRow
	err := d.sql.QueryRowContext(ctx, `
		SELECT ts, cpu, mem, swap, gpu, load, rx, tx, disk_read, disk_write
		FROM samples WHERE ts <= ? AND ts >= ? ORDER BY ts DESC LIMIT 1`,
		ts, ts-2000,
	).Scan(&sample.ts, &sample.cpu, &sample.mem, &sample.swap, &sample.gpu, &sample.load, &sample.rx, &sample.tx, &sample.read, &sample.write)
	if err == nil {
		return d.rawDetails(ctx, sample)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	for _, resolution := range []int64{60, 3600} {
		details, err := d.rollupDetails(ctx, ts, resolution)
		if err != nil {
			return nil, err
		}
		if details != nil {
			return details, nil
		}
	}
	return nil, nil
}

func (d *DB) rawDetails(ctx context.Context, sample sampleRow) (*Details, error) {
	out := &Details{
		Timestamp:  sample.ts,
		Resolution: 1,
		CPUPercent: sample.cpu,
		Memory: MemoryDetails{
			Percent: sample.mem,
		},
	}

	var coresJSON, gpusJSON string
	var memUsed, memTotal, swapUsed, swapTotal uint64
	err := d.sql.QueryRowContext(ctx, `
		SELECT cores, gpus, mem_used, mem_total, swap_used, swap_total
		FROM details WHERE ts = ?`, sample.ts,
	).Scan(&coresJSON, &gpusJSON, &memUsed, &memTotal, &swapUsed, &swapTotal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	_ = json.Unmarshal([]byte(coresJSON), &out.PerCore)
	_ = json.Unmarshal([]byte(gpusJSON), &out.GPUs)
	out.Memory.Used = memUsed
	out.Memory.Total = memTotal
	out.Memory.SwapUsed = swapUsed
	out.Memory.SwapTotal = swapTotal

	out.Disks, err = d.disksNear(ctx, sample.ts, 120_000)
	if err != nil {
		return nil, err
	}
	out.Processes, err = d.processesAt(ctx, sample.ts)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (d *DB) rollupDetails(ctx context.Context, ts, resolution int64) (*Details, error) {
	var (
		rowTS                                     int64
		cpu, mem                                  float64
		memUsed, memTotal, swapUsed, swapTotal    uint64
		coresJSON, gpusJSON, disksJSON, procsJSON string
	)
	err := d.sql.QueryRowContext(ctx, `
		SELECT ts, cpu_avg, mem_avg, mem_used, mem_total, swap_used, swap_total,
		       cores_avg, gpus, disks, processes
		FROM rollups
		WHERE resolution = ? AND ts <= ? AND ts >= ?
		ORDER BY ts DESC LIMIT 1`,
		resolution, ts, ts-resolution*1000,
	).Scan(&rowTS, &cpu, &mem, &memUsed, &memTotal, &swapUsed, &swapTotal,
		&coresJSON, &gpusJSON, &disksJSON, &procsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	out := &Details{
		Timestamp:   rowTS,
		Resolution:  resolution,
		Approximate: true,
		CPUPercent:  cpu,
		Memory: MemoryDetails{
			Used:      memUsed,
			Total:     memTotal,
			Percent:   mem,
			SwapUsed:  swapUsed,
			SwapTotal: swapTotal,
		},
	}
	_ = json.Unmarshal([]byte(coresJSON), &out.PerCore)
	_ = json.Unmarshal([]byte(gpusJSON), &out.GPUs)
	_ = json.Unmarshal([]byte(disksJSON), &out.Disks)
	_ = json.Unmarshal([]byte(procsJSON), &out.Processes)
	return out, nil
}

func (d *DB) disksNear(ctx context.Context, ts, windowMs int64) ([]collector.Disk, error) {
	var diskTS sql.NullInt64
	if err := d.sql.QueryRowContext(ctx,
		`SELECT MAX(ts) FROM disk_usage WHERE ts <= ? AND ts >= ?`,
		ts, ts-windowMs,
	).Scan(&diskTS); err != nil {
		return nil, err
	}
	if !diskTS.Valid {
		return nil, nil
	}
	rows, err := d.sql.QueryContext(ctx, `
		SELECT mount, device, fstype, total, used, free, percent
		FROM disk_usage WHERE ts = ? ORDER BY mount`, diskTS.Int64)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var disks []collector.Disk
	for rows.Next() {
		var disk collector.Disk
		if err := rows.Scan(&disk.Mountpoint, &disk.Device, &disk.Fstype,
			&disk.Total, &disk.Used, &disk.Free, &disk.Percent); err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	return disks, rows.Err()
}

func (d *DB) processesAt(ctx context.Context, ts int64) ([]collector.Process, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT p.pid, p.name, s.cpu, s.mem, s.rss
		FROM process_samples s
		JOIN processes p ON p.id = s.process_id
		WHERE s.ts = ?
		ORDER BY s.cpu DESC
		LIMIT 24`, ts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var processes []collector.Process
	for rows.Next() {
		var proc collector.Process
		if err := rows.Scan(&proc.PID, &proc.Name, &proc.CPUPercent, &proc.MemPercent, &proc.RSS); err != nil {
			return nil, err
		}
		processes = append(processes, proc)
	}
	return processes, rows.Err()
}
