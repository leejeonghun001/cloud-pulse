package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func mustCreateTestChannel(t *testing.T, db *DB) models.NotifyChannel {
	t.Helper()
	ch, err := db.CreateNotifyChannel(context.Background(), models.NotifyChannel{
		Name: "c1", Type: models.NotifyChannelDiscord, Enabled: true,
		Config: map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/tok"},
	})
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}
	return ch
}

func TestChannelHealthSettings_DefaultsWhenAbsent(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)

	s, err := db.GetChannelHealthSettings(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetChannelHealthSettings: %v", err)
	}
	if s.Enabled || s.ChannelID != ch.ID {
		t.Errorf("s = %+v, want disabled defaults for channel %d", s, ch.ID)
	}
}

func TestChannelHealthSettings_SetGetRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)

	want := models.ChannelHealthSettings{ChannelID: ch.ID, Enabled: true, Weekday: 2, MinuteOfDay: 600, LastRunAt: 1000, UpdatedAt: 2000}
	if err := db.SetChannelHealthSettings(ctx, want); err != nil {
		t.Fatalf("SetChannelHealthSettings: %v", err)
	}
	got, err := db.GetChannelHealthSettings(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetChannelHealthSettings: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Upsert: change the schedule, LastRunAt untouched by intent (still
	// passed explicitly here since Set always writes every field).
	want2 := want
	want2.Weekday = 5
	want2.MinuteOfDay = 60
	want2.UpdatedAt = 3000
	if err := db.SetChannelHealthSettings(ctx, want2); err != nil {
		t.Fatalf("SetChannelHealthSettings (update): %v", err)
	}
	got2, err := db.GetChannelHealthSettings(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetChannelHealthSettings: %v", err)
	}
	if got2 != want2 {
		t.Errorf("got %+v, want %+v", got2, want2)
	}
}

func TestChannelHealthSettings_UpdateLastRunOnlyTouchesThatField(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)

	want := models.ChannelHealthSettings{ChannelID: ch.ID, Enabled: true, Weekday: 3, MinuteOfDay: 100, LastRunAt: 0, UpdatedAt: 1000}
	if err := db.SetChannelHealthSettings(ctx, want); err != nil {
		t.Fatalf("SetChannelHealthSettings: %v", err)
	}
	if err := db.UpdateChannelHealthSettingsLastRun(ctx, ch.ID, 5000); err != nil {
		t.Fatalf("UpdateChannelHealthSettingsLastRun: %v", err)
	}
	got, err := db.GetChannelHealthSettings(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetChannelHealthSettings: %v", err)
	}
	if got.LastRunAt != 5000 {
		t.Errorf("LastRunAt = %d, want 5000", got.LastRunAt)
	}
	if got.Weekday != 3 || got.MinuteOfDay != 100 {
		t.Errorf("schedule fields changed unexpectedly: %+v", got)
	}
}

func TestChannelHealthSettings_UpdateLastRunNoOpWhenNoRow(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)

	if err := db.UpdateChannelHealthSettingsLastRun(ctx, ch.ID, 999); err != nil {
		t.Fatalf("UpdateChannelHealthSettingsLastRun: %v", err)
	}
	s, err := db.GetChannelHealthSettings(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetChannelHealthSettings: %v", err)
	}
	if s.Enabled {
		t.Errorf("s.Enabled = true, want default false (no row should have been created)")
	}
}

func TestChannelHealthSettings_ListSortedByChannelID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch1 := mustCreateTestChannel(t, db)
	ch2, err := db.CreateNotifyChannel(ctx, models.NotifyChannel{Name: "c2", Type: models.NotifyChannelWebhook, Config: map[string]string{}})
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}

	if err := db.SetChannelHealthSettings(ctx, models.ChannelHealthSettings{ChannelID: ch2.ID, Enabled: true, UpdatedAt: 1}); err != nil {
		t.Fatalf("SetChannelHealthSettings: %v", err)
	}
	if err := db.SetChannelHealthSettings(ctx, models.ChannelHealthSettings{ChannelID: ch1.ID, Enabled: true, UpdatedAt: 1}); err != nil {
		t.Fatalf("SetChannelHealthSettings: %v", err)
	}

	list, err := db.ListChannelHealthSettings(ctx)
	if err != nil {
		t.Fatalf("ListChannelHealthSettings: %v", err)
	}
	if len(list) != 2 || list[0].ChannelID != ch1.ID || list[1].ChannelID != ch2.ID {
		t.Errorf("list = %+v, want sorted by channel_id (%d, %d)", list, ch1.ID, ch2.ID)
	}
}

func TestChannelHealthSettings_DeleteChannelCascades(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)
	if err := db.SetChannelHealthSettings(ctx, models.ChannelHealthSettings{ChannelID: ch.ID, Enabled: true, UpdatedAt: 1}); err != nil {
		t.Fatalf("SetChannelHealthSettings: %v", err)
	}
	if err := db.DeleteNotifyChannel(ctx, ch.ID); err != nil {
		t.Fatalf("DeleteNotifyChannel: %v", err)
	}
	list, err := db.ListChannelHealthSettings(ctx)
	if err != nil {
		t.Fatalf("ListChannelHealthSettings: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list = %+v, want empty after cascade delete", list)
	}
}

