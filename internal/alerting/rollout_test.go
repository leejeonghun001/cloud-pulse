package alerting

import (
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// --- agent_outdated ---

func TestEvaluateAgentOutdated_FiresOnceThresholdReached(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	clock := newFakeClock(time.Unix(2_000_000, 0))
	store.setChannel(models.NotifyChannel{ID: 1, Name: "ops", Enabled: true, Type: models.NotifyChannelWebhook})
	store.setRule(models.AlertRule{ID: 1, Name: "outdated", Enabled: true, Metric: models.AlertMetricAgentOutdated, DurationSec: 14 * 86400, ChannelIDs: []int64{1}})

	sender := &fakeSender{}
	engine := New(Options{
		Store:         store,
		SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender, nil },
		Clock:         clock.Now,
		Logger:        testLogger(),
	})
	defer engine.Close()

	now := clock.Now()
	hosts := []AgentOutdatedInput{
		{HostID: "h1", Hostname: "host-one", Outdated: true, OutdatedSince: now.Unix() - int64(13*86400)}, // not yet 14 days
	}
	if err := engine.EvaluateAgentOutdated(t.Context(), now, hosts); err != nil {
		t.Fatalf("EvaluateAgentOutdated (13d) error = %v", err)
	}
	engine.Wait()
	if sender.callCount() != 0 {
		t.Fatalf("callCount = %d, want 0 before threshold reached", sender.callCount())
	}

	// Advance to exactly 14 days behind.
	hosts[0].OutdatedSince = now.Unix() - int64(14*86400)
	if err := engine.EvaluateAgentOutdated(t.Context(), now, hosts); err != nil {
		t.Fatalf("EvaluateAgentOutdated (14d) error = %v", err)
	}
	engine.Wait()
	if sender.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1 once threshold reached", sender.callCount())
	}

	// Re-evaluating again immediately must not re-notify (no cooldown elapsed).
	if err := engine.EvaluateAgentOutdated(t.Context(), now, hosts); err != nil {
		t.Fatalf("EvaluateAgentOutdated (re-eval) error = %v", err)
	}
	engine.Wait()
	if sender.callCount() != 1 {
		t.Fatalf("callCount = %d, want still 1 (no cooldown elapsed)", sender.callCount())
	}
}

func TestEvaluateAgentOutdated_CooldownReNotifies(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	clock := newFakeClock(time.Unix(2_000_000, 0))
	store.setChannel(models.NotifyChannel{ID: 1, Name: "ops", Enabled: true, Type: models.NotifyChannelWebhook})
	store.setRule(models.AlertRule{ID: 1, Name: "outdated", Enabled: true, Metric: models.AlertMetricAgentOutdated, DurationSec: 86400, CooldownSec: 3600, ChannelIDs: []int64{1}})

	sender := &fakeSender{}
	engine := New(Options{
		Store:         store,
		SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender, nil },
		Clock:         clock.Now,
		Logger:        testLogger(),
	})
	defer engine.Close()

	now := clock.Now()
	hosts := []AgentOutdatedInput{{HostID: "h1", Hostname: "host-one", Outdated: true, OutdatedSince: now.Unix() - 90000}}
	if err := engine.EvaluateAgentOutdated(t.Context(), now, hosts); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if sender.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1", sender.callCount())
	}

	// Before cooldown elapses: no re-notify.
	clock.Advance(30 * time.Minute)
	now2 := clock.Now()
	if err := engine.EvaluateAgentOutdated(t.Context(), now2, hosts); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if sender.callCount() != 1 {
		t.Fatalf("callCount = %d, want still 1 before cooldown", sender.callCount())
	}

	// After cooldown elapses: re-notify.
	clock.Advance(45 * time.Minute)
	now3 := clock.Now()
	if err := engine.EvaluateAgentOutdated(t.Context(), now3, hosts); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if sender.callCount() != 2 {
		t.Fatalf("callCount = %d, want 2 after cooldown elapsed", sender.callCount())
	}
}

