package storage

import (
	"context"
	"fmt"
	"time"
)

// GetSetting returns the stored value for key and ok=true, or ok=false if
// no such setting has been set.
func (db *DB) GetSetting(ctx context.Context, key string) (string, bool, error) {
	row := db.sql.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key)

	var value string
	if err := row.Scan(&value); err != nil {
		if isNoRows(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("storage: get setting: %w", err)
	}
	return value, true, nil
}

// SetSetting stores value under key. An empty value deletes the setting
// (restoring its default), matching GetSetting's ok=false behavior.
func (db *DB) SetSetting(ctx context.Context, key, value string) error {
	if value == "" {
		if _, err := db.sql.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
			return fmt.Errorf("storage: set setting: delete: %w", err)
		}
		return nil
	}

	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at
	`, key, value, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("storage: set setting: %w", err)
	}
	return nil
}
