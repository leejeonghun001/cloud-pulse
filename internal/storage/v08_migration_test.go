package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// TestOpen_UpgradeFrom0006AppliesV08Migration builds a database that has
// gone through exactly migrations 0001-0006 (a v0.7.0 database on
// disk), bypassing the embedded-migration runner to control the schema
// version precisely, then opens it through the normal Open path and
// asserts migration 0007_v08.sql applied: the new channel_health_settings/
// channel_synthetic_results tables exist, update_jobs gained its
// (batch_id, created_at) index, pre-existing data is preserved, and
// re-running migrate() is a no-op.
func TestOpen_UpgradeFrom0006AppliesV08Migration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v07.db")

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
		"migrations/0006_v07.sql",
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
	for v := 1; v <= 6; v++ {
		if _, err := legacy.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, time.Now().Unix(),
		); err != nil {
			_ = legacy.Close()
			t.Fatalf("insert schema_migrations row %d: %v", v, err)
		}
	}
	// A pre-existing v0.7.0 notify_channels row must survive the
	// upgrade so channel_health_settings can reference it.
	if _, err := legacy.ExecContext(ctx,
		`INSERT INTO notify_channels (name, type, enabled, config, created_at, updated_at) VALUES ('c1', 'discord', 1, '{}', 1000, 1000)`,
	); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy notify_channels row: %v", err)
	}
	// A pre-existing v0.6.0+ update_jobs row must survive the upgrade
	// with the new index usable against it.
	if _, err := legacy.ExecContext(ctx,
		`INSERT INTO update_jobs (batch_id, host_id, target, state, platform, created_at, updated_at) VALUES ('b1', 'h1', 'latest', 'queued', 'linux', 1000, 1000)`,
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

	// The pre-existing update_jobs row must still be present.
	var target string
	if err := db.sql.QueryRowContext(ctx, `SELECT target FROM update_jobs WHERE batch_id = 'b1'`).Scan(&target); err != nil {
		t.Fatalf("scan preserved update_jobs row: %v", err)
	}
	if target != "latest" {
		t.Errorf("target = %q, want %q", target, "latest")
	}

	// New v0.8.0 tables must exist and be queryable.
	for _, table := range []string{"channel_health_settings", "channel_synthetic_results", "channel_failure_incidents"} {
		var count int
		if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Errorf("query %s: %v", table, err)
		}
	}

	// Inserting a channel_health_settings row referencing the
	// pre-existing channel must work (foreign key + WITHOUT ROWID
	// primary key on channel_id).
	var channelID int64
	if err := db.sql.QueryRowContext(ctx, `SELECT id FROM notify_channels WHERE name = 'c1'`).Scan(&channelID); err != nil {
		t.Fatalf("look up legacy channel id: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO channel_health_settings (channel_id, enabled, weekday, minute_of_day, updated_at) VALUES (?, 1, 1, 540, 2000)`,
		channelID,
	); err != nil {
		t.Fatalf("insert channel_health_settings row: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO channel_synthetic_results (channel_id, started_at, finished_at, outcome, diagnosis_code, detail) VALUES (?, 2000, 2001, 'verified', '', '')`,
		channelID,
	); err != nil {
		t.Fatalf("insert channel_synthetic_results row: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO channel_failure_incidents (channel_id, opened_at, notified_at) VALUES (?, 2000, 2000)`,
		channelID,
	); err != nil {
		t.Fatalf("insert channel_failure_incidents row: %v", err)
	}

	// Deleting the channel must cascade into both new tables.
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM notify_channels WHERE id = ?`, channelID); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	var remaining int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM channel_health_settings WHERE channel_id = ?`, channelID).Scan(&remaining); err != nil {
		t.Fatalf("count channel_health_settings after cascade: %v", err)
	}
	if remaining != 0 {
		t.Errorf("channel_health_settings rows after cascade delete = %d, want 0", remaining)
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM channel_synthetic_results WHERE channel_id = ?`, channelID).Scan(&remaining); err != nil {
		t.Fatalf("count channel_synthetic_results after cascade: %v", err)
	}
	if remaining != 0 {
		t.Errorf("channel_synthetic_results rows after cascade delete = %d, want 0", remaining)
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM channel_failure_incidents WHERE channel_id = ?`, channelID).Scan(&remaining); err != nil {
		t.Fatalf("count channel_failure_incidents after cascade: %v", err)
	}
	if remaining != 0 {
		t.Errorf("channel_failure_incidents rows after cascade delete = %d, want 0", remaining)
	}

	// Inserting a new update_jobs row must still work, and the new
	// (batch_id, created_at) index must be usable by the query planner
	// without error (functional check; EXPLAIN QUERY PLAN isn't
	// asserted on to avoid coupling the test to SQLite's planner
	// internals).
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO update_jobs (batch_id, host_id, target, state, platform, created_at, updated_at) VALUES ('b2', 'h2', 'latest', 'queued', 'darwin', 3000, 3000)`,
	); err != nil {
		t.Fatalf("insert new update_jobs row: %v", err)
	}
	var batchCount int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM update_jobs WHERE batch_id = 'b2' AND created_at >= 0 ORDER BY created_at`,
	).Scan(&batchCount); err != nil {
		t.Fatalf("query update_jobs via new index columns: %v", err)
	}
	if batchCount != 1 {
		t.Errorf("batchCount = %d, want 1", batchCount)
	}

	// A second application of migrate() (simulating a second process
	// start) must be a no-op: no error, no duplicate table/index.
	if err := db.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

func TestOpen_UpgradeFrom0007BackfillsRuleAndEventSeverity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v08.db")
	legacyDSN := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	legacy, err := sql.Open("sqlite", legacyDSN)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	for _, name := range []string{
		"migrations/0001_init.sql", "migrations/0002_limits_settings.sql", "migrations/0003_sessions.sql",
		"migrations/0004_alerting.sql", "migrations/0005_v06.sql", "migrations/0006_v07.sql", "migrations/0007_v08.sql",
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
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		_ = legacy.Close()
		t.Fatalf("create schema_migrations: %v", err)
	}
	for version := 1; version <= 7; version++ {
		if _, err := legacy.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, 1000); err != nil {
			_ = legacy.Close()
			t.Fatalf("record legacy migration %d: %v", version, err)
		}
	}
	legacyMetrics := []string{"host_down", "update_batch_failed", "cpu"}
	for _, metric := range legacyMetrics {
		if _, err := legacy.ExecContext(ctx, `INSERT INTO alert_rules (name, enabled, metric, operator, threshold, created_at, updated_at) VALUES (?, 1, ?, '>', 0, 1000, 1000)`, metric, metric); err != nil {
			_ = legacy.Close()
			t.Fatalf("insert legacy %s rule: %v", metric, err)
		}
		if _, err := legacy.ExecContext(ctx, `INSERT INTO alert_events (rule_id, rule_name, metric, state, started_at) VALUES (?, ?, ?, 'firing', 1000)`, 1, metric, metric); err != nil {
			_ = legacy.Close()
			t.Fatalf("insert legacy %s event: %v", metric, err)
		}
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
	rules, err := db.ListAlertRules(ctx)
	if err != nil {
		t.Fatalf("ListAlertRules: %v", err)
	}
	wantCritical := map[models.AlertMetric]bool{models.AlertMetricHostDown: true, models.AlertMetricUpdateBatchFailed: true}
	// Earlier migrations seed their own default rules; check only the
	// rules inserted above (named after their metric), but all of them.
	isLegacy := map[string]bool{}
	for _, metric := range legacyMetrics {
		isLegacy[metric] = true
	}
	checked := 0
	for _, rule := range rules {
		if !isLegacy[rule.Name] {
			continue
		}
		checked++
		want := models.AlertSeverityWarning
		if wantCritical[rule.Metric] {
			want = models.AlertSeverityCritical
		}
		if rule.Severity != want {
			t.Errorf("rule %q severity = %q, want %q", rule.Name, rule.Severity, want)
		}
	}
	if checked != len(legacyMetrics) {
		t.Fatalf("legacy rules after upgrade = %d, want %d", checked, len(legacyMetrics))
	}
	events, err := db.ListAlertEvents(ctx, "", "", 0, 0)
	if err != nil {
		t.Fatalf("ListAlertEvents: %v", err)
	}
	if len(events) != len(legacyMetrics) {
		t.Fatalf("events after upgrade = %d, want %d", len(events), len(legacyMetrics))
	}
	for _, event := range events {
		want := models.AlertSeverityWarning
		if wantCritical[event.Metric] {
			want = models.AlertSeverityCritical
		}
		if event.Severity != want {
			t.Errorf("event metric %q severity = %q, want %q", event.Metric, event.Severity, want)
		}
	}
}