func TestEvaluateAgentOutdated_ResolvesWhenNoLongerOutdated(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	clock := newFakeClock(time.Unix(2_000_000, 0))
	store.setChannel(models.NotifyChannel{ID: 1, Name: "ops", Enabled: true, Type: models.NotifyChannelWebhook})
	store.setRule(models.AlertRule{ID: 1, Name: "outdated", Enabled: true, Metric: models.AlertMetricAgentOutdated, DurationSec: 86400, NotifyResolved: true, ChannelIDs: []int64{1}})

	sender := &fakeSender{}
	engine := New(Options{
		Store:         store,
		SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender, nil },
		Clock:         clock.Now,
		Logger:        testLogger(),
	})
	defer engine.Close()

	now := clock.Now()
	hosts := []AgentOutdatedInput{{HostID: "h1", Hostname: "host-one", Outdated: true, OutdatedSince: now.Unix() - 90000}}
	if err := engine.EvaluateAgentOutdated(t.Context(), now, hosts); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if got := store.getState(1, "h1").State; got != models.AlertStateFiring {
		t.Fatalf("state after firing = %q, want firing", got)
	}

	// Host upgrades: no longer outdated.
	hosts[0].Outdated = false
	hosts[0].OutdatedSince = 0
	if err := engine.EvaluateAgentOutdated(t.Context(), now, hosts); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if got := store.getState(1, "h1").State; got != models.AlertStateOK {
		t.Fatalf("state after resolving = %q, want ok", got)
	}
	if sender.callCount() != 2 {
		t.Fatalf("callCount = %d, want 2 (fire + resolve)", sender.callCount())
	}
}

func TestEvaluateAgentOutdated_ScopedToOneHost(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	clock := newFakeClock(time.Unix(2_000_000, 0))
	store.setChannel(models.NotifyChannel{ID: 1, Name: "ops", Enabled: true, Type: models.NotifyChannelWebhook})
	store.setRule(models.AlertRule{ID: 1, Name: "outdated", Enabled: true, Metric: models.AlertMetricAgentOutdated, HostID: "h1", DurationSec: 1, ChannelIDs: []int64{1}})

	sender := &fakeSender{}
	engine := New(Options{Store: store, SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender, nil }, Clock: clock.Now, Logger: testLogger()})
	defer engine.Close()

	now := clock.Now()
	hosts := []AgentOutdatedInput{
		{HostID: "h1", Outdated: true, OutdatedSince: now.Unix() - 10},
		{HostID: "h2", Outdated: true, OutdatedSince: now.Unix() - 10},
	}
	if err := engine.EvaluateAgentOutdated(t.Context(), now, hosts); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if sender.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1 (only h1 in scope)", sender.callCount())
	}
	if store.getState(1, "h2").State != "" && store.getState(1, "h2").State != models.AlertStateOK {
		t.Errorf("h2 state = %q, want untouched/ok (not in rule scope)", store.getState(1, "h2").State)
	}
}

