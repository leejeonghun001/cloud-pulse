package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestAlertRules_CRUD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	r := models.AlertRule{
		Name:           "CPU high",
		Enabled:        true,
		Metric:         models.AlertMetricCPU,
		HostID:         "",
		Operator:       models.AlertOperatorGT,
		Threshold:      90,
		DurationSec:    300,
		CooldownSec:    3600,
		NotifyResolved: true,
		ChannelIDs:     []int64{1, 2},
	}

	created, err := db.CreateAlertRule(ctx, r)
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("CreateAlertRule: ID not assigned")
	}
	if created.CreatedAt == 0 || created.UpdatedAt == 0 {
		t.Fatal("CreateAlertRule: timestamps not populated")
	}

	got, err := db.GetAlertRule(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAlertRule: %v", err)
	}
	if got.Name != r.Name || got.Threshold != r.Threshold || len(got.ChannelIDs) != 2 {
		t.Fatalf("GetAlertRule = %+v, want matching %+v", got, r)
	}

	list, err := db.ListAlertRules(ctx)
	if err != nil {
		t.Fatalf("ListAlertRules: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("ListAlertRules len = %d, want 4 (1 created + 3 migration-seeded defaults)", len(list))
	}

	got.Threshold = 95
	got.ChannelIDs = []int64{3}
	updated, err := db.UpdateAlertRule(ctx, got)
	if err != nil {
		t.Fatalf("UpdateAlertRule: %v", err)
	}
	if updated.Threshold != 95 || len(updated.ChannelIDs) != 1 || updated.ChannelIDs[0] != 3 {
		t.Fatalf("UpdateAlertRule result = %+v, want threshold=95 channel_ids=[3]", updated)
	}
	if updated.CreatedAt != created.CreatedAt {
		t.Errorf("UpdateAlertRule changed CreatedAt: got %d, want %d", updated.CreatedAt, created.CreatedAt)
	}

	if _, err := db.UpdateAlertRule(ctx, models.AlertRule{ID: 99999}); !isModelsNotFound(err) {
		t.Fatalf("UpdateAlertRule missing id: err = %v, want ErrNotFound", err)
	}

	if err := db.DeleteAlertRule(ctx, created.ID); err != nil {
		t.Fatalf("DeleteAlertRule: %v", err)
	}
	if _, err := db.GetAlertRule(ctx, created.ID); !isModelsNotFound(err) {
		t.Fatalf("GetAlertRule after delete: err = %v, want ErrNotFound", err)
	}
	// Deleting again must not error.
	if err := db.DeleteAlertRule(ctx, created.ID); err != nil {
		t.Fatalf("DeleteAlertRule (already deleted): %v", err)
	}
}

func TestAlertRules_EmptyChannelIDsRoundTripsAsEmptySlice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	created, err := db.CreateAlertRule(ctx, models.AlertRule{Name: "no channels", Metric: models.AlertMetricMemory})
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}
	if created.ChannelIDs == nil || len(created.ChannelIDs) != 0 {
		t.Fatalf("ChannelIDs = %#v, want non-nil empty slice", created.ChannelIDs)
	}

	got, err := db.GetAlertRule(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAlertRule: %v", err)
	}
	if got.ChannelIDs == nil || len(got.ChannelIDs) != 0 {
		t.Fatalf("reloaded ChannelIDs = %#v, want non-nil empty slice", got.ChannelIDs)
	}
}

