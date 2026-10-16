package hub

import (
	"context"
	"math"
	"time"
	"unicode/utf8"

	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Bounds for an agent-reported cost snapshot. The agent's own collector
// never comes near them; they only stop a misbehaving agent from
// writing an oversized or nonsensical row.
const (
	maxAgentCostResources  = 10000
	maxAgentCostResourceID = 255
	maxAgentCostDetail     = 200
	maxAgentCostCurrency   = 8
	maxAgentCostAmount     = 1e12
)

// ingestAgentCloudCost persists a provider cost snapshot an agent
// collected itself (CP_OCI_BILLING=instance_principal), merged the same
// way as the hub's own collector output. Like inventory, it is
// supplementary to the report: an invalid snapshot is dropped and a
// storage failure is logged, never failing metrics ingestion. Ignored
// when billing is disabled on the hub (CP_BILLING=off).
func (s *Server) ingestAgentCloudCost(ctx context.Context, hostID string, snap models.CloudCostSnapshot, now time.Time) {
	if s.opts.Billing == nil {
		return
	}
	clean, ok := sanitizeAgentCloudCost(snap)
	if !ok {
		s.logger.Warn("ingest: dropping invalid agent cloud cost snapshot", "host_id", hostID, "provider", snap.Provider)
		return
	}
	clean.SourceHost = hostID
	// Not serialized with Billing.runMu: a hub CLI run can hold it for
	// minutes, and this runs inside the agent's report request. The
	// upsert is atomic; with the hub's own OCI collection configured too
	// (unsupported alongside agent-side collection) the last write wins.
	interval := s.resolveBillingInterval(ctx)
	if _, err := billing.PersistSnapshot(ctx, s.store, clean, now, interval); err != nil {
		s.logger.Error("ingest: persist agent cloud cost failed", "host_id", hostID, "error", err)
		return
	}
	s.evaluateCostAlerts(ctx, now)
}

// sanitizeAgentCloudCost validates an agent-reported snapshot and
// returns only the fields an agent may set; timestamps and staleness
// are the hub's to assign. Only OCI is accepted: it is the one provider
// with agent-side collection.
func sanitizeAgentCloudCost(snap models.CloudCostSnapshot) (models.CloudCostSnapshot, bool) {
	if snap.Provider != models.CloudBillingOCI {
		return models.CloudCostSnapshot{}, false
	}
	switch snap.Status {
	case models.CloudBillingOK, models.CloudBillingNotInstalled, models.CloudBillingNotConfigured,
		models.CloudBillingAuthFailed, models.CloudBillingPermissionDenied, models.CloudBillingError:
	default:
		return models.CloudCostSnapshot{}, false
	}
	switch snap.ForecastMethod {
	case "", "api", "linear":
	default:
		return models.CloudCostSnapshot{}, false
	}
	if !validAmount(snap.MTDCost) || !validAmount(snap.ForecastCost) {
		return models.CloudCostSnapshot{}, false
	}
	if len(snap.Currency) > maxAgentCostCurrency || len(snap.PerResource) > maxAgentCostResources {
		return models.CloudCostSnapshot{}, false
	}
	for id, cost := range snap.PerResource {
		if id == "" || len(id) > maxAgentCostResourceID || !utf8.ValidString(id) || !validAmount(cost) {
			return models.CloudCostSnapshot{}, false
		}
	}
	detail := snap.StatusDetail
	if !utf8.ValidString(detail) {
		detail = ""
	}
	if len(detail) > maxAgentCostDetail {
		detail = detail[:maxAgentCostDetail]
		for !utf8.ValidString(detail) {
			detail = detail[:len(detail)-1]
		}
	}
	return models.CloudCostSnapshot{
		Provider:       snap.Provider,
		Status:         snap.Status,
		StatusDetail:   detail,
		Currency:       snap.Currency,
		MTDCost:        snap.MTDCost,
		ForecastCost:   snap.ForecastCost,
		ForecastMethod: snap.ForecastMethod,
		AccountLevel:   snap.AccountLevel,
		PerResource:    snap.PerResource,
	}, true
}

// validAmount reports whether v is a finite cost within a sane range.
// Negative values are allowed: credits and refunds can make a
// month-to-date total negative.
func validAmount(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= maxAgentCostAmount
}
