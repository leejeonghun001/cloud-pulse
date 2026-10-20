package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
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
		AccountID:      "ocid1.tenancy.oc1..t",
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

	got := store.cloudCostSnapshots["oci:ocid1.tenancy.oc1..t"]
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
		"aws bad account": {Provider: models.CloudBillingAWS, AccountID: "12345", Status: models.CloudBillingOK},
		"bad tenancy":     {Provider: models.CloudBillingOCI, AccountID: "ocid1.compartment.oc1..x", Status: models.CloudBillingOK},
		"unknown status":  {Provider: models.CloudBillingOCI, Status: "great"},
		"huge cost":       {Provider: models.CloudBillingOCI, Status: models.CloudBillingOK, MTDCost: 1e15},
		"empty resource":  {Provider: models.CloudBillingOCI, Status: models.CloudBillingOK, PerResource: map[string]float64{"": 1}},
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
	store.cloudCostSnapshots["aws"] = models.CloudCostSnapshot{Provider: models.CloudBillingAWS, Status: models.CloudBillingNotInstalled}
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
	want := []alerting.CloudCostInput{{Provider: models.CloudBillingOCI, AccountID: "host:oci-box", Currency: "SGD", MTD: 3, Forecast: 6}}
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

func TestBillingRefresh_RelaysToAgentOnItsNextReport(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	opts.Billing = &BillingRuntime{Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()}), Now: func() time.Time { return now }}
	store.cloudCostSnapshots["oci:ocid1.tenancy.oc1..t"] = models.CloudCostSnapshot{Provider: models.CloudBillingOCI, AccountID: "ocid1.tenancy.oc1..t", Status: models.CloudBillingOK, SourceHost: "oci-box", LastSuccessAt: 1}
	s := newTestServer(t, opts, store)

	refresh := func() *httptest.ResponseRecorder {
		return doRequest(t, s.Handler(), http.MethodPost, "/api/v1/billing/refresh", "127.0.0.1:1", testUIToken, nil)
	}
	report := func() models.IngestResponse {
		body, _ := json.Marshal(models.AgentReport{Host: sampleHostInfo("oci-box"), Samples: []models.Sample{{Timestamp: now.Unix()}}})
		rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
		return decodeJSON[models.IngestResponse](t, rec.Body)
	}

	rec := refresh()
	if rec.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d, want 200", rec.Code)
	}
	got := decodeJSON[map[string]any](t, rec.Body)
	if req, _ := got["agent_refresh_requested"].([]any); len(req) != 1 || req[0] != "oci-box" {
		t.Errorf("agent_refresh_requested = %v, want [oci-box]", got["agent_refresh_requested"])
	}
	if !report().CloudRefresh {
		t.Error("the agent's next report should carry cloud_refresh")
	}
	if report().CloudRefresh {
		t.Error("cloud_refresh must be handed out once")
	}

	// Both the hub (10 min) and this agent (2 min) were just refreshed.
	if rec := refresh(); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("immediate second refresh status = %d, want 429", rec.Code)
	}
}

func TestIngest_AgentAccountsStaySeparateAndPlaceholderIsReplaced(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	opts.Billing = &BillingRuntime{Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()})}
	s := newTestServer(t, opts, store)
	post := func(host string, snap models.CloudCostSnapshot) {
		body, _ := json.Marshal(models.AgentReport{Host: sampleHostInfo(host), Samples: []models.Sample{{Timestamp: now.Unix()}}, CloudCost: &snap})
		if rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}

	// Pre-upgrade agent: no account ID yet.
	post("box-a", models.CloudCostSnapshot{Provider: models.CloudBillingOCI, Status: models.CloudBillingOK, Currency: "SGD"})
	if _, ok := store.cloudCostSnapshots["oci:host:box-a"]; !ok {
		t.Fatalf("snapshots = %v, want a host: placeholder for the legacy agent", store.cloudCostSnapshots)
	}
	// Upgraded: real tenancy replaces the placeholder.
	post("box-a", models.CloudCostSnapshot{Provider: models.CloudBillingOCI, AccountID: "ocid1.tenancy.oc1..a", Status: models.CloudBillingOK, Currency: "SGD"})
	// A later report without the account (a failed lookup) must not
	// displace the real one.
	post("box-a", models.CloudCostSnapshot{Provider: models.CloudBillingOCI, Status: models.CloudBillingError})
	delete(store.cloudCostSnapshots, "oci:host:box-a")
	// A second tenancy from another agent, and an AWS account.
	post("box-b", models.CloudCostSnapshot{Provider: models.CloudBillingOCI, AccountID: "ocid1.tenancy.oc1..b", Status: models.CloudBillingOK, Currency: "USD"})
	post("ec2-1", models.CloudCostSnapshot{Provider: models.CloudBillingAWS, AccountID: "123456789012", Status: models.CloudBillingOK, Currency: "USD"})

	keys := make([]string, 0, len(store.cloudCostSnapshots))
	for k := range store.cloudCostSnapshots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"aws:123456789012", "oci:ocid1.tenancy.oc1..a", "oci:ocid1.tenancy.oc1..b"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("accounts = %v, want %v", keys, want)
	}
}

