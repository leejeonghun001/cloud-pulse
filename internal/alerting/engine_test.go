package alerting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// newTestEngine constructs an Engine wired to store/clock for tests,
// with retry backoff sleeps replaced by an instant channel so delivery
// retries (SPEC-v0.5 §B: 3 attempts with backoff) don't slow down tests.
func newTestEngine(t *testing.T, store *fakeStore, clock *fakeClock, sender SenderFactory) *Engine {
	t.Helper()
	e := New(Options{
		Store:         store,
		SenderFactory: sender,
		Clock:         clock.Now,
		Logger:        testLogger(),
	})
	e.delivery.afterFunc = instantAfter
	t.Cleanup(e.Close)
	return e
}

func hostSnapshot(id string, latest *models.Sample) models.HostSnapshot {
	return models.HostSnapshot{
		HostID:   id,
		Hostname: id + "-host",
		Status:   models.HostUp,
		LastSeen: 0,
		Latest:   latest,
	}
}

func TestEvaluate_ImmediateFiring_NoDuration(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU high", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 90, DurationSec: 0, CooldownSec: 3600,
		ChannelIDs: []int64{1},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 95})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close() // wait for async delivery

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
	st := store.getState(1, "h1")
	if st.State != models.AlertStateFiring {
		t.Fatalf("state = %s, want firing", st.State)
	}
	events := store.listEvents()
	if len(events) != 1 || events[0].State != models.AlertEventFiring {
		t.Fatalf("events = %+v, want one firing event", events)
	}
}

func TestEvaluate_SustainedWindow_RequiresEveryRawSampleBreaching(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)) // now = 00:05:00
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	const durationSec = 300 // 5 minutes
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU sustained", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 90, DurationSec: durationSec, CooldownSec: 3600,
		ChannelIDs: []int64{1},
	})

	base := clock.Now().Add(-durationSec * time.Second).Unix()
	// One low sample in the middle of the window breaks sustained-ness.
	store.setSeriesCPU("h1", []seriesPoint{
		{base, 95}, {base + 60, 95}, {base + 120, 50}, {base + 180, 95}, {base + 240, 95}, {base + 300, 95},
	})
	host := hostSnapshot("h1", &models.Sample{CPUPercent: 95})

	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0 (window has a non-breaching sample)", got)
	}
	st := store.getState(1, "h1")
	if st.State != models.AlertStatePending {
		t.Fatalf("state = %s, want pending", st.State)
	}
}

func TestEvaluate_SustainedWindow_AllBreachingFires(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	const durationSec = 300
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU sustained", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 90, DurationSec: durationSec, CooldownSec: 3600,
		ChannelIDs: []int64{1},
	})

	base := clock.Now().Add(-durationSec * time.Second).Unix()
	store.setSeriesCPU("h1", []seriesPoint{
		{base, 95}, {base + 60, 96}, {base + 120, 97}, {base + 180, 95}, {base + 240, 95}, {base + 300, 95},
	})
	host := hostSnapshot("h1", &models.Sample{CPUPercent: 95})

	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1 (every sample breaches)", got)
	}
	st := store.getState(1, "h1")
	if st.State != models.AlertStateFiring {
		t.Fatalf("state = %s, want firing", st.State)
	}
}

func TestEvaluate_SustainedWindow_PartialHistoryNotSustained(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	const durationSec = 300
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU sustained", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 90, DurationSec: durationSec, CooldownSec: 3600,
		ChannelIDs: []int64{1},
	})

	// Only 2 minutes of history exist even though the window needs 5 —
	// the host only started reporting recently. Every present sample
	// breaches, but the window itself isn't covered.
	base := clock.Now().Add(-120 * time.Second).Unix()
	store.setSeriesCPU("h1", []seriesPoint{{base, 95}, {base + 60, 96}, {base + 120, 97}})
	host := hostSnapshot("h1", &models.Sample{CPUPercent: 95})

	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0 (window not covered by history)", got)
	}
	st := store.getState(1, "h1")
	if st.State != models.AlertStatePending {
		t.Fatalf("state = %s, want pending (not yet sustained)", st.State)
	}
}

