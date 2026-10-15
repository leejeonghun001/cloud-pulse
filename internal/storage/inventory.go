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

// ListHostInventoryCounts returns, for every host with a stored
// inventory snapshot, its running-container and listening-port counts in
// one query, so the fleet listing does not query per host. Snapshots are
// decoded into a minimal shape holding only what the counts need, which
// is measurably cheaper than decoding full models.Inventory values (and
// than counting with SQLite's JSON1 functions). Undecodable snapshots are
// skipped: the returned map still holds every other host, together with
// an error naming the skipped hosts.
func (db *DB) ListHostInventoryCounts(ctx context.Context) (map[string]models.InventoryCounts, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT host_id, json FROM host_inventory`)
	if err != nil {
		return nil, fmt.Errorf("storage: list host inventory counts: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make(map[string]models.InventoryCounts)
	var badHosts []string
	for rows.Next() {
		var hostID string
		var raw sql.NullString
		if err := rows.Scan(&hostID, &raw); err != nil {
			return nil, fmt.Errorf("storage: list host inventory counts: %w", err)
		}
		var snap struct {
			Ports  []json.RawMessage `json:"ports"`
			Docker struct {
				Containers []struct {
					State string `json:"state"`
				} `json:"containers"`
			} `json:"docker"`
		}
		if raw.Valid && raw.String != "" {
			if err := json.Unmarshal([]byte(raw.String), &snap); err != nil {
				// One undecodable snapshot must not hide every other
				// host's counters: skip it and report it alongside.
				badHosts = append(badHosts, hostID)
				continue
			}
		}
		c := models.InventoryCounts{ListeningPorts: len(snap.Ports)}
		for _, ct := range snap.Docker.Containers {
			if ct.State == "running" {
				c.ContainersRunning++
			}
		}
		out[hostID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list host inventory counts: %w", err)
	}
	if len(badHosts) > 0 {
		return out, fmt.Errorf("storage: list host inventory counts: undecodable snapshot for hosts %v", badHosts)
	}
	return out, nil
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
