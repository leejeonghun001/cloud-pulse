package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// InsertSamples inserts samples for hostID into metrics_raw, ignoring any
// sample whose (host_id, ts) already exists (idempotent). For rows actually
// inserted, it accumulates NetTxBytes/NetRxBytes into egress_monthly (keyed
// by models.MonthOf(ts)) and, if the sample is newer than the host's
// current latest, updates hosts.latest_ts/latest_json. All of this happens
// in a single transaction. It returns the number of rows actually
// inserted (duplicates are not counted).
func (db *DB) InsertSamples(ctx context.Context, hostID string, samples []models.Sample) (int, error) {
	if len(samples) == 0 {
		return 0, nil
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("storage: insert samples: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // best-effort rollback; no-op after a successful Commit

	insertStmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO metrics_raw (
			host_id, ts, cpu, mem_used, mem_total, mem_pct,
			swap_used, swap_total, disk_used, disk_total, disk_pct,
			disk_read_bps, disk_write_bps, net_rx_bps, net_tx_bps,
			net_rx_bytes, net_tx_bytes, load1, load5, load15
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, fmt.Errorf("storage: insert samples: prepare: %w", err)
	}
	defer func() { _ = insertStmt.Close() }() // best-effort close; tx.Rollback/Commit already finalizes work

	egressDelta := make(map[string][2]uint64) // month -> [tx, rx]
	inserted := 0
	var maxTS int64 = -1
	var maxSample *models.Sample

	for i := range samples {
		s := &samples[i]
		res, err := insertStmt.ExecContext(ctx, hostID, s.Timestamp,
			s.CPUPercent, s.MemUsed, s.MemTotal, s.MemUsedPercent,
			s.SwapUsed, s.SwapTotal, s.DiskUsed, s.DiskTotal, s.DiskUsedPercent,
			s.DiskReadBps, s.DiskWriteBps, s.NetRxBps, s.NetTxBps,
			s.NetRxBytes, s.NetTxBytes, s.Load1, s.Load5, s.Load15,
		)
		if err != nil {
			return 0, fmt.Errorf("storage: insert samples: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("storage: insert samples: rows affected: %w", err)
		}
		if n != 1 {
			continue
		}
		inserted++

		month := models.MonthOf(time.Unix(s.Timestamp, 0))
		d := egressDelta[month]
		d[0] += s.NetTxBytes
		d[1] += s.NetRxBytes
		egressDelta[month] = d

		if s.Timestamp > maxTS {
			maxTS = s.Timestamp
			maxSample = s
		}
	}

	for month, d := range egressDelta {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO egress_monthly (host_id, month, tx_bytes, rx_bytes, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(host_id, month) DO UPDATE SET
				tx_bytes = tx_bytes + excluded.tx_bytes,
				rx_bytes = rx_bytes + excluded.rx_bytes,
				updated_at = excluded.updated_at
		`, hostID, month, d[0], d[1], time.Now().Unix()); err != nil {
			return 0, fmt.Errorf("storage: insert samples: update egress: %w", err)
		}
	}

	if maxSample != nil {
		latestJSON, err := json.Marshal(maxSample)
		if err != nil {
			return 0, fmt.Errorf("storage: insert samples: marshal latest: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE hosts SET latest_ts = ?, latest_json = ?
			WHERE id = ? AND (latest_ts IS NULL OR latest_ts < ?)
		`, maxSample.Timestamp, string(latestJSON), hostID, maxSample.Timestamp); err != nil {
			return 0, fmt.Errorf("storage: insert samples: update latest: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("storage: insert samples: commit: %w", err)
	}
	return inserted, nil
}

// seriesTable maps a resolution (seconds) to its backing table name.
func seriesTable(resolution int) string {
	switch resolution {
	case models.Resolution5m:
		return "metrics_5m"
	case models.Resolution1h:
		return "metrics_1h"
	default:
		return "metrics_raw"
	}
}

// QuerySeries returns the metric series for hostID over [from, to], picking
// the storage tier via models.ResolutionFor. If hostID has no rows in the
// selected tier (including if the host does not exist), an empty Series is
// returned rather than an error.
func (db *DB) QuerySeries(ctx context.Context, hostID string, from, to int64) (models.Series, error) {
	resolution := models.ResolutionFor(from, to)
	table := seriesTable(resolution)
	series := models.NewSeries(hostID, resolution, from, to)

	//nolint:gosec // table is one of three fixed constant names, never user input
	query := fmt.Sprintf(`
		SELECT ts, cpu, mem_pct, disk_pct, net_rx_bps, net_tx_bps, disk_read_bps, disk_write_bps, load1
		FROM %s WHERE host_id = ? AND ts >= ? AND ts <= ?
		ORDER BY ts
	`, table)

	rows, err := db.sql.QueryContext(ctx, query, hostID, from, to)
	if err != nil {
		return models.Series{}, fmt.Errorf("storage: query series: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	for rows.Next() {
		var p models.SeriesPoint
		if err := rows.Scan(&p.TS, &p.CPU, &p.Mem, &p.Disk, &p.NetRx, &p.NetTx, &p.DiskRead, &p.DiskWrite, &p.Load1); err != nil {
			return models.Series{}, fmt.Errorf("storage: query series: %w", err)
		}
		series.Append(p)
	}
	if err := rows.Err(); err != nil {
		return models.Series{}, fmt.Errorf("storage: query series: %w", err)
	}
	return series, nil
}

// ListEgress returns every host's egress accumulator for month, sorted by
// host_id.
func (db *DB) ListEgress(ctx context.Context, month string) ([]models.EgressRecord, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT host_id, month, tx_bytes, rx_bytes
		FROM egress_monthly WHERE month = ?
		ORDER BY host_id
	`, month)
	if err != nil {
		return nil, fmt.Errorf("storage: list egress: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.EgressRecord, 0)
	for rows.Next() {
		var r models.EgressRecord
		if err := rows.Scan(&r.HostID, &r.Month, &r.TxBytes, &r.RxBytes); err != nil {
			return nil, fmt.Errorf("storage: list egress: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list egress: %w", err)
	}
	return out, nil
}
