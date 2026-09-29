package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// hostSnapshots builds the []models.HostSnapshot the alerting engine
// evaluates rules against, for every known host, as of now. It makes
// exactly one ListHosts, one ListEgress, and one ListHostLimits call
// (no N+1), reusing the same egress/limit resolution buildHostSummary
// uses for the read API so alert evaluation sees identical percentages
// to what the dashboard displays.
func (s *Server) hostSnapshots(ctx context.Context, now time.Time) ([]models.HostSnapshot, error) {
	records, err := s.store.ListHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("hub: list hosts for alert evaluation: %w", err)
	}

	month := models.MonthOf(now)
	egressRecords, err := s.store.ListEgress(ctx, month)
	if err != nil {
		return nil, fmt.Errorf("hub: list egress for alert evaluation: %w", err)
	}
	egressByHost := indexEgressByHost(egressRecords)

	limits, err := s.store.ListHostLimits(ctx)
	if err != nil {
		return nil, fmt.Errorf("hub: list host limits for alert evaluation: %w", err)
	}
	limitsByHost := indexLimitsByHost(limits)

	out := make([]models.HostSnapshot, 0, len(records))
	for _, rec := range records {
		tx, rx := egressByHost[rec.Info.ID].TxBytes, egressByHost[rec.Info.ID].RxBytes
		status := models.HostDown
		if now.Sub(time.Unix(rec.LastSeen, 0)) <= s.opts.OfflineAfter {
			status = models.HostUp
		}

		txLimit, rxLimit, txSource, rxSource := models.EffectiveLimits(rec.Info.EgressLimitBytes, limitsByHost[rec.Info.ID])
		egress := models.ComputeEgress(month, tx, rx, txLimit, rxLimit, now)
		egress.LimitSource = txSource
		egress.RxLimitSource = rxSource

		out = append(out, models.HostSnapshot{
			HostID:   rec.Info.ID,
			Hostname: rec.Info.Hostname,
			Status:   status,
			LastSeen: rec.LastSeen,
			Latest:   rec.Latest,
			Egress:   egress,
		})
	}
	return out, nil
}

// hostSnapshotFor builds a single-element []models.HostSnapshot for
// hostID, used by afterIngest to evaluate rules against just the host
// that was just ingested rather than the whole fleet.
func (s *Server) hostSnapshotFor(ctx context.Context, now time.Time, hostID string) ([]models.HostSnapshot, error) {
	snapshots, err := s.hostSnapshots(ctx, now)
	if err != nil {
		return nil, err
	}
	for _, snap := range snapshots {
		if snap.HostID == hostID {
			return []models.HostSnapshot{snap}, nil
		}
	}
	return nil, nil
}
