package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestOpen_UpgradeFrom0005AppliesV07Migration builds a database that has
// gone through exactly migrations 0001-0005 (a v0.6.0 database on
// disk), bypassing the embedded-migration runner to control the schema
// version precisely, then opens it through the normal Open path and
// asserts migration 0006_v07.sql applied: the new storage_accounts/
// storage_snapshots tables exist, update_jobs gained its platform
// column, pre-existing data is preserved, and re-running migrate() is a
// no-op.
func TestOpen_UpgradeFrom0005AppliesV07Migration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v06.db")

	legacyDSN := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	legacy, err := sql.Open("sqlite", legacyDSN)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	for _, name := range []string{
		"migrations/0001_init.sql",
		"migrations/0002_limits_settings.sql",
		"migrations/0003_sessions.sql",
		"migrations/0004_alerting.sql",
		"migrations/0005_v06.sql",
	} {
		content, err := migrationFS.ReadFile(name)
		if err != nil {
			_ = legacy.Close()
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := legacy.ExecContext(ctx, string(content)); err != nil {
			_ = legacy.Close()
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		_ = legacy.Close()
		t.Fatalf("create schema_migrations: %v", err)
	}
	for v := 1; v <= 5; v++ {
		if _, err := legacy.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, time.Now().Unix(),
		); err != nil {
			_ = legacy.Close()
			t.Fatalf("insert schema_migrations row %d: %v", v, err)
		}
	}
	// A pre-existing v0.6.0 update_jobs row must survive the upgrade
	// with platform defaulting to "" (the new column's DEFAULT).
	if _, err := legacy.ExecContext(ctx,
		`INSERT INTO update_jobs (batch_id, host_id, target, state, created_at, updated_at) VALUES ('b1', 'h1', 'latest', 'queued', 1000, 1000)`,
	); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy update_jobs row: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	// The pre-existing update_jobs row must still be present, with
	// platform defaulted to "".
	var platform string
	if err := db.sql.QueryRowContext(ctx, `SELECT platform FROM update_jobs WHERE batch_id = 'b1'`).Scan(&platform); err != nil {
		t.Fatalf("scan preserved update_jobs row: %v", err)
	}
	if platform != "" {
		t.Errorf("platform = %q, want \"\" (default for a pre-migration row)", platform)
	}

	// New v0.7.0 tables must exist and be queryable.
	for _, table := range []string{"storage_accounts", "storage_snapshots"} {
		var count int
		if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Errorf("query %s: %v", table, err)
		}
	}

	// Inserting a new job with a platform value must work.
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO update_jobs (batch_id, host_id, target, state, platform, created_at, updated_at) VALUES ('b2', 'h2', 'latest', 'queued', 'darwin', 2000, 2000)`,
	); err != nil {
		t.Fatalf("insert new update_jobs row with platform: %v", err)
	}

	// A second application of migrate() (simulating a second process
	// start) must be a no-op: no error, no duplicate column.
	if err := db.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

// TestOpen_FreshDBAppliesV07Migration is a smoke check that a brand-new
// database (no upgrade path) also ends up with the v0.7.0 tables/column
// via the normal all-migrations-from-scratch path every other storage
// test already exercises indirectly through Open.
func TestOpen_FreshDBAppliesV07Migration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fresh-v07.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	for _, table := range []string{"storage_accounts", "storage_snapshots"} {
		var count int
		if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Errorf("query %s: %v", table, err)
		}
	}

	var platform string
	row := db.sql.QueryRowContext(ctx, `SELECT platform FROM update_jobs LIMIT 1`)
	err = row.Scan(&platform)
	if err != nil && err != sql.ErrNoRows {
		t.Errorf("query update_jobs.platform: %v", err)
	}
}
