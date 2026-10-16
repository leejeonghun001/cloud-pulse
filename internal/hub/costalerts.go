package hub

import (
	"context"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
)

// evaluateCostAlerts runs the cost_mtd/cost_forecast budget rules
// against every provider snapshot that has collected successfully at
// least once. Called after each persisted billing snapshot (hub poll
// or agent report), which is the only time the figures change. A
// never-successful provider has no amount to compare and is skipped.
func (s *Server) evaluateCostAlerts(ctx context.Context, now time.Time) {
	evaluator, ok := s.opts.Alerting.(CostAlertEvaluator)
	if !ok || s.opts.Billing == nil {
		return
	}
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
			Provider: snap.Provider,
			Currency: snap.Currency,
			MTD:      snap.MTDCost,
			Forecast: snap.ForecastCost,
		})
	}
	if err := evaluator.EvaluateCost(ctx, now, providers); err != nil {
		s.logger.Error("billing: evaluate cost alerts failed", "error", err)
	}
}