func TestSyntheticResults_CreateUpdateListRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)

	created, err := db.CreateSyntheticResult(ctx, models.SyntheticResult{ChannelID: ch.ID, StartedAt: 1000, Outcome: models.SyntheticAccepted})
	if err != nil {
		t.Fatalf("CreateSyntheticResult: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("created.ID = 0, want non-zero")
	}

	// Still "running" (finished_at == 0): must not appear in
	// ListSyntheticResults, which only returns completed runs.
	list, err := db.ListSyntheticResults(ctx, ch.ID, 0)
	if err != nil {
		t.Fatalf("ListSyntheticResults: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list = %+v, want empty while run is still in-flight", list)
	}

	created.FinishedAt = 1005
	created.Outcome = models.SyntheticVerified
	if err := db.UpdateSyntheticResult(ctx, created); err != nil {
		t.Fatalf("UpdateSyntheticResult: %v", err)
	}

	list, err = db.ListSyntheticResults(ctx, ch.ID, 0)
	if err != nil {
		t.Fatalf("ListSyntheticResults: %v", err)
	}
	if len(list) != 1 || list[0] != created {
		t.Errorf("list = %+v, want [%+v]", list, created)
	}
}

func TestSyntheticResults_UpdateNotFound(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	err := db.UpdateSyntheticResult(ctx, models.SyntheticResult{ID: 9999, FinishedAt: 1, Outcome: models.SyntheticFailed})
	if !errors.Is(err, models.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSyntheticResults_ListNewestFirstAndLimit(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)

	for i, ts := range []int64{100, 200, 300} {
		r, err := db.CreateSyntheticResult(ctx, models.SyntheticResult{ChannelID: ch.ID, StartedAt: ts, Outcome: models.SyntheticAccepted})
		if err != nil {
			t.Fatalf("CreateSyntheticResult[%d]: %v", i, err)
		}
		r.FinishedAt = ts + 1
		if err := db.UpdateSyntheticResult(ctx, r); err != nil {
			t.Fatalf("UpdateSyntheticResult[%d]: %v", i, err)
		}
	}

	list, err := db.ListSyntheticResults(ctx, ch.ID, 2)
	if err != nil {
		t.Fatalf("ListSyntheticResults: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].StartedAt != 300 || list[1].StartedAt != 200 {
		t.Errorf("list = %+v, want newest-first [300, 200]", list)
	}
}

func TestSyntheticResults_DeleteChannelCascades(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)
	r, err := db.CreateSyntheticResult(ctx, models.SyntheticResult{ChannelID: ch.ID, StartedAt: 1, FinishedAt: 2, Outcome: models.SyntheticAccepted})
	if err != nil {
		t.Fatalf("CreateSyntheticResult: %v", err)
	}
	if err := db.DeleteNotifyChannel(ctx, ch.ID); err != nil {
		t.Fatalf("DeleteNotifyChannel: %v", err)
	}
	list, err := db.ListSyntheticResults(ctx, r.ChannelID, 0)
	if err != nil {
		t.Fatalf("ListSyntheticResults: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list = %+v, want empty after cascade delete", list)
	}
}

func TestChannelFailureIncident_OpenNotifyRecoverLifecycle(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)
	fallback := mustCreateTestChannel(t, db) // distinct row (id differs; name collision is fine for this test)

	_, ok, err := db.GetOpenChannelFailureIncident(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if ok {
		t.Fatal("expected no open incident initially")
	}

	if err := db.OpenChannelFailureIncident(ctx, ch.ID, 1000); err != nil {
		t.Fatalf("OpenChannelFailureIncident: %v", err)
	}
	inc, ok, err := db.GetOpenChannelFailureIncident(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if !ok || inc.OpenedAt != 1000 || inc.NotifiedAt != 0 {
		t.Errorf("inc = %+v, ok = %v, want opened at 1000, not yet notified", inc, ok)
	}

	// Opening again while already open must not reset OpenedAt in a
	// way that breaks de-duplication expectations for a second call
	// during the same still-open incident window (callers are expected
	// to check GetOpenChannelFailureIncident first; this exercises the
	// idempotent-insert path directly).
	if err := db.MarkChannelFailureIncidentNotified(ctx, ch.ID, 1100, fallback.ID); err != nil {
		t.Fatalf("MarkChannelFailureIncidentNotified: %v", err)
	}
	inc, ok, err = db.GetOpenChannelFailureIncident(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if !ok || inc.NotifiedAt != 1100 || inc.FallbackChannelID != fallback.ID {
		t.Errorf("inc = %+v, ok = %v, want notified at 1100 via fallback %d", inc, ok, fallback.ID)
	}

	if err := db.RecoverChannelFailureIncident(ctx, ch.ID, 1200); err != nil {
		t.Fatalf("RecoverChannelFailureIncident: %v", err)
	}
	_, ok, err = db.GetOpenChannelFailureIncident(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if ok {
		t.Error("expected no open incident after recovery")
	}

	// A fresh incident after recovery is a genuinely new one.
	if err := db.OpenChannelFailureIncident(ctx, ch.ID, 2000); err != nil {
		t.Fatalf("OpenChannelFailureIncident (second incident): %v", err)
	}
	inc, ok, err = db.GetOpenChannelFailureIncident(ctx, ch.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if !ok || inc.OpenedAt != 2000 || inc.NotifiedAt != 0 {
		t.Errorf("inc = %+v, ok = %v, want a fresh not-yet-notified incident opened at 2000", inc, ok)
	}
}

func TestChannelFailureIncident_RecoverNoOpWhenNoneOpen(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	ch := mustCreateTestChannel(t, db)

	if err := db.RecoverChannelFailureIncident(ctx, ch.ID, 999); err != nil {
		t.Fatalf("RecoverChannelFailureIncident: %v", err)
	}
}
