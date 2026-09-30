package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// GetChannelHealthSettings returns the synthetic-test schedule for
// channelID, or a zero-value models.ChannelHealthSettings{ChannelID:
// channelID, Enabled: false} with a nil error if no row exists (the
// default: synthetic testing off, per SPEC-v0.8 §3-2's opt-in
// requirement).
func (db *DB) GetChannelHealthSettings(ctx context.Context, channelID int64) (models.ChannelHealthSettings, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT channel_id, enabled, weekday, minute_of_day, last_run_at, updated_at
		FROM channel_health_settings WHERE channel_id = ?
	`, channelID)
	s, err := scanChannelHealthSettings(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.ChannelHealthSettings{ChannelID: channelID}, nil
		}
		return models.ChannelHealthSettings{}, fmt.Errorf("storage: get channel health settings: %w", err)
	}
	return s, nil
}

// ListChannelHealthSettings returns every channel's stored synthetic-
// test schedule (channels with no row are simply absent — callers
// wanting a zero-value entry for those must consult
// ListNotifyChannels separately), sorted by channel_id.
func (db *DB) ListChannelHealthSettings(ctx context.Context) ([]models.ChannelHealthSettings, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT channel_id, enabled, weekday, minute_of_day, last_run_at, updated_at
		FROM channel_health_settings ORDER BY channel_id
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list channel health settings: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.ChannelHealthSettings, 0)
	for rows.Next() {
		s, err := scanChannelHealthSettings(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("storage: list channel health settings: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list channel health settings: %w", err)
	}
	return out, nil
}

// SetChannelHealthSettings upserts s's row for s.ChannelID (UpdatedAt
// refreshed to now unless the caller pre-set a non-zero value, matched
// with UpdateAlertRule-style "caller controls timestamps for tests"
// convention elsewhere in this package — most callers pass 0 and let
// this method fill it in). LastRunAt is preserved as given (0 to leave
// it untouched is not supported by SQL "upsert with excluded" — callers
// that only want to change the schedule, not LastRunAt, must read-
// modify-write via GetChannelHealthSettings first; see
// UpdateChannelHealthSettingsLastRun for the run-only update path).
func (db *DB) SetChannelHealthSettings(ctx context.Context, s models.ChannelHealthSettings) error {
	now := s.UpdatedAt
	if now == 0 {
		now = time.Now().Unix()
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO channel_health_settings (channel_id, enabled, weekday, minute_of_day, last_run_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (channel_id) DO UPDATE SET
			enabled = excluded.enabled,
			weekday = excluded.weekday,
			minute_of_day = excluded.minute_of_day,
			last_run_at = excluded.last_run_at,
			updated_at = excluded.updated_at
	`, s.ChannelID, s.Enabled, s.Weekday, s.MinuteOfDay, s.LastRunAt, now)
	if err != nil {
		return fmt.Errorf("storage: set channel health settings: %w", err)
	}
	return nil
}

// UpdateChannelHealthSettingsLastRun sets LastRunAt for channelID's
// existing row to lastRunAt, leaving every other field untouched. A
// no-op (not an error) if no row exists for channelID — a channel with
// synthetic testing never enabled has nothing to record a run against.
func (db *DB) UpdateChannelHealthSettingsLastRun(ctx context.Context, channelID int64, lastRunAt int64) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE channel_health_settings SET last_run_at = ? WHERE channel_id = ?
	`, lastRunAt, channelID)
	if err != nil {
		return fmt.Errorf("storage: update channel health settings last run: %w", err)
	}
	return nil
}

func scanChannelHealthSettings(scan func(dest ...any) error) (models.ChannelHealthSettings, error) {
	var s models.ChannelHealthSettings
	if err := scan(&s.ChannelID, &s.Enabled, &s.Weekday, &s.MinuteOfDay, &s.LastRunAt, &s.UpdatedAt); err != nil {
		return models.ChannelHealthSettings{}, err
	}
	return s, nil
}

// CreateSyntheticResult inserts r (ignoring r.ID) and returns the row
// with its assigned ID populated. Called at the start of a synthetic
// test run with FinishedAt == 0 (still running); the caller updates
// the same row via UpdateSyntheticResult once the test completes.
func (db *DB) CreateSyntheticResult(ctx context.Context, r models.SyntheticResult) (models.SyntheticResult, error) {
	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO channel_synthetic_results (channel_id, started_at, finished_at, outcome, diagnosis_code, detail)
		VALUES (?, ?, ?, ?, ?, ?)
	`, r.ChannelID, r.StartedAt, r.FinishedAt, string(r.Outcome), string(r.Diagnosis), r.Detail)
	if err != nil {
		return models.SyntheticResult{}, fmt.Errorf("storage: create synthetic result: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.SyntheticResult{}, fmt.Errorf("storage: create synthetic result: last insert id: %w", err)
	}
	r.ID = id
	return r, nil
}