// TestOpen_FreshDBAppliesV08Migration is a smoke check that a brand-new
// database (no upgrade path) also ends up with the v0.8.0 tables/index
// via the normal all-migrations-from-scratch path every other storage
// test already exercises indirectly through Open.
func TestOpen_FreshDBAppliesV08Migration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fresh-v08.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	for _, table := range []string{"channel_health_settings", "channel_synthetic_results", "channel_failure_incidents"} {
		var count int
		if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Errorf("query %s: %v", table, err)
		}
	}

	var indexCount int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_update_jobs_batch_created'`,
	).Scan(&indexCount); err != nil {
		t.Fatalf("query sqlite_master for new index: %v", err)
	}
	if indexCount != 1 {
		t.Errorf("idx_update_jobs_batch_created present = %d, want 1", indexCount)
	}
}

// TestOpen_SeededEgressRulesSeverityMatchesName checks that the three
// egress rules 0004 seeds carry the severity their names promise once
// 0008 has run.
func TestOpen_SeededEgressRulesSeverityMatchesName(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	rules, err := db.ListAlertRules(context.Background())
	if err != nil {
		t.Fatalf("ListAlertRules: %v", err)
	}
	want := map[string]models.AlertSeverity{
		"Outbound traffic 80% (warning)":   models.AlertSeverityWarning,
		"Outbound traffic 95% (critical)":  models.AlertSeverityCritical,
		"Outbound traffic 100% (exceeded)": models.AlertSeverityCritical,
	}
	for _, rule := range rules {
		w, ok := want[rule.Name]
		if !ok {
			continue
		}
		if rule.Severity != w {
			t.Errorf("rule %q severity = %q, want %q", rule.Name, rule.Severity, w)
		}
		delete(want, rule.Name)
	}
	if len(want) != 0 {
		t.Fatalf("seeded rules missing: %v", want)
	}
}
