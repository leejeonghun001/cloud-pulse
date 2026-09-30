package models

// CloudBillingProvider identifies which cloud CLI a CloudCostSnapshot was
// collected from.
type CloudBillingProvider string

// Supported cloud billing providers (SPEC-v0.6 §1).
const (
	CloudBillingAWS CloudBillingProvider = "aws"
	CloudBillingOCI CloudBillingProvider = "oci"
)

// CloudBillingStatus is the outcome of the most recent attempt to query a
// provider's billing CLI.
type CloudBillingStatus string

// Supported cloud billing statuses (SPEC-v0.6 §1). Every status other
// than CloudBillingOK is a "quiet skip", never surfaced as a hub error.
const (
	CloudBillingOK               CloudBillingStatus = "ok"
	CloudBillingNotInstalled     CloudBillingStatus = "not_installed"
	CloudBillingNotConfigured    CloudBillingStatus = "not_configured"
	CloudBillingAuthFailed       CloudBillingStatus = "auth_failed"
	CloudBillingPermissionDenied CloudBillingStatus = "permission_denied"
	CloudBillingError            CloudBillingStatus = "error"
)

// BillingInterval is one of the three allowed cloud billing polling
// periods (SPEC-v0.6 §1).
type BillingInterval string

// Supported billing intervals; BillingIntervalDefault is used when
// neither the hub-side setting nor CP_BILLING_INTERVAL specify one.
const (
	BillingInterval6h  BillingInterval = "6h"
	BillingInterval12h BillingInterval = "12h"
	BillingInterval24h BillingInterval = "24h"

	BillingIntervalDefault = BillingInterval24h
)

// ValidBillingInterval reports whether v is one of the three allowed
// billing interval values.
func ValidBillingInterval(v BillingInterval) bool {
	switch v {
	case BillingInterval6h, BillingInterval12h, BillingInterval24h:
		return true
	default:
		return false
	}
}

// CloudCostSnapshot is one provider's most recently observed (or
// attempted) monthly cost, persisted so a quiet-skip status can still
// display the last successful figures (SPEC-v0.6 §1 개선 a).
type CloudCostSnapshot struct {
	Provider CloudBillingProvider `json:"provider"`
	Status   CloudBillingStatus   `json:"status"`
	// StatusDetail is a short, secret-free human-readable reason (e.g.
	// "aws CLI not found on PATH"), empty when Status is
	// CloudBillingOK.
	StatusDetail string `json:"status_detail,omitempty"`
	// Currency is the billing currency the provider CLI reported (e.g.
	// "USD"); empty if never successfully collected.
	Currency string `json:"currency,omitempty"`
	// MTDCost is the month-to-date actual cost in Currency.
	MTDCost float64 `json:"mtd_cost"`
	// ForecastCost is the projected month-end cost in Currency.
	ForecastCost float64 `json:"forecast_cost"`
	// ForecastMethod is "api" (the provider's own forecast endpoint) or
	// "linear" (cloud-pulse's own linear projection from MTD, used as a
	// fallback when a provider's forecast isn't available/supported).
	ForecastMethod string `json:"forecast_method,omitempty"`
	// AccountLevel is true when MTDCost/ForecastCost are an
	// account/tenancy-wide total rather than split per resource (e.g.
	// AWS with CP_BILLING_AWS_RESOURCES off).
	AccountLevel bool `json:"account_level"`
	// PerResource maps a cloud resource ID (e.g. an EC2 instance ID or
	// OCI resource OCID) to its own cost in Currency, empty when
	// AccountLevel is true or resource-level data is unavailable.
	PerResource map[string]float64 `json:"per_resource,omitempty"`
	// CollectedAt is the unix-seconds time this snapshot's MTDCost/
	// ForecastCost were produced; equal to LastSuccessAt.
	CollectedAt int64 `json:"collected_at,omitempty"`
	// LastSuccessAt is the unix-seconds time of the most recent
	// successful collection for Provider, 0 if never successful.
	LastSuccessAt int64 `json:"last_success_at"`
	// LastAttemptAt is the unix-seconds time of the most recent
	// collection attempt (successful or not), 0 if never attempted.
	LastAttemptAt int64 `json:"last_attempt_at"`
	// Stale is true when LastSuccessAt is non-zero and now minus
	// LastSuccessAt exceeds twice the configured billing interval (see
	// CloudCostStale).
	Stale bool `json:"stale"`
}

// CloudCostStale reports whether a snapshot last successful at
// lastSuccessAt (unix seconds, 0 = never) should be flagged stale at
// time now given a poll interval, per SPEC-v0.6 §1 개선 a: stale once
// the elapsed time exceeds twice the interval. A never-successful
// snapshot (lastSuccessAt == 0) is not "stale" (there is nothing aged to
// flag); callers distinguish that case via LastSuccessAt itself.
func CloudCostStale(lastSuccessAt, now int64, interval BillingInterval) bool {
	if lastSuccessAt <= 0 {
		return false
	}
	d := billingIntervalSeconds(interval)
	if d <= 0 {
		return false
	}
	return now-lastSuccessAt > 2*d
}

// billingIntervalSeconds returns the number of seconds in v, defaulting
// to BillingIntervalDefault's duration for an unrecognized value.
func billingIntervalSeconds(v BillingInterval) int64 {
	switch v {
	case BillingInterval6h:
		return 6 * 3600
	case BillingInterval12h:
		return 12 * 3600
	case BillingInterval24h:
		return 24 * 3600
	default:
		return 24 * 3600
	}
}

// HostCost is one host's combined cloud-billing + network-estimate cost
// for the Costs page's per-host table (SPEC-v0.6 §1, §3).
type HostCost struct {
	HostID   string               `json:"host_id"`
	Hostname string               `json:"hostname"`
	Provider CloudBillingProvider `json:"provider,omitempty"`
	// InstanceID is the matched cloud resource ID (HostInfo.CloudInstanceID),
	// empty if unmatched or unavailable.
	InstanceID string `json:"instance_id,omitempty"`
	// Matched reports whether InstanceID was found in the provider's
	// PerResource map for the current billing snapshot.
	Matched bool `json:"matched"`
	// CloudMTD/CloudForecast are the matched resource's own cost, or
	// the provider's account-level totals when the snapshot is
	// AccountLevel (in which case Matched is false and these represent
	// "account total", not this host's own share).
	CloudMTD      float64 `json:"cloud_mtd"`
	CloudForecast float64 `json:"cloud_forecast"`
	// NetworkEstimate is this host's estimated network egress cost for
	// the month (see EstimatedCost), 0 if no pricing plan applies.
	NetworkEstimate EstimatedCost `json:"network_estimate"`
	// TotalMTD/TotalForecast sum CloudMTD/CloudForecast (only when
	// Matched) with NetworkEstimate's MTD/Projected.
	TotalMTD      float64 `json:"total_mtd"`
	TotalForecast float64 `json:"total_forecast"`
}

// BillingView is the response body for GET /api/v1/billing.
type BillingView struct {
	Snapshots []CloudCostSnapshot `json:"snapshots"`
	Hosts     []HostCost          `json:"hosts"`
	// IntervalSeconds is the currently effective billing polling
	// interval in seconds.
	IntervalSeconds int64 `json:"interval_seconds"`
	// DisplayCurrency echoes the hub's current display currency
	// setting (see DisplayCurrencySettings).
	DisplayCurrency DisplayCurrencySettings `json:"display_currency"`
}
