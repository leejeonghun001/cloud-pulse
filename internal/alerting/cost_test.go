package alerting

import (
	"context"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func newCostTestEngine(t *testing.T, rules ...models.AlertRule) (*Engine, *fakeStore, *fakeSender, *fakeClock) {
	t.Helper()
	store := newFakeStore()
	clock := newFakeClock(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	sender := &fakeSender{}
	e := newTestEngine(t, store, clock, func(models.NotifyChannel) (Sender, error) { return sender, nil })
	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	for _, r := range rules {
		store.setRule(r)
	}
	return e, store, sender, clock
}

func TestEvaluateCost_ForecastBudgetFiresWithCurrencyUnit(t *testing.T) {
	t.Parallel()
	e, store, sender, clock := newCostTestEngine(t, models.AlertRule{
		ID: 1, Name: "OCI budget", Enabled: true, Metric: models.AlertMetricCostForecast, HostID: "oci",
		Operator: models.AlertOperatorGTE, Threshold: 20, DurationSec: 3600, CooldownSec: 86400, ChannelIDs: []int64{1},
	})
	providers := []CloudCostInput{
		{Provider: models.CloudBillingOCI, Currency: "SGD", MTD: 9, Forecast: 25.5},
		{Provider: models.CloudBillingAWS, Currency: "USD", MTD: 500, Forecast: 900},
	}
	if err := e.EvaluateCost(context.Background(), clock.Now(), providers); err != nil {
		t.Fatalf("EvaluateCost: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1 (scoped to oci, DurationSec ignored)", got)
	}
	if st := store.getState(1, "oci"); st.State != models.AlertStateFiring {
		t.Fatalf("oci state = %s, want firing", st.State)
	}
	msg := sender.calls[0]
	if want := "OCI budget is 25.50 SGD (threshold 20.00 SGD)"; msg.Text != want {
		t.Errorf("Text = %q, want %q", msg.Text, want)
	}
}

func TestEvaluateCost_MTDUnderBudgetAndAllProviders(t *testing.T) {
	t.Parallel()
	e, store, sender, clock := newCostTestEngine(t, models.AlertRule{
		ID: 2, Name: "Any spend", Enabled: true, Metric: models.AlertMetricCostMTD,
		Operator: models.AlertOperatorGT, Threshold: 100, CooldownSec: 86400, ChannelIDs: []int64{1},
	})
	providers := []CloudCostInput{
		{Provider: models.CloudBillingOCI, Currency: "SGD", MTD: 99, Forecast: 500},
		{Provider: models.CloudBillingAWS, Currency: "USD", MTD: 150, Forecast: 150},
	}
	if err := e.EvaluateCost(context.Background(), clock.Now(), providers); err != nil {
		t.Fatalf("EvaluateCost: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1 (only AWS MTD is over)", got)
	}
	if st := store.getState(2, "aws"); st.State != models.AlertStateFiring {
		t.Errorf("aws state = %s, want firing", st.State)
	}
	if st := store.getState(2, "oci"); st.State == models.AlertStateFiring {
		t.Errorf("oci state = %s, want not firing (forecast is irrelevant to cost_mtd)", st.State)
	}
}

func TestEvaluate_SkipsAccountScopedMetrics(t *testing.T) {
	t.Parallel()
	e, _, sender, clock := newCostTestEngine(t,
		models.AlertRule{ID: 3, Name: "cost", Enabled: true, Metric: models.AlertMetricCostMTD, Operator: models.AlertOperatorGTE, ChannelIDs: []int64{1}},
		models.AlertRule{ID: 4, Name: "drive", Enabled: true, Metric: models.AlertMetricStorageUsagePct, Operator: models.AlertOperatorGTE, ChannelIDs: []int64{1}},
	)
	hosts := []models.HostSnapshot{{HostID: "h1", Hostname: "h1", Status: models.HostUp, LastSeen: clock.Now().Unix(), Latest: &models.Sample{}}}
	if err := e.Evaluate(context.Background(), clock.Now(), hosts); err != nil {
		t.Fatalf("Evaluate returned %v, want nil: account-scoped rules have their own evaluators", err)
	}
	e.Close()
	if got := sender.callCount(); got != 0 {
		t.Fatalf("sender calls = %d, want 0", got)
	}
}

func TestEvaluateCost_OneAccountScopeAndLabelledNotification(t *testing.T) {
	t.Parallel()
	e, store, sender, clock := newCostTestEngine(t, models.AlertRule{
		ID: 5, Name: "Work budget", Enabled: true, Metric: models.AlertMetricCostMTD, HostID: "oci:ocid1.tenancy.oc1..work",
		Operator: models.AlertOperatorGTE, Threshold: 10, CooldownSec: 86400, ChannelIDs: []int64{1},
	})
	accounts := []CloudCostInput{
		{Provider: models.CloudBillingOCI, AccountID: "ocid1.tenancy.oc1..work", Label: "work", Currency: "SGD", MTD: 12},
		{Provider: models.CloudBillingOCI, AccountID: "ocid1.tenancy.oc1..home", Currency: "SGD", MTD: 99},
	}
	if err := e.EvaluateCost(context.Background(), clock.Now(), accounts); err != nil {
		t.Fatalf("EvaluateCost: %v", err)
	}
	e.Close()
	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1 (only the scoped account)", got)
	}
	if st := store.getState(5, "oci:ocid1.tenancy.oc1..work"); st.State != models.AlertStateFiring {
		t.Errorf("state = %s, want firing under the account key", st.State)
	}
	if want := "OCI · work: Work budget"; sender.calls[0].Title != want {
		t.Errorf("title = %q, want %q", sender.calls[0].Title, want)
	}
}
