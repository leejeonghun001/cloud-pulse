// Package storage implements the hub's persistence layer on top of
// modernc.org/sqlite (a CGO-free SQLite driver). It provides a single type,
// DB, whose method set satisfies the Store interface consumed by
// internal/hub: host/sample ingestion with idempotent inserts, tiered
// rollups (raw/5m/1h), retention pruning, egress accounting, and cloud
// bucket statistics.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB is a SQLite-backed implementation of the hub's Store interface. A DB
// wraps a single *sql.DB connection (MaxOpenConns(1)) so that all access is
// serialized, which is the concurrency model SQLite performs best under and
// avoids relying on cgo-only features.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path, applies
// any pending embedded migrations, and returns a ready DB. The parent
// directory of path is created with mode 0o750 if missing. Callers must
// call Close when done.
func Open(ctx context.Context, path string) (*DB, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("storage: create data dir: %w", err)
		}
	}

	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close() // best-effort close after a failed ping, nothing to recover
		return nil, fmt.Errorf("storage: ping: %w", err)
	}

	db := &DB{sql: sqlDB}
	if err := db.migrate(ctx); err != nil {
		_ = sqlDB.Close() // best-effort close after a failed migration, nothing to recover
		return nil, err
	}
	return db, nil
}

// Close releases the underlying database connection.
func (db *DB) Close() error {
	if err := db.sql.Close(); err != nil {
		return fmt.Errorf("storage: close: %w", err)
	}
	return nil
}

// migrate applies every embedded migration in filename order that has not
// yet been recorded in schema_migrations, each inside its own transaction.
func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("storage: create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("storage: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	applied := make(map[int]bool)
	rows, err := db.sql.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("storage: query schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close() // best-effort close; the Scan error below is returned instead
			return fmt.Errorf("storage: scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close() // best-effort close; rows.Err() below is returned instead
		return fmt.Errorf("storage: scan schema_migrations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("storage: close schema_migrations rows: %w", err)
	}

	for i, name := range names {
		version := i + 1
		if applied[version] {
			continue
		}
		if err := db.applyMigration(ctx, version, name); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) applyMigration(ctx context.Context, version int, name string) error {
	content, err := migrationFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("storage: read migration %s: %w", name, err)
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin migration %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }() // best-effort rollback; no-op after a successful Commit

	if _, err := tx.ExecContext(ctx, string(content)); err != nil {
		return fmt.Errorf("storage: apply migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		version, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("storage: record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit migration %s: %w", name, err)
	}
	return nil
}

// isNoRows reports whether err is sql.ErrNoRows.
func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