func TestAgentOutdatedValue(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		name        string
		host        AgentOutdatedInput
		durationSec int
		want        float64
	}{
		{"not_outdated", AgentOutdatedInput{Outdated: false, OutdatedSince: 900000}, 100, 0},
		{"outdated_zero_since", AgentOutdatedInput{Outdated: true, OutdatedSince: 0}, 100, 0},
		{"outdated_not_yet_sustained", AgentOutdatedInput{Outdated: true, OutdatedSince: now.Unix() - 50}, 100, 0},
		{"outdated_sustained", AgentOutdatedInput{Outdated: true, OutdatedSince: now.Unix() - 150}, 100, 1},
		{"zero_duration_uses_default", AgentOutdatedInput{Outdated: true, OutdatedSince: now.Unix() - AgentOutdatedDurationDefaultSec - 1}, 0, 1},
	}
	for _, tc := range cases {
		if got := agentOutdatedValue(now, tc.host, tc.durationSec); got != tc.want {
			t.Errorf("%s: agentOutdatedValue() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// --- update_batch_failed ---

func TestEvaluateUpdateBatch_FiresOnceOnCompletionWithFailure(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	clock := newFakeClock(time.Unix(2_000_000, 0))
	store.setChannel(models.NotifyChannel{ID: 1, Name: "ops", Enabled: true, Type: models.NotifyChannelWebhook})
	store.setRule(models.AlertRule{ID: 1, Name: "batch failed", Enabled: true, Metric: models.AlertMetricUpdateBatchFailed, ChannelIDs: []int64{1}})

	sender := &fakeSender{}
	engine := New(Options{Store: store, SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender, nil }, Clock: clock.Now, Logger: testLogger()})
	defer engine.Close()

	now := clock.Now()
	outcome := UpdateBatchOutcome{
		BatchID: "batch-abc", Target: "latest", Succeeded: 2, Failed: 1, Pending: 0,
		FailedHosts: []FailedUpdateHost{{HostID: "h3", Hostname: "host-three", Reason: models.UpdateReasonTimeout}},
	}
	if err := engine.EvaluateUpdateBatch(t.Context(), now, outcome); err != nil {
		t.Fatalf("EvaluateUpdateBatch error = %v", err)
	}
	engine.Wait()
	if sender.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1", sender.callCount())
	}
	msg := sender.lastMessage()
	if !msg.HasImage() {
		t.Error("message has no chart image attached")
	}

	// Calling again for the same batch must not re-notify.
	if err := engine.EvaluateUpdateBatch(t.Context(), now, outcome); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if sender.callCount() != 1 {
		t.Fatalf("callCount = %d after re-eval, want still 1 (no duplicate)", sender.callCount())
	}
}

func TestEvaluateUpdateBatch_NoFireWhilePending(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	clock := newFakeClock(time.Unix(2_000_000, 0))
	store.setChannel(models.NotifyChannel{ID: 1, Name: "ops", Enabled: true, Type: models.NotifyChannelWebhook})
	store.setRule(models.AlertRule{ID: 1, Name: "batch failed", Enabled: true, Metric: models.AlertMetricUpdateBatchFailed, ChannelIDs: []int64{1}})
	sender := &fakeSender{}
	engine := New(Options{Store: store, SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender, nil }, Clock: clock.Now, Logger: testLogger()})
	defer engine.Close()

	outcome := UpdateBatchOutcome{BatchID: "batch-abc", Failed: 1, Pending: 2}
	if err := engine.EvaluateUpdateBatch(t.Context(), clock.Now(), outcome); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if sender.callCount() != 0 {
		t.Fatalf("callCount = %d, want 0 while batch still has pending jobs", sender.callCount())
	}
}

func TestEvaluateUpdateBatch_NoFireWithoutFailure(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	clock := newFakeClock(time.Unix(2_000_000, 0))
	store.setChannel(models.NotifyChannel{ID: 1, Name: "ops", Enabled: true, Type: models.NotifyChannelWebhook})
	store.setRule(models.AlertRule{ID: 1, Name: "batch failed", Enabled: true, Metric: models.AlertMetricUpdateBatchFailed, ChannelIDs: []int64{1}})
	sender := &fakeSender{}
	engine := New(Options{Store: store, SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender, nil }, Clock: clock.Now, Logger: testLogger()})
	defer engine.Close()

	outcome := UpdateBatchOutcome{BatchID: "batch-ok", Succeeded: 3, Failed: 0, Pending: 0}
	if err := engine.EvaluateUpdateBatch(t.Context(), clock.Now(), outcome); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if sender.callCount() != 0 {
		t.Fatalf("callCount = %d, want 0 for a fully-succeeded batch", sender.callCount())
	}
}

