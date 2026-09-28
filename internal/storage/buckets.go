package storage

import (
	"context"
	"fmt"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// SaveBucketStats inserts one row per element of stats into bucket_stats,
// keyed by (provider, bucket, collected_at).
func (db *DB) SaveBucketStats(ctx context.Context, stats []models.BucketStats) error {
	if len(stats) == 0 {
		return nil
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: save bucket stats: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // best-effort rollback; no-op after a successful Commit

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO bucket_stats (
			provider, bucket, ts, region, size_bytes, object_count,
			class_a_ops_mtd, class_b_ops_mtd, requests_window, window_seconds,
			egress_bytes_mtd, request_metrics_available, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("storage: save bucket stats: prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }() // best-effort close; tx.Rollback/Commit already finalizes work

	for _, s := range stats {
		if _, err := stmt.ExecContext(ctx,
			string(s.Provider), s.Bucket, s.CollectedAt, s.Region, s.SizeBytes, s.ObjectCount,
			s.ClassAOpsMTD, s.ClassBOpsMTD, s.RequestsWindow, s.WindowSeconds,
			s.EgressBytesMTD, s.RequestMetricsAvailable, s.Error,
		); err != nil {
			return fmt.Errorf("storage: save bucket stats: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: save bucket stats: commit: %w", err)
	}
	return nil
}

// LatestBuckets returns the most recent row for every distinct
// (provider, bucket), sorted by provider then bucket.
func (db *DB) LatestBuckets(ctx context.Context) ([]models.BucketStats, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT b.provider, b.bucket, b.ts, b.region, b.size_bytes, b.object_count,
			b.class_a_ops_mtd, b.class_b_ops_mtd, b.requests_window, b.window_seconds,
			b.egress_bytes_mtd, b.request_metrics_available, b.error
		FROM bucket_stats b
		JOIN (
			SELECT provider, bucket, MAX(ts) AS max_ts
			FROM bucket_stats
			GROUP BY provider, bucket
		) latest ON latest.provider = b.provider AND latest.bucket = b.bucket AND latest.max_ts = b.ts
		ORDER BY b.provider, b.bucket
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: latest buckets: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.BucketStats, 0)
	for rows.Next() {
		s, err := scanBucketStats(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: latest buckets: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: latest buckets: %w", err)
	}
	return out, nil
}

// BucketHistory returns bucket_stats rows for the given provider/bucket
// with collected_at >= since, ordered ascending by collected_at.
func (db *DB) BucketHistory(ctx context.Context, provider models.StorageProvider, bucket string, since int64) ([]models.BucketPoint, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT ts, size_bytes, object_count, requests_window, class_a_ops_mtd, class_b_ops_mtd
		FROM bucket_stats
		WHERE provider = ? AND bucket = ? AND ts >= ?
		ORDER BY ts ASC
	`, string(provider), bucket, since)
	if err != nil {
		return nil, fmt.Errorf("storage: bucket history: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.BucketPoint, 0)
	for rows.Next() {
		var p models.BucketPoint
		if err := rows.Scan(&p.Timestamp, &p.SizeBytes, &p.ObjectCount, &p.RequestsWindow, &p.ClassAOpsMTD, &p.ClassBOpsMTD); err != nil {
			return nil, fmt.Errorf("storage: bucket history: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: bucket history: %w", err)
	}
	return out, nil
}

// rowScanner is the subset of *sql.Rows used by scanBucketStats, kept small
// so it is trivially satisfied by *sql.Rows without importing database/sql
// here just for the type name.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanBucketStats(r rowScanner) (models.BucketStats, error) {
	var s models.BucketStats
	var provider string
	if err := r.Scan(
		&provider, &s.Bucket, &s.CollectedAt, &s.Region, &s.SizeBytes, &s.ObjectCount,
		&s.ClassAOpsMTD, &s.ClassBOpsMTD, &s.RequestsWindow, &s.WindowSeconds,
		&s.EgressBytesMTD, &s.RequestMetricsAvailable, &s.Error,
	); err != nil {
		return models.BucketStats{}, err
	}
	s.Provider = models.StorageProvider(provider)
	return s, nil
}
