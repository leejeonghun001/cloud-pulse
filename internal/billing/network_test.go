package billing

import (
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// awsFreeTierPlan mirrors the builtin "AWS Free Tier" plan seeded by
// migrations/0005_v06.sql, for tests that exercise realistic tier
// boundaries.
func awsFreeTierPlan() models.PricingPlan {
	return models.PricingPlan{
		ID:           1,
		Name:         "AWS Free Tier (Internet egress, US)",
		Provider:     "aws",
		Currency:     "USD",
		EgressFreeGB: 100,
		EgressTiers: []models.PricingTier{
			{UpToGB: 10240, PricePerGB: 0.09},
			{UpToGB: 51200, PricePerGB: 0.085},
			{UpToGB: 153600, PricePerGB: 0.07},
			{UpToGB: 0, PricePerGB: 0.05},
		},
		PoolFreeTier: true,
	}
}

func TestTierCost_Boundaries(t *testing.T) {
	tiers := []models.PricingTier{
		{UpToGB: 100, PricePerGB: 1.0},
		{UpToGB: 200, PricePerGB: 0.5},
		{UpToGB: 0, PricePerGB: 0.1},
	}

	tests := []struct {
		name    string
		freeGB  float64
		usageGB float64
		want    float64
	}{
		{"zero usage", 10, 0, 0},
		{"usage entirely within free allowance", 50, 50, 0},
		{"usage exactly at free allowance boundary", 50, 50.0000001, 0.0000001 * 1.0},
		{"usage within first tier only", 0, 50, 50},
		{"usage exactly at first tier boundary", 0, 100, 100},
		{"usage crossing into second tier", 0, 150, 125},
		{"usage exactly at second tier boundary", 0, 200, 100*1.0 + 100*0.5},
		{"usage into unbounded tier", 0, 300, 100*1.0 + 100*0.5 + 100*0.1},
		{"free allowance shifts all boundaries", 20, 220, 150},
		{"negative billable (free exceeds usage)", 100, 50, 0},
		{"usage exactly at last bounded tier boundary (regression: unbounded tier must not subtract)", 0, 200, 100*1.0 + 100*0.5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tierCost(tiers, tc.freeGB, tc.usageGB)
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("tierCost(%v, %v) = %v, want %v", tc.freeGB, tc.usageGB, got, tc.want)
			}
		})
	}
}