func TestEvaluateUpdateBatch_RestartPersistenceViaAlertState(t *testing.T) {
	t.Parallel()

	// Simulates a hub restart: a fresh Engine backed by the same Store
	// (which already has the alert_state row from a prior process)
	// must still recognize the batch as already-notified.
	store := newFakeStore()
	clock := newFakeClock(time.Unix(2_000_000, 0))
	store.setChannel(models.NotifyChannel{ID: 1, Name: "ops", Enabled: true, Type: models.NotifyChannelWebhook})
	store.setRule(models.AlertRule{ID: 1, Name: "batch failed", Enabled: true, Metric: models.AlertMetricUpdateBatchFailed, ChannelIDs: []int64{1}})

	sender1 := &fakeSender{}
	engine1 := New(Options{Store: store, SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender1, nil }, Clock: clock.Now, Logger: testLogger()})
	outcome := UpdateBatchOutcome{BatchID: "batch-xyz", Failed: 1, Pending: 0}
	if err := engine1.EvaluateUpdateBatch(t.Context(), clock.Now(), outcome); err != nil {
		t.Fatal(err)
	}
	engine1.Wait()
	engine1.Close()
	if sender1.callCount() != 1 {
		t.Fatalf("first engine callCount = %d, want 1", sender1.callCount())
	}

	// "Restart": a new Engine instance, same store.
	sender2 := &fakeSender{}
	engine2 := New(Options{Store: store, SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender2, nil }, Clock: clock.Now, Logger: testLogger()})
	defer engine2.Close()
	if err := engine2.EvaluateUpdateBatch(t.Context(), clock.Now(), outcome); err != nil {
		t.Fatal(err)
	}
	engine2.Wait()
	if sender2.callCount() != 0 {
		t.Fatalf("second engine (post-restart) callCount = %d, want 0 (already notified before restart)", sender2.callCount())
	}
}

func TestEvaluateUpdateBatch_DisabledRuleSkipped(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	clock := newFakeClock(time.Unix(2_000_000, 0))
	store.setChannel(models.NotifyChannel{ID: 1, Name: "ops", Enabled: true, Type: models.NotifyChannelWebhook})
	store.setRule(models.AlertRule{ID: 1, Name: "batch failed", Enabled: false, Metric: models.AlertMetricUpdateBatchFailed, ChannelIDs: []int64{1}})
	sender := &fakeSender{}
	engine := New(Options{Store: store, SenderFactory: func(models.NotifyChannel) (Sender, error) { return sender, nil }, Clock: clock.Now, Logger: testLogger()})
	defer engine.Close()

	outcome := UpdateBatchOutcome{BatchID: "batch-abc", Failed: 1, Pending: 0}
	if err := engine.EvaluateUpdateBatch(t.Context(), clock.Now(), outcome); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if sender.callCount() != 0 {
		t.Fatalf("callCount = %d, want 0 for a disabled rule", sender.callCount())
	}
}

func TestBuildUpdateBatchMessage_ListsFailedHostsAndReasons(t *testing.T) {
	t.Parallel()
	outcome := UpdateBatchOutcome{
		BatchID: "batch-abc", Target: "latest", Succeeded: 1, Failed: 2,
		FailedHosts: []FailedUpdateHost{
			{HostID: "h1", Hostname: "host-one", Reason: models.UpdateReasonTimeout},
			{HostID: "h2", Hostname: "", Reason: models.UpdateReasonDownloadFailed},
		},
	}
	rule := models.AlertRule{Name: "batch failed"}
	msg := buildUpdateBatchMessage(rule, outcome)

	foundHostOne, foundH2 := false, false
	for _, f := range msg.Fields {
		if f.Name == "host-one" && f.Value == string(models.UpdateReasonTimeout) {
			foundHostOne = true
		}
		if f.Name == "h2" && f.Value == string(models.UpdateReasonDownloadFailed) {
			foundH2 = true
		}
	}
	if !foundHostOne {
		t.Errorf("message fields missing host-one/timeout: %+v", msg.Fields)
	}
	if !foundH2 {
		t.Errorf("message fields missing h2 (hostname fallback)/download_failed: %+v", msg.Fields)
	}
}

func TestRenderUpdateBatchChart_NeverErrors(t *testing.T) {
	t.Parallel()
	cases := []UpdateBatchOutcome{
		{BatchID: "b1", Succeeded: 0, Failed: 0},
		{BatchID: "b2", Succeeded: 3, Failed: 0},
		{BatchID: "b3", Succeeded: 0, Failed: 2, FailedHosts: []FailedUpdateHost{{Reason: models.UpdateReasonTimeout}, {Reason: models.UpdateReasonDownloadFailed}}},
	}
	for _, outcome := range cases {
		if img := renderUpdateBatchChart(outcome); len(img) == 0 {
			t.Errorf("renderUpdateBatchChart(%+v) returned empty image", outcome)
		}
	}
}
