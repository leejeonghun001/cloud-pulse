package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func postAgentCloudCost(t *testing.T, enableBilling bool, snap models.CloudCostSnapshot) (*fakeStore, time.Time) {
	t.Helper()
	store := newFakeStore()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	if enableBilling {
		opts.Billing = &BillingRuntime{Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()})}
	}
	s := newTestServer(t, opts, store)

	body, err := json.Marshal(models.AgentReport{
		Host:      sampleHostInfo("oci-box"),
		Samples:   []models.Sample{{Timestamp: now.Unix(), CPUPercent: 1}},
		CloudCost: &snap,
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	return store, now
}

func TestIngest_AgentCloudCostPersisted(t *testing.T) {
	t.Parallel()
	store, now := postAgentCloudCost(t, true, models.CloudCostSnapshot{
		Provider:       models.CloudBillingOCI,
		Status:         models.CloudBillingOK,
		Currency:       "USD",
		MTDCost:        12.5,
		ForecastCost:   25,
		ForecastMethod: "linear",
		PerResource:    map[string]float64{"ocid1.instance.oc1..a": 12.5},
		SourceHost:     "spoofed",
		LastSuccessAt:  1,
		Stale:          true,
	})

	got := store.cloudCostSnapshots[models.CloudBillingOCI]
	if got.SourceHost != "oci-box" {
		t.Errorf("SourceHost = %q, want the reporting host, not the agent's claim", got.SourceHost)
	}
	if got.Status != models.CloudBillingOK || got.MTDCost != 12.5 || got.PerResource["ocid1.instance.oc1..a"] != 12.5 {
		t.Errorf("snapshot = %+v, want the reported cost", got)
	}
	if got.LastSuccessAt != now.Unix() || got.Stale {
		t.Errorf("LastSuccessAt/Stale = %d/%v, want hub-assigned %d/false", got.LastSuccessAt, got.Stale, now.Unix())
	}
}

func TestIngest_AgentCloudCostRejected(t *testing.T) {
	t.Parallel()
	cases := map[string]models.CloudCostSnapshot{
		"aws provider":   {Provider: models.CloudBillingAWS, Status: models.CloudBillingOK},
		"unknown status": {Provider: models.CloudBillingOCI, Status: "great"},
		"huge cost":      {Provider: models.CloudBillingOCI, Status: models.CloudBillingOK, MTDCost: 1e15},
		"empty resource": {Provider: models.CloudBillingOCI, Status: models.CloudBillingOK, PerResource: map[string]float64{"": 1}},
	}
	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, _ := postAgentCloudCost(t, true, snap)
			if len(store.cloudCostSnapshots) != 0 {
				t.Errorf("snapshots = %+v, want none stored", store.cloudCostSnapshots)
			}
		})
	}
}

func TestIngest_AgentCloudCostIgnoredWithBillingOff(t *testing.T) {
	t.Parallel()
	store, _ := postAgentCloudCost(t, false, models.CloudCostSnapshot{Provider: models.CloudBillingOCI, Status: models.CloudBillingOK, MTDCost: 1})
	if len(store.cloudCostSnapshots) != 0 {
		t.Errorf("snapshots = %+v, want none stored with CP_BILLING=off", store.cloudCostSnapshots)
	}
}

// fakeCostAlertEngine records EvaluateCost calls.
type fakeCostAlertEngine struct {
	mu    sync.Mutex
	calls [][]alerting.CloudCostInput
}

func (f *fakeCostAlertEngine) Evaluate(context.Context, time.Time, []models.HostSnapshot) error {
	return nil
}

func (f *fakeCostAlertEngine) Preview(context.Context, time.Time, models.AlertRule, []models.HostSnapshot) (map[string]bool, error) {
	return nil, nil
}

func (f *fakeCostAlertEngine) EvaluateCost(_ context.Context, _ time.Time, providers []alerting.CloudCostInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, providers)
	return nil
}

