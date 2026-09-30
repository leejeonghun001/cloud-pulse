package alerting

import (
	"context"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestEvaluateStorage_ImmediateFiring_NoDurationConcept(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	// DurationSec is set but must be ignored for storage_usage_pct
	// (SPEC-v0.7 §3: "지속 시간 미사용") — still fires immediately.
	store.setRule(models.AlertRule{
		ID: 1, Name: "Drive above 90%", Enabled: true, Metric: models.AlertMetricStorageUsagePct,
		Operator: models.AlertOperatorGTE, Threshold: 90, DurationSec: 300, CooldownSec: 86400,
		ChannelIDs: []int64{1},
	})

	accounts := []StorageAccountUsage{{AccountID: 42, AccountName: "My Drive", UsedPercent: 95}}
	if err := e.EvaluateStorage(context.Background(), clock.Now(), accounts); err != nil {
		t.Fatalf("EvaluateStorage: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
	st := store.getState(1, "42")
	if st.State != models.AlertStateFiring {
		t.Fatalf("state = %s, want firing", st.State)
	}
}

func TestEvaluateStorage_UnlimitedAccountNeverFires(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "Drive above 0%", Enabled: true, Metric: models.AlertMetricStorageUsagePct,
		Operator: models.AlertOperatorGTE, Threshold: 0, CooldownSec: 86400,
		ChannelIDs: []int64{1},
	})

	accounts := []StorageAccountUsage{{AccountID: 1, UsedPercent: 0, Unlimited: true}}
	if err := e.EvaluateStorage(context.Background(), clock.Now(), accounts); err != nil {
		t.Fatalf("EvaluateStorage: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0 (unlimited account must never fire)", got)
	}
}

func TestEvaluateStorage_ScopedToOneAccountByHostIDField(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "Only account 5", Enabled: true, Metric: models.AlertMetricStorageUsagePct,
		HostID: "5", Operator: models.AlertOperatorGTE, Threshold: 50, CooldownSec: 86400,
		ChannelIDs: []int64{1},
	})

	accounts := []StorageAccountUsage{
		{AccountID: 5, UsedPercent: 80},
		{AccountID: 6, UsedPercent: 99}, // not scoped to this rule
	}
	if err := e.EvaluateStorage(context.Background(), clock.Now(), accounts); err != nil {
		t.Fatalf("EvaluateStorage: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1 (only account 5 scoped)", got)
	}
	if st := store.getState(1, "6"); st.State != "" {
		t.Errorf("account 6 (out of scope) state = %q, want empty (never evaluated)", st.State)
	}
}

func TestEvaluateStorage_EmptyHostIDAppliesToEveryAccount(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "Every account above 50%", Enabled: true, Metric: models.AlertMetricStorageUsagePct,
		Operator: models.AlertOperatorGTE, Threshold: 50, CooldownSec: 86400,
		ChannelIDs: []int64{1},
	})

	accounts := []StorageAccountUsage{
		{AccountID: 1, UsedPercent: 80},
		{AccountID: 2, UsedPercent: 90},
	}
	if err := e.EvaluateStorage(context.Background(), clock.Now(), accounts); err != nil {
		t.Fatalf("EvaluateStorage: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 2 {
		t.Fatalf("sender calls = %d, want 2 (both accounts breach)", got)
	}
}

func TestEvaluateStorage_ResolvesWhenBelowThreshold(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "Drive above 90%", Enabled: true, Metric: models.AlertMetricStorageUsagePct,
		Operator: models.AlertOperatorGTE, Threshold: 90, CooldownSec: 86400, NotifyResolved: true,
		ChannelIDs: []int64{1},
	})

	accounts := []StorageAccountUsage{{AccountID: 1, UsedPercent: 95}}
	if err := e.EvaluateStorage(context.Background(), clock.Now(), accounts); err != nil {
		t.Fatalf("EvaluateStorage (fire): %v", err)
	}
	e.Wait()
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls after firing = %d, want 1", got)
	}

	clock.Advance(time.Hour)
	accounts[0].UsedPercent = 40
	if err := e.EvaluateStorage(context.Background(), clock.Now(), accounts); err != nil {
		t.Fatalf("EvaluateStorage (resolve): %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 2 {
		t.Fatalf("sender calls after resolving = %d, want 2 (fire + resolve)", got)
	}
	st := store.getState(1, "1")
	if st.State != models.AlertStateOK {
		t.Errorf("state after drop = %s, want ok", st.State)
	}
}

func TestEvaluateStorage_DisabledRuleSkipped(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "disabled", Enabled: false, Metric: models.AlertMetricStorageUsagePct,
		Operator: models.AlertOperatorGTE, Threshold: 0, ChannelIDs: []int64{1},
	})

	accounts := []StorageAccountUsage{{AccountID: 1, UsedPercent: 99}}
	if err := e.EvaluateStorage(context.Background(), clock.Now(), accounts); err != nil {
		t.Fatalf("EvaluateStorage: %v", err)
	}
	e.Close()
	if got := sender.callCount(); got != 0 {
		t.Errorf("sender calls = %d, want 0 for a disabled rule", got)
	}
}

func TestEvaluateStorage_NonStorageMetricRuleIgnored(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "cpu rule", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 0, ChannelIDs: []int64{1},
	})

	accounts := []StorageAccountUsage{{AccountID: 1, UsedPercent: 99}}
	if err := e.EvaluateStorage(context.Background(), clock.Now(), accounts); err != nil {
		t.Fatalf("EvaluateStorage: %v", err)
	}
	e.Close()
	if got := sender.callCount(); got != 0 {
		t.Errorf("sender calls = %d, want 0 (a cpu-metric rule must not evaluate storage accounts)", got)
	}
}

func TestScopedStorageAccounts_InvalidHostIDYieldsNoMatches(t *testing.T) {
	t.Parallel()
	rule := models.AlertRule{HostID: "not-a-number"}
	accounts := []StorageAccountUsage{{AccountID: 1}, {AccountID: 2}}
	got := scopedStorageAccounts(rule, accounts)
	if len(got) != 0 {
		t.Errorf("scopedStorageAccounts with a non-numeric HostID = %v, want empty", got)
	}
}

func TestMetricUnit_StorageUsagePctIsPercent(t *testing.T) {
	t.Parallel()
	if got := metricUnit(models.AlertMetricStorageUsagePct); got != "%" {
		t.Errorf("metricUnit(storage_usage_pct) = %q, want %%", got)
	}
}

func TestBuildChartImage_StorageMetricNeverProducesAChart(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	e := newTestEngine(t, store, clock, func(models.NotifyChannel) (Sender, error) { return &fakeSender{}, nil })

	rule := models.AlertRule{ID: 1, Metric: models.AlertMetricStorageUsagePct, Threshold: 90}
	host := models.HostSnapshot{HostID: "1", Hostname: "acct"}
	ev := models.AlertEvent{StartedAt: clock.Now().Unix()}

	img := e.buildChartImage(context.Background(), clock.Now(), rule, host, ev)
	if img != nil {
		t.Error("expected no chart image for storage_usage_pct (no queryable series)")
	}
}
