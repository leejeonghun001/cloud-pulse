package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCloudCostSnapshotJSON(t *testing.T) {
	t.Parallel()

	snap := CloudCostSnapshot{
		Provider:       CloudBillingAWS,
		Status:         CloudBillingOK,
		StatusDetail:   "",
		Currency:       "USD",
		MTDCost:        12.34,
		ForecastCost:   30.5,
		ForecastMethod: "api",
		AccountLevel:   false,
		PerResource:    map[string]float64{"i-0123456789abcdef0": 5.5},
		CollectedAt:    1000,
		LastSuccessAt:  1000,
		LastAttemptAt:  1000,
		Stale:          false,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{
		"provider", "status", "currency", "mtd_cost", "forecast_cost",
		"forecast_method", "account_level", "per_resource", "collected_at",
		"last_success_at", "last_attempt_at", "stale",
	} {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing JSON field %q in %s", field, b)
		}
	}

	var decoded CloudCostSnapshot
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, snap) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, snap)
	}
}

func TestCloudCostSnapshotJSON_QuietSkipOmitsOptionalFields(t *testing.T) {
	t.Parallel()

	snap := CloudCostSnapshot{
		Provider: CloudBillingOCI,
		Status:   CloudBillingNotConfigured,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{"status_detail", "currency", "forecast_method", "per_resource", "collected_at"} {
		if _, ok := raw[field]; ok {
			t.Errorf("expected field %q to be omitted for a quiet-skip snapshot, got %s", field, b)
		}
	}
}

func TestCloudCostStale(t *testing.T) {
	t.Parallel()

	const day = 24 * 3600
	tests := []struct {
		name          string
		lastSuccessAt int64
		now           int64
		interval      BillingInterval
		want          bool
	}{
		{"never succeeded", 0, 10 * day, BillingInterval24h, false},
		{"just collected", 10 * day, 10*day + 60, BillingInterval24h, false},
		{"exactly 2x interval, not yet stale", 10 * day, 10*day + 2*day, BillingInterval24h, false},
		{"just past 2x interval", 10 * day, 10*day + 2*day + 1, BillingInterval24h, true},
		{"6h interval stale sooner", 10 * day, 10*day + 12*3600 + 1, BillingInterval6h, true},
		{"6h interval not yet stale", 10 * day, 10*day + 12*3600, BillingInterval6h, false},
		{"unrecognized interval falls back to 24h", 10 * day, 10*day + 2*day + 1, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := CloudCostStale(tc.lastSuccessAt, tc.now, tc.interval)
			if got != tc.want {
				t.Errorf("CloudCostStale(%d, %d, %q) = %v, want %v", tc.lastSuccessAt, tc.now, tc.interval, got, tc.want)
			}
		})
	}
}

func TestValidBillingInterval(t *testing.T) {
	t.Parallel()
	for _, v := range []BillingInterval{BillingInterval6h, BillingInterval12h, BillingInterval24h} {
		if !ValidBillingInterval(v) {
			t.Errorf("ValidBillingInterval(%q) = false, want true", v)
		}
	}
	for _, v := range []BillingInterval{"", "1h", "48h", "24H"} {
		if ValidBillingInterval(v) {
			t.Errorf("ValidBillingInterval(%q) = true, want false", v)
		}
	}
}

func TestHostCostJSON(t *testing.T) {
	t.Parallel()

	hc := HostCost{
		HostID:          "host-1",
		Hostname:        "web-1",
		Provider:        CloudBillingAWS,
		InstanceID:      "i-0123456789abcdef0",
		Matched:         true,
		CloudMTD:        5.5,
		CloudForecast:   13.2,
		NetworkEstimate: EstimatedCost{MTD: 1.1, Projected: 2.2, PlanID: 3, PlanName: "AWS free tier"},
		TotalMTD:        6.6,
		TotalForecast:   15.4,
	}
	b, err := json.Marshal(hc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded HostCost
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, hc) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, hc)
	}
}

func TestBillingViewJSON(t *testing.T) {
	t.Parallel()

	view := BillingView{
		Snapshots:       []CloudCostSnapshot{{Provider: CloudBillingAWS, Status: CloudBillingOK}},
		Hosts:           []HostCost{{HostID: "host-1"}},
		IntervalSeconds: 86400,
		DisplayCurrency: DisplayCurrencySettings{Currency: DisplayCurrencyUSD},
	}
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded BillingView
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, view) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, view)
	}
}
