package hub

import (
	"context"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// updateBatchScanInterval is how often runUpdateBatchAlertLoop checks
// for newly-finished remote-update batches to notify on (SPEC-v0.8
// §3-1's update_batch_failed rule). Independent of and slightly less
// frequent than updateJobTimeoutCheckInterval (1 minute) — batch
// completion detection isn't as time-critical as failing a stuck
// in_progress job.
const updateBatchScanInterval = 1 * time.Minute

// evaluateAgentOutdatedAlerts runs SPEC-v0.8 §3-1's agent_outdated
// rules against the fleet's current outdated-host set, if the
// configured AlertEngine additionally implements RolloutAlertEvaluator
// (see alertengine.go's doc comment on why this is a type assertion).
// A nil Options.Alerting, or one that doesn't implement the optional
// interface, silently skips rollout alerting — the same tolerance
// evaluateStorageAlerts already has for storage_usage_pct.
func (s *Server) evaluateAgentOutdatedAlerts(ctx context.Context, now time.Time) {
	evaluator, ok := s.opts.Alerting.(RolloutAlertEvaluator)
	if s.opts.Alerting == nil || !ok {
		return
	}

	records, err := s.store.ListHosts(ctx)
	if err != nil {
		s.logger.Error("rollout: list hosts for agent_outdated evaluation failed", "error", err)
		return
	}

	inputs := make([]fleetVersionHostInput, 0, len(records))
	for _, rec := range records {
		inputs = append(inputs, fleetVersionHostInput{
			HostID:   rec.Info.ID,
			Hostname: rec.Info.Hostname,
			Version:  rec.Info.AgentVersion,
			OS:       rec.Info.OS,
		})
	}
	st := s.updateStatusSnapshot()
	since := s.outdatedSinceSnapshot(inputs, st.latestVersion, now)

	hosts := make([]alerting.AgentOutdatedInput, 0, len(inputs))
	for _, in := range inputs {
		outdated := classifyVersionState(in.Version, st.latestVersion) == versionStateOutdated
		hosts = append(hosts, alerting.AgentOutdatedInput{
			HostID:        in.HostID,
			Hostname:      in.Hostname,
			Outdated:      outdated,
			OutdatedSince: since[in.HostID],
		})
	}

	if err := evaluator.EvaluateAgentOutdated(ctx, now, hosts); err != nil {
		s.logger.Error("rollout: evaluate agent_outdated alerts failed", "error", err)
	}
}

// runUpdateBatchAlertLoop periodically scans for remote-update batches
// that have just finished (no pending jobs) with at least one failure,
// notifying update_batch_failed rules exactly once per batch (SPEC-v0.8
// §3-1). Uses the same "list every job, group in Go" approach as
// handleListUpdateBatches (updatebatches.go) — the fleet's job volume
// doesn't warrant a dedicated SQL aggregation query.
func (s *Server) runUpdateBatchAlertLoop(ctx context.Context) {
	ticker := time.NewTicker(updateBatchScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanUpdateBatchesForAlerts(ctx, s.opts.now())
		}
	}
}

// scanUpdateBatchesForAlerts is runUpdateBatchAlertLoop's per-tick body,
// extracted so tests can call it directly with an injected time
// instead of waiting on a real ticker.
func (s *Server) scanUpdateBatchesForAlerts(ctx context.Context, now time.Time) {
	evaluator, ok := s.opts.Alerting.(RolloutAlertEvaluator)
	if s.opts.Alerting == nil || !ok {
		return
	}

	jobs, err := s.store.ListUpdateJobs(ctx, "", "")
	if err != nil {
		s.logger.Error("rollout: list update jobs for batch alert scan failed", "error", err)
		return
	}
	summaries := BuildUpdateBatchSummaries(jobs)

	byBatch := make(map[string][]models.UpdateJob)
	for _, j := range jobs {
		byBatch[j.BatchID] = append(byBatch[j.BatchID], j)
	}

	for _, summary := range summaries {
		if summary.Pending > 0 || summary.Failed == 0 {
			continue
		}
		outcome := alerting.UpdateBatchOutcome{
			BatchID:   summary.BatchID,
			Target:    summary.Target,
			Succeeded: summary.Succeeded,
			Failed:    summary.Failed,
			Pending:   summary.Pending,
		}
		for _, j := range byBatch[summary.BatchID] {
			if j.State != models.UpdateJobFailed {
				continue
			}
			host, err := s.store.GetHost(ctx, j.HostID)
			hostname := j.HostID
			if err == nil {
				hostname = host.Info.Hostname
			}
			outcome.FailedHosts = append(outcome.FailedHosts, alerting.FailedUpdateHost{
				HostID:   j.HostID,
				Hostname: hostname,
				Reason:   j.Reason,
			})
		}
		if err := evaluator.EvaluateUpdateBatch(ctx, now, outcome); err != nil {
			s.logger.Error("rollout: evaluate update_batch_failed failed", "batch_id", summary.BatchID, "error", err)
		}
	}
}
