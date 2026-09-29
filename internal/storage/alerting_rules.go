package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// ListAlertRules returns every configured alert rule, sorted by id.
func (db *DB) ListAlertRules(ctx context.Context) ([]models.AlertRule, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, enabled, metric, host_id, operator, threshold,
		       duration_sec, cooldown_sec, notify_resolved, channel_ids,
		       created_at, updated_at
		FROM alert_rules ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list alert rules: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.AlertRule, 0)
	for rows.Next() {
		r, err := scanAlertRule(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("storage: list alert rules: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list alert rules: %w", err)
	}
	return out, nil
}

// GetAlertRule returns one alert rule by id, or models.ErrNotFound.
func (db *DB) GetAlertRule(ctx context.Context, id int64) (models.AlertRule, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, enabled, metric, host_id, operator, threshold,
		       duration_sec, cooldown_sec, notify_resolved, channel_ids,
		       created_at, updated_at
		FROM alert_rules WHERE id = ?
	`, id)
	r, err := scanAlertRule(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.AlertRule{}, fmt.Errorf("storage: get alert rule: %w", models.ErrNotFound)
		}
		return models.AlertRule{}, fmt.Errorf("storage: get alert rule: %w", err)
	}
	return r, nil
}

// CreateAlertRule inserts r (ignoring r.ID) and returns the row with its
// assigned ID and CreatedAt/UpdatedAt populated.
func (db *DB) CreateAlertRule(ctx context.Context, r models.AlertRule) (models.AlertRule, error) {
	channelIDsJSON, err := marshalChannelIDs(r.ChannelIDs)
	if err != nil {
		return models.AlertRule{}, fmt.Errorf("storage: create alert rule: %w", err)
	}

	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO alert_rules (name, enabled, metric, host_id, operator, threshold,
		                         duration_sec, cooldown_sec, notify_resolved, channel_ids,
		                         created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.Name, r.Enabled, string(r.Metric), r.HostID, string(r.Operator), r.Threshold,
		r.DurationSec, r.CooldownSec, r.NotifyResolved, channelIDsJSON, now, now)
	if err != nil {
		return models.AlertRule{}, fmt.Errorf("storage: create alert rule: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.AlertRule{}, fmt.Errorf("storage: create alert rule: last insert id: %w", err)
	}

	r.ID = id
	r.CreatedAt, r.UpdatedAt = now, now
	if r.ChannelIDs == nil {
		r.ChannelIDs = []int64{}
	}
	return r, nil
}

// UpdateAlertRule replaces the stored rule matching r.ID with r
// (UpdatedAt refreshed to now), or returns models.ErrNotFound if no rule
// with that ID exists.
func (db *DB) UpdateAlertRule(ctx context.Context, r models.AlertRule) (models.AlertRule, error) {
	channelIDsJSON, err := marshalChannelIDs(r.ChannelIDs)
	if err != nil {
		return models.AlertRule{}, fmt.Errorf("storage: update alert rule: %w", err)
	}

	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		UPDATE alert_rules SET
			name = ?, enabled = ?, metric = ?, host_id = ?, operator = ?, threshold = ?,
			duration_sec = ?, cooldown_sec = ?, notify_resolved = ?, channel_ids = ?,
			updated_at = ?
		WHERE id = ?
	`, r.Name, r.Enabled, string(r.Metric), r.HostID, string(r.Operator), r.Threshold,
		r.DurationSec, r.CooldownSec, r.NotifyResolved, channelIDsJSON, now, r.ID)
	if err != nil {
		return models.AlertRule{}, fmt.Errorf("storage: update alert rule: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return models.AlertRule{}, fmt.Errorf("storage: update alert rule: rows affected: %w", err)
	}
	if n == 0 {
		return models.AlertRule{}, fmt.Errorf("storage: update alert rule: %w", models.ErrNotFound)
	}

	updated, err := db.GetAlertRule(ctx, r.ID)
	if err != nil {
		return models.AlertRule{}, fmt.Errorf("storage: update alert rule: reload: %w", err)
	}
	return updated, nil
}

// DeleteAlertRule removes the rule with id. Deleting a non-existent rule
// is not an error.
func (db *DB) DeleteAlertRule(ctx context.Context, id int64) error {
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id); err != nil {
		return fmt.Errorf("storage: delete alert rule: %w", err)
	}
	return nil
}

// scanAlertRule scans one alert_rules row via scan (either *sql.Row.Scan
// or *sql.Rows.Scan) into a models.AlertRule.
func scanAlertRule(scan func(dest ...any) error) (models.AlertRule, error) {
	var r models.AlertRule
	var metric, operator, channelIDsJSON string
	if err := scan(
		&r.ID, &r.Name, &r.Enabled, &metric, &r.HostID, &operator, &r.Threshold,
		&r.DurationSec, &r.CooldownSec, &r.NotifyResolved, &channelIDsJSON,
		&r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return models.AlertRule{}, err
	}
	r.Metric = models.AlertMetric(metric)
	r.Operator = models.AlertOperator(operator)
	ids, err := unmarshalChannelIDs(channelIDsJSON)
	if err != nil {
		return models.AlertRule{}, fmt.Errorf("unmarshal channel_ids: %w", err)
	}
	r.ChannelIDs = ids
	return r, nil
}

func marshalChannelIDs(ids []int64) (string, error) {
	if ids == nil {
		ids = []int64{}
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("marshal channel_ids: %w", err)
	}
	return string(b), nil
}

func unmarshalChannelIDs(s string) ([]int64, error) {
	if s == "" {
		return []int64{}, nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(s), &ids); err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}