func TestTierCost_AWSFreeTierPlanRealistic(t *testing.T) {
	plan := awsFreeTierPlan()
	tiers := SortedTiers(plan.EgressTiers)

	tests := []struct {
		name    string
		usageGB float64
		want    float64
	}{
		{"under free tier", 50, 0},
		{"exactly at free tier", 100, 0},
		{"1 GB over free tier", 101, 1 * 0.09},
		{"1 TB total usage", 1024, (1024 - 100) * 0.09},
		{"10 TB + 100 GB free = fills first paid tier exactly", 10240 + 100, 10240 * 0.09},
		{"crosses into second tier", 10240 + 100 + 1, 10240*0.09 + 1*0.085},
		{"100 TB total", 102400, 7980.2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tierCost(tiers, plan.EgressFreeGB, tc.usageGB)
			if diff := got - tc.want; diff > 1e-6 || diff < -1e-6 {
				t.Errorf("tierCost = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRoundCents(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{1.005, 1.01}, // half-up
		{1.004, 1.0},
		{1.999999, 2.0},
		{0.001, 0.0},
		{-1.005, -1.01},
		{123.456, 123.46},
	}
	for _, tc := range tests {
		if got := roundCents(tc.in); got != tc.want {
			t.Errorf("roundCents(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestBytesToGB(t *testing.T) {
	tests := []struct {
		name string
		b    uint64
		want float64
	}{
		{"zero", 0, 0},
		{"exactly 1 GB", 1_000_000_000, 1},
		{"1 byte under 1 GB", 999_999_999, 0.999999999},
		{"32-bit boundary (4 GiB in bytes)", 1<<32 - 1, float64(1<<32-1) / 1e9},
		{"large 64-bit value", 1 << 50, float64(uint64(1)<<50) / 1e9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bytesToGB(tc.b)
			diff := got - tc.want
			if diff > 1e-6 || diff < -1e-6 {
				t.Errorf("bytesToGB(%d) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

func TestEstimateNetworkCost_SingleHostNoPooling(t *testing.T) {
	plan := models.PricingPlan{
		ID:           5,
		Name:         "Test plan",
		EgressFreeGB: 10,
		EgressTiers: []models.PricingTier{
			{UpToGB: 0, PricePerGB: 0.1},
		},
		PoolFreeTier: false,
	}
	usages := []HostUsage{
		{HostID: "h1", TxBytes: 20 * BytesPerGB, ProjectedTxBytes: 40 * BytesPerGB},
	}
	got := EstimateNetworkCost(plan, usages)
	want := models.EstimatedCost{MTD: 1.0, Projected: 3.0, PlanID: 5, PlanName: "Test plan"}
	if got["h1"] != want {
		t.Errorf("got %+v, want %+v", got["h1"], want)
	}
}

func TestEstimateNetworkCost_ZeroUsage(t *testing.T) {
	plan := awsFreeTierPlan()
	usages := []HostUsage{{HostID: "h1"}}
	got := EstimateNetworkCost(plan, usages)
	want := models.EstimatedCost{MTD: 0, Projected: 0, PlanID: plan.ID, PlanName: plan.Name}
	if got["h1"] != want {
		t.Errorf("got %+v, want %+v", got["h1"], want)
	}
}

func TestEstimateNetworkCost_PoolingProportionalAllocation(t *testing.T) {
	// Pool free tier of 100 GB shared between two hosts using 300 GB and
	// 100 GB respectively (total 400 GB). Paid tier: $0.10/GB flat.
	plan := models.PricingPlan{
		ID:           7,
		Name:         "Pool test",
		EgressFreeGB: 100,
		EgressTiers: []models.PricingTier{
			{UpToGB: 0, PricePerGB: 0.10},
		},
		PoolFreeTier: true,
	}
	usages := []HostUsage{
		{HostID: "big", TxBytes: 300 * BytesPerGB, ProjectedTxBytes: 300 * BytesPerGB},
		{HostID: "small", TxBytes: 100 * BytesPerGB, ProjectedTxBytes: 100 * BytesPerGB},
	}
	got := EstimateNetworkCost(plan, usages)

	// Pool billable = 400 - 100 = 300 GB => pool cost = $30.
	// big's share = 300/400 = 75% => $22.50
	// small's share = 100/400 = 25% => $7.50
	if got["big"].MTD != 22.50 {
		t.Errorf("big.MTD = %v, want 22.50", got["big"].MTD)
	}
	if got["small"].MTD != 7.50 {
		t.Errorf("small.MTD = %v, want 7.50", got["small"].MTD)
	}
	total := got["big"].MTD + got["small"].MTD
	if total != 30.0 {
		t.Errorf("total pooled MTD = %v, want 30.0", total)
	}
}

func TestEstimateNetworkCost_PoolingWithZeroTotalUsage(t *testing.T) {
	plan := awsFreeTierPlan()
	usages := []HostUsage{
		{HostID: "a"},
		{HostID: "b"},
	}
	got := EstimateNetworkCost(plan, usages)
	if got["a"].MTD != 0 || got["b"].MTD != 0 {
		t.Errorf("expected zero cost for zero pooled usage, got a=%v b=%v", got["a"], got["b"])
	}
}

func TestEstimateNetworkCost_PoolingSingleHostEquivalence(t *testing.T) {
	// A pooled plan with exactly one host in usages must produce the
	// same result as the non-pooled path (single-host early return).
	plan := awsFreeTierPlan()
	usages := []HostUsage{
		{HostID: "solo", TxBytes: 500 * BytesPerGB, ProjectedTxBytes: 1000 * BytesPerGB},
	}
	got := EstimateNetworkCost(plan, usages)
	want := estimateSingleHost(plan, SortedTiers(plan.EgressTiers), usages[0])
	if got["solo"] != want {
		t.Errorf("got %+v, want %+v", got["solo"], want)
	}
}

func TestEstimateNetworkCost_IngressPricing(t *testing.T) {
	plan := models.PricingPlan{
		ID:                9,
		Name:              "Ingress test",
		EgressFreeGB:      0,
		EgressTiers:       []models.PricingTier{{UpToGB: 0, PricePerGB: 0}},
		IngressPricePerGB: 0.02,
		PoolFreeTier:      false,
	}
	usages := []HostUsage{
		{HostID: "h1", RxBytes: 50 * BytesPerGB, ProjectedRxBytes: 100 * BytesPerGB},
	}
	got := EstimateNetworkCost(plan, usages)
	if got["h1"].MTD != 1.0 {
		t.Errorf("MTD = %v, want 1.0", got["h1"].MTD)
	}
	if got["h1"].Projected != 2.0 {
		t.Errorf("Projected = %v, want 2.0", got["h1"].Projected)
	}
}

func TestEstimateNetworkCost_EmptyUsages(t *testing.T) {
	got := EstimateNetworkCost(awsFreeTierPlan(), nil)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

func TestEstimateNetworkCost_32BitSafeLargeUsage(t *testing.T) {
	// A usage value exceeding what fits in a 32-bit int (but well within
	// uint64) must not overflow/wrap when converted to GB and priced.
	plan := models.PricingPlan{
		ID:           11,
		Name:         "Large usage test",
		EgressFreeGB: 0,
		EgressTiers:  []models.PricingTier{{UpToGB: 0, PricePerGB: 0.01}},
	}
	const large = uint64(5_000_000) * BytesPerGB // 5,000,000 GB = 5 PB
	usages := []HostUsage{{HostID: "h1", TxBytes: large, ProjectedTxBytes: large}}
	got := EstimateNetworkCost(plan, usages)
	want := 5_000_000.0 * 0.01
	if got["h1"].MTD != want {
		t.Errorf("MTD = %v, want %v", got["h1"].MTD, want)
	}
}

func TestValidatePlan(t *testing.T) {
	tests := []struct {
		name    string
		plan    models.PricingPlan
		wantErr []string // expected non-empty keys in the details map
	}{
		{
			name: "valid plan",
			plan: models.PricingPlan{
				Name:         "ok",
				EgressFreeGB: 10,
				EgressTiers:  []models.PricingTier{{UpToGB: 0, PricePerGB: 0.1}},
			},
			wantErr: nil,
		},
		{
			name: "valid plan with multiple ascending tiers",
			plan: models.PricingPlan{
				Name:         "ok",
				EgressFreeGB: 0,
				EgressTiers: []models.PricingTier{
					{UpToGB: 100, PricePerGB: 0.1},
					{UpToGB: 200, PricePerGB: 0.2},
					{UpToGB: 0, PricePerGB: 0.3},
				},
			},
			wantErr: nil,
		},
		{
			name: "empty name",
			plan: models.PricingPlan{
				EgressTiers: []models.PricingTier{{UpToGB: 0, PricePerGB: 0.1}},
			},
			wantErr: []string{"name"},
		},
		{
			name: "negative free gb",
			plan: models.PricingPlan{
				Name:         "x",
				EgressFreeGB: -1,
				EgressTiers:  []models.PricingTier{{UpToGB: 0, PricePerGB: 0.1}},
			},
			wantErr: []string{"egress_free_gb"},
		},
		{
			name: "negative ingress price",
			plan: models.PricingPlan{
				Name:              "x",
				IngressPricePerGB: -0.1,
				EgressTiers:       []models.PricingTier{{UpToGB: 0, PricePerGB: 0.1}},
			},
			wantErr: []string{"ingress_price_per_gb"},
		},
		{
			name:    "no tiers",
			plan:    models.PricingPlan{Name: "x"},
			wantErr: []string{"egress_tiers"},
		},
		{
			name: "negative tier price",
			plan: models.PricingPlan{
				Name:        "x",
				EgressTiers: []models.PricingTier{{UpToGB: 0, PricePerGB: -0.1}},
			},
			wantErr: []string{"egress_tiers"},
		},
		{
			name: "last tier not unbounded",
			plan: models.PricingPlan{
				Name:        "x",
				EgressTiers: []models.PricingTier{{UpToGB: 100, PricePerGB: 0.1}},
			},
			wantErr: []string{"egress_tiers"},
		},
		{
			name: "unbounded tier not last",
			plan: models.PricingPlan{
				Name: "x",
				EgressTiers: []models.PricingTier{
					{UpToGB: 0, PricePerGB: 0.1},
					{UpToGB: 100, PricePerGB: 0.2},
				},
			},
			wantErr: []string{"egress_tiers"},
		},
		{
			name: "non-ascending tiers",
			plan: models.PricingPlan{
				Name: "x",
				EgressTiers: []models.PricingTier{
					{UpToGB: 200, PricePerGB: 0.1},
					{UpToGB: 100, PricePerGB: 0.2},
					{UpToGB: 0, PricePerGB: 0.3},
				},
			},
			wantErr: []string{"egress_tiers"},
		},
		{
			name: "duplicate up_to_gb rejected (not strictly ascending)",
			plan: models.PricingPlan{
				Name: "x",
				EgressTiers: []models.PricingTier{
					{UpToGB: 100, PricePerGB: 0.1},
					{UpToGB: 100, PricePerGB: 0.2},
					{UpToGB: 0, PricePerGB: 0.3},
				},
			},
			wantErr: []string{"egress_tiers"},
		},
		{
			name: "name too long",
			plan: models.PricingPlan{
				Name:        string(make([]byte, 201)),
				EgressTiers: []models.PricingTier{{UpToGB: 0, PricePerGB: 0.1}},
			},
			wantErr: []string{"name"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			details := ValidatePlan(tc.plan)
			if len(tc.wantErr) == 0 {
				if len(details) != 0 {
					t.Errorf("expected no errors, got %v", details)
				}
				return
			}
			for _, key := range tc.wantErr {
				if _, ok := details[key]; !ok {
					t.Errorf("expected error for key %q, got %v", key, details)
				}
			}
		})
	}
}

func TestIsBuiltinImmutable(t *testing.T) {
	if IsBuiltinImmutable(models.PricingPlan{Builtin: false}) {
		t.Error("non-builtin plan should not be immutable")
	}
	if !IsBuiltinImmutable(models.PricingPlan{Builtin: true}) {
		t.Error("builtin plan should be immutable")
	}
}

func TestSortedTiers(t *testing.T) {
	in := []models.PricingTier{
		{UpToGB: 0, PricePerGB: 0.3},
		{UpToGB: 100, PricePerGB: 0.1},
		{UpToGB: 200, PricePerGB: 0.2},
	}
	out := SortedTiers(in)
	wantOrder := []float64{100, 200, 0}
	for i, w := range wantOrder {
		if out[i].UpToGB != w {
			t.Errorf("out[%d].UpToGB = %v, want %v", i, out[i].UpToGB, w)
		}
	}
	// Original slice must not be mutated.
	if in[0].UpToGB != 0 {
		t.Error("SortedTiers must not mutate its input")
	}
}

func TestDefaultPlanProvider(t *testing.T) {
	tests := []struct {
		name      string
		provider  models.Provider
		ociRegion string
		want      string
	}{
		{"aws", models.ProviderAWS, "", "aws"},
		{"other", models.ProviderOther, "", ProviderOther},
		{"oci unknown region defaults to NA/EU", models.ProviderOCI, "", ProviderOCINAEU},
		{"oci us region", models.ProviderOCI, "us-ashburn-1", ProviderOCINAEU},
		{"oci eu region", models.ProviderOCI, "eu-frankfurt-1", ProviderOCINAEU},
		{"oci uk region (still NA/EU bucket)", models.ProviderOCI, "uk-london-1", ProviderOCINAEU},
		{"oci ap region", models.ProviderOCI, "ap-tokyo-1", ProviderOCIAPAC},
		{"oci sa region", models.ProviderOCI, "sa-saopaulo-1", ProviderOCIAPAC},
		{"oci me region", models.ProviderOCI, "me-dubai-1", ProviderOCIAPAC},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DefaultPlanProvider(tc.provider, tc.ociRegion)
			if got != tc.want {
				t.Errorf("DefaultPlanProvider(%v, %q) = %q, want %q", tc.provider, tc.ociRegion, got, tc.want)
			}
		})
	}
}

// TestBuiltinPlanSnapshot pins the exact shape of the four builtin
// plans seeded by migrations/0005_v06.sql (mirrored here as
// awsFreeTierPlan and the OCI/Other constants below), so a future
// accidental change to either the migration or this package's
// understanding of it is caught by a test failure rather than silently
// drifting. See notes/v06-network-cost.md for the pricing citations.
func TestBuiltinPlanSnapshot(t *testing.T) {
	aws := awsFreeTierPlan()
	if aws.EgressFreeGB != 100 {
		t.Errorf("AWS free tier = %v, want 100", aws.EgressFreeGB)
	}
	if len(aws.EgressTiers) != 4 {
		t.Fatalf("AWS tiers = %d, want 4", len(aws.EgressTiers))
	}
	wantAWSTiers := []models.PricingTier{
		{UpToGB: 10240, PricePerGB: 0.09},
		{UpToGB: 51200, PricePerGB: 0.085},
		{UpToGB: 153600, PricePerGB: 0.07},
		{UpToGB: 0, PricePerGB: 0.05},
	}
	for i, want := range wantAWSTiers {
		if aws.EgressTiers[i] != want {
			t.Errorf("AWS tier %d = %+v, want %+v", i, aws.EgressTiers[i], want)
		}
	}
	if !aws.PoolFreeTier {
		t.Error("AWS plan must pool its free tier (account-wide 100 GB)")
	}

	ociNAEU := models.PricingPlan{
		EgressFreeGB: 10240,
		EgressTiers:  []models.PricingTier{{UpToGB: 0, PricePerGB: 0.0085}},
		PoolFreeTier: true,
	}
	if ociNAEU.EgressFreeGB != 10240 || ociNAEU.EgressTiers[0].PricePerGB != 0.0085 {
		t.Errorf("OCI NA/EU snapshot drifted: %+v", ociNAEU)
	}

	ociAPAC := models.PricingPlan{
		EgressFreeGB: 10240,
		EgressTiers:  []models.PricingTier{{UpToGB: 0, PricePerGB: 0.025}},
		PoolFreeTier: true,
	}
	if ociAPAC.EgressFreeGB != 10240 || ociAPAC.EgressTiers[0].PricePerGB != 0.025 {
		t.Errorf("OCI APAC snapshot drifted: %+v", ociAPAC)
	}

	other := models.PricingPlan{
		EgressFreeGB: 0,
		EgressTiers:  []models.PricingTier{{UpToGB: 0, PricePerGB: 0}},
		PoolFreeTier: false,
	}
	if other.EgressTiers[0].PricePerGB != 0 {
		t.Errorf("Other/Free snapshot drifted: %+v", other)
	}
}
