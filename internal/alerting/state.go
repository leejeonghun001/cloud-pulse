package alerting

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// transitionToOK handles a not-currently-satisfied condition: resolving
// a firing/pending event back to ok, notifying on resolution if
// rule.NotifyResolved, and clearing the pending timer if the rule was
// only pending (never actually fired).
func (e *Engine) transitionToOK(ctx context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot, st models.AlertState, value float64) error {
	if st.State == models.AlertStateOK {
		return nil
	}

	wasFiring := st.State == models.AlertStateFiring
	newState := models.AlertState{RuleID: rule.ID, HostID: host.HostID, State: models.AlertStateOK, Since: now.Unix(), LastValue: value}
	if err := e.store.SetAlertState(ctx, newState); err != nil {
		return fmt.Errorf("set alert state (ok): %w", err)
	}

	if !wasFiring {
		// Was only "pending" (never notified as firing) — nothing to
		// resolve.
		return nil
	}

	ev, err := e.store.GetActiveAlertEvent(ctx, rule.ID, host.HostID)
	if err != nil {
		if isModelsNotFound(err) {
			// No active event to resolve (shouldn't normally happen if
			// st.State was Firing, but tolerate it defensively).
			return nil
		}
		return fmt.Errorf("get active alert event: %w", err)
	}
	ev.State = models.AlertEventResolved
	ev.ResolvedAt = now.Unix()
	ev.Value = value
	if err := e.store.UpdateAlertEvent(ctx, ev); err != nil {
		return fmt.Errorf("update alert event (resolve): %w", err)
	}

	if rule.NotifyResolved {
		e.notifyEvent(ctx, now, rule, host, ev, SeverityResolved)
	}
	return nil
}

// transitionToPending records that rule's condition is newly satisfied
// but hasn't yet been sustained for rule.DurationSec. If already
// pending or firing, this only refreshes LastValue (the Since timestamp
// of an existing pending window is preserved so the window's start time
// doesn't reset on every evaluation).
func (e *Engine) transitionToPending(ctx context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot, st models.AlertState, value float64) error {
	if st.State != models.AlertStateOK {
		// Already pending or firing: just refresh the observed value.
		st.LastValue = value
		if err := e.store.SetAlertState(ctx, st); err != nil {
			return fmt.Errorf("set alert state (pending refresh): %w", err)
		}
		return nil
	}

	newState := models.AlertState{RuleID: rule.ID, HostID: host.HostID, State: models.AlertStatePending, Since: now.Unix(), LastValue: value}
	if err := e.store.SetAlertState(ctx, newState); err != nil {
		return fmt.Errorf("set alert state (pending): %w", err)
	}
	return nil
}

// transitionToFiring records that rule's condition has been satisfied
// for at least rule.DurationSec, creating (on the ok/pending -> firing
// edge) or continuing (already firing) an AlertEvent, and notifying on
// the initial firing or on cooldown expiry ("still firing").
func (e *Engine) transitionToFiring(ctx context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot, st models.AlertState, value float64) error {
	if st.State == models.AlertStateFiring {
		return e.maybeRenotifyStillFiring(ctx, now, rule, host, st, value)
	}

	// ok or pending -> firing: create a new event and notify.
	since := now.Unix()
	ev := models.AlertEvent{
		RuleID:    rule.ID,
		RuleName:  rule.Name,
		HostID:    host.HostID,
		Hostname:  host.Hostname,
		Metric:    rule.Metric,
		State:     models.AlertEventFiring,
		Value:     value,
		Threshold: rule.Threshold,
		StartedAt: since,
	}
	created, err := e.store.CreateAlertEvent(ctx, ev)
	if err != nil {
		return fmt.Errorf("create alert event: %w", err)
	}

	newState := models.AlertState{RuleID: rule.ID, HostID: host.HostID, State: models.AlertStateFiring, Since: since, LastNotified: now.Unix(), LastValue: value}
	if err := e.store.SetAlertState(ctx, newState); err != nil {
		return fmt.Errorf("set alert state (firing): %w", err)
	}

	created.NotifiedAt = now.Unix()
	if err := e.store.UpdateAlertEvent(ctx, created); err != nil {
		return fmt.Errorf("update alert event (notified_at): %w", err)
	}

	e.notifyEvent(ctx, now, rule, host, created, severityFor(rule))
	return nil
}