func TestNotifyChannels_CRUD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	ch := models.NotifyChannel{
		Name:    "ops discord",
		Type:    models.NotifyChannelDiscord,
		Enabled: true,
		Config:  map[string]string{"webhook_url": "https://discord.com/api/webhooks/123/abc"},
	}
	created, err := db.CreateNotifyChannel(ctx, ch)
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("CreateNotifyChannel: ID not assigned")
	}

	got, err := db.GetNotifyChannel(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetNotifyChannel: %v", err)
	}
	if got.Config["webhook_url"] != ch.Config["webhook_url"] {
		t.Fatalf("GetNotifyChannel Config = %v, want %v", got.Config, ch.Config)
	}

	list, err := db.ListNotifyChannels(ctx)
	if err != nil {
		t.Fatalf("ListNotifyChannels: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListNotifyChannels len = %d, want 1", len(list))
	}
	// ListNotifyChannels returns Config unredacted per the Store
	// interface's doc comment.
	if list[0].Config["webhook_url"] != ch.Config["webhook_url"] {
		t.Fatalf("ListNotifyChannels did not return unredacted config: %v", list[0].Config)
	}

	got.Name = "ops discord (renamed)"
	got.Config["webhook_url"] = "https://discord.com/api/webhooks/456/def"
	updated, err := db.UpdateNotifyChannel(ctx, got)
	if err != nil {
		t.Fatalf("UpdateNotifyChannel: %v", err)
	}
	if updated.Name != "ops discord (renamed)" || updated.Config["webhook_url"] != "https://discord.com/api/webhooks/456/def" {
		t.Fatalf("UpdateNotifyChannel result = %+v", updated)
	}

	if _, err := db.UpdateNotifyChannel(ctx, models.NotifyChannel{ID: 99999}); !isModelsNotFound(err) {
		t.Fatalf("UpdateNotifyChannel missing id: err = %v, want ErrNotFound", err)
	}

	if err := db.DeleteNotifyChannel(ctx, created.ID); err != nil {
		t.Fatalf("DeleteNotifyChannel: %v", err)
	}
	if _, err := db.GetNotifyChannel(ctx, created.ID); !isModelsNotFound(err) {
		t.Fatalf("GetNotifyChannel after delete: err = %v, want ErrNotFound", err)
	}
	if err := db.DeleteNotifyChannel(ctx, created.ID); err != nil {
		t.Fatalf("DeleteNotifyChannel (already deleted): %v", err)
	}
}

func TestAlertState_GetDefaultsToOK(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	st, err := db.GetAlertState(ctx, 42, "host-1")
	if err != nil {
		t.Fatalf("GetAlertState: %v", err)
	}
	want := models.AlertState{RuleID: 42, HostID: "host-1", State: models.AlertStateOK}
	if st != want {
		t.Fatalf("GetAlertState (missing row) = %+v, want %+v", st, want)
	}
}

func TestAlertState_SetGetRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	st := models.AlertState{
		RuleID:       7,
		HostID:       "host-a",
		State:        models.AlertStatePending,
		Since:        1000,
		LastNotified: 0,
		LastValue:    91.5,
	}
	if err := db.SetAlertState(ctx, st); err != nil {
		t.Fatalf("SetAlertState: %v", err)
	}
	got, err := db.GetAlertState(ctx, 7, "host-a")
	if err != nil {
		t.Fatalf("GetAlertState: %v", err)
	}
	if got != st {
		t.Fatalf("GetAlertState = %+v, want %+v", got, st)
	}

	// Upsert: same key, different values.
	st.State = models.AlertStateFiring
	st.LastNotified = 2000
	if err := db.SetAlertState(ctx, st); err != nil {
		t.Fatalf("SetAlertState (update): %v", err)
	}
	got, err = db.GetAlertState(ctx, 7, "host-a")
	if err != nil {
		t.Fatalf("GetAlertState (after update): %v", err)
	}
	if got.State != models.AlertStateFiring || got.LastNotified != 2000 {
		t.Fatalf("GetAlertState (after update) = %+v, want firing/2000", got)
	}
}

