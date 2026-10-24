package historydb

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"time"

	"monitor/internal/collector"
)

const (
	minuteMs = int64(60_000)
	hourMs   = int64(3_600_000)
	maxCatch = 6 * int64(time.Hour/time.Millisecond)
)

type rollupValues struct {
	cpuAvg, cpuMax      float64
	memAvg, memMax      float64
	swapAvg, swapMax    float64
	gpuAvg, gpuMax      float64
	loadAvg, loadMax    float64
	rxAvg, txAvg        float64
	readAvg, writeAvg   float64
	memUsed, memTotal   uint64
	swapUsed, swapTotal uint64
	coresAvg, coresMax  []float64
	gpus                []collector.GPU
	disks               []collector.Disk
	processes           []collector.Process
}

func (d *DB) maintenanceLoop() {
	defer d.wg.Done()

	d.aggregatePending()
	d.prune()

	rollupTicker := time.NewTicker(30 * time.Second)
	pruneTicker := time.NewTicker(6 * time.Hour)
	defer rollupTicker.Stop()
	defer pruneTicker.Stop()

	for {
		select {
		case <-d.done:
			return
		case <-rollupTicker.C:
			d.aggregatePending()
		case <-pruneTicker.C:
			d.prune()
		}
	}
}

func (d *DB) aggregatePending() {
	ctx := context.Background()
	if err := d.aggregateMinutes(ctx); err != nil {
		log.Printf("history rollup (1m) failed: %v", err)
	}
	if err := d.aggregateHours(ctx); err != nil {
		log.Printf("history rollup (1h) failed: %v", err)
	}
}