// maybeRenotifyStillFiring re-notifies an already-firing rule+host if
// rule.CooldownSec has elapsed since the last notification (0 means
// never re-notify). It always refreshes LastValue.
func (e *Engine) maybeRenotifyStillFiring(ctx context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot, st models.AlertState, value float64) error {
	st.LastValue = value

	if rule.CooldownSec <= 0 {
		if err := e.store.SetAlertState(ctx, st); err != nil {
			return fmt.Errorf("set alert state (firing refresh): %w", err)
		}
		return nil
	}

	elapsed := now.Unix() - st.LastNotified
	if elapsed < int64(rule.CooldownSec) {
		if err := e.store.SetAlertState(ctx, st); err != nil {
			return fmt.Errorf("set alert state (firing refresh): %w", err)
		}
		return nil
	}

	ev, err := e.store.GetActiveAlertEvent(ctx, rule.ID, host.HostID)
	if err != nil {
		if isModelsNotFound(err) {
			// State says firing but no active event exists (shouldn't
			// normally happen); just refresh state rather than erroring
			// the whole evaluation pass.
			return e.store.SetAlertState(ctx, st)
		}
		return fmt.Errorf("get active alert event (renotify): %w", err)
	}
	ev.Value = value
	ev.NotifiedAt = now.Unix()
	if err := e.store.UpdateAlertEvent(ctx, ev); err != nil {
		return fmt.Errorf("update alert event (renotify): %w", err)
	}

	st.LastNotified = now.Unix()
	if err := e.store.SetAlertState(ctx, st); err != nil {
		return fmt.Errorf("set alert state (renotify): %w", err)
	}

	e.notifyEvent(ctx, now, rule, host, ev, severityFor(rule))
	return nil
}

// severityFor maps a rule to the Severity its firing notifications
// should carry. There is currently no per-rule severity configuration
// (SPEC-v0.5 §B doesn't define one), so every firing alert is "warning"
// except host_down, which is always "critical".
func severityFor(rule models.AlertRule) Severity {
	if rule.Metric == models.AlertMetricHostDown {
		return SeverityCritical
	}
	return SeverityWarning
}

// notifyEvent builds a Message for ev (attaching a rendered chart image
// when the metric has a chartable series — see buildChartImage) and
// enqueues delivery to every channel referenced by rule.ChannelIDs,
// skipping channels that are disabled, unknown (since deleted), or fail
// to load — each such skip is logged but never fails the overall
// evaluation.
func (e *Engine) notifyEvent(ctx context.Context, now time.Time, rule models.AlertRule, host models.HostSnapshot, ev models.AlertEvent, severity Severity) {
	msg := buildMessage(rule, ev, severity)
	if img := e.buildChartImage(ctx, now, rule, host, ev); img != nil {
		msg.Image = img
		msg.ImageName = chartImageName
	}
	for _, chID := range rule.ChannelIDs {
		ch, err := e.store.GetNotifyChannel(ctx, chID)
		if err != nil {
			if !isModelsNotFound(err) {
				e.logger.Error("alerting: load notify channel failed", "channel_id", chID, "error", err)
			}
			continue
		}
		if !ch.Enabled {
			continue
		}
		e.delivery.enqueue(deliveryJob{event: ev, channel: ch, message: msg})
	}
}

// buildMessage renders ev/rule into a notification Message.
func buildMessage(rule models.AlertRule, ev models.AlertEvent, severity Severity) Message {
	title := fmt.Sprintf("%s: %s", ev.Hostname, rule.Name)
	unit := metricUnit(rule.Metric)
	text := fmt.Sprintf("%s is %s%s (threshold %s%s)", rule.Name, formatValue(ev.Value), unit, formatValue(ev.Threshold), unit)
	if ev.State == models.AlertEventResolved {
		text = fmt.Sprintf("%s has recovered: now %s%s (threshold was %s%s)", rule.Name, formatValue(ev.Value), unit, formatValue(ev.Threshold), unit)
	}

	return Message{
		Title:    title,
		Text:     text,
		Severity: severity,
		Fields: []Field{
			{Name: "Host", Value: ev.Hostname},
			{Name: "Metric", Value: string(ev.Metric)},
			{Name: "Value", Value: formatValue(ev.Value) + unit},
			{Name: "Threshold", Value: formatValue(ev.Threshold) + unit},
			{Name: "State", Value: string(ev.State)},
		},
	}
}

func metricUnit(metric models.AlertMetric) string {
	switch metric {
	case models.AlertMetricCPU, models.AlertMetricMemory, models.AlertMetricDisk,
		models.AlertMetricEgressOutPct, models.AlertMetricEgressInPct:
		return "%"
	default:
		return ""
	}
}

func formatValue(v float64) string {
	return fmt.Sprintf("%.1f", v)
}

// isModelsNotFound reports whether err wraps models.ErrNotFound.
func isModelsNotFound(err error) bool {
	return errors.Is(err, models.ErrNotFound)
}
