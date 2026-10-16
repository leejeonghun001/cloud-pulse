// cost.go implements the cost_mtd and cost_forecast alert metrics: a
// provider's month-to-date or month-end-forecast cost reaching a
// budget amount. Like storage_usage_pct they are account-scoped, not
// host-scoped: AlertRule.HostID holds a billing provider ("aws",
// "oci"), "" meaning every provider, and each provider is driven
// through the shared ok/pending/firing state machine as a synthetic
// models.HostSnapshot. Amounts are in the provider's own billing
// currency, which notifications show as the unit.
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
	Provider models.CloudBillingProvider
	Currency string
	MTD      float64
	Forecast float64
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
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Provider < sorted[j].Provider })

	var firstErr error
	for _, rule := range rules {
		if !rule.Enabled || !IsCostMetric(rule.Metric) {
			continue
		}
		for _, p := range sorted {
			if rule.HostID != "" && rule.HostID != string(p.Provider) {
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
		HostID:   string(p.Provider),
		Hostname: costProviderLabel(p.Provider),
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

func costProviderLabel(p models.CloudBillingProvider) string {
	switch p {
	case models.CloudBillingAWS:
		return "AWS"
	case models.CloudBillingOCI:
		return "OCI"
	}
	return string(p)
}
