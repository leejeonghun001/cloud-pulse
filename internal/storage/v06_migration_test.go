package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestOpen_UpgradeFrom0004AppliesV06Migration builds a database that has
// gone through exactly migrations 0001-0004 (a v0.5.0 database on disk),
// bypassing the embedded-migration runner to control the schema version
// precisely, then opens it through the normal Open path and asserts
// migration 0005_v06.sql applied: the new tables exist, the four
// built-in pricing plans were seeded exactly once, and re-running
// migrate() is a no-op.
func TestOpen_UpgradeFrom0004AppliesV06Migration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v05.db")

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
	for v := 1; v <= 4; v++ {
		if _, err := legacy.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, time.Now().Unix(),
		); err != nil {
			_ = legacy.Close()
			t.Fatalf("insert schema_migrations row %d: %v", v, err)
		}
	}
	// A pre-existing v0.5.0 host row must survive the upgrade untouched
	// (0005_v06.sql adds only new tables, no shape change to hosts).
	if _, err := legacy.ExecContext(ctx,
		`INSERT INTO hosts (id, info_json, last_seen) VALUES ('h1', '{}', 1000)`,
	); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy host row: %v", err)
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

	// The pre-existing host row must still be present post-upgrade.
	var lastSeen int64
	if err := db.sql.QueryRowContext(ctx, `SELECT last_seen FROM hosts WHERE id = 'h1'`).Scan(&lastSeen); err != nil {
		t.Fatalf("scan preserved host row: %v", err)
	}
	if lastSeen != 1000 {
		t.Errorf("last_seen = %d, want 1000 (preserved)", lastSeen)
	}

	// New v0.6.0 tables must exist and be queryable.
	for _, table := range []string{"update_jobs", "pricing_plans", "host_pricing", "cloud_cost_snapshots", "audit_log"} {
		var count int
		if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Errorf("query %s: %v", table, err)
		}
	}

	// The four built-in pricing plans must be seeded exactly once.
	var builtinCount int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM pricing_plans WHERE builtin = 1`).Scan(&builtinCount); err != nil {
		t.Fatalf("count builtin pricing_plans: %v", err)
	}
	if builtinCount != 4 {
		t.Errorf("builtin pricing_plans count = %d, want 4", builtinCount)
	}

	var awsFreeGB float64
	if err := db.sql.QueryRowContext(ctx,
		`SELECT egress_free_gb FROM pricing_plans WHERE provider = 'aws'`,
	).Scan(&awsFreeGB); err != nil {
		t.Fatalf("query aws plan: %v", err)
	}
	if awsFreeGB != 100 {
		t.Errorf("aws plan egress_free_gb = %v, want 100", awsFreeGB)
	}

	// A second application of migrate() (simulating a second process
	// start) must be a no-op: no duplicate rows, no error.
	if err := db.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	var builtinCountAfter int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM pricing_plans WHERE builtin = 1`).Scan(&builtinCountAfter); err != nil {
		t.Fatalf("count builtin pricing_plans after re-migrate: %v", err)
	}
	if builtinCountAfter != 4 {
		t.Errorf("builtin pricing_plans count after re-migrate = %d, want 4 (no duplicate seeding)", builtinCountAfter)
	}
}

// TestOpen_FreshDBAppliesV06Migration is a smoke check that a brand-new
// database (no upgrade path) also ends up with the v0.6.0 tables and
// seeded plans, via the normal all-migrations-from-scratch path every
// other storage test already exercises indirectly through Open.
func TestOpen_FreshDBAppliesV06Migration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fresh.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	var count int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM pricing_plans WHERE builtin = 1`).Scan(&count); err != nil {
		t.Fatalf("count builtin pricing_plans: %v", err)
	}
	if count != 4 {
		t.Errorf("builtin pricing_plans count = %d, want 4", count)
	}
}
