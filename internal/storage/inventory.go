package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// GetHostInventory returns hostID's most recently stored inventory
// snapshot, or models.ErrNotFound if the host has never reported one
// (host_inventory holds at most one row per host, see
// migrations/0004_alerting.sql).
func (db *DB) GetHostInventory(ctx context.Context, hostID string) (models.Inventory, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT json FROM host_inventory WHERE host_id = ?
	`, hostID)

	var raw sql.NullString
	if err := row.Scan(&raw); err != nil {
		if isNoRows(err) {
			return models.Inventory{}, fmt.Errorf("storage: get host inventory: %w", models.ErrNotFound)
		}
		return models.Inventory{}, fmt.Errorf("storage: get host inventory: %w", err)
	}

	var inv models.Inventory
	if raw.Valid && raw.String != "" {
		if err := json.Unmarshal([]byte(raw.String), &inv); err != nil {
			return models.Inventory{}, fmt.Errorf("storage: get host inventory: unmarshal: %w", err)
		}
	}
	return inv, nil
}

// SetHostInventory upserts hostID's inventory snapshot, replacing any
// previously stored one (host_inventory keeps only the latest snapshot
// per host, not a history).
func (db *DB) SetHostInventory(ctx context.Context, hostID string, inv models.Inventory) error {
	raw, err := json.Marshal(inv)
	if err != nil {
		return fmt.Errorf("storage: set host inventory: marshal: %w", err)
	}

	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO host_inventory (host_id, collected_at, json)
		VALUES (?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET
			collected_at = excluded.collected_at,
			json = excluded.json
	`, hostID, inv.CollectedAt, string(raw))
	if err != nil {
		return fmt.Errorf("storage: set host inventory: %w", err)
	}
	return nil
}
