package hub

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

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
