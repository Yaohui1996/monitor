package historydb

import (
	"log"
	"time"
)

// prune enforces the approved retention policy:
//
//	raw samples/details/process ranking  14 days
//	disk usage snapshots                 90 days
//	1-minute rollups                     90 days
//	1-hour rollups                       2 years
func (d *DB) prune() {
	now := time.Now().UnixMilli()
	cutoff := func(retention time.Duration) int64 {
		return now - retention.Milliseconds()
	}

	for _, table := range []string{"samples", "details", "process_samples"} {
		if _, err := d.sql.Exec("DELETE FROM "+table+" WHERE ts < ?", cutoff(d.opts.RawRetention)); err != nil {
			log.Printf("history prune %s failed: %v", table, err)
		}
	}
	if _, err := d.sql.Exec("DELETE FROM disk_usage WHERE ts < ?", cutoff(d.opts.MinuteRetention)); err != nil {
		log.Printf("history prune disk_usage failed: %v", err)
	}
	if _, err := d.sql.Exec("DELETE FROM rollups WHERE resolution = 60 AND ts < ?", cutoff(d.opts.MinuteRetention)); err != nil {
		log.Printf("history prune 1m rollups failed: %v", err)
	}
	if _, err := d.sql.Exec("DELETE FROM rollups WHERE resolution = 3600 AND ts < ?", cutoff(d.opts.HourRetention)); err != nil {
		log.Printf("history prune 1h rollups failed: %v", err)
	}

	if _, err := d.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("history wal checkpoint failed: %v", err)
	}
	if _, err := d.sql.Exec("PRAGMA incremental_vacuum(2000)"); err != nil {
		log.Printf("history incremental vacuum failed: %v", err)
	}
}