func (d *DB) aggregateMinutes(ctx context.Context) error {
	end := time.Now().Truncate(time.Minute).UnixMilli() - minuteMs
	start := end - 2*minuteMs

	var last sql.NullInt64
	if err := d.sql.QueryRowContext(ctx,
		`SELECT MAX(ts) FROM rollups WHERE resolution = 60`).Scan(&last); err != nil {
		return err
	}
	if last.Valid {
		if next := last.Int64 + minuteMs; next < start {
			start = next
		}
	} else {
		var oldest sql.NullInt64
		if err := d.sql.QueryRowContext(ctx, `SELECT MIN(ts) FROM samples`).Scan(&oldest); err == nil && oldest.Valid {
			if oldest.Int64 < start {
				start = oldest.Int64
			}
		}
	}
	if start < end-maxCatch {
		start = end - maxCatch
	}
	if start > end {
		return nil
	}
	start -= start % minuteMs

	for ts := start; ts <= end; ts += minuteMs {
		if err := d.aggregateMinute(ctx, ts); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) aggregateMinute(ctx context.Context, bucket int64) error {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT s.ts, s.cpu, s.mem, s.swap, s.gpu, s.load, s.rx, s.tx, s.disk_read, s.disk_write,
		       d.cores, d.gpus, d.mem_used, d.mem_total, d.swap_used, d.swap_total
		FROM samples s
		LEFT JOIN details d ON d.ts = s.ts
		WHERE s.ts >= ? AND s.ts < ?
		ORDER BY s.ts`, bucket, bucket+minuteMs)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		sum, maxV                  [9]float64
		coresSum, coresMax         []float64
		coresCount                 []int
		memUsedSum                 uint64
		lastMemTotal, lastSwapUsed uint64
		lastSwapTotal              uint64
		lastGPUs                   []collector.GPU
		count                      int
	)
	for rows.Next() {
		var (
			ts                        int64
			cpu, mem, swap, gpu, load float64
			rx, tx, read, write       float64
			coresJSON, gpusJSON       sql.NullString
			memUsed, memTotal         sql.NullInt64
			swapUsed, swapTotal       sql.NullInt64
		)
		if err := rows.Scan(&ts, &cpu, &mem, &swap, &gpu, &load, &rx, &tx, &read, &write,
			&coresJSON, &gpusJSON, &memUsed, &memTotal, &swapUsed, &swapTotal); err != nil {
			return err
		}
		values := [9]float64{cpu, mem, swap, gpu, load, rx, tx, read, write}
		for i, v := range values {
			sum[i] += v
			if v > maxV[i] {
				maxV[i] = v
			}
		}
		if coresJSON.Valid {
			var cores []float64
			if json.Unmarshal([]byte(coresJSON.String), &cores) == nil {
				for len(coresSum) < len(cores) {
					coresSum = append(coresSum, 0)
					coresMax = append(coresMax, 0)
					coresCount = append(coresCount, 0)
				}
				for i, v := range cores {
					coresSum[i] += v
					coresCount[i]++
					if v > coresMax[i] {
						coresMax[i] = v
					}
				}
			}
		}
		if gpusJSON.Valid && gpusJSON.String != "" {
			var gpus []collector.GPU
			if json.Unmarshal([]byte(gpusJSON.String), &gpus) == nil {
				lastGPUs = gpus
			}
		}
		if memUsed.Valid {
			memUsedSum += uint64(memUsed.Int64)
		}
		if memTotal.Valid {
			lastMemTotal = uint64(memTotal.Int64)
		}
		if swapUsed.Valid {
			lastSwapUsed = uint64(swapUsed.Int64)
		}
		if swapTotal.Valid {
			lastSwapTotal = uint64(swapTotal.Int64)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	fcount := float64(count)
	avgCores := make([]float64, len(coresSum))
	for i := range coresSum {
		if coresCount[i] > 0 {
			avgCores[i] = coresSum[i] / float64(coresCount[i])
		}
	}
	disks, err := d.disksNear(ctx, bucket+minuteMs, 120_000)
	if err != nil {
		return err
	}
	processes, err := d.processSummary(ctx, bucket, bucket+minuteMs)
	if err != nil {
		return err
	}
	vals := rollupValues{
		cpuAvg: sum[0] / fcount, cpuMax: maxV[0],
		memAvg: sum[1] / fcount, memMax: maxV[1],
		swapAvg: sum[2] / fcount, swapMax: maxV[2],
		gpuAvg: sum[3] / fcount, gpuMax: maxV[3],
		loadAvg: sum[4] / fcount, loadMax: maxV[4],
		rxAvg: sum[5] / fcount, txAvg: sum[6] / fcount,
		readAvg: sum[7] / fcount, writeAvg: sum[8] / fcount,
		memUsed: memUsedSum / uint64(count), memTotal: lastMemTotal,
		swapUsed: lastSwapUsed, swapTotal: lastSwapTotal,
		coresAvg: avgCores, coresMax: coresMax,
		gpus: lastGPUs, disks: disks, processes: processes,
	}
	return d.insertRollup(ctx, 60, bucket, vals)
}

func (d *DB) aggregateHours(ctx context.Context) error {
	end := time.Now().Truncate(time.Hour).UnixMilli() - hourMs
	start := end - 2*hourMs

	var last sql.NullInt64
	if err := d.sql.QueryRowContext(ctx,
		`SELECT MAX(ts) FROM rollups WHERE resolution = 3600`).Scan(&last); err != nil {
		return err
	}
	if last.Valid {
		if next := last.Int64 + hourMs; next < start {
			start = next
		}
	} else {
		var oldest sql.NullInt64
		if err := d.sql.QueryRowContext(ctx,
			`SELECT MIN(ts) FROM rollups WHERE resolution = 60`).Scan(&oldest); err == nil && oldest.Valid {
			if oldest.Int64 < start {
				start = oldest.Int64
			}
		}
	}
	if start < end-maxCatch {
		start = end - maxCatch
	}
	if start > end {
		return nil
	}
	start -= start % hourMs

	for ts := start; ts <= end; ts += hourMs {
		if err := d.aggregateHour(ctx, ts); err != nil {
			return err
		}
	}
	return nil
}

type processAcc struct {
	proc    collector.Process
	cpuSum  float64
	memSum  float64
	rssSum  float64
	samples int
}

func (d *DB) aggregateHour(ctx context.Context, bucket int64) error {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT ts, cpu_avg, cpu_max, mem_avg, mem_max, swap_avg, swap_max,
		       gpu_avg, gpu_max, load_avg, load_max, rx_avg, tx_avg, read_avg, write_avg,
		       mem_used, mem_total, swap_used, swap_total,
		       cores_avg, cores_max, gpus, disks, processes
		FROM rollups
		WHERE resolution = 60 AND ts >= ? AND ts < ?
		ORDER BY ts`, bucket, bucket+hourMs)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		sum, maxV                  [9]float64
		coresSum, coresMax         []float64
		coresCount                 []int
		memUsedSum                 uint64
		lastMemTotal, lastSwapUsed uint64
		lastSwapTotal              uint64
		lastGPUs                   []collector.GPU
		lastDisks                  []collector.Disk
		procs                      = make(map[string]*processAcc)
		count                      int
	)
	for rows.Next() {
		var (
			ts                                               int64
			cpuAvg, cpuMax, memAvg, memMax, swapAvg, swapMax float64
			gpuAvg, gpuMax, loadAvg, loadMax                 float64
			rxAvg, txAvg, readAvg, writeAvg                  float64
			memUsed, memTotal, swapUsed, swapTotal           uint64
			coresAvgJSON, coresMaxJSON, gpusJSON, disksJSON  string
			procsJSON                                        string
		)
		if err := rows.Scan(&ts, &cpuAvg, &cpuMax, &memAvg, &memMax, &swapAvg, &swapMax,
			&gpuAvg, &gpuMax, &loadAvg, &loadMax, &rxAvg, &txAvg, &readAvg, &writeAvg,
			&memUsed, &memTotal, &swapUsed, &swapTotal,
			&coresAvgJSON, &coresMaxJSON, &gpusJSON, &disksJSON, &procsJSON); err != nil {
			return err
		}
		avgs := [9]float64{cpuAvg, memAvg, swapAvg, gpuAvg, loadAvg, rxAvg, txAvg, readAvg, writeAvg}
		maxs := [9]float64{cpuMax, memMax, swapMax, gpuMax, loadMax, 0, 0, 0, 0}
		for i := range avgs {
			sum[i] += avgs[i]
			if maxs[i] > maxV[i] {
				maxV[i] = maxs[i]
			}
		}
		mergeCores(coresAvgJSON, coresMaxJSON, &coresSum, &coresCount, &coresMax)
		if gpusJSON != "" && gpusJSON != "[]" {
			_ = json.Unmarshal([]byte(gpusJSON), &lastGPUs)
		}
		if disksJSON != "" && disksJSON != "[]" {
			_ = json.Unmarshal([]byte(disksJSON), &lastDisks)
		}
		mergeProcesses(procsJSON, procs)
		memUsedSum += memUsed
		lastMemTotal = memTotal
		lastSwapUsed = swapUsed
		lastSwapTotal = swapTotal
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	fcount := float64(count)
	avgCores := make([]float64, len(coresSum))
	for i := range coresSum {
		if coresCount[i] > 0 {
			avgCores[i] = coresSum[i] / float64(coresCount[i])
		}
	}
	vals := rollupValues{
		cpuAvg: sum[0] / fcount, cpuMax: maxV[0],
		memAvg: sum[1] / fcount, memMax: maxV[1],
		swapAvg: sum[2] / fcount, swapMax: maxV[2],
		gpuAvg: sum[3] / fcount, gpuMax: maxV[3],
		loadAvg: sum[4] / fcount, loadMax: maxV[4],
		rxAvg: sum[5] / fcount, txAvg: sum[6] / fcount,
		readAvg: sum[7] / fcount, writeAvg: sum[8] / fcount,
		memUsed: memUsedSum / uint64(count), memTotal: lastMemTotal,
		swapUsed: lastSwapUsed, swapTotal: lastSwapTotal,
		coresAvg: avgCores, coresMax: coresMax,
		gpus: lastGPUs, disks: lastDisks, processes: summarizeProcesses(procs),
	}
	return d.insertRollup(ctx, 3600, bucket, vals)
}

