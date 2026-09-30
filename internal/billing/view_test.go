package billing

import (
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestBuildBillingView_MatchedHost(t *testing.T) {
	snapshots := []models.CloudCostSnapshot{
		{
			Provider:     models.CloudBillingAWS,
			Status:       models.CloudBillingOK,
			MTDCost:      100,
			ForecastCost: 200,
			AccountLevel: false,
			PerResource:  map[string]float64{"i-abc": 40, "i-def": 60},
		},
	}
	hosts := []models.HostRecord{
		{Info: models.HostInfo{ID: "host1", Hostname: "web1", Provider: models.ProviderAWS, CloudInstanceID: "i-abc"}},
	}

	view := BuildBillingView(snapshots, hosts, nil, 86400, models.DisplayCurrencySettings{Currency: models.DisplayCurrencyUSD})

	if len(view.Hosts) != 1 {
		t.Fatalf("len(Hosts) = %d, want 1", len(view.Hosts))
	}
	hc := view.Hosts[0]
	if !hc.Matched {
		t.Error("Matched = false, want true")
	}
	if hc.CloudMTD != 40 {
		t.Errorf("CloudMTD = %v, want 40", hc.CloudMTD)
	}
	// forecast approximated by (matched MTD / account MTD) * account forecast = 40/100*200 = 80
	if hc.CloudForecast != 80 {
		t.Errorf("CloudForecast = %v, want 80", hc.CloudForecast)
	}
}

// TestBuildBillingView_UnmatchedHostGracefully verifies notes/v06-prep.md
// flag (b): a host whose CloudInstanceID is empty (agent stub always
// returns "" until this stage lands) never panics and reports
// Matched=false with account-level totals surfaced when available.
func TestBuildBillingView_UnmatchedHostGracefully(t *testing.T) {
	snapshots := []models.CloudCostSnapshot{
		{
			Provider:     models.CloudBillingAWS,
			Status:       models.CloudBillingOK,
			MTDCost:      500,
			ForecastCost: 900,
			AccountLevel: true,
		},
	}
	hosts := []models.HostRecord{
		{Info: models.HostInfo{ID: "host1", Hostname: "web1", Provider: models.ProviderAWS, CloudInstanceID: ""}},
	}

	view := BuildBillingView(snapshots, hosts, nil, 86400, models.DisplayCurrencySettings{})

	hc := view.Hosts[0]
	if hc.Matched {
		t.Error("Matched = true, want false for an empty CloudInstanceID")
	}
	if hc.CloudMTD != 500 || hc.CloudForecast != 900 {
		t.Errorf("account-level totals not surfaced: CloudMTD=%v CloudForecast=%v", hc.CloudMTD, hc.CloudForecast)
	}
}

func TestBuildBillingView_NoSnapshotForProvider(t *testing.T) {
	hosts := []models.HostRecord{
		{Info: models.HostInfo{ID: "host1", Provider: models.ProviderOCI, CloudInstanceID: "ocid1.instance.oc1..a"}},
	}
	view := BuildBillingView(nil, hosts, nil, 86400, models.DisplayCurrencySettings{})
	hc := view.Hosts[0]
	if hc.Matched {
		t.Error("Matched = true, want false when no snapshot exists for the provider")
	}
	if hc.CloudMTD != 0 {
		t.Errorf("CloudMTD = %v, want 0", hc.CloudMTD)
	}
}

func TestBuildBillingView_OtherProviderNeverMatchesBilling(t *testing.T) {
	hosts := []models.HostRecord{
		{Info: models.HostInfo{ID: "host1", Provider: models.ProviderOther}},
	}
	view := BuildBillingView(nil, hosts, nil, 86400, models.DisplayCurrencySettings{})
	hc := view.Hosts[0]
	if hc.Provider != "" {
		t.Errorf("Provider = %q, want empty for models.ProviderOther", hc.Provider)
	}
}

func TestBuildBillingView_NetworkEstimateAlwaysApplied(t *testing.T) {
	hosts := []models.HostRecord{
		{Info: models.HostInfo{ID: "host1", Provider: models.ProviderOther}},
	}
	estimator := func(hostID string) models.EstimatedCost {
		if hostID != "host1" {
			t.Errorf("estimator called with %q, want host1", hostID)
		}
		return models.EstimatedCost{MTD: 5, Projected: 15, PlanID: 1, PlanName: "Test Plan"}
	}

	view := BuildBillingView(nil, hosts, estimator, 86400, models.DisplayCurrencySettings{})
	hc := view.Hosts[0]
	if hc.NetworkEstimate.MTD != 5 || hc.NetworkEstimate.Projected != 15 {
		t.Errorf("NetworkEstimate = %+v, want MTD=5 Projected=15", hc.NetworkEstimate)
	}
	if hc.TotalMTD != 5 || hc.TotalForecast != 15 {
		t.Errorf("TotalMTD/TotalForecast = %v/%v, want 5/15 (no cloud cost)", hc.TotalMTD, hc.TotalForecast)
	}
}

func TestBuildBillingView_NilEstimatorUsesZero(t *testing.T) {
	hosts := []models.HostRecord{{Info: models.HostInfo{ID: "host1"}}}
	view := BuildBillingView(nil, hosts, nil, 86400, models.DisplayCurrencySettings{})
	if view.Hosts[0].NetworkEstimate != (models.EstimatedCost{}) {
		t.Errorf("NetworkEstimate = %+v, want zero value with a nil estimator", view.Hosts[0].NetworkEstimate)
	}
}

func TestBuildBillingView_EchoesIntervalAndDisplayCurrency(t *testing.T) {
	view := BuildBillingView(nil, nil, nil, 3600, models.DisplayCurrencySettings{Currency: models.DisplayCurrencyKRW, KRWPerUSD: 1385.5})
	if view.IntervalSeconds != 3600 {
		t.Errorf("IntervalSeconds = %d, want 3600", view.IntervalSeconds)
	}
	if view.DisplayCurrency.Currency != models.DisplayCurrencyKRW || view.DisplayCurrency.KRWPerUSD != 1385.5 {
		t.Errorf("DisplayCurrency = %+v, want KRW @ 1385.5", view.DisplayCurrency)
	}
}