func TestEvaluate_PendingToFiringToResolved(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU high", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 90, DurationSec: 0, CooldownSec: 3600,
		NotifyResolved: true, ChannelIDs: []int64{1},
	})

	ctx := context.Background()

	// Breach -> immediate firing (DurationSec 0).
	host := hostSnapshot("h1", &models.Sample{CPUPercent: 95})
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (fire): %v", err)
	}
	if st := store.getState(1, "h1"); st.State != models.AlertStateFiring {
		t.Fatalf("state after breach = %s, want firing", st.State)
	}

	// Condition clears -> resolved.
	clock.Advance(time.Minute)
	host = hostSnapshot("h1", &models.Sample{CPUPercent: 50})
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (resolve): %v", err)
	}
	e.Close()

	st := store.getState(1, "h1")
	if st.State != models.AlertStateOK {
		t.Fatalf("state after resolve = %s, want ok", st.State)
	}
	events := store.listEvents()
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly 1 (updated in place, not duplicated)", events)
	}
	if events[0].State != models.AlertEventResolved {
		t.Fatalf("event state = %s, want resolved", events[0].State)
	}
	if events[0].ResolvedAt == 0 {
		t.Fatal("event ResolvedAt not set")
	}
	if got := sender.callCount(); got != 2 {
		t.Fatalf("sender calls = %d, want 2 (fire + resolve)", got)
	}
	lastSeverity := sender.lastMessage().Severity
	if lastSeverity != SeverityResolved {
		t.Fatalf("last message severity = %s, want resolved", lastSeverity)
	}
}

func TestEvaluate_PendingClearsWithoutNotification(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	const durationSec = 300
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU sustained", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 90, DurationSec: durationSec, CooldownSec: 3600,
		NotifyResolved: true, ChannelIDs: []int64{1},
	})

	ctx := context.Background()
	base := clock.Now().Add(-durationSec * time.Second).Unix()
	store.setSeriesCPU("h1", []seriesPoint{{base, 95}, {base + 150, 50}, {base + 300, 95}})
	host := hostSnapshot("h1", &models.Sample{CPUPercent: 95})

	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if st := store.getState(1, "h1"); st.State != models.AlertStatePending {
		t.Fatalf("state = %s, want pending", st.State)
	}

	// Condition now clears entirely -> back to ok, no notification since
	// it never reached firing.
	clock.Advance(time.Minute)
	host = hostSnapshot("h1", &models.Sample{CPUPercent: 10})
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (clear): %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0 (never fired)", got)
	}
	if st := store.getState(1, "h1"); st.State != models.AlertStateOK {
		t.Fatalf("state after clear = %s, want ok", st.State)
	}
	if events := store.listEvents(); len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
}

func TestEvaluate_CooldownGatesReNotification(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	const cooldownSec = 600
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU high", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 90, DurationSec: 0, CooldownSec: cooldownSec,
		ChannelIDs: []int64{1},
	})

	ctx := context.Background()
	host := hostSnapshot("h1", &models.Sample{CPUPercent: 95})
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (initial fire): %v", err)
	}
	e.Wait()
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls after initial fire = %d, want 1", got)
	}

	// Still firing, well within cooldown: no re-notification.
	clock.Advance(60 * time.Second)
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (within cooldown): %v", err)
	}
	e.Wait()
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls within cooldown = %d, want still 1", got)
	}

	// Cooldown has elapsed: "still firing" re-notification.
	clock.Advance(cooldownSec * time.Second)
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (after cooldown): %v", err)
	}
	e.Close()
	if got := sender.callCount(); got != 2 {
		t.Fatalf("sender calls after cooldown elapsed = %d, want 2", got)
	}

	events := store.listEvents()
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly 1 (still the same firing event)", events)
	}
}

