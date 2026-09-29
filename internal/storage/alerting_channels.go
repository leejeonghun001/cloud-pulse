package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// ListNotifyChannels returns every configured notification channel,
// sorted by id, with Config returned unredacted.
func (db *DB) ListNotifyChannels(ctx context.Context) ([]models.NotifyChannel, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, type, enabled, config, created_at, updated_at
		FROM notify_channels ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list notify channels: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.NotifyChannel, 0)
	for rows.Next() {
		ch, err := scanNotifyChannel(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("storage: list notify channels: %w", err)
		}
		out = append(out, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list notify channels: %w", err)
	}
	return out, nil
}

// GetNotifyChannel returns one channel by id, or models.ErrNotFound.
func (db *DB) GetNotifyChannel(ctx context.Context, id int64) (models.NotifyChannel, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, type, enabled, config, created_at, updated_at
		FROM notify_channels WHERE id = ?
	`, id)
	ch, err := scanNotifyChannel(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.NotifyChannel{}, fmt.Errorf("storage: get notify channel: %w", models.ErrNotFound)
		}
		return models.NotifyChannel{}, fmt.Errorf("storage: get notify channel: %w", err)
	}
	return ch, nil
}

// CreateNotifyChannel inserts ch (ignoring ch.ID) and returns the row
// with its assigned ID and CreatedAt/UpdatedAt populated.
func (db *DB) CreateNotifyChannel(ctx context.Context, ch models.NotifyChannel) (models.NotifyChannel, error) {
	configJSON, err := marshalChannelConfig(ch.Config)
	if err != nil {
		return models.NotifyChannel{}, fmt.Errorf("storage: create notify channel: %w", err)
	}

	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO notify_channels (name, type, enabled, config, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, ch.Name, string(ch.Type), ch.Enabled, configJSON, now, now)
	if err != nil {
		return models.NotifyChannel{}, fmt.Errorf("storage: create notify channel: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.NotifyChannel{}, fmt.Errorf("storage: create notify channel: last insert id: %w", err)
	}

	ch.ID = id
	ch.CreatedAt, ch.UpdatedAt = now, now
	return ch, nil
}

// UpdateNotifyChannel replaces the stored channel matching ch.ID with ch
// (UpdatedAt refreshed to now), or returns models.ErrNotFound if no
// channel with that ID exists. Callers are responsible for merging
// preserved secret fields into ch before calling this method.
func (db *DB) UpdateNotifyChannel(ctx context.Context, ch models.NotifyChannel) (models.NotifyChannel, error) {
	configJSON, err := marshalChannelConfig(ch.Config)
	if err != nil {
		return models.NotifyChannel{}, fmt.Errorf("storage: update notify channel: %w", err)
	}

	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		UPDATE notify_channels SET
			name = ?, type = ?, enabled = ?, config = ?, updated_at = ?
		WHERE id = ?
	`, ch.Name, string(ch.Type), ch.Enabled, configJSON, now, ch.ID)
	if err != nil {
		return models.NotifyChannel{}, fmt.Errorf("storage: update notify channel: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return models.NotifyChannel{}, fmt.Errorf("storage: update notify channel: rows affected: %w", err)
	}
	if n == 0 {
		return models.NotifyChannel{}, fmt.Errorf("storage: update notify channel: %w", models.ErrNotFound)
	}

	updated, err := db.GetNotifyChannel(ctx, ch.ID)
	if err != nil {
		return models.NotifyChannel{}, fmt.Errorf("storage: update notify channel: reload: %w", err)
	}
	return updated, nil
}

// DeleteNotifyChannel removes the channel with id. Deleting a
// non-existent channel is not an error. It does not cascade into
// alert_rules.channel_ids.
func (db *DB) DeleteNotifyChannel(ctx context.Context, id int64) error {
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM notify_channels WHERE id = ?`, id); err != nil {
		return fmt.Errorf("storage: delete notify channel: %w", err)
	}
	return nil
}

func scanNotifyChannel(scan func(dest ...any) error) (models.NotifyChannel, error) {
	var ch models.NotifyChannel
	var typ, configJSON string
	if err := scan(&ch.ID, &ch.Name, &typ, &ch.Enabled, &configJSON, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
		return models.NotifyChannel{}, err
	}
	ch.Type = models.NotifyChannelType(typ)
	cfg, err := unmarshalChannelConfig(configJSON)
	if err != nil {
		return models.NotifyChannel{}, fmt.Errorf("unmarshal config: %w", err)
	}
	ch.Config = cfg
	return ch, nil
}

func marshalChannelConfig(cfg map[string]string) (string, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return string(b), nil
}

func unmarshalChannelConfig(s string) (map[string]string, error) {
	if s == "" {
		return map[string]string{}, nil
	}
	var cfg map[string]string
	if err := json.Unmarshal([]byte(s), &cfg); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = map[string]string{}
	}
	return cfg, nil
}
