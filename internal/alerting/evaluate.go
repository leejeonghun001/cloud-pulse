package alerting

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Evaluate runs every enabled rule against hosts as of now, advancing
// each rule+host's persisted state machine and enqueuing any resulting
// notifications. It is safe to call repeatedly and concurrently is NOT
// guaranteed lock-free: callers (the hub's ingest path and its 30s
// scheduler tick) are expected to call Evaluate serially themselves
// (e.g. via a single background goroutine plus one call per ingest,
// naturally serialized by Go's scheduler for typical hub loads); Evaluate
// itself does not implement additional locking beyond what Store's
// concrete implementation provides for individual reads/writes.
func (e *Engine) Evaluate(ctx context.Context, now time.Time, hosts []models.HostSnapshot) error {
	rules, err := e.store.ListAlertRules(ctx)
	if err != nil {
		return fmt.Errorf("alerting: evaluate: list rules: %w", err)
	}

	var firstErr error
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		for _, host := range scopedHosts(rule, hosts) {
			if err := e.evaluateRuleHost(ctx, now, rule, host); err != nil {
				e.logger.Error("alerting: evaluate rule failed",
					"rule_id", rule.ID, "host_id", host.HostID, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

// Preview reports whether rule is currently satisfied for each host,
// without altering any persisted state.
func (e *Engine) Preview(ctx context.Context, now time.Time, rule models.AlertRule, hosts []models.HostSnapshot) (map[string]bool, error) {
	out := make(map[string]bool, len(hosts))
	for _, host := range scopedHosts(rule, hosts) {
		value, ok, err := e.metricValue(ctx, now, rule, host)
		if err != nil {
			return nil, fmt.Errorf("alerting: preview: %w", err)
		}
		if !ok {
			out[host.HostID] = false
			continue
		}
		out[host.HostID] = conditionSatisfiedNow(rule, value) && e.sustainedOverWindow(ctx, now, rule, host)
	}
	return out, nil
}

// scopedHosts filters hosts to those rule applies to: every host when
// rule.HostID == "", or just the matching one otherwise. Sorted by
// HostID for deterministic iteration order (tests, logs).
func scopedHosts(rule models.AlertRule, hosts []models.HostSnapshot) []models.HostSnapshot {
	var out []models.HostSnapshot
	if rule.HostID == "" {
		out = append(out, hosts...)
	} else {
		for _, h := range hosts {
			if h.HostID == rule.HostID {
				out = append(out, h)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostID < out[j].HostID })
	return out
}

// evaluateRuleHost advances rule+host's persisted state machine by one
// step given the current condition, handling egress's once-per-month
// dedupe as a special case (SPEC-v0.5 §B).
func (e *Engine) evaluateRuleHost(ctx context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot) error {
	if isEgressMetric(rule.Metric) {
		return e.evaluateEgressRuleHost(ctx, now, rule, host)
	}

	value, ok, err := e.metricValue(ctx, now, rule, host)
	if err != nil {
		return err
	}
	if !ok {
		// No data yet for this host/metric (e.g. never reported a
		// sample) — nothing to evaluate this round.
		return nil
	}

	satisfiedNow := conditionSatisfiedNow(rule, value)
	st, err := e.store.GetAlertState(ctx, rule.ID, host.HostID)
	if err != nil {
		return fmt.Errorf("get alert state: %w", err)
	}

	if !satisfiedNow {
		return e.transitionToOK(ctx, now, rule, host, st, value)
	}

	// host_down already folds DurationSec into hostDownValue's own
	// down-for-how-long comparison (SPEC-v0.5 §B: "the effective offline
	// threshold is OfflineAfter + DurationSec") — value being > 0 IS the
	// sustained condition, so it never needs a series-based sustained
	// check (host_down has no metric series to query in the first
	// place).
	sustained := rule.Metric == models.AlertMetricHostDown || rule.DurationSec == 0 || e.sustainedOverWindow(ctx, now, rule, host)
	if !sustained {
		return e.transitionToPending(ctx, now, rule, host, st, value)
	}
	return e.transitionToFiring(ctx, now, rule, host, st, value)
}

// metricValue resolves rule's metric for host at now, returning ok=false
// when there is no data to evaluate against yet (e.g. no sample at all
// for a non-host_down metric).
func (e *Engine) metricValue(_ context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot) (float64, bool, error) {
	switch rule.Metric {
	case models.AlertMetricHostDown:
		return hostDownValue(now, host, rule.DurationSec), true, nil
	case models.AlertMetricEgressOutPct:
		return host.Egress.Percent, true, nil
	case models.AlertMetricEgressInPct:
		return host.Egress.RxPercent, true, nil
	}

	if host.Latest == nil {
		return 0, false, nil
	}
	switch rule.Metric {
	case models.AlertMetricCPU:
		return host.Latest.CPUPercent, true, nil
	case models.AlertMetricMemory:
		return host.Latest.MemUsedPercent, true, nil
	case models.AlertMetricDisk:
		return host.Latest.DiskUsedPercent, true, nil
	case models.AlertMetricLoad1:
		return host.Latest.Load1, true, nil
	default:
		return 0, false, fmt.Errorf("unknown metric %q", rule.Metric)
	}
}

// hostDownValue returns a synthetic "seconds down beyond the offline
// threshold" value for host_down evaluation: positive (and growing) once
// the host has been unseen for longer than durationSec, 0/negative
// (never satisfies a ">" or ">=" 0 condition) while up. host_down rules
// always use threshold 0 with operator ">" (checked by validation at the
// API layer, not here) so this returns a value that is > 0 exactly when
// the host should be considered "down" for durationSec.
func hostDownValue(now time.Time, host models.HostSnapshot, durationSec int) float64 {
	if host.Status != models.HostDown {
		return 0
	}
	downFor := now.Unix() - host.LastSeen
	threshold := int64(durationSec)
	if downFor >= threshold {
		return 1
	}
	return 0
}

// isEgressMetric reports whether metric is one of the two egress
// percentage metrics, which use monthly dedupe rather than the general
// sustained-window/cooldown state machine.
func isEgressMetric(metric models.AlertMetric) bool {
	return metric == models.AlertMetricEgressOutPct || metric == models.AlertMetricEgressInPct
}

// conditionSatisfiedNow reports whether value satisfies rule's
// operator/threshold.
func conditionSatisfiedNow(rule models.AlertRule, value float64) bool {
	switch rule.Operator {
	case models.AlertOperatorGTE:
		return value >= rule.Threshold
	default: // models.AlertOperatorGT and any unrecognized operator
		return value > rule.Threshold
	}
}

// sustainedOverWindow reports whether every raw sample in
// [now-DurationSec, now] for host satisfies rule's condition, AND the
// window is actually covered by data (the earliest returned sample must
// be at or before now-DurationSec, within seriesLookbackTolerance) —
// see SPEC-v0.5 §B "Sustained semantics". A window that isn't yet fully
// covered by history (e.g. the host started reporting less than
// DurationSec ago) is treated as NOT sustained, so a rule cannot fire
// prematurely on partial data. Not used for host_down/egress metrics
// (evaluateRuleHost dispatches those separately).
func (e *Engine) sustainedOverWindow(ctx context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot) bool {
	if rule.DurationSec <= 0 {
		return true
	}

	from := now.Add(-time.Duration(rule.DurationSec) * time.Second).Unix()
	to := now.Unix()
	series, err := e.store.QuerySeries(ctx, host.HostID, from, to)
	if err != nil {
		e.logger.Error("alerting: query series for sustained window failed", "host_id", host.HostID, "rule_id", rule.ID, "error", err)
		return false
	}
	if series.Len() == 0 {
		return false
	}

	// The window must be covered: the earliest sample returned must be
	// at or before "from" (with a small tolerance for the collection
	// interval), otherwise there isn't DurationSec worth of history yet.
	earliest := series.Timestamps[0]
	if earliest > from+int64(seriesLookbackTolerance.Seconds()) {
		return false
	}

	for i, ts := range series.Timestamps {
		if ts < from || ts > to {
			continue
		}
		value := seriesValueFor(series, rule.Metric, i)
		if !conditionSatisfiedNow(rule, value) {
			return false
		}
	}
	return true
}

// seriesValueFor extracts rule metric's value at index i of series. Only
// called for metrics with a Series column (cpu/memory/disk/load1);
// egress/host_down never reach here (see sustainedOverWindow's callers).
func seriesValueFor(series models.Series, metric models.AlertMetric, i int) float64 {
	switch metric {
	case models.AlertMetricCPU:
		return series.CPU[i]
	case models.AlertMetricMemory:
		return series.Mem[i]
	case models.AlertMetricDisk:
		return series.Disk[i]
	case models.AlertMetricLoad1:
		return series.Load1[i]
	default:
		return 0
	}
}