func TestEvaluate_ZeroCooldownNeverReNotifies(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU high", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 90, DurationSec: 0, CooldownSec: 0,
		ChannelIDs: []int64{1},
	})

	ctx := context.Background()
	host := hostSnapshot("h1", &models.Sample{CPUPercent: 95})
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	clock.Advance(365 * 24 * time.Hour)
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (much later): %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1 (cooldown 0 means never re-notify)", got)
	}
}

func TestEvaluate_HostDown(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "Host offline", Enabled: true, Metric: models.AlertMetricHostDown,
		Operator: models.AlertOperatorGT, Threshold: 0, DurationSec: 120, CooldownSec: 3600,
		ChannelIDs: []int64{1},
	})

	ctx := context.Background()
	// Host last seen 5 minutes ago (300s), rule wants down for >= 120s
	// beyond offline detection -- host_down's "durationSec" here directly
	// gates via hostDownValue.
	lastSeen := clock.Now().Add(-5 * time.Minute).Unix()
	host := models.HostSnapshot{HostID: "h1", Hostname: "h1-host", Status: models.HostDown, LastSeen: lastSeen}

	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
	if sender.lastMessage().Severity != SeverityCritical {
		t.Fatalf("severity = %s, want critical for host_down", sender.lastMessage().Severity)
	}
	st := store.getState(1, "h1")
	if st.State != models.AlertStateFiring {
		t.Fatalf("state = %s, want firing", st.State)
	}
}

func TestEvaluate_HostDownNotYetPastDuration(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "Host offline", Enabled: true, Metric: models.AlertMetricHostDown,
		Operator: models.AlertOperatorGT, Threshold: 0, DurationSec: 300, CooldownSec: 3600,
		ChannelIDs: []int64{1},
	})

	ctx := context.Background()
	lastSeen := clock.Now().Add(-30 * time.Second).Unix() // down for 30s < 300s threshold
	host := models.HostSnapshot{HostID: "h1", Hostname: "h1-host", Status: models.HostDown, LastSeen: lastSeen}

	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0 (not down long enough yet)", got)
	}
}

func TestEvaluate_EgressMonthlyDedupe(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "Outbound 80%", Enabled: true, Metric: models.AlertMetricEgressOutPct,
		Operator: models.AlertOperatorGTE, Threshold: 80, DurationSec: 0, CooldownSec: 0,
		ChannelIDs: []int64{1},
	})

	ctx := context.Background()
	host := models.HostSnapshot{HostID: "h1", Hostname: "h1-host", Status: models.HostUp, Egress: models.EgressUsage{Percent: 85}}

	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (first breach): %v", err)
	}
	e.Wait()
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}

	// Repeat evaluation same month, still breaching: no re-notify
	// (cooldown ignored per spec, dedupe is per-month-per-threshold).
	clock.Advance(24 * time.Hour)
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (repeat same month): %v", err)
	}
	e.Wait()
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls after repeat = %d, want still 1", got)
	}

	// A dip below threshold and back up again, still same month: still
	// no re-notify.
	host.Egress.Percent = 50
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (dip): %v", err)
	}
	host.Egress.Percent = 90
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (re-breach same month): %v", err)
	}
	e.Close()
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls after re-breach same month = %d, want still 1", got)
	}

	events := store.listEvents()
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly 1 for the month", events)
	}
}

func TestEvaluate_EgressMonthlyDedupe_NewMonthReNotifies(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "Outbound 80%", Enabled: true, Metric: models.AlertMetricEgressOutPct,
		Operator: models.AlertOperatorGTE, Threshold: 80, ChannelIDs: []int64{1},
	})

	ctx := context.Background()
	host := models.HostSnapshot{HostID: "h1", Hostname: "h1-host", Status: models.HostUp, Egress: models.EgressUsage{Percent: 85}}
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (jan): %v", err)
	}

	// Advance into February; usage resets and breaches again.
	clock.Advance(31 * 24 * time.Hour)
	if err := e.Evaluate(ctx, clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate (feb): %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 2 {
		t.Fatalf("sender calls = %d, want 2 (new month resets dedupe)", got)
	}
}

