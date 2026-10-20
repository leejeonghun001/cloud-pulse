package hub

import (
	"context"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// evaluateCostAlerts runs the cost_mtd/cost_forecast budget rules
// against every provider snapshot that has collected successfully at
// least once. Called after each persisted billing snapshot (hub poll
// or agent report), which is the only time the figures change. A
// never-successful provider has no amount to compare and is skipped.
//
// Serialized: agents report concurrently, and two evaluations reading
// the same "ok" state would both fire the same alert.
func (s *Server) evaluateCostAlerts(ctx context.Context, now time.Time) {
	evaluator, ok := s.opts.Alerting.(CostAlertEvaluator)
	if !ok || s.opts.Billing == nil {
		return
	}
	s.opts.Billing.costAlertMu.Lock()
	defer s.opts.Billing.costAlertMu.Unlock()

	snapshots, err := s.store.ListCloudCostSnapshots(ctx)
	if err != nil {
		s.logger.Error("billing: list snapshots for cost alerts failed", "error", err)
		return
	}
	providers := make([]alerting.CloudCostInput, 0, len(snapshots))
	for _, snap := range snapshots {
		if snap.LastSuccessAt == 0 {
			continue
		}
		providers = append(providers, alerting.CloudCostInput{
			Provider:  snap.Provider,
			AccountID: snap.AccountID,
			Label:     snap.Label,
			Currency:  snap.Currency,
			MTD:       snap.MTDCost,
			Forecast:  snap.ForecastCost,
		})
	}
	if err := evaluator.EvaluateCost(ctx, now, providers); err != nil {
		s.logger.Error("billing: evaluate cost alerts failed", "error", err)
	}
	s.retireOrphanCostAlerts(ctx, now, snapshots)
}

// retireOrphanCostAlerts closes firing cost alerts whose account no
// longer exists (removed in Settings, an agent's placeholder replaced by
// its real account, a hub row dropped). Nothing would ever evaluate
// them again, so they would stay "firing" forever. They are closed
// quietly: no "recovered" notification, since nothing recovered.
func (s *Server) retireOrphanCostAlerts(ctx context.Context, now time.Time, snapshots []models.CloudCostSnapshot) {
	current := make(map[string]bool, len(snapshots))
	for _, snap := range snapshots {
		current[snap.Key()] = true
	}
	active, err := s.store.ListActiveAlertEvents(ctx)
	if err != nil {
		s.logger.Error("billing: list active alerts failed", "error", err)
		return
	}
	for _, ev := range active {
		if !alerting.IsCostMetric(ev.Metric) || current[ev.HostID] {
			continue
		}
		ev.State = models.AlertEventResolved
		ev.ResolvedAt = now.Unix()
		if err := s.store.UpdateAlertEvent(ctx, ev); err != nil {
			s.logger.Error("billing: close orphan cost alert failed", "event_id", ev.ID, "error", err)
			continue
		}
		if err := s.store.SetAlertState(ctx, models.AlertState{RuleID: ev.RuleID, HostID: ev.HostID, State: models.AlertStateOK, Since: now.Unix()}); err != nil {
			s.logger.Error("billing: reset orphan cost alert state failed", "event_id", ev.ID, "error", err)
		}
	}
}
