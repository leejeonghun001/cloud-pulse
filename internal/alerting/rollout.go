// rollout.go implements SPEC-v0.8 §3-1's two rollout alert metrics:
// agent_outdated (a host's agent version has been behind the fleet's
// latest known release for at least DurationSec) and
// update_batch_failed (a completed remote-update batch had at least
// one failed job). Both reuse the existing rule engine/state
// persistence/notification pipeline rather than introducing a
// parallel one — agent_outdated slots into the same
// ok/pending/firing state machine every host-scoped metric already
// uses (see state.go), exactly like storageusage.go's
// storage_usage_pct did for account-scoped rules;
// update_batch_failed bypasses the state machine entirely (see its
// own doc comment below) since a batch's "did it fail" fact never
// resolves or re-fires the way a sustained threshold does.
package alerting

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting/chart"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// AgentOutdatedInput is the per-host input EvaluateAgentOutdated
// evaluates rules against.
type AgentOutdatedInput struct {
	HostID   string
	Hostname string
	// Outdated reports whether this host is currently behind the
	// fleet's latest known release (mirrors
	// models.FleetVersionHost.Outdated).
	Outdated bool
	// OutdatedSince is the unix-seconds time this host was first
	// observed outdated, 0 if not currently outdated or unknown (see
	// models.FleetVersionHost.OutdatedSince and
	// hub.fleetVersionTracker).
	OutdatedSince int64
}

