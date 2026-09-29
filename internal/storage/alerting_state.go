package storage

import (
	"context"
	"fmt"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// GetAlertState returns the persisted state-machine row for (ruleID,
// hostID), or a zero-value models.AlertState{RuleID, HostID, State:
// models.AlertStateOK} with a nil error if no row exists yet.
func (db *DB) GetAlertState(ctx context.Context, ruleID int64, hostID string) (models.AlertState, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT state, since, last_notified, last_value
		FROM alert_state WHERE rule_id = ? AND host_id = ?
	`, ruleID, hostID)

	var state string
	var st models.AlertState
	if err := row.Scan(&state, &st.Since, &st.LastNotified, &st.LastValue); err != nil {
		if isNoRows(err) {
			return models.AlertState{RuleID: ruleID, HostID: hostID, State: models.AlertStateOK}, nil
		}
		return models.AlertState{}, fmt.Errorf("storage: get alert state: %w", err)
	}
	st.RuleID = ruleID
	st.HostID = hostID
	st.State = models.AlertRuleState(state)
	return st, nil
}

// SetAlertState upserts the state-machine row for (st.RuleID, st.HostID).
func (db *DB) SetAlertState(ctx context.Context, st models.AlertState) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO alert_state (rule_id, host_id, state, since, last_notified, last_value)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(rule_id, host_id) DO UPDATE SET
			state = excluded.state,
			since = excluded.since,
			last_notified = excluded.last_notified,
			last_value = excluded.last_value
	`, st.RuleID, st.HostID, string(st.State), st.Since, st.LastNotified, st.LastValue)
	if err != nil {
		return fmt.Errorf("storage: set alert state: %w", err)
	}
	return nil
}

// ListAlertStates returns every persisted state-machine row whose State
// is not models.AlertStateOK (pending or firing).
func (db *DB) ListAlertStates(ctx context.Context) ([]models.AlertState, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT rule_id, host_id, state, since, last_notified, last_value
		FROM alert_state WHERE state <> 'ok' ORDER BY rule_id, host_id
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list alert states: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.AlertState, 0)
	for rows.Next() {
		var st models.AlertState
		var state string
		if err := rows.Scan(&st.RuleID, &st.HostID, &state, &st.Since, &st.LastNotified, &st.LastValue); err != nil {
			return nil, fmt.Errorf("storage: list alert states: %w", err)
		}
		st.State = models.AlertRuleState(state)
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list alert states: %w", err)
	}
	return out, nil
}

// DeleteAlertStatesForRule removes every state-machine row for ruleID.
func (db *DB) DeleteAlertStatesForRule(ctx context.Context, ruleID int64) error {
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM alert_state WHERE rule_id = ?`, ruleID); err != nil {
		return fmt.Errorf("storage: delete alert states for rule: %w", err)
	}
	return nil
}