// UpdateSyntheticResult replaces the stored result matching r.ID with r
// in full, or returns models.ErrNotFound if no result with that ID
// exists. Used to record a run's final outcome once the synthetic test
// completes (r.FinishedAt/Outcome/Diagnosis/Detail all set by then).
func (db *DB) UpdateSyntheticResult(ctx context.Context, r models.SyntheticResult) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE channel_synthetic_results SET
			finished_at = ?, outcome = ?, diagnosis_code = ?, detail = ?
		WHERE id = ?
	`, r.FinishedAt, string(r.Outcome), string(r.Diagnosis), r.Detail, r.ID)
	if err != nil {
		return fmt.Errorf("storage: update synthetic result: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: update synthetic result: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("storage: update synthetic result: %w", models.ErrNotFound)
	}
	return nil
}

// ListSyntheticResults returns up to limit of channelID's most recent
// completed (finished_at != 0) synthetic test results, newest first.
// limit <= 0 means no limit.
func (db *DB) ListSyntheticResults(ctx context.Context, channelID int64, limit int) ([]models.SyntheticResult, error) {
	query := `
		SELECT id, channel_id, started_at, finished_at, outcome, diagnosis_code, detail
		FROM channel_synthetic_results
		WHERE channel_id = ? AND finished_at != 0
		ORDER BY started_at DESC
	`
	args := []any{channelID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list synthetic results: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.SyntheticResult, 0)
	for rows.Next() {
		r, err := scanSyntheticResult(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("storage: list synthetic results: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list synthetic results: %w", err)
	}
	return out, nil
}

func scanSyntheticResult(scan func(dest ...any) error) (models.SyntheticResult, error) {
	var r models.SyntheticResult
	var outcome, diagnosis string
	if err := scan(&r.ID, &r.ChannelID, &r.StartedAt, &r.FinishedAt, &outcome, &diagnosis, &r.Detail); err != nil {
		return models.SyntheticResult{}, err
	}
	r.Outcome = models.SyntheticOutcome(outcome)
	r.Diagnosis = models.DiagnosisCode(diagnosis)
	return r, nil
}

// --- Failure incidents (fallback-notification de-duplication) ---

// channelFailureIncident mirrors one channel_failure_incidents row.
// Unexported: this table is an internal de-duplication mechanism for
// the channel-failure fallback notifier, not part of the public
// models.* API surface any handler returns directly.
type channelFailureIncident struct {
	ChannelID         int64
	OpenedAt          int64
	NotifiedAt        int64
	FallbackChannelID sql.NullInt64
	RecoveredAt       int64
}

// GetOpenChannelFailureIncident returns channelID's currently open
// (RecoveredAt == 0) incident row, or (zero value, false, nil) if none
// is open.
func (db *DB) GetOpenChannelFailureIncident(ctx context.Context, channelID int64) (channelFailureIncident, bool, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT channel_id, opened_at, notified_at, fallback_channel_id, recovered_at
		FROM channel_failure_incidents WHERE channel_id = ? AND recovered_at = 0
	`, channelID)
	var inc channelFailureIncident
	if err := row.Scan(&inc.ChannelID, &inc.OpenedAt, &inc.NotifiedAt, &inc.FallbackChannelID, &inc.RecoveredAt); err != nil {
		if isNoRows(err) {
			return channelFailureIncident{}, false, nil
		}
		return channelFailureIncident{}, false, fmt.Errorf("storage: get open channel failure incident: %w", err)
	}
	return inc, true, nil
}

// OpenChannelFailureIncident inserts (or, if one already exists for
// channelID regardless of recovered state — the PRIMARY KEY is
// channel_id alone, one row per channel at a time — replaces) a fresh
// open incident starting at openedAt with NotifiedAt/FallbackChannelID/
// RecoveredAt all zero/null. Callers must check
// GetOpenChannelFailureIncident first and only call this when no open
// incident already exists, to avoid resetting NotifiedAt on an
// already-notified incident.
func (db *DB) OpenChannelFailureIncident(ctx context.Context, channelID, openedAt int64) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO channel_failure_incidents (channel_id, opened_at, notified_at, fallback_channel_id, recovered_at)
		VALUES (?, ?, 0, NULL, 0)
		ON CONFLICT (channel_id) DO UPDATE SET
			opened_at = excluded.opened_at,
			notified_at = 0,
			fallback_channel_id = NULL,
			recovered_at = 0
	`, channelID, openedAt)
	if err != nil {
		return fmt.Errorf("storage: open channel failure incident: %w", err)
	}
	return nil
}

// MarkChannelFailureIncidentNotified records that channelID's open
// incident's fallback notification was sent via fallbackChannelID at
// notifiedAt.
func (db *DB) MarkChannelFailureIncidentNotified(ctx context.Context, channelID, notifiedAt, fallbackChannelID int64) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE channel_failure_incidents SET notified_at = ?, fallback_channel_id = ?
		WHERE channel_id = ? AND recovered_at = 0
	`, notifiedAt, fallbackChannelID, channelID)
	if err != nil {
		return fmt.Errorf("storage: mark channel failure incident notified: %w", err)
	}
	return nil
}

// RecoverChannelFailureIncident closes channelID's open incident (if
// any) by setting RecoveredAt to recoveredAt. A no-op if no open
// incident exists.
func (db *DB) RecoverChannelFailureIncident(ctx context.Context, channelID, recoveredAt int64) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE channel_failure_incidents SET recovered_at = ?
		WHERE channel_id = ? AND recovered_at = 0
	`, recoveredAt, channelID)
	if err != nil {
		return fmt.Errorf("storage: recover channel failure incident: %w", err)
	}
	return nil
}