func TestIngest_AgentCloudCostEvaluatesCostAlerts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	engine := &fakeCostAlertEngine{}
	opts := testOptions()
	opts.Now = fixedNow(now)
	opts.Alerting = engine
	opts.Billing = &BillingRuntime{Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()})}
	// A provider that never collected must not reach the evaluator.
	store.cloudCostSnapshots[models.CloudBillingAWS] = models.CloudCostSnapshot{Provider: models.CloudBillingAWS, Status: models.CloudBillingNotInstalled}
	s := newTestServer(t, opts, store)

	body, err := json.Marshal(models.AgentReport{
		Host:    sampleHostInfo("oci-box"),
		Samples: []models.Sample{{Timestamp: now.Unix(), CPUPercent: 1}},
		CloudCost: &models.CloudCostSnapshot{
			Provider: models.CloudBillingOCI, Status: models.CloudBillingOK, Currency: "SGD", MTDCost: 3, ForecastCost: 6,
		},
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.calls) != 1 {
		t.Fatalf("EvaluateCost calls = %d, want 1", len(engine.calls))
	}
	want := []alerting.CloudCostInput{{Provider: models.CloudBillingOCI, Currency: "SGD", MTD: 3, Forecast: 6}}
	if got := engine.calls[0]; len(got) != 1 || got[0] != want[0] {
		t.Errorf("EvaluateCost providers = %+v, want %+v", got, want)
	}
}

func TestValidateRuleRequest_CostMetrics(t *testing.T) {
	t.Parallel()
	rule, details := validateRuleRequest(ruleRequest{Name: "budget", Metric: "cost_forecast", Operator: ">=", Threshold: 20, HostID: "oci"})
	if len(details) != 0 {
		t.Fatalf("details = %v, want none", details)
	}
	if rule.CooldownSec != 86400 {
		t.Errorf("CooldownSec = %d, want the daily default 86400", rule.CooldownSec)
	}
	for _, bad := range []ruleRequest{
		{Name: "x", Metric: "cost_mtd", Operator: ">=", HostID: "gcp"},
		{Name: "x", Metric: "cost_mtd", Operator: ">=", Threshold: -1},
	} {
		if _, details := validateRuleRequest(bad); len(details) == 0 {
			t.Errorf("validateRuleRequest(%+v) accepted, want a validation error", bad)
		}
	}
}

func TestIngest_AgentCloudNetworkStoredAndKeptOnFailure(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	post := func(u models.CloudNetworkUsage) {
		body, err := json.Marshal(models.AgentReport{
			Host:         sampleHostInfo("oci-box"),
			Samples:      []models.Sample{{Timestamp: now.Unix(), CPUPercent: 1}},
			CloudNetwork: &u,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}

	post(models.CloudNetworkUsage{Provider: models.CloudBillingOCI, Month: "2026-10", InBytes: 24e9, OutBytes: 5e9, Status: models.CloudBillingOK})
	post(models.CloudNetworkUsage{Provider: models.CloudBillingOCI, Status: models.CloudBillingPermissionDenied, StatusDetail: "CLI reported a permission error"})

	got := store.cloudNetwork["oci-box"]
	if got.Status != models.CloudBillingPermissionDenied || got.Month != "2026-10" || got.InBytes != 24e9 || got.LastSuccessAt != now.Unix() {
		t.Fatalf("stored = %+v, want the failure status over the last good figures", got)
	}

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts/oci-box", "127.0.0.1:1", opts.UIToken, nil)
	summary := decodeJSON[models.HostSummary](t, rec.Body)
	if summary.CloudNetwork == nil || summary.CloudNetwork.OutBytes != 5e9 {
		t.Errorf("host summary cloud_network = %+v, want the stored usage", summary.CloudNetwork)
	}

	post(models.CloudNetworkUsage{Provider: models.CloudBillingAWS, Month: "2026-10", Status: models.CloudBillingOK})
	if store.cloudNetwork["oci-box"].Provider != models.CloudBillingOCI {
		t.Error("a non-OCI usage must be dropped")
	}
}
