package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// CreateAlertEvent inserts ev (ignoring ev.ID) and returns the row with
// its assigned ID populated.
func (db *DB) CreateAlertEvent(ctx context.Context, ev models.AlertEvent) (models.AlertEvent, error) {
	deliveriesJSON, err := marshalDeliveries(ev.Deliveries)
	if err != nil {
		return models.AlertEvent{}, fmt.Errorf("storage: create alert event: %w", err)
	}

	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO alert_events (rule_id, rule_name, host_id, hostname, metric, state,
		                          value, threshold, started_at, notified_at, resolved_at, deliveries)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ev.RuleID, ev.RuleName, ev.HostID, ev.Hostname, string(ev.Metric), string(ev.State),
		ev.Value, ev.Threshold, ev.StartedAt, ev.NotifiedAt, ev.ResolvedAt, deliveriesJSON)
	if err != nil {
		return models.AlertEvent{}, fmt.Errorf("storage: create alert event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.AlertEvent{}, fmt.Errorf("storage: create alert event: last insert id: %w", err)
	}
	ev.ID = id
	if ev.Deliveries == nil {
		ev.Deliveries = []models.Delivery{}
	}
	return ev, nil
}

// UpdateAlertEvent replaces the stored event matching ev.ID with ev in
// full, or returns models.ErrNotFound if no event with that ID exists.
func (db *DB) UpdateAlertEvent(ctx context.Context, ev models.AlertEvent) error {
	deliveriesJSON, err := marshalDeliveries(ev.Deliveries)
	if err != nil {
		return fmt.Errorf("storage: update alert event: %w", err)
	}

	res, err := db.sql.ExecContext(ctx, `
		UPDATE alert_events SET
			rule_id = ?, rule_name = ?, host_id = ?, hostname = ?, metric = ?, state = ?,
			value = ?, threshold = ?, started_at = ?, notified_at = ?, resolved_at = ?, deliveries = ?
		WHERE id = ?
	`, ev.RuleID, ev.RuleName, ev.HostID, ev.Hostname, string(ev.Metric), string(ev.State),
		ev.Value, ev.Threshold, ev.StartedAt, ev.NotifiedAt, ev.ResolvedAt, deliveriesJSON, ev.ID)
	if err != nil {
		return fmt.Errorf("storage: update alert event: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: update alert event: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("storage: update alert event: %w", models.ErrNotFound)
	}
	return nil
}

// GetAlertEvent returns one event by id, or models.ErrNotFound.
func (db *DB) GetAlertEvent(ctx context.Context, id int64) (models.AlertEvent, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, rule_id, rule_name, host_id, hostname, metric, state,
		       value, threshold, started_at, notified_at, resolved_at, deliveries
		FROM alert_events WHERE id = ?
	`, id)
	ev, err := scanAlertEvent(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.AlertEvent{}, fmt.Errorf("storage: get alert event: %w", models.ErrNotFound)
		}
		return models.AlertEvent{}, fmt.Errorf("storage: get alert event: %w", err)
	}
	return ev, nil
}

// GetActiveAlertEvent returns the currently-firing event for (ruleID,
// hostID), or models.ErrNotFound if none is firing.
func (db *DB) GetActiveAlertEvent(ctx context.Context, ruleID int64, hostID string) (models.AlertEvent, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, rule_id, rule_name, host_id, hostname, metric, state,
		       value, threshold, started_at, notified_at, resolved_at, deliveries
		FROM alert_events
		WHERE rule_id = ? AND host_id = ? AND state = ?
		ORDER BY started_at DESC LIMIT 1
	`, ruleID, hostID, string(models.AlertEventFiring))
	ev, err := scanAlertEvent(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.AlertEvent{}, fmt.Errorf("storage: get active alert event: %w", models.ErrNotFound)
		}
		return models.AlertEvent{}, fmt.Errorf("storage: get active alert event: %w", err)
	}
	return ev, nil
}

// ListAlertEvents returns events matching the given filters, newest
// first, at most limit rows. state == "" matches any state; hostID == ""
// matches any host; before == 0 means no upper bound on StartedAt.
func (db *DB) ListAlertEvents(ctx context.Context, state models.AlertEventState, hostID string, before int64, limit int) ([]models.AlertEvent, error) {
	query := `
		SELECT id, rule_id, rule_name, host_id, hostname, metric, state,
		       value, threshold, started_at, notified_at, resolved_at, deliveries
		FROM alert_events WHERE 1=1
	`
	args := make([]any, 0, 4)
	if state != "" {
		query += ` AND state = ?`
		args = append(args, string(state))
	}
	if hostID != "" {
		query += ` AND host_id = ?`
		args = append(args, hostID)
	}
	if before != 0 {
		query += ` AND started_at < ?`
		args = append(args, before)
	}
	query += ` ORDER BY started_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list alert events: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.AlertEvent, 0)
	for rows.Next() {
		ev, err := scanAlertEvent(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("storage: list alert events: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list alert events: %w", err)
	}
	return out, nil
}

// ListActiveAlertEvents returns every event currently in state
// models.AlertEventFiring, newest first.
func (db *DB) ListActiveAlertEvents(ctx context.Context) ([]models.AlertEvent, error) {
	return db.ListAlertEvents(ctx, models.AlertEventFiring, "", 0, 0)
}

// PruneAlertEvents deletes resolved events older than the 180-day
// retention window as of now. Events still firing are never pruned.
func (db *DB) PruneAlertEvents(ctx context.Context, now time.Time) error {
	const retention = 180 * 24 * time.Hour
	cutoff := now.Add(-retention).Unix()
	_, err := db.sql.ExecContext(ctx, `
		DELETE FROM alert_events WHERE state = ? AND started_at < ?
	`, string(models.AlertEventResolved), cutoff)
	if err != nil {
		return fmt.Errorf("storage: prune alert events: %w", err)
	}
	return nil
}

func scanAlertEvent(scan func(dest ...any) error) (models.AlertEvent, error) {
	var ev models.AlertEvent
	var metric, state, deliveriesJSON string
	if err := scan(
		&ev.ID, &ev.RuleID, &ev.RuleName, &ev.HostID, &ev.Hostname, &metric, &state,
		&ev.Value, &ev.Threshold, &ev.StartedAt, &ev.NotifiedAt, &ev.ResolvedAt, &deliveriesJSON,
	); err != nil {
		return models.AlertEvent{}, err
	}
	ev.Metric = models.AlertMetric(metric)
	ev.State = models.AlertEventState(state)
	deliveries, err := unmarshalDeliveries(deliveriesJSON)
	if err != nil {
		return models.AlertEvent{}, fmt.Errorf("unmarshal deliveries: %w", err)
	}
	ev.Deliveries = deliveries
	return ev, nil
}

func marshalDeliveries(deliveries []models.Delivery) (string, error) {
	if deliveries == nil {
		deliveries = []models.Delivery{}
	}
	b, err := json.Marshal(deliveries)
	if err != nil {
		return "", fmt.Errorf("marshal deliveries: %w", err)
	}
	return string(b), nil
}

func unmarshalDeliveries(s string) ([]models.Delivery, error) {
	if s == "" {
		return []models.Delivery{}, nil
	}
	var deliveries []models.Delivery
	if err := json.Unmarshal([]byte(s), &deliveries); err != nil {
		return nil, err
	}
	if deliveries == nil {
		deliveries = []models.Delivery{}
	}
	return deliveries, nil
}
