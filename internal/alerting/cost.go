// cost.go implements the cost_mtd and cost_forecast alert metrics: a
// billing account's month-to-date or month-end-forecast cost reaching a
// budget amount. Like storage_usage_pct they are account-scoped, not
// host-scoped: AlertRule.HostID is "" (every account), a provider
// ("aws", "oci": every account of it) or one account's
// CloudCostSnapshot.Key ("oci:<tenancy OCID>"). Each account is driven
// through the shared ok/pending/firing state machine as a synthetic
// models.HostSnapshot keyed by that Key. Amounts are in the account's
// own billing currency, which notifications show as the unit.
package alerting

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// CloudCostInput is one provider's latest cost snapshot, the input
// EvaluateCost compares cost rules against.
type CloudCostInput struct {
	Provider  models.CloudBillingProvider
	AccountID string
	Label     string
	Currency  string
	MTD       float64
	Forecast  float64
}

// Key is the account's alert scope and state key (see
// models.CloudCostKey).
func (c CloudCostInput) Key() string { return models.CloudCostKey(c.Provider, c.AccountID) }

// CostRuleApplies reports whether a cost rule's scope (AlertRule.HostID)
// covers an account.
func CostRuleApplies(scope string, provider models.CloudBillingProvider, accountID string) bool {
	return scope == "" || scope == string(provider) || scope == models.CloudCostKey(provider, accountID)
}

// IsCostMetric reports whether metric is one of the provider-cost
// metrics.
func IsCostMetric(metric models.AlertMetric) bool {
	return metric == models.AlertMetricCostMTD || metric == models.AlertMetricCostForecast
}

// EvaluateCost runs every enabled cost rule against providers as of
// now. Only providers with a successful collection should be passed:
// a provider that never reported has no amount to compare. Errors are
// handled per rule+provider like EvaluateStorage; the first is
// returned after every pair has been processed.
func (e *Engine) EvaluateCost(ctx context.Context, now time.Time, providers []CloudCostInput) error {
	rules, err := e.store.ListAlertRules(ctx)
	if err != nil {
		return fmt.Errorf("alerting: evaluate cost: list rules: %w", err)
	}
	sorted := append([]CloudCostInput(nil), providers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key() < sorted[j].Key() })

	var firstErr error
	for _, rule := range rules {
		if !rule.Enabled || !IsCostMetric(rule.Metric) {
			continue
		}
		for _, p := range sorted {
			if !CostRuleApplies(rule.HostID, p.Provider, p.AccountID) {
				continue
			}
			if err := e.evaluateCostRule(ctx, now, rule, p); err != nil {
				e.logger.Error("alerting: evaluate cost rule failed", "rule_id", rule.ID, "provider", p.Provider, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

// evaluateCostRule advances rule+provider's state machine by one step.
// DurationSec is ignored: a cost figure changes at most once per poll,
// so a breach fires as soon as it is seen.
func (e *Engine) evaluateCostRule(ctx context.Context, now time.Time, rule models.AlertRule, p CloudCostInput) error {
	value := p.MTD
	if rule.Metric == models.AlertMetricCostForecast {
		value = p.Forecast
	}
	host := models.HostSnapshot{
		HostID:   p.Key(),
		Hostname: costAccountLabel(p),
		Unit:     " " + p.Currency,
	}
	st, err := e.store.GetAlertState(ctx, rule.ID, host.HostID)
	if err != nil {
		return fmt.Errorf("get alert state: %w", err)
	}
	if !conditionSatisfiedNow(rule, value) {
		return e.transitionToOK(ctx, now, rule, host, st, value)
	}
	return e.transitionToFiring(ctx, now, rule, host, st, value)
}

// costAccountLabel names an account in notifications: "OCI", or
// "OCI · <label>" / "OCI · <short account ID>" when there is one.
func costAccountLabel(p CloudCostInput) string {
	name := string(p.Provider)
	switch p.Provider {
	case models.CloudBillingAWS:
		name = "AWS"
	case models.CloudBillingOCI:
		name = "OCI"
	}
	switch {
	case p.Label != "":
		return name + " · " + p.Label
	case p.AccountID != "":
		return name + " · " + ShortAccountID(p.AccountID)
	}
	return name
}

// ShortAccountID abbreviates a long account identifier (an OCI OCID)
// to its last 8 characters; short ones (a 12-digit AWS ID) are kept.
func ShortAccountID(id string) string {
	if len(id) <= 14 {
		return id
	}
	return "…" + id[len(id)-8:]
}
