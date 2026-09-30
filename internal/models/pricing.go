package models

// PricingTier is one usage-based step in a PricingPlan's egress pricing
// (SPEC-v0.6 §3). Tiers are evaluated in ascending UpToGB order; a tier
// with UpToGB == 0 has no upper bound (the plan's last tier must be
// unbounded).
type PricingTier struct {
	// UpToGB is the cumulative usage (in GB, 10^9 bytes) this tier
	// applies up to; 0 means unbounded ("and beyond").
	UpToGB float64 `json:"up_to_gb"`
	// PricePerGB is the per-GB price in USD within this tier.
	PricePerGB float64 `json:"price_per_gb"`
}

// PricingPlan models one provider's egress billing shape (SPEC-v0.6
// §3). All monetary values are fixed in USD (see DisplayCurrencySettings
// for presentation-only conversion).
type PricingPlan struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Provider is a free-form label (e.g. "aws", "oci-na-eu",
	// "oci-apac", "other") used only for the default host->plan
	// mapping; it does not have to match models.Provider's enum.
	Provider string `json:"provider"`
	Currency string `json:"currency"`
	// EgressFreeGB is the monthly free egress allowance in GB (10^9
	// bytes, the unit cloud providers bill in).
	EgressFreeGB float64 `json:"egress_free_gb"`
	// EgressTiers must be sorted ascending by UpToGB, with exactly one
	// unbounded (UpToGB == 0) tier as the last entry.
	EgressTiers []PricingTier `json:"egress_tiers"`
	// IngressPricePerGB is the per-GB inbound price in USD; 0 (the
	// common case) means inbound is free.
	IngressPricePerGB float64 `json:"ingress_price_per_gb"`
	// PoolFreeTier is true when EgressFreeGB is shared across every
	// host assigned to this plan (billed at the account/tenancy level
	// by the real provider) rather than granted per host.
	PoolFreeTier bool `json:"pool_free_tier"`
	// Builtin marks one of the seeded default plans: read-only via the
	// API (create a copy to customize).
	Builtin   bool  `json:"builtin"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// EstimatedCost is the result of applying a PricingPlan to a host's (or
// pool's) usage for a month (SPEC-v0.6 §3).
type EstimatedCost struct {
	// MTD is the cost of usage accumulated so far this month, in USD.
	MTD float64 `json:"mtd"`
	// Projected is MTD plus the estimated remaining cost through
	// month-end, in USD, based on a linear projection of the current
	// daily rate.
	Projected float64 `json:"projected"`
	// PlanID/PlanName identify the plan used for this calculation, 0/""
	// if no plan is assigned (cost is then always zero).
	PlanID   int64  `json:"plan_id,omitempty"`
	PlanName string `json:"plan_name,omitempty"`
}

// HostPricing maps one host to the pricing plan used for its network
// cost estimate (SPEC-v0.6 §3, table host_pricing).
type HostPricing struct {
	HostID string `json:"host_id"`
	PlanID int64  `json:"plan_id"`
}

// DisplayCurrency is the hub-wide presentation currency for billing
// figures (SPEC-v0.6 §3). All storage/calculation stays USD; KRW is a
// display-only conversion using a manually entered rate.
type DisplayCurrency string

// Supported display currencies.
const (
	DisplayCurrencyUSD DisplayCurrency = "USD"
	DisplayCurrencyKRW DisplayCurrency = "KRW"
)

// ValidDisplayCurrency reports whether v is a supported display
// currency.
func ValidDisplayCurrency(v DisplayCurrency) bool {
	switch v {
	case DisplayCurrencyUSD, DisplayCurrencyKRW:
		return true
	default:
		return false
	}
}

// DisplayCurrencySettings is the hub-wide display currency
// configuration (SPEC-v0.6 §3): settings keys
// "billing_display_currency", "billing_krw_per_usd",
// "billing_rate_updated_at".
type DisplayCurrencySettings struct {
	Currency DisplayCurrency `json:"currency"`
	// KRWPerUSD is the manually entered exchange rate (KRW per 1 USD);
	// 0/unset when Currency is USD or no rate has ever been entered.
	KRWPerUSD float64 `json:"krw_per_usd,omitempty"`
	// RateUpdatedAt is the unix-seconds time KRWPerUSD was last set, 0
	// if never set.
	RateUpdatedAt int64 `json:"rate_updated_at,omitempty"`
}
