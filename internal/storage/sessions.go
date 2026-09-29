package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// CreateSession persists a new dashboard login session.
func (db *DB) CreateSession(ctx context.Context, sess models.Session) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO sessions (id_hash, created_at, last_seen, expires_at, remote, user_agent)
		VALUES (?, ?, ?, ?, ?, ?)
	`, sess.IDHash, sess.CreatedAt, sess.LastSeen, sess.ExpiresAt, sess.Remote, sess.UserAgent)
	if err != nil {
		return fmt.Errorf("storage: create session: %w", err)
	}
	return nil
}

// GetSession returns the session identified by idHash, or
// models.ErrNotFound if no such session exists.
func (db *DB) GetSession(ctx context.Context, idHash string) (models.Session, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id_hash, created_at, last_seen, expires_at, remote, user_agent
		FROM sessions WHERE id_hash = ?
	`, idHash)

	var sess models.Session
	if err := row.Scan(&sess.IDHash, &sess.CreatedAt, &sess.LastSeen, &sess.ExpiresAt, &sess.Remote, &sess.UserAgent); err != nil {
		if isNoRows(err) {
			return models.Session{}, models.ErrNotFound
		}
		return models.Session{}, fmt.Errorf("storage: get session: %w", err)
	}
	return sess, nil
}

// TouchSession updates the session's LastSeen/ExpiresAt (sliding idle
// expiry).
func (db *DB) TouchSession(ctx context.Context, idHash string, lastSeen, expiresAt int64) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE sessions SET last_seen = ?, expires_at = ? WHERE id_hash = ?
	`, lastSeen, expiresAt, idHash)
	if err != nil {
		return fmt.Errorf("storage: touch session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: touch session: rows affected: %w", err)
	}
	if n == 0 {
		return models.ErrNotFound
	}
	return nil
}

// DeleteSession removes one session by idHash. Deleting a non-existent
// session is not an error.
func (db *DB) DeleteSession(ctx context.Context, idHash string) error {
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash); err != nil {
		return fmt.Errorf("storage: delete session: %w", err)
	}
	return nil
}

// DeleteSessionsExcept removes every session except keepIDHash (an
// empty keepIDHash deletes all sessions), returning the number deleted.
func (db *DB) DeleteSessionsExcept(ctx context.Context, keepIDHash string) (int, error) {
	var (
		res interface {
			RowsAffected() (int64, error)
		}
		err error
	)
	if keepIDHash == "" {
		res, err = db.sql.ExecContext(ctx, `DELETE FROM sessions`)
	} else {
		res, err = db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash != ?`, keepIDHash)
	}
	if err != nil {
		return 0, fmt.Errorf("storage: delete sessions except: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: delete sessions except: rows affected: %w", err)
	}
	return int(n), nil
}

// ListSessions returns every stored session.
func (db *DB) ListSessions(ctx context.Context) ([]models.Session, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id_hash, created_at, last_seen, expires_at, remote, user_agent
		FROM sessions ORDER BY id_hash
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; the loop below returns any scan/iteration error

	out := make([]models.Session, 0)
	for rows.Next() {
		var sess models.Session
		if err := rows.Scan(&sess.IDHash, &sess.CreatedAt, &sess.LastSeen, &sess.ExpiresAt, &sess.Remote, &sess.UserAgent); err != nil {
			return nil, fmt.Errorf("storage: list sessions: scan: %w", err)
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list sessions: %w", err)
	}
	return out, nil
}

// PruneSessions deletes sessions whose ExpiresAt is at or before now.
func (db *DB) PruneSessions(ctx context.Context, now time.Time) error {
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		return fmt.Errorf("storage: prune sessions: %w", err)
	}
	return nil
}