func TestAlertState_ListOnlyReturnsNonOK(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	if err := db.SetAlertState(ctx, models.AlertState{RuleID: 1, HostID: "h1", State: models.AlertStateOK}); err != nil {
		t.Fatalf("SetAlertState ok: %v", err)
	}
	if err := db.SetAlertState(ctx, models.AlertState{RuleID: 1, HostID: "h2", State: models.AlertStatePending}); err != nil {
		t.Fatalf("SetAlertState pending: %v", err)
	}
	if err := db.SetAlertState(ctx, models.AlertState{RuleID: 2, HostID: "h1", State: models.AlertStateFiring}); err != nil {
		t.Fatalf("SetAlertState firing: %v", err)
	}

	list, err := db.ListAlertStates(ctx)
	if err != nil {
		t.Fatalf("ListAlertStates: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListAlertStates len = %d, want 2 (ok row excluded)", len(list))
	}
	for _, st := range list {
		if st.State == models.AlertStateOK {
			t.Errorf("ListAlertStates returned an ok-state row: %+v", st)
		}
	}
}

func TestAlertState_DeleteStatesForRule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	if err := db.SetAlertState(ctx, models.AlertState{RuleID: 1, HostID: "h1", State: models.AlertStateFiring}); err != nil {
		t.Fatalf("SetAlertState: %v", err)
	}
	if err := db.SetAlertState(ctx, models.AlertState{RuleID: 1, HostID: "h2", State: models.AlertStatePending}); err != nil {
		t.Fatalf("SetAlertState: %v", err)
	}
	if err := db.SetAlertState(ctx, models.AlertState{RuleID: 2, HostID: "h1", State: models.AlertStateFiring}); err != nil {
		t.Fatalf("SetAlertState: %v", err)
	}

	if err := db.DeleteAlertStatesForRule(ctx, 1); err != nil {
		t.Fatalf("DeleteAlertStatesForRule: %v", err)
	}

	list, err := db.ListAlertStates(ctx)
	if err != nil {
		t.Fatalf("ListAlertStates: %v", err)
	}
	if len(list) != 1 || list[0].RuleID != 2 {
		t.Fatalf("ListAlertStates after delete = %+v, want only rule 2's row", list)
	}
}

