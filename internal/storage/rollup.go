package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Retention windows applied by Prune, per SPEC.
const (
	rawRetention    = 26 * time.Hour
	fiveMRetention  = 30 * 24 * time.Hour
	oneHRetention   = 400 * 24 * time.Hour
	bucketRetention = 90 * 24 * time.Hour
)

// Rollup windows: how far back each tier re-aggregates from its source on
// every call, per SPEC. Re-rolling a trailing window (rather than only the
// newest bucket) makes Rollup idempotent and self-healing for late-arriving
// or backfilled samples.
const (
	rollup5mWindow = 3 * time.Hour
	rollup1hWindow = 6 * time.Hour
)

// floorTo rounds down unix seconds ts to the nearest multiple of step
// seconds.
func floorTo(ts, step int64) int64 {
	return (ts / step) * step
}

// Rollup re-aggregates metrics_raw into metrics_5m (over the trailing
// rollup5mWindow) and metrics_5m into metrics_1h (over the trailing
// rollup1hWindow), relative to now. It uses INSERT OR REPLACE so re-running
// it is idempotent: existing bucket rows are recomputed from source data
// rather than incremented.
func (db *DB) Rollup(ctx context.Context, now time.Time) error {
	nowTS := now.Unix()

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: rollup: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // best-effort rollback; no-op after a successful Commit

	from5m := floorTo(nowTS-int64(rollup5mWindow.Seconds()), models.Resolution5m)
	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO metrics_5m (
			host_id, ts, cpu, mem_used, mem_total, mem_pct,
			swap_used, swap_total, disk_used, disk_total, disk_pct,
			disk_read_bps, disk_write_bps, net_rx_bps, net_tx_bps,
			net_rx_bytes, net_tx_bytes, load1, load5, load15
		)
		SELECT
			host_id,
			(ts / 300) * 300 AS bucket,
			AVG(cpu), CAST(AVG(mem_used) AS INTEGER), CAST(AVG(mem_total) AS INTEGER), AVG(mem_pct),
			CAST(AVG(swap_used) AS INTEGER), CAST(AVG(swap_total) AS INTEGER),
			CAST(AVG(disk_used) AS INTEGER), CAST(AVG(disk_total) AS INTEGER), AVG(disk_pct),
			AVG(disk_read_bps), AVG(disk_write_bps), AVG(net_rx_bps), AVG(net_tx_bps),
			CAST(SUM(net_rx_bytes) AS INTEGER), CAST(SUM(net_tx_bytes) AS INTEGER),
			AVG(load1), AVG(load5), AVG(load15)
		FROM metrics_raw
		WHERE ts >= ? AND ts <= ?
		GROUP BY host_id, bucket
	`, from5m, nowTS); err != nil {
		return fmt.Errorf("storage: rollup 5m: %w", err)
	}

	from1h := floorTo(nowTS-int64(rollup1hWindow.Seconds()), models.Resolution1h)
	if _, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO metrics_1h (
			host_id, ts, cpu, mem_used, mem_total, mem_pct,
			swap_used, swap_total, disk_used, disk_total, disk_pct,
			disk_read_bps, disk_write_bps, net_rx_bps, net_tx_bps,
			net_rx_bytes, net_tx_bytes, load1, load5, load15
		)
		SELECT
			host_id,
			(ts / 3600) * 3600 AS bucket,
			AVG(cpu), CAST(AVG(mem_used) AS INTEGER), CAST(AVG(mem_total) AS INTEGER), AVG(mem_pct),
			CAST(AVG(swap_used) AS INTEGER), CAST(AVG(swap_total) AS INTEGER),
			CAST(AVG(disk_used) AS INTEGER), CAST(AVG(disk_total) AS INTEGER), AVG(disk_pct),
			AVG(disk_read_bps), AVG(disk_write_bps), AVG(net_rx_bps), AVG(net_tx_bps),
			CAST(SUM(net_rx_bytes) AS INTEGER), CAST(SUM(net_tx_bytes) AS INTEGER),
			AVG(load1), AVG(load5), AVG(load15)
		FROM metrics_5m
		WHERE ts >= ? AND ts <= ?
		GROUP BY host_id, bucket
	`, from1h, nowTS); err != nil {
		return fmt.Errorf("storage: rollup 1h: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: rollup: commit: %w", err)
	}
	return nil
}

// Prune deletes data older than its retention window, relative to now:
// metrics_raw older than 26h, metrics_5m older than 30d, metrics_1h older
// than 400d, and bucket_stats older than 90d. egress_monthly and
// alerts_sent are retained forever.
func (db *DB) Prune(ctx context.Context, now time.Time) error {
	nowTS := now.Unix()

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // best-effort rollback; no-op after a successful Commit

	stmts := []struct {
		query string
		cut   int64
	}{
		{`DELETE FROM metrics_raw WHERE ts < ?`, nowTS - int64(rawRetention.Seconds())},
		{`DELETE FROM metrics_5m WHERE ts < ?`, nowTS - int64(fiveMRetention.Seconds())},
		{`DELETE FROM metrics_1h WHERE ts < ?`, nowTS - int64(oneHRetention.Seconds())},
		{`DELETE FROM bucket_stats WHERE ts < ?`, nowTS - int64(bucketRetention.Seconds())},
		// Expired dashboard sessions (see PruneSessions): kept in the
		// same transaction/schedule as the other retention deletes
		// rather than a separate call, per SPEC-v0.4 §1 ("Prune expired
		// in Prune()").
		{`DELETE FROM sessions WHERE expires_at <= ?`, nowTS},
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s.query, s.cut); err != nil {
			return fmt.Errorf("storage: prune: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: prune: commit: %w", err)
	}
	return nil
}
