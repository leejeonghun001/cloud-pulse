package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// UpsertHost inserts h as a new host or, if it already exists, updates its
// info_json and advances last_seen to max(existing, seenAt).
func (db *DB) UpsertHost(ctx context.Context, h models.HostInfo, seenAt int64) error {
	infoJSON, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("storage: upsert host: marshal info: %w", err)
	}

	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO hosts (id, info_json, last_seen, latest_ts, latest_json)
		VALUES (?, ?, ?, NULL, NULL)
		ON CONFLICT(id) DO UPDATE SET
			info_json = excluded.info_json,
			last_seen = MAX(hosts.last_seen, excluded.last_seen)
	`, h.ID, string(infoJSON), seenAt)
	if err != nil {
		return fmt.Errorf("storage: upsert host: %w", err)
	}
	return nil
}

// GetHost returns the stored record for id, or models.ErrNotFound if no
// such host exists.
func (db *DB) GetHost(ctx context.Context, id string) (models.HostRecord, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT info_json, last_seen, latest_json
		FROM hosts WHERE id = ?
	`, id)

	var infoJSON, latestJSON sql.NullString
	var lastSeen int64
	if err := row.Scan(&infoJSON, &lastSeen, &latestJSON); err != nil {
		if isNoRows(err) {
			return models.HostRecord{}, fmt.Errorf("storage: get host: %w", models.ErrNotFound)
		}
		return models.HostRecord{}, fmt.Errorf("storage: get host: %w", err)
	}

	rec, err := decodeHostRecord(infoJSON.String, lastSeen, latestJSON)
	if err != nil {
		return models.HostRecord{}, fmt.Errorf("storage: get host: %w", err)
	}
	return rec, nil
}

// ListHosts returns all stored hosts sorted by id.
func (db *DB) ListHosts(ctx context.Context) ([]models.HostRecord, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT info_json, last_seen, latest_json
		FROM hosts ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list hosts: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.HostRecord, 0)
	for rows.Next() {
		var infoJSON, latestJSON sql.NullString
		var lastSeen int64
		if err := rows.Scan(&infoJSON, &lastSeen, &latestJSON); err != nil {
			return nil, fmt.Errorf("storage: list hosts: %w", err)
		}
		rec, err := decodeHostRecord(infoJSON.String, lastSeen, latestJSON)
		if err != nil {
			return nil, fmt.Errorf("storage: list hosts: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list hosts: %w", err)
	}
	return out, nil
}

func decodeHostRecord(infoJSON string, lastSeen int64, latestJSON sql.NullString) (models.HostRecord, error) {
	var rec models.HostRecord
	if infoJSON != "" {
		if err := json.Unmarshal([]byte(infoJSON), &rec.Info); err != nil {
			return models.HostRecord{}, fmt.Errorf("unmarshal info: %w", err)
		}
	}
	rec.LastSeen = lastSeen
	if latestJSON.Valid && latestJSON.String != "" {
		var s models.Sample
		if err := json.Unmarshal([]byte(latestJSON.String), &s); err != nil {
			return models.HostRecord{}, fmt.Errorf("unmarshal latest sample: %w", err)
		}
		rec.Latest = &s
	}
	return rec, nil
}