func TestEvaluate_DisabledRuleSkipped(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "disabled rule", Enabled: false, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 1, ChannelIDs: []int64{1},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 99})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()
	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0 (rule disabled)", got)
	}
}

func TestEvaluate_HostScopedRuleIgnoresOtherHosts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "scoped", Enabled: true, Metric: models.AlertMetricCPU, HostID: "h1",
		Operator: models.AlertOperatorGT, Threshold: 50, ChannelIDs: []int64{1},
	})

	hosts := []models.HostSnapshot{
		hostSnapshot("h1", &models.Sample{CPUPercent: 90}),
		hostSnapshot("h2", &models.Sample{CPUPercent: 90}),
	}
	if err := e.Evaluate(context.Background(), clock.Now(), hosts); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1 (only h1 in scope)", got)
	}
	if st := store.getState(1, "h2"); st.State != "" {
		t.Fatalf("h2 state = %+v, want zero-value (out of scope, never evaluated/persisted)", st)
	}
}

func TestEvaluate_DisabledChannelSkipsDelivery(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: false})
	store.setRule(models.AlertRule{
		ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 50, ChannelIDs: []int64{1},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 90})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()
	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0 (channel disabled)", got)
	}
	events := store.listEvents()
	if len(events) != 1 {
		t.Fatalf("events = %+v, want 1 (event still recorded even though delivery was skipped)", events)
	}
}

func TestEvaluate_UnknownChannelSkippedGracefully(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	// Rule references a channel ID that doesn't exist (since deleted).
	store.setRule(models.AlertRule{
		ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 50, ChannelIDs: []int64{999},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 90})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()
	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0", got)
	}
}

func TestPreview_DoesNotMutateState(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	e := newTestEngine(t, store, clock, nil)

	rule := models.AlertRule{ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU, Operator: models.AlertOperatorGT, Threshold: 50}
	hosts := []models.HostSnapshot{
		hostSnapshot("h1", &models.Sample{CPUPercent: 90}),
		hostSnapshot("h2", &models.Sample{CPUPercent: 10}),
	}

	result, err := e.Preview(context.Background(), clock.Now(), rule, hosts)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !result["h1"] {
		t.Error("Preview[h1] = false, want true (90 > 50)")
	}
	if result["h2"] {
		t.Error("Preview[h2] = true, want false (10 <= 50)")
	}

	// No state must have been written.
	if st := store.getState(1, "h1"); st.State != "" {
		t.Errorf("Preview wrote state: %+v", st)
	}
	if events := store.listEvents(); len(events) != 0 {
		t.Errorf("Preview created events: %+v", events)
	}
}

func TestDelivery_RetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	sender := &fakeSender{failTimes: 2} // fails twice, succeeds on 3rd (max attempts)
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 50, ChannelIDs: []int64{1},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 90})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 3 {
		t.Fatalf("sender calls = %d, want 3 (2 failures + 1 success)", got)
	}
	events := store.listEvents()
	if len(events) != 1 || len(events[0].Deliveries) != 1 || !events[0].Deliveries[0].OK {
		t.Fatalf("event deliveries = %+v, want one OK delivery recorded", events)
	}
}

func TestDelivery_AllAttemptsFailRecordsFailure(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	sender := &fakeSender{failTimes: 99}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 50, ChannelIDs: []int64{1},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 90})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != maxDeliveryAttempts {
		t.Fatalf("sender calls = %d, want %d", got, maxDeliveryAttempts)
	}
	events := store.listEvents()
	if len(events) != 1 || len(events[0].Deliveries) != 1 || events[0].Deliveries[0].OK {
		t.Fatalf("event deliveries = %+v, want one failed delivery recorded", events)
	}
	if events[0].Deliveries[0].Error == "" {
		t.Error("delivery Error is empty, want a message")
	}
}