func mergeCores(avgJSON, maxJSON string, sums *[]float64, counts *[]int, maxes *[]float64) {
	var avg []float64
	if json.Unmarshal([]byte(avgJSON), &avg) == nil {
		for len(*sums) < len(avg) {
			*sums = append(*sums, 0)
			*counts = append(*counts, 0)
			*maxes = append(*maxes, 0)
		}
		for i, v := range avg {
			(*sums)[i] += v
			(*counts)[i]++
		}
	}
	var maxValues []float64
	if json.Unmarshal([]byte(maxJSON), &maxValues) == nil {
		for i, v := range maxValues {
			if i >= len(*maxes) {
				break
			}
			if v > (*maxes)[i] {
				(*maxes)[i] = v
			}
		}
	}
}

func mergeProcesses(raw string, acc map[string]*processAcc) {
	if raw == "" {
		return
	}
	var procs []collector.Process
	if json.Unmarshal([]byte(raw), &procs) != nil {
		return
	}
	for _, proc := range procs {
		key := processKey(proc)
		entry, ok := acc[key]
		if !ok {
			entry = &processAcc{proc: proc}
			acc[key] = entry
		}
		entry.cpuSum += proc.CPUPercent
		entry.memSum += proc.MemPercent
		entry.rssSum += float64(proc.RSS)
		entry.samples++
	}
}

