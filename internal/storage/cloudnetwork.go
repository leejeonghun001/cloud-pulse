package storage

import (
	"context"
	"fmt"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// GetHostCloudNetwork returns hostID's stored provider-measured traffic,
// or models.ErrNotFound when its agent never reported one.
func (db *DB) GetHostCloudNetwork(ctx context.Context, hostID string) (models.CloudNetworkUsage, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT provider, month, in_bytes, out_bytes, status, status_detail, last_success_at, last_attempt_at
		FROM host_cloud_network WHERE host_id = ?
	`, hostID)
	u, err := scanCloudNetwork(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.CloudNetworkUsage{}, fmt.Errorf("storage: get host cloud network: %w", models.ErrNotFound)
		}
		return models.CloudNetworkUsage{}, fmt.Errorf("storage: get host cloud network: %w", err)
	}
	return u, nil
}

// ListHostCloudNetwork returns every host's stored provider-measured
// traffic, keyed by host ID.
func (db *DB) ListHostCloudNetwork(ctx context.Context) (map[string]models.CloudNetworkUsage, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT host_id, provider, month, in_bytes, out_bytes, status, status_detail, last_success_at, last_attempt_at
		FROM host_cloud_network
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list host cloud network: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make(map[string]models.CloudNetworkUsage)
	for rows.Next() {
		var hostID string
		u, err := scanCloudNetwork(func(dest ...any) error { return rows.Scan(append([]any{&hostID}, dest...)...) })
		if err != nil {
			return nil, fmt.Errorf("storage: list host cloud network: %w", err)
		}
		out[hostID] = u
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list host cloud network: %w", err)
	}
	return out, nil
}

// SetHostCloudNetwork upserts hostID's provider-measured traffic.
func (db *DB) SetHostCloudNetwork(ctx context.Context, hostID string, u models.CloudNetworkUsage) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO host_cloud_network (
			host_id, provider, month, in_bytes, out_bytes, status, status_detail, last_success_at, last_attempt_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET
			provider = excluded.provider,
			month = excluded.month,
			in_bytes = excluded.in_bytes,
			out_bytes = excluded.out_bytes,
			status = excluded.status,
			status_detail = excluded.status_detail,
			last_success_at = excluded.last_success_at,
			last_attempt_at = excluded.last_attempt_at
	`, hostID, string(u.Provider), u.Month, int64(u.InBytes), int64(u.OutBytes), string(u.Status), u.StatusDetail, u.LastSuccessAt, u.LastAttemptAt)
	if err != nil {
		return fmt.Errorf("storage: set host cloud network: %w", err)
	}
	return nil
}

func scanCloudNetwork(scan func(dest ...any) error) (models.CloudNetworkUsage, error) {
	var u models.CloudNetworkUsage
	var provider, status string
	var in, out int64
	if err := scan(&provider, &u.Month, &in, &out, &status, &u.StatusDetail, &u.LastSuccessAt, &u.LastAttemptAt); err != nil {
		return models.CloudNetworkUsage{}, err
	}
	u.Provider = models.CloudBillingProvider(provider)
	u.Status = models.CloudBillingStatus(status)
	u.InBytes, u.OutBytes = uint64(max(in, 0)), uint64(max(out, 0))
	return u, nil
}