func TestDelivery_DiagnoseClassifiesFailure(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	sentinelErr := errors.New("boom")
	sender := &fakeSender{failTimes: 99, err: sentinelErr}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }

	// Diagnose is configured directly (bypassing newTestEngine's
	// shared helper, which doesn't plumb Options.Diagnose) — mirrors
	// how cmd/hub/alerting.go wires internal/notify.DiagnoseError in
	// the real binary, without this package importing internal/notify
	// itself (see notify.go's doc comment on why it can't).
	wantCode := models.DiagnosisPlatformError
	e := New(Options{
		Store:         store,
		SenderFactory: senderFactory,
		Clock:         clock.Now,
		Logger:        testLogger(),
		Diagnose: func(err error) *models.DiagnosisCode {
			if !errors.Is(err, sentinelErr) {
				t.Errorf("Diagnose called with unexpected error: %v", err)
			}
			code := wantCode
			return &code
		},
	})
	e.delivery.afterFunc = instantAfter
	t.Cleanup(e.Close)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 50, ChannelIDs: []int64{1},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 90})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	events := store.listEvents()
	if len(events) != 1 || len(events[0].Deliveries) != 1 {
		t.Fatalf("event deliveries = %+v, want exactly one delivery", events)
	}
	d := events[0].Deliveries[0]
	if d.OK {
		t.Fatal("delivery OK = true, want false")
	}
	if d.Diagnosis == nil || *d.Diagnosis != wantCode {
		t.Errorf("delivery Diagnosis = %v, want %q", d.Diagnosis, wantCode)
	}
}

func TestDelivery_NilDiagnoseLeavesDiagnosisUnset(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	sender := &fakeSender{failTimes: 99}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	// newTestEngine leaves Options.Diagnose nil — the default/older-
	// wiring case — confirming Delivery.Diagnosis stays unset rather
	// than panicking on a nil function value.
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 50, ChannelIDs: []int64{1},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 90})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	events := store.listEvents()
	if len(events) != 1 || len(events[0].Deliveries) != 1 {
		t.Fatalf("event deliveries = %+v, want exactly one delivery", events)
	}
	if d := events[0].Deliveries[0]; d.Diagnosis != nil {
		t.Errorf("delivery Diagnosis = %v, want nil when Options.Diagnose is unset", *d.Diagnosis)
	}
}

func TestDelivery_RetryAfterHintHonored(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	sender := &fakeSender{failTimes: 1, retryHint: 3 * time.Second}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	var observedWait time.Duration
	e.delivery.afterFunc = func(d time.Duration) <-chan time.Time {
		observedWait = d
		return instantAfter(d)
	}

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 50, ChannelIDs: []int64{1},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 90})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if observedWait != 3*time.Second {
		t.Fatalf("observed retry wait = %v, want 3s (from RetryAfterError)", observedWait)
	}
}

func TestDelivery_NoSenderFactoryRecordsError(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	e := newTestEngine(t, store, clock, nil) // no factory configured

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 50, ChannelIDs: []int64{1},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 90})
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	events := store.listEvents()
	if len(events) != 1 || len(events[0].Deliveries) != 1 || events[0].Deliveries[0].OK {
		t.Fatalf("event deliveries = %+v, want one failed delivery (no factory)", events)
	}
}

func TestEvaluate_MetricValueMissing_NoLatestSample(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Now())
	e := newTestEngine(t, store, clock, nil)

	store.setRule(models.AlertRule{
		ID: 1, Name: "r", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 50,
	})

	host := hostSnapshot("h1", nil) // never reported a sample
	if err := e.Evaluate(context.Background(), clock.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if st := store.getState(1, "h1"); st.State != "" {
		t.Errorf("state = %+v, want untouched (no data to evaluate)", st)
	}
}