func TestAlertEvents_CreateUpdateGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	ev := models.AlertEvent{
		RuleID:     1,
		RuleName:   "CPU high",
		HostID:     "host-1",
		Hostname:   "box1",
		Metric:     models.AlertMetricCPU,
		State:      models.AlertEventFiring,
		Value:      95.5,
		Threshold:  90,
		StartedAt:  1000,
		NotifiedAt: 1001,
	}
	created, err := db.CreateAlertEvent(ctx, ev)
	if err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("CreateAlertEvent: ID not assigned")
	}
	if created.Deliveries == nil {
		t.Fatal("CreateAlertEvent: Deliveries is nil, want non-nil empty slice")
	}

	got, err := db.GetAlertEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAlertEvent: %v", err)
	}
	if got.RuleName != ev.RuleName || got.Value != ev.Value {
		t.Fatalf("GetAlertEvent = %+v", got)
	}

	active, err := db.GetActiveAlertEvent(ctx, ev.RuleID, ev.HostID)
	if err != nil {
		t.Fatalf("GetActiveAlertEvent: %v", err)
	}
	if active.ID != created.ID {
		t.Fatalf("GetActiveAlertEvent returned id %d, want %d", active.ID, created.ID)
	}

	got.Deliveries = []models.Delivery{{ChannelID: 5, ChannelName: "discord", OK: true, At: 1002}}
	got.State = models.AlertEventResolved
	got.ResolvedAt = 2000
	if err := db.UpdateAlertEvent(ctx, got); err != nil {
		t.Fatalf("UpdateAlertEvent: %v", err)
	}

	reloaded, err := db.GetAlertEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetAlertEvent (after update): %v", err)
	}
	if reloaded.State != models.AlertEventResolved || reloaded.ResolvedAt != 2000 {
		t.Fatalf("GetAlertEvent (after update) = %+v", reloaded)
	}
	if len(reloaded.Deliveries) != 1 || reloaded.Deliveries[0].ChannelName != "discord" {
		t.Fatalf("GetAlertEvent (after update) Deliveries = %+v", reloaded.Deliveries)
	}

	// No longer active once resolved.
	if _, err := db.GetActiveAlertEvent(ctx, ev.RuleID, ev.HostID); !isModelsNotFound(err) {
		t.Fatalf("GetActiveAlertEvent after resolve: err = %v, want ErrNotFound", err)
	}

	if err := db.UpdateAlertEvent(ctx, models.AlertEvent{ID: 99999}); !isModelsNotFound(err) {
		t.Fatalf("UpdateAlertEvent missing id: err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetAlertEvent(ctx, 99999); !isModelsNotFound(err) {
		t.Fatalf("GetAlertEvent missing id: err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetActiveAlertEvent(ctx, 99999, "nope"); !isModelsNotFound(err) {
		t.Fatalf("GetActiveAlertEvent missing: err = %v, want ErrNotFound", err)
	}
}

func TestAlertEvents_ListFiltersAndPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	mk := func(hostID string, state models.AlertEventState, startedAt int64) {
		t.Helper()
		if _, err := db.CreateAlertEvent(ctx, models.AlertEvent{
			RuleID: 1, HostID: hostID, Metric: models.AlertMetricCPU,
			State: state, StartedAt: startedAt,
		}); err != nil {
			t.Fatalf("CreateAlertEvent: %v", err)
		}
	}
	mk("h1", models.AlertEventFiring, 100)
	mk("h1", models.AlertEventResolved, 200)
	mk("h2", models.AlertEventFiring, 300)
	mk("h2", models.AlertEventResolved, 400)

	all, err := db.ListAlertEvents(ctx, "", "", 0, 0)
	if err != nil {
		t.Fatalf("ListAlertEvents (all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("ListAlertEvents (all) len = %d, want 4", len(all))
	}
	// Newest first.
	if all[0].StartedAt != 400 || all[3].StartedAt != 100 {
		t.Fatalf("ListAlertEvents not sorted newest-first: %+v", all)
	}

	byHost, err := db.ListAlertEvents(ctx, "", "h1", 0, 0)
	if err != nil {
		t.Fatalf("ListAlertEvents (host filter): %v", err)
	}
	if len(byHost) != 2 {
		t.Fatalf("ListAlertEvents (host=h1) len = %d, want 2", len(byHost))
	}

	byState, err := db.ListAlertEvents(ctx, models.AlertEventFiring, "", 0, 0)
	if err != nil {
		t.Fatalf("ListAlertEvents (state filter): %v", err)
	}
	if len(byState) != 2 {
		t.Fatalf("ListAlertEvents (state=firing) len = %d, want 2", len(byState))
	}

	paged, err := db.ListAlertEvents(ctx, "", "", 0, 2)
	if err != nil {
		t.Fatalf("ListAlertEvents (limit): %v", err)
	}
	if len(paged) != 2 {
		t.Fatalf("ListAlertEvents (limit=2) len = %d, want 2", len(paged))
	}

	before, err := db.ListAlertEvents(ctx, "", "", 300, 0)
	if err != nil {
		t.Fatalf("ListAlertEvents (before): %v", err)
	}
	for _, ev := range before {
		if ev.StartedAt >= 300 {
			t.Errorf("ListAlertEvents (before=300) returned StartedAt=%d, want < 300", ev.StartedAt)
		}
	}
	if len(before) != 2 {
		t.Fatalf("ListAlertEvents (before=300) len = %d, want 2", len(before))
	}

	active, err := db.ListActiveAlertEvents(ctx)
	if err != nil {
		t.Fatalf("ListActiveAlertEvents: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("ListActiveAlertEvents len = %d, want 2", len(active))
	}
	for _, ev := range active {
		if ev.State != models.AlertEventFiring {
			t.Errorf("ListActiveAlertEvents returned non-firing event: %+v", ev)
		}
	}
}

func TestAlertEvents_PruneRetainsFiringAndRecentResolved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	const day = 24 * time.Hour

	oldResolved, err := db.CreateAlertEvent(ctx, models.AlertEvent{
		RuleID: 1, HostID: "h1", Metric: models.AlertMetricCPU,
		State: models.AlertEventResolved, StartedAt: now.Add(-200 * day).Unix(),
	})
	if err != nil {
		t.Fatalf("CreateAlertEvent (old resolved): %v", err)
	}
	recentResolved, err := db.CreateAlertEvent(ctx, models.AlertEvent{
		RuleID: 1, HostID: "h1", Metric: models.AlertMetricCPU,
		State: models.AlertEventResolved, StartedAt: now.Add(-10 * day).Unix(),
	})
	if err != nil {
		t.Fatalf("CreateAlertEvent (recent resolved): %v", err)
	}
	oldFiring, err := db.CreateAlertEvent(ctx, models.AlertEvent{
		RuleID: 1, HostID: "h2", Metric: models.AlertMetricCPU,
		State: models.AlertEventFiring, StartedAt: now.Add(-300 * day).Unix(),
	})
	if err != nil {
		t.Fatalf("CreateAlertEvent (old firing): %v", err)
	}

	if err := db.PruneAlertEvents(ctx, now); err != nil {
		t.Fatalf("PruneAlertEvents: %v", err)
	}

	if _, err := db.GetAlertEvent(ctx, oldResolved.ID); !isModelsNotFound(err) {
		t.Errorf("old resolved event survived prune: err = %v", err)
	}
	if _, err := db.GetAlertEvent(ctx, recentResolved.ID); err != nil {
		t.Errorf("recent resolved event was pruned: %v", err)
	}
	if _, err := db.GetAlertEvent(ctx, oldFiring.ID); err != nil {
		t.Errorf("old firing event was pruned (must never be pruned while firing): %v", err)
	}
}

// isModelsNotFound reports whether err wraps models.ErrNotFound.
func isModelsNotFound(err error) bool {
	return errors.Is(err, models.ErrNotFound)
}

// TestOpen_UpgradeFrom0003SeedsDefaultAlertRules verifies migration
// 0004's default-rule seeding: a database that has gone through
// migrations 0001-0003 only (simulating a real v0.4.0 database on disk)
// with a pre-existing alert_webhook_url setting must, after
// storage.Open applies 0004, end up with:
//   - a "Default webhook" notify_channels row created from that setting
//   - three egress_out_pct alert_rules rows (80/95/100, all enabled,
//     cooldown 0) each referencing that channel's ID
//   - a working host_inventory table (added by the same migration)
func TestOpen_UpgradeFrom0003SeedsDefaultAlertRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v04.db")

	legacyDSN := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	legacy, err := sql.Open("sqlite", legacyDSN)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	for _, name := range []string{"migrations/0001_init.sql", "migrations/0002_limits_settings.sql", "migrations/0003_sessions.sql"} {
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
	for v := 1; v <= 3; v++ {
		if _, err := legacy.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, time.Now().Unix(),
		); err != nil {
			_ = legacy.Close()
			t.Fatalf("insert schema_migrations row %d: %v", v, err)
		}
	}
	// Simulate an existing CP_ALERT_WEBHOOK_URL-derived setting, as the
	// v0.4.0 hub would have stored via PUT /api/v1/settings/alerts or
	// startup env seeding.
	if _, err := legacy.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES ('alert_webhook_url', ?, ?)`,
		"https://hooks.example.com/legacy-webhook", time.Now().Unix(),
	); err != nil {
		_ = legacy.Close()
		t.Fatalf("insert legacy webhook setting: %v", err)
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

	channels, err := db.ListNotifyChannels(ctx)
	if err != nil {
		t.Fatalf("ListNotifyChannels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("ListNotifyChannels len = %d, want 1 (seeded Default webhook)", len(channels))
	}
	ch := channels[0]
	if ch.Name != "Default webhook" || ch.Type != models.NotifyChannelWebhook {
		t.Fatalf("seeded channel = %+v, want Default webhook/webhook", ch)
	}
	if ch.Config["webhook_url"] != "https://hooks.example.com/legacy-webhook" {
		t.Fatalf("seeded channel webhook_url = %q, want the legacy setting's value", ch.Config["webhook_url"])
	}

	rules, err := db.ListAlertRules(ctx)
	if err != nil {
		t.Fatalf("ListAlertRules: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("ListAlertRules len = %d, want 3 (seeded egress thresholds)", len(rules))
	}
	wantThresholds := map[float64]bool{80: false, 95: false, 100: false}
	for _, r := range rules {
		if r.Metric != models.AlertMetricEgressOutPct {
			t.Errorf("seeded rule %q has metric %q, want egress_out_pct", r.Name, r.Metric)
		}
		if !r.Enabled {
			t.Errorf("seeded rule %q is not enabled", r.Name)
		}
		if r.CooldownSec != 0 {
			t.Errorf("seeded rule %q has cooldown_sec = %d, want 0 (egress dedupe is monthly, engine-managed)", r.Name, r.CooldownSec)
		}
		if len(r.ChannelIDs) != 1 || r.ChannelIDs[0] != ch.ID {
			t.Errorf("seeded rule %q has channel_ids = %v, want [%d]", r.Name, r.ChannelIDs, ch.ID)
		}
		if _, ok := wantThresholds[r.Threshold]; !ok {
			t.Errorf("seeded rule %q has unexpected threshold %v", r.Name, r.Threshold)
		}
		wantThresholds[r.Threshold] = true
	}
	for threshold, seen := range wantThresholds {
		if !seen {
			t.Errorf("no seeded rule found for threshold %v", threshold)
		}
	}

	// host_inventory table (added by the same 0004 migration) must be
	// usable via GetHostInventory/SetHostInventory even though those
	// methods are currently the v05_stub.go placeholder (inventory
	// stage's real implementation replaces them later) — verify the
	// table itself exists and is queryable directly.
	var count int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM host_inventory`).Scan(&count); err != nil {
		t.Fatalf("query host_inventory (table must exist after 0004): %v", err)
	}

	// Re-applying migrate() must be a no-op (idempotent upgrade path).
	if err := db.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	rulesAfterReMigrate, err := db.ListAlertRules(ctx)
	if err != nil {
		t.Fatalf("ListAlertRules (after re-migrate): %v", err)
	}
	if len(rulesAfterReMigrate) != 3 {
		t.Fatalf("ListAlertRules (after re-migrate) len = %d, want still 3 (no duplicate seeding)", len(rulesAfterReMigrate))
	}
}