func TestBillingAccounts_LabelAndDelete(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.Billing = &BillingRuntime{Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()})}
	store.cloudCostSnapshots["oci:ocid1.tenancy.oc1..a"] = models.CloudCostSnapshot{Provider: models.CloudBillingOCI, AccountID: "ocid1.tenancy.oc1..a"}
	s := newTestServer(t, opts, store)

	body := []byte(`{"provider":"oci","account_id":"ocid1.tenancy.oc1..a","label":"  업무 테넌시  "}`)
	if rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/billing/accounts/label", "127.0.0.1:1", testUIToken, body); rec.Code != http.StatusOK {
		t.Fatalf("label status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := store.cloudCostSnapshots["oci:ocid1.tenancy.oc1..a"].Label; got != "업무 테넌시" {
		t.Errorf("label = %q, want trimmed", got)
	}
	if rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/billing/accounts/label", "127.0.0.1:1", testUIToken, []byte(`{"provider":"oci","account_id":"missing","label":"x"}`)); rec.Code != http.StatusNotFound {
		t.Errorf("label on missing account status = %d, want 404", rec.Code)
	}
	if rec := doRequest(t, s.Handler(), http.MethodDelete, "/api/v1/billing/accounts?provider=oci&account_id=ocid1.tenancy.oc1..a", "127.0.0.1:1", testUIToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rec.Code)
	}
	if len(store.cloudCostSnapshots) != 0 {
		t.Errorf("snapshots = %v, want the account deleted", store.cloudCostSnapshots)
	}
}

func TestEvaluateCostAlerts_ClosesAlertsOfRemovedAccounts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	opts.Alerting = &fakeCostAlertEngine{}
	opts.Billing = &BillingRuntime{Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()})}
	store.cloudCostSnapshots["oci:ocid1.tenancy.oc1..a"] = models.CloudCostSnapshot{Provider: models.CloudBillingOCI, AccountID: "ocid1.tenancy.oc1..a", LastSuccessAt: 1}
	s := newTestServer(t, opts, store)

	orphan, err := store.CreateAlertEvent(t.Context(), models.AlertEvent{RuleID: 4, HostID: "oci:host:old-box", Metric: models.AlertMetricCostMTD, State: models.AlertEventFiring, StartedAt: 1})
	if err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}
	live, err := store.CreateAlertEvent(t.Context(), models.AlertEvent{RuleID: 4, HostID: "oci:ocid1.tenancy.oc1..a", Metric: models.AlertMetricCostMTD, State: models.AlertEventFiring, StartedAt: 1})
	if err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}

	s.evaluateCostAlerts(t.Context(), now)

	if got, _ := store.GetAlertEvent(t.Context(), orphan.ID); got.State != models.AlertEventResolved || got.ResolvedAt != now.Unix() {
		t.Errorf("orphan event = %+v, want resolved now", got)
	}
	if got, _ := store.GetAlertEvent(t.Context(), live.ID); got.State != models.AlertEventFiring {
		t.Errorf("live account's event = %+v, want still firing", got)
	}
}