// EvaluateAgentOutdated runs every enabled agent_outdated rule against
// hosts as of now, advancing each rule+host's persisted state machine
// (the same alert_state rows every other host-scoped metric uses) and
// enqueuing any resulting notifications. Mirrors Evaluate's/
// EvaluateStorage's shape and error-handling convention.
func (e *Engine) EvaluateAgentOutdated(ctx context.Context, now time.Time, hosts []AgentOutdatedInput) error {
	rules, err := e.store.ListAlertRules(ctx)
	if err != nil {
		return fmt.Errorf("alerting: evaluate agent outdated: list rules: %w", err)
	}

	var firstErr error
	for _, rule := range rules {
		if !rule.Enabled || rule.Metric != models.AlertMetricAgentOutdated {
			continue
		}
		for _, host := range scopedAgentOutdatedHosts(rule, hosts) {
			if err := e.evaluateAgentOutdatedRuleHost(ctx, now, rule, host); err != nil {
				e.logger.Error("alerting: evaluate agent_outdated rule failed",
					"rule_id", rule.ID, "host_id", host.HostID, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

// scopedAgentOutdatedHosts filters hosts to those rule applies to,
// mirroring scopedHosts' all-hosts-vs-one-host convention.
func scopedAgentOutdatedHosts(rule models.AlertRule, hosts []AgentOutdatedInput) []AgentOutdatedInput {
	var out []AgentOutdatedInput
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

// agentOutdatedDurationDefault is the "at least N days behind" default
// applied by the hub API layer when creating an agent_outdated rule
// with DurationSec left at 0 (SPEC-v0.8 §3-1: default 14 days),
// exported for that caller the same way DefaultCooldownSec is.
const AgentOutdatedDurationDefaultSec = 14 * 24 * 60 * 60

// evaluateAgentOutdatedRuleHost advances rule+host's persisted state
// machine by one step given the host's current outdated status. The
// "sustained for DurationSec" condition is derived from
// host.OutdatedSince (a fleetVersionTracker-computed timestamp)
// rather than a query series: a host is "satisfied" once
// now - OutdatedSince >= DurationSec, mirroring host_down's own
// "duration derived from a liveness timestamp, not a metric series"
// approach (see hostDownValue in evaluate.go) rather than introducing
// a third sustained-window mechanism.
func (e *Engine) evaluateAgentOutdatedRuleHost(ctx context.Context, now time.Time, rule models.AlertRule, host AgentOutdatedInput) error {
	value := agentOutdatedValue(now, host, rule.DurationSec)
	satisfiedNow := value > 0

	hostSnap := models.HostSnapshot{HostID: host.HostID, Hostname: host.Hostname}
	st, err := e.store.GetAlertState(ctx, rule.ID, host.HostID)
	if err != nil {
		return fmt.Errorf("get alert state: %w", err)
	}

	if !satisfiedNow {
		return e.transitionToOK(ctx, now, rule, hostSnap, st, value)
	}
	// agent_outdated's duration is already folded into agentOutdatedValue
	// (mirroring host_down's own hostDownValue convention), so it never
	// needs a series-based sustained check.
	return e.transitionToFiring(ctx, now, rule, hostSnap, st, value)
}

// agentOutdatedValue returns a synthetic "how satisfied" value: 1 once
// host has been outdated for at least durationSec (defaulting to
// AgentOutdatedDurationDefaultSec when durationSec <= 0, matching the
// hub API's own default-application convention for other rules'
// zero-value fields), 0 otherwise (not outdated at all, or outdated
// but not yet for long enough).
func agentOutdatedValue(now time.Time, host AgentOutdatedInput, durationSec int) float64 {
	if !host.Outdated || host.OutdatedSince == 0 {
		return 0
	}
	threshold := durationSec
	if threshold <= 0 {
		threshold = AgentOutdatedDurationDefaultSec
	}
	behindFor := now.Unix() - host.OutdatedSince
	if behindFor >= int64(threshold) {
		return 1
	}
	return 0
}

// --- update_batch_failed ---

// UpdateBatchOutcome is the per-batch input EvaluateUpdateBatch checks
// for a first-time completion notification.
type UpdateBatchOutcome struct {
	BatchID string
	Target  string
	// Succeeded/Failed/Pending mirror models.UpdateBatchSummary's own
	// fields; Pending must be 0 for a batch to be considered
	// "finished" (see EvaluateUpdateBatch's doc comment).
	Succeeded int
	Failed    int
	Pending   int
	// FailedHosts is one entry per failed job in this batch, used to
	// build the notification message body and the attached bar chart.
	FailedHosts []FailedUpdateHost
}

// FailedUpdateHost is one host's failure detail within an
// UpdateBatchOutcome.
type FailedUpdateHost struct {
	HostID   string
	Hostname string
	Reason   models.UpdateJobReason
}

// updateBatchFailedScopeID formats a batch ID as the synthetic
// "host ID" scope used to key its alert_state row, exactly mirroring
// storageusage.go's storageScopeID convention for account-scoped
// state — this reuses the existing alert_state table (keyed on
// rule_id + host_id) as an idempotent, restart-surviving "have I
// already notified this batch" record without a new table: a batch ID
// is already prefixed "batch-" (see hub/updateengine.go's newBatchID),
// so it can never collide with a real host ID's own charset
// (models.ValidHostID also allows "-", but hub-generated batch IDs are
// namespaced distinctly enough in practice; a rule scoped to this
// metric never mixes with a host-scoped rule's own state rows since
// they're partitioned by rule_id first).
func updateBatchFailedScopeID(batchID string) string {
	return batchID
}

// EvaluateUpdateBatch checks every enabled update_batch_failed rule
// against outcome, firing exactly once per batch ID the first time
// this is called with Failed > 0 and Pending == 0 (a "finished with at
// least one failure" batch) — never re-fires for the same batch again
// (there is nothing to "resolve": the batch already finished, its
// outcome is immutable), and never fires at all for Pending > 0 (still
// in progress) or Failed == 0 (fully succeeded) batches. Idempotency
// is tracked via a per-rule alert_state row keyed on
// updateBatchFailedScopeID(outcome.BatchID) (State ==
// models.AlertStateFiring means "already notified"), which — being a
// real persisted row via the same Store every other metric uses —
// survives a hub restart without a separate table, matching this
// package's existing convention of reusing alert_state for a metric
// whose "host ID" is really some other scope identifier (see
// storageusage.go).
//
// Callers (hub's batch-completion scanner) are expected to call this
// at most once per batch per scan tick, but calling it repeatedly for
// the same already-finished batch is safe and cheap: the second call
// observes State == Firing and returns immediately without
// re-notifying.
func (e *Engine) EvaluateUpdateBatch(ctx context.Context, now time.Time, outcome UpdateBatchOutcome) error {
	if outcome.Pending > 0 || outcome.Failed == 0 {
		return nil
	}

	rules, err := e.store.ListAlertRules(ctx)
	if err != nil {
		return fmt.Errorf("alerting: evaluate update batch: list rules: %w", err)
	}

	scopeID := updateBatchFailedScopeID(outcome.BatchID)
	var firstErr error
	for _, rule := range rules {
		if !rule.Enabled || rule.Metric != models.AlertMetricUpdateBatchFailed {
			continue
		}
		if err := e.notifyUpdateBatchOnce(ctx, now, rule, scopeID, outcome); err != nil {
			e.logger.Error("alerting: notify update_batch_failed failed",
				"rule_id", rule.ID, "batch_id", outcome.BatchID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// notifyUpdateBatchOnce sends rule's notification for outcome exactly
// once, using the same alert_state row every other metric uses as an
// idempotency marker (see EvaluateUpdateBatch's doc comment).
func (e *Engine) notifyUpdateBatchOnce(ctx context.Context, now time.Time, rule models.AlertRule, scopeID string, outcome UpdateBatchOutcome) error {
	st, err := e.store.GetAlertState(ctx, rule.ID, scopeID)
	if err != nil {
		return fmt.Errorf("get alert state: %w", err)
	}
	if st.State == models.AlertStateFiring {
		// Already notified for this batch.
		return nil
	}

	newState := models.AlertState{RuleID: rule.ID, HostID: scopeID, State: models.AlertStateFiring, Since: now.Unix(), LastNotified: now.Unix(), LastValue: float64(outcome.Failed)}
	if err := e.store.SetAlertState(ctx, newState); err != nil {
		return fmt.Errorf("set alert state: %w", err)
	}

	ev := models.AlertEvent{
		RuleID:    rule.ID,
		RuleName:  rule.Name,
		HostID:    scopeID,
		Hostname:  outcome.BatchID,
		Metric:    models.AlertMetricUpdateBatchFailed,
		State:     models.AlertEventFiring,
		Value:     float64(outcome.Failed),
		StartedAt: now.Unix(),
	}
	created, err := e.store.CreateAlertEvent(ctx, ev)
	if err != nil {
		return fmt.Errorf("create alert event: %w", err)
	}
	created.NotifiedAt = now.Unix()
	if err := e.store.UpdateAlertEvent(ctx, created); err != nil {
		return fmt.Errorf("update alert event (notified_at): %w", err)
	}

	msg := buildUpdateBatchMessage(rule, outcome)
	if img := renderUpdateBatchChart(outcome); img != nil {
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
		e.delivery.enqueue(deliveryJob{event: created, channel: ch, message: msg})
	}
	return nil
}

// buildUpdateBatchMessage renders outcome into a notification Message
// listing every failed host and its reason, per SPEC-v0.8 §3-1 ("실패
// 호스트 목록과 사유를 포함").
func buildUpdateBatchMessage(rule models.AlertRule, outcome UpdateBatchOutcome) Message {
	title := fmt.Sprintf("Update batch %s: %d failed", outcome.BatchID, outcome.Failed)
	text := fmt.Sprintf("Batch %s (target %s) finished: %d succeeded, %d failed.", outcome.BatchID, outcome.Target, outcome.Succeeded, outcome.Failed)

	fields := []Field{
		{Name: "Batch", Value: outcome.BatchID},
		{Name: "Target", Value: outcome.Target},
		{Name: "Succeeded", Value: fmt.Sprintf("%d", outcome.Succeeded)},
		{Name: "Failed", Value: fmt.Sprintf("%d", outcome.Failed)},
	}
	for _, h := range outcome.FailedHosts {
		name := h.Hostname
		if name == "" {
			name = h.HostID
		}
		fields = append(fields, Field{Name: name, Value: string(h.Reason)})
	}

	return Message{
		Title:    title,
		Text:     text,
		Severity: SeverityCritical,
		Fields:   fields,
	}
}

// renderUpdateBatchChart renders a success/failure bar chart image for
// outcome, grouped by failure reason (so the attachment visually
// answers "why did these fail" at a glance) plus one "succeeded"-only
// bar, returning nil (not an error) if rendering fails — a chart is a
// best-effort visual enhancement, never a reason to block the
// notification itself (mirrors chart.go's buildChartImage convention).
func renderUpdateBatchChart(outcome UpdateBatchOutcome) []byte {
	reasonCounts := make(map[models.UpdateJobReason]int)
	for _, h := range outcome.FailedHosts {
		reasonCounts[h.Reason]++
	}

	var bars []chart.Bar
	if outcome.Succeeded > 0 {
		bars = append(bars, chart.Bar{Label: "ok", Success: outcome.Succeeded})
	}
	reasons := make([]models.UpdateJobReason, 0, len(reasonCounts))
	for r := range reasonCounts {
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i] < reasons[j] })
	for _, r := range reasons {
		label := string(r)
		if label == "" {
			label = "unknown"
		}
		bars = append(bars, chart.Bar{Label: label, Failure: reasonCounts[r]})
	}

	img, err := chart.RenderBars(chart.BarParams{
		Title:        fmt.Sprintf("Update batch %s", outcome.BatchID),
		Bars:         bars,
		SuccessLabel: "Succeeded",
		FailureLabel: "Failed",
	})
	if err != nil {
		return nil
	}
	return img
}
