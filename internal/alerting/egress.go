package alerting

import (
	"context"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// evaluateEgressRuleHost implements egress_out_pct/egress_in_pct's
// once-per-month-per-threshold dedupe (SPEC-v0.5 §B: "fire once per
// month per threshold, cooldown ignored; resolved not sent"), reusing
// the persisted AlertState row per (rule, host) as the dedupe record:
// State encodes whether this rule+host has already notified for the
// current calendar month (models.AlertStateFiring), and Since stores the
// unix-seconds start of the month it last notified for, so a new month
// naturally resets eligibility without a separate table.
func (e *Engine) evaluateEgressRuleHost(ctx context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot) error {
	value, _, err := e.metricValue(ctx, now, rule, host)
	if err != nil {
		return err
	}
	satisfied := conditionSatisfiedNow(rule, value)

	st, err := e.store.GetAlertState(ctx, rule.ID, host.HostID)
	if err != nil {
		return fmt.Errorf("get alert state: %w", err)
	}

	monthStart, _, err := models.MonthBounds(models.MonthOf(now))
	if err != nil {
		return fmt.Errorf("month bounds: %w", err)
	}

	alreadyNotifiedThisMonth := st.State == models.AlertStateFiring && st.Since >= monthStart.Unix()

	if !satisfied {
		// Egress alerts are never "resolved" (SPEC-v0.5 §B: "resolved
		// not sent") — but once usage drops back under the threshold we
		// still want a later crossing (this month or next) to be
		// eligible again isn't correct per spec, which says "once per
		// month per threshold" unconditionally. So a drop below
		// threshold does NOT reset the per-month dedupe; only a new
		// calendar month does (handled implicitly by the monthStart
		// comparison above on the next satisfied evaluation). Nothing to
		// do here.
		return nil
	}

	if alreadyNotifiedThisMonth {
		// Still refresh LastValue for the preview/API surface even
		// though no new notification is sent.
		st.LastValue = value
		if err := e.store.SetAlertState(ctx, st); err != nil {
			return fmt.Errorf("set alert state (egress refresh): %w", err)
		}
		return nil
	}

	// First time this threshold is satisfied this month: notify and
	// record the dedupe marker.
	ev := models.AlertEvent{
		RuleID:    rule.ID,
		RuleName:  rule.Name,
		HostID:    host.HostID,
		Hostname:  host.Hostname,
		Metric:    rule.Metric,
		State:     models.AlertEventFiring,
		Value:     value,
		Threshold: rule.Threshold,
		StartedAt: now.Unix(),
	}
	created, err := e.store.CreateAlertEvent(ctx, ev)
	if err != nil {
		return fmt.Errorf("create alert event (egress): %w", err)
	}
	created.NotifiedAt = now.Unix()
	if err := e.store.UpdateAlertEvent(ctx, created); err != nil {
		return fmt.Errorf("update alert event (egress notified_at): %w", err)
	}

	newState := models.AlertState{
		RuleID:       rule.ID,
		HostID:       host.HostID,
		State:        models.AlertStateFiring,
		Since:        now.Unix(),
		LastNotified: now.Unix(),
		LastValue:    value,
	}
	if err := e.store.SetAlertState(ctx, newState); err != nil {
		return fmt.Errorf("set alert state (egress): %w", err)
	}

	e.notifyEvent(ctx, now, rule, host, created, SeverityWarning)
	return nil
}