func summarizeProcesses(acc map[string]*processAcc) []collector.Process {
	if len(acc) == 0 {
		return nil
	}
	all := make([]collector.Process, 0, len(acc))
	for _, entry := range acc {
		if entry.samples == 0 {
			continue
		}
		n := float64(entry.samples)
		proc := entry.proc
		proc.CPUPercent = entry.cpuSum / n
		proc.MemPercent = entry.memSum / n
		proc.RSS = uint64(entry.rssSum / n)
		all = append(all, proc)
	}
	return pickTopProcesses(all, 12)
}

func processKey(proc collector.Process) string {
	return strconv.Itoa(int(proc.PID)) + "\x00" + proc.Name
}

func pickTopProcesses(procs []collector.Process, n int) []collector.Process {
	byCPU := append([]collector.Process(nil), procs...)
	sort.SliceStable(byCPU, func(i, j int) bool { return byCPU[i].CPUPercent > byCPU[j].CPUPercent })
	byMem := append([]collector.Process(nil), procs...)
	sort.SliceStable(byMem, func(i, j int) bool { return byMem[i].MemPercent > byMem[j].MemPercent })

	seen := make(map[int32]struct{}, n*2)
	out := make([]collector.Process, 0, n*2)
	for _, list := range [][]collector.Process{byCPU, byMem} {
		for i := 0; i < len(list) && i < n; i++ {
			p := list[i]
			if _, ok := seen[p.PID]; ok {
				continue
			}
			seen[p.PID] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

func (d *DB) processSummary(ctx context.Context, from, to int64) ([]collector.Process, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT p.pid, p.name, AVG(s.cpu), AVG(s.mem), AVG(s.rss)
		FROM process_samples s
		JOIN processes p ON p.id = s.process_id
		WHERE s.ts >= ? AND s.ts < ?
		GROUP BY p.id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var procs []collector.Process
	for rows.Next() {
		var (
			proc   collector.Process
			cpu    float64
			mem    float64
			rssAvg float64
		)
		if err := rows.Scan(&proc.PID, &proc.Name, &cpu, &mem, &rssAvg); err != nil {
			return nil, err
		}
		proc.CPUPercent = cpu
		proc.MemPercent = mem
		proc.RSS = uint64(rssAvg)
		procs = append(procs, proc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return pickTopProcesses(procs, 12), nil
}

func (d *DB) insertRollup(ctx context.Context, resolution, ts int64, v rollupValues) error {
	coresAvg := marshalJSON(v.coresAvg)
	coresMax := marshalJSON(v.coresMax)
	gpus := marshalJSON(v.gpus)
	disks := marshalJSON(v.disks)
	processes := marshalJSON(v.processes)
	_, err := d.sql.ExecContext(ctx, `
		INSERT OR REPLACE INTO rollups
			(resolution, ts, cpu_avg, cpu_max, mem_avg, mem_max, swap_avg, swap_max,
			 gpu_avg, gpu_max, load_avg, load_max, rx_avg, tx_avg, read_avg, write_avg,
			 mem_used, mem_total, swap_used, swap_total,
			 cores_avg, cores_max, gpus, disks, processes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		resolution, ts,
		v.cpuAvg, v.cpuMax, v.memAvg, v.memMax, v.swapAvg, v.swapMax,
		v.gpuAvg, v.gpuMax, v.loadAvg, v.loadMax, v.rxAvg, v.txAvg, v.readAvg, v.writeAvg,
		int64(v.memUsed), int64(v.memTotal), int64(v.swapUsed), int64(v.swapTotal),
		coresAvg, coresMax, gpus, disks, processes,
	)
	return err
}