// TestOpen_UpgradeFrom0003NoWebhookNoChannelSeeded verifies that a
// fresh-ish v0.4.0 database with no alert_webhook_url setting at all
// still gets the three default egress rules, but with no channel
// attached (channel_ids empty) rather than failing migration 0004.
func TestOpen_UpgradeFrom0003NoWebhookNoChannelSeeded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-v04-nowebhook.db")

	legacyDSN := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	legacy, err := sql.Open("sqlite", legacyDSN)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	for _, name := range []string{"migrations/0001_init.sql", "migrations/0002_limits_settings.sql", "migrations/0003_sessions.sql"} {
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
	for v := 1; v <= 3; v++ {
		if _, err := legacy.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, time.Now().Unix(),
		); err != nil {
			_ = legacy.Close()
			t.Fatalf("insert schema_migrations row %d: %v", v, err)
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

	channels, err := db.ListNotifyChannels(ctx)
	if err != nil {
		t.Fatalf("ListNotifyChannels: %v", err)
	}
	if len(channels) != 0 {
		t.Fatalf("ListNotifyChannels len = %d, want 0 (no webhook setting to seed from)", len(channels))
	}

	rules, err := db.ListAlertRules(ctx)
	if err != nil {
		t.Fatalf("ListAlertRules: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("ListAlertRules len = %d, want 3", len(rules))
	}
	for _, r := range rules {
		if len(r.ChannelIDs) != 0 {
			t.Errorf("seeded rule %q has channel_ids = %v, want empty (no channel to attach)", r.Name, r.ChannelIDs)
		}
	}
}
