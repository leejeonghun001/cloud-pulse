package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPricingPlanJSON(t *testing.T) {
	t.Parallel()

	plan := PricingPlan{
		ID:           1,
		Name:         "AWS free tier (US)",
		Provider:     "aws",
		Currency:     "USD",
		EgressFreeGB: 100,
		EgressTiers: []PricingTier{
			{UpToGB: 10240, PricePerGB: 0.09},
			{UpToGB: 51200, PricePerGB: 0.085},
			{UpToGB: 153600, PricePerGB: 0.07},
			{UpToGB: 0, PricePerGB: 0.05},
		},
		IngressPricePerGB: 0,
		PoolFreeTier:      true,
		Builtin:           true,
		CreatedAt:         100,
		UpdatedAt:         200,
	}
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{
		"id", "name", "provider", "currency", "egress_free_gb", "egress_tiers",
		"ingress_price_per_gb", "pool_free_tier", "builtin", "created_at", "updated_at",
	} {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing JSON field %q in %s", field, b)
		}
	}

	var decoded PricingPlan
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, plan) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, plan)
	}
}

func TestHostPricingJSON(t *testing.T) {
	t.Parallel()

	hp := HostPricing{HostID: "host-1", PlanID: 2}
	b, err := json.Marshal(hp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded HostPricing
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, hp) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, hp)
	}
}

func TestDisplayCurrencySettingsJSON(t *testing.T) {
	t.Parallel()

	dc := DisplayCurrencySettings{Currency: DisplayCurrencyKRW, KRWPerUSD: 1385.5, RateUpdatedAt: 100}
	b, err := json.Marshal(dc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded DisplayCurrencySettings
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, dc) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, dc)
	}
}

func TestValidDisplayCurrency(t *testing.T) {
	t.Parallel()
	for _, v := range []DisplayCurrency{DisplayCurrencyUSD, DisplayCurrencyKRW} {
		if !ValidDisplayCurrency(v) {
			t.Errorf("ValidDisplayCurrency(%q) = false, want true", v)
		}
	}
	for _, v := range []DisplayCurrency{"", "usd", "EUR"} {
		if ValidDisplayCurrency(v) {
			t.Errorf("ValidDisplayCurrency(%q) = true, want false", v)
		}
	}
}

func TestEstimatedCostJSON(t *testing.T) {
	t.Parallel()

	ec := EstimatedCost{MTD: 1.23, Projected: 4.56, PlanID: 7, PlanName: "OCI Always Free"}
	b, err := json.Marshal(ec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded EstimatedCost
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, ec) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, ec)
	}
}
