package hub

import (
	"context"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// applyUpdateIngest processes the remote-update side channel carried on
// an agent report (SPEC-v0.6 §2 steps 2-5): applies any AgentUpdateStatus
// the agent reported, then resolves the UpdateRequest (if any) to embed
// in this report's IngestResponse. Called from handleIngest after the
// report's samples/inventory have been persisted, but before the
// response is written, so a storage error here never blocks accepting
// the report's primary metrics payload.
//
// When a queued job is handed back as the response's UpdateRequest,
// this also immediately acks the job to in_progress (see
// updateengine.go's ackUpdateJob) so the parallel-limit accounting used
// by the next report (for this host or any other host in the same
// batch) is correct without waiting for a separate acknowledgement
// message — the agent's push-only architecture means there isn't one.
func (s *Server) applyUpdateIngest(ctx context.Context, report models.AgentReport) *models.UpdateRequest {
	if report.UpdateStatus != nil {
		s.handleAgentUpdateStatus(ctx, report.Host.ID, report.UpdateStatus)
	}

	req := s.pendingUpdateRequestForHost(ctx, report.Host)
	if req == nil {
		return nil
	}

	job, err := s.store.GetUpdateJob(ctx, req.JobID)
	if err != nil {
		if !isNotFound(err) {
			s.logger.Error("update: get job to ack failed", "job_id", req.JobID, "error", err)
		}
		return req
	}
	s.ackUpdateJob(ctx, job)
	return req
}
