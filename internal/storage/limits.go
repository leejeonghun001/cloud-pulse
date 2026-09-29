package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// GetHostLimits returns the hub-side limit overrides for hostID. If no row
// exists, it returns models.HostLimits{HostID: hostID} with nil pointers
// (no overrides configured) and a nil error.
func (db *DB) GetHostLimits(ctx context.Context, hostID string) (models.HostLimits, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT egress_limit_bytes, ingress_limit_bytes, updated_at
		FROM host_limits WHERE host_id = ?
	`, hostID)

	var egress, ingress sql.NullInt64
	var updatedAt int64
	if err := row.Scan(&egress, &ingress, &updatedAt); err != nil {
		if isNoRows(err) {
			return models.HostLimits{HostID: hostID}, nil
		}
		return models.HostLimits{}, fmt.Errorf("storage: get host limits: %w", err)
	}

	return models.HostLimits{
		HostID:            hostID,
		EgressLimitBytes:  nullInt64ToUint64Ptr(egress),
		IngressLimitBytes: nullInt64ToUint64Ptr(ingress),
		UpdatedAt:         updatedAt,
	}, nil
}

// ListHostLimits returns every host's limit overrides, sorted by host_id.
// Hosts with no override row are simply absent from the result (callers
// that need a HostLimits for every host should treat a missing entry the
// same as GetHostLimits' no-row case).
func (db *DB) ListHostLimits(ctx context.Context) ([]models.HostLimits, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT host_id, egress_limit_bytes, ingress_limit_bytes, updated_at
		FROM host_limits ORDER BY host_id
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list host limits: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.HostLimits, 0)
	for rows.Next() {
		var hostID string
		var egress, ingress sql.NullInt64
		var updatedAt int64
		if err := rows.Scan(&hostID, &egress, &ingress, &updatedAt); err != nil {
			return nil, fmt.Errorf("storage: list host limits: %w", err)
		}
		out = append(out, models.HostLimits{
			HostID:            hostID,
			EgressLimitBytes:  nullInt64ToUint64Ptr(egress),
			IngressLimitBytes: nullInt64ToUint64Ptr(ingress),
			UpdatedAt:         updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list host limits: %w", err)
	}
	return out, nil
}

// SetHostLimits stores l's overrides for l.HostID. If both
// EgressLimitBytes and IngressLimitBytes are nil, the override row (if
// any) is deleted, restoring default behavior. If l.UpdatedAt is 0, it is
// set to the current Unix time before storing.
//
// Callers are responsible for validating that override values fit in a
// signed 64-bit integer (SQLite INTEGER is signed 64-bit); this method
// stores values as-is via uint64->int64 conversion, which is
// bit-preserving but the hub layer must reject values above 1<<62.
func (db *DB) SetHostLimits(ctx context.Context, l models.HostLimits) error {
	if l.EgressLimitBytes == nil && l.IngressLimitBytes == nil {
		if _, err := db.sql.ExecContext(ctx, `DELETE FROM host_limits WHERE host_id = ?`, l.HostID); err != nil {
			return fmt.Errorf("storage: set host limits: delete: %w", err)
		}
		return nil
	}

	updatedAt := l.UpdatedAt
	if updatedAt == 0 {
		updatedAt = time.Now().Unix()
	}

	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO host_limits (host_id, egress_limit_bytes, ingress_limit_bytes, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET
			egress_limit_bytes = excluded.egress_limit_bytes,
			ingress_limit_bytes = excluded.ingress_limit_bytes,
			updated_at = excluded.updated_at
	`, l.HostID, uint64PtrToNullInt64(l.EgressLimitBytes), uint64PtrToNullInt64(l.IngressLimitBytes), updatedAt)
	if err != nil {
		return fmt.Errorf("storage: set host limits: %w", err)
	}
	return nil
}

// nullInt64ToUint64Ptr converts a nullable SQLite INTEGER column into a
// *uint64, returning nil when the column is NULL.
func nullInt64ToUint64Ptr(n sql.NullInt64) *uint64 {
	if !n.Valid {
		return nil
	}
	v := uint64(n.Int64) //nolint:gosec // bit-preserving conversion; values are validated <= 1<<62 by the hub layer
	return &v
}

// uint64PtrToNullInt64 converts a *uint64 into a nullable SQLite INTEGER
// value, returning an invalid (NULL) NullInt64 when p is nil.
func uint64PtrToNullInt64(p *uint64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true} //nolint:gosec // bit-preserving conversion; values are validated <= 1<<62 by the hub layer
}
