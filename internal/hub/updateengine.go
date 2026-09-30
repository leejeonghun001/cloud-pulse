package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// defaultMaxParallel is UpdateBatchCreate.MaxParallel's default when 0
// (SPEC-v0.6 §2).
const defaultMaxParallel = 3

// updateJobTimeout is how long a job may remain in_progress before it
// is force-failed with UpdateReasonTimeout (SPEC-v0.6 §2 step 5).
const updateJobTimeout = 15 * time.Minute

// remoteUpdateMinAgentVersion is the minimum agent version eligible to
// receive a remote update request: only a v0.6.0+ agent is guaranteed
// to have the request-file/result-file handling this feature depends
// on (an older agent would just ignore IngestResponse.UpdateRequest,
// silently stranding the job in "queued" forever without this check).
const remoteUpdateMinAgentVersion = "v0.6.0"

// createUpdateBatch validates req and creates one queued models.UpdateJob
// per host ID, returning the created jobs (in the same order as
// req.HostIDs) and a batch ID shared by all of them. A host ID that
// does not correspond to a known host is skipped (not an error) since
// the caller may have raced with a host removal; callers that want a
// hard error for an unknown host should check the returned jobs' count
// against len(req.HostIDs) themselves.
func (s *Server) createUpdateBatch(ctx context.Context, req models.UpdateBatchCreate) (models.UpdateBatch, error) {
	target := req.Target
	if target == "" {
		target = "latest"
	}
	if target != "latest" && !version.ValidTag(target) {
		return models.UpdateBatch{}, fmt.Errorf("hub: create update batch: invalid target %q", target)
	}

	maxParallel := req.MaxParallel
	if maxParallel <= 0 {
		maxParallel = defaultMaxParallel
	}

	batchID, err := newBatchID()
	if err != nil {
		return models.UpdateBatch{}, fmt.Errorf("hub: create update batch: generate batch id: %w", err)
	}

	now := s.opts.now()
	jobs := make([]models.UpdateJob, 0, len(req.HostIDs))
	for _, hostID := range req.HostIDs {
		if _, err := s.store.GetHost(ctx, hostID); err != nil {
			if isNotFound(err) {
				continue
			}
			return models.UpdateBatch{}, fmt.Errorf("hub: create update batch: get host %s: %w", hostID, err)
		}
		job, err := s.store.CreateUpdateJob(ctx, models.UpdateJob{
			BatchID: batchID,
			HostID:  hostID,
			Target:  target,
			State:   models.UpdateJobQueued,
		})
		if err != nil {
			return models.UpdateBatch{}, fmt.Errorf("hub: create update batch: create job for %s: %w", hostID, err)
		}
		jobs = append(jobs, job)
	}

	return models.UpdateBatch{
		BatchID:     batchID,
		Target:      target,
		MaxParallel: maxParallel,
		CreatedAt:   now.Unix(),
		Jobs:        jobs,
	}, nil
}

// newBatchID returns a random, URL-safe batch identifier.
func newBatchID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return "batch-" + hex.EncodeToString(buf), nil
}

// pendingUpdateRequestForHost returns the models.UpdateRequest to embed
// in this host's next IngestResponse, or nil if there is none — either
// because no job is queued/in_progress for hostID, or the batch's
// rolling max_parallel limit is currently saturated by other
// in_progress jobs in the same batch.
//
// A queued job for a host that has not opted in or isn't capable
// (SPEC-v0.6 §2: Linux + systemd + agent >= v0.6.0) is actively failed
// here (reason not_enabled/unsupported) rather than left queued
// forever — SPEC-v0.6 §2 requires such jobs to surface as a visible
// failure ("옵트인하지 않은 에이전트는 failed(사유 not_enabled)로
// 표시"), and there is no other scheduler path that would ever revisit
// a queued (not in_progress) job otherwise.
//
// This is called from handleIngest for every report (see
// updateingest.go), so it must be cheap: at most one
// GetLatestUpdateJobForHost call plus, only when that job is queued and
// the host is eligible, one ListUpdateJobs call scoped to the same
// batch to count in_progress jobs against the parallel limit.
func (s *Server) pendingUpdateRequestForHost(ctx context.Context, host models.HostInfo) *models.UpdateRequest {
	ineligibleReason := models.UpdateReasonNone
	switch {
	case !remoteUpdateCapable(host):
		ineligibleReason = models.UpdateReasonUnsupported
	case !host.RemoteUpdate.OptedIn:
		ineligibleReason = models.UpdateReasonNotEnabled
	}

	job, err := s.store.GetLatestUpdateJobForHost(ctx, host.ID)
	if err != nil {
		if !isNotFound(err) {
			s.logger.Error("update: get latest job for host failed", "host_id", host.ID, "error", err)
		}
		return nil
	}

	if ineligibleReason != models.UpdateReasonNone {
		if job.State == models.UpdateJobQueued {
			s.failIneligibleUpdateJob(ctx, job, ineligibleReason)
		}
		return nil
	}

	switch job.State {
	case models.UpdateJobInProgress:
		// Already handed to the agent; re-send unconditionally so a
		// dropped/duplicated report doesn't strand the agent without
		// its request (writing the same request file again is a
		// harmless no-op on the agent side).
		return &models.UpdateRequest{JobID: job.ID, Target: job.Target}
	case models.UpdateJobQueued:
		// Fall through to the parallel-limit check below.
	default:
		return nil
	}

	maxParallel := s.batchMaxParallel(job.BatchID)
	inProgress, err := s.store.ListUpdateJobs(ctx, job.BatchID, models.UpdateJobInProgress)
	if err != nil {
		s.logger.Error("update: list in-progress jobs failed", "batch_id", job.BatchID, "error", err)
		return nil
	}
	if len(inProgress) >= maxParallel {
		return nil
	}

	return &models.UpdateRequest{JobID: job.ID, Target: job.Target}
}

// failIneligibleUpdateJob marks a queued job failed with reason (always
// UpdateReasonNotEnabled or UpdateReasonUnsupported — see
// pendingUpdateRequestForHost) once the hub observes, via a report,
// that the target host can never actually receive it. Errors are
// logged, never returned: a storage failure here must not disrupt the
// ingest response the agent is waiting on.
func (s *Server) failIneligibleUpdateJob(ctx context.Context, job models.UpdateJob, reason models.UpdateJobReason) {
	job.State = models.UpdateJobFailed
	job.Reason = reason
	if err := s.store.UpdateUpdateJob(ctx, job); err != nil {
		s.logger.Error("update: fail ineligible job failed", "job_id", job.ID, "reason", reason, "error", err)
	}
}

// batchMaxParallel returns the max_parallel value in effect for
// batchID. The value itself isn't persisted per-batch (SPEC-v0.6 §2's
// schema has no such column); instead every job created for the same
// batch shares its target/rolling semantics, so this re-derives the
// limit from the batch's queued+in_progress job count only as a
// fallback default — in practice s.batchLimits (populated by
// createUpdateBatch within the same process lifetime) holds the exact
// value the admin requested. A hub restart loses this in-memory value
// and falls back to defaultMaxParallel, which is an acceptable
// degradation (a restart is rare and the fallback is still a sane
// default, never unbounded).
func (s *Server) batchMaxParallel(batchID string) int {
	s.batchLimitsMu.Lock()
	defer s.batchLimitsMu.Unlock()
	if v, ok := s.batchLimits[batchID]; ok {
		return v
	}
	return defaultMaxParallel
}

// setBatchMaxParallel records max_parallel for batchID for the lifetime
// of this process (see batchMaxParallel's doc comment on why this is
// in-memory only).
func (s *Server) setBatchMaxParallel(batchID string, maxParallel int) {
	s.batchLimitsMu.Lock()
	defer s.batchLimitsMu.Unlock()
	if s.batchLimits == nil {
		s.batchLimits = make(map[string]int)
	}
	s.batchLimits[batchID] = maxParallel
}

// remoteUpdateCapable reports whether host is eligible to receive a
// remote update job at all: it must self-report RemoteUpdateCapability
// as Supported (SPEC-v0.6 §2: Linux + systemd), and its own
// AgentVersion must be a valid, parseable release tag at or above
// remoteUpdateMinAgentVersion. This is hub-side defense-in-depth on top
// of the agent's own (currently coarse, see notes/v06-prep.md) Linux
// gate — an old agent's optimistic self-report is never trusted alone.
func remoteUpdateCapable(host models.HostInfo) bool {
	if !host.RemoteUpdate.Supported {
		return false
	}
	if host.OS != "" && host.OS != "linux" {
		return false
	}
	return !version.IsNewer(remoteUpdateMinAgentVersion, host.AgentVersion) // AgentVersion >= min
}

// handleAgentUpdateStatus applies an agent-reported models.AgentUpdateStatus
// to its job (SPEC-v0.6 §2 step 5): a report claiming "succeeded" is
// only trusted if the reported version actually matches the job's
// target (or the job's target is "latest", in which case any version
// newer than the job's pre-update version is accepted — tracked
// implicitly by trusting the agent's own success/failure classification
// once the version-match check passes for an explicit tag target).
// Errors are logged, never returned, since a malformed/stale status
// must never fail the ingest response the agent is waiting on.
func (s *Server) handleAgentUpdateStatus(ctx context.Context, hostID string, st *models.AgentUpdateStatus) {
	if st == nil {
		return
	}

	job, err := s.store.GetUpdateJob(ctx, st.JobID)
	if err != nil {
		if !isNotFound(err) {
			s.logger.Error("update: get job for status report failed", "job_id", st.JobID, "host_id", hostID, "error", err)
		}
		return
	}
	if job.HostID != hostID {
		s.logger.Warn("update: status report host mismatch, ignoring", "job_id", st.JobID, "reported_host", hostID, "job_host", job.HostID)
		return
	}
	if job.State == models.UpdateJobSucceeded || job.State == models.UpdateJobFailed {
		// Already terminal (e.g. a duplicate report, or a timeout
		// already recorded it as failed); do not overwrite.
		return
	}

	switch st.State {
	case models.UpdateJobSucceeded:
		if job.Target != "latest" && !versionMatchesTarget(st.Version, job.Target) {
			job.State = models.UpdateJobFailed
			job.Reason = models.UpdateReasonVerifyFailed
			job.Error = fmt.Sprintf("agent reported version %q, target was %q", st.Version, job.Target)
			break
		}
		job.State = models.UpdateJobSucceeded
		job.Reason = models.UpdateReasonNone
		job.Error = ""
	case models.UpdateJobFailed:
		job.State = models.UpdateJobFailed
		job.Reason = st.ErrorCode
		if job.Reason == "" {
			job.Reason = models.UpdateReasonUnknown
		}
		job.Error = st.Error
		// already_up_to_date is completion, not failure (SPEC-v0.6 §2).
		if job.Reason == models.UpdateReasonAlreadyUpToDate {
			job.State = models.UpdateJobSucceeded
		}
	default:
		s.logger.Warn("update: status report has unexpected state, ignoring", "job_id", st.JobID, "state", st.State)
		return
	}

	if err := s.store.UpdateUpdateJob(ctx, job); err != nil {
		s.logger.Error("update: persist status report failed", "job_id", st.JobID, "error", err)
	}
}

// versionMatchesTarget reports whether reported (an agent's own version
// string after attempting an update) satisfies target (an exact
// "vX.Y.Z" tag): reported must parse and be equal to target. version
// strings are compared as exact tag text, not semver equality, since
// "success" for an explicit-tag target means "the agent is now running
// exactly that tag," not merely "some newer version."
func versionMatchesTarget(reported, target string) bool {
	return reported == target
}

// ackUpdateJob transitions job from queued to in_progress, called when
// handleIngest observes that a report is (implicitly) the agent's
// acknowledgement of a previously-delivered UpdateRequest — i.e. the
// same job was the one returned by pendingUpdateRequestForHost on a
// prior report, and this report no longer needs re-delivery because the
// agent's next report already carries no further need for it. In
// practice the hub has no separate "ack" message from the agent (the
// agent's push-only architecture means the write to
// update-request.json happens fully out of band from the hub's view);
// this method is called immediately after handing a queued job's
// request to an agent (see updateingest.go), advancing the job to
// in_progress at the same moment so the parallel-limit accounting in
// pendingUpdateRequestForHost reflects it on the very next evaluation.
func (s *Server) ackUpdateJob(ctx context.Context, job models.UpdateJob) {
	if job.State != models.UpdateJobQueued {
		return
	}
	job.State = models.UpdateJobInProgress
	if err := s.store.UpdateUpdateJob(ctx, job); err != nil {
		s.logger.Error("update: ack (queued -> in_progress) failed", "job_id", job.ID, "host_id", job.HostID, "error", err)
	}
}

// checkUpdateJobTimeouts scans every in_progress job and fails any that
// has been in_progress for longer than updateJobTimeout (SPEC-v0.6 §2
// step 5), using now as the reference time so tests can inject a clock.
func (s *Server) checkUpdateJobTimeouts(ctx context.Context, now time.Time) {
	jobs, err := s.store.ListUpdateJobs(ctx, "", models.UpdateJobInProgress)
	if err != nil {
		s.logger.Error("update: list in-progress jobs for timeout check failed", "error", err)
		return
	}
	cutoff := now.Add(-updateJobTimeout).Unix()
	for _, job := range jobs {
		if job.UpdatedAt > cutoff {
			continue
		}
		job.State = models.UpdateJobFailed
		job.Reason = models.UpdateReasonTimeout
		job.Error = "no status report within 15 minutes"
		if err := s.store.UpdateUpdateJob(ctx, job); err != nil {
			s.logger.Error("update: timeout transition failed", "job_id", job.ID, "host_id", job.HostID, "error", err)
		}
	}
}

// retryUpdateJob creates a new queued job cloned from a failed job
// (SPEC-v0.6 §2 step 6). It refuses to retry a job whose Reason is
// "unsupported" or "not_enabled" (the caller — the API handler — is
// responsible for surfacing the guidance message instead of a retry
// button per the spec; this method itself just enforces the same rule
// server-side so a direct API call can't bypass it).
func (s *Server) retryUpdateJob(ctx context.Context, jobID int64) (models.UpdateJob, error) {
	old, err := s.store.GetUpdateJob(ctx, jobID)
	if err != nil {
		return models.UpdateJob{}, err
	}
	if old.State != models.UpdateJobFailed {
		return models.UpdateJob{}, fmt.Errorf("hub: retry update job: job %d is not failed (state=%s)", jobID, old.State)
	}
	if old.Reason == models.UpdateReasonUnsupported || old.Reason == models.UpdateReasonNotEnabled {
		return models.UpdateJob{}, fmt.Errorf("hub: retry update job: job %d reason %q is not retryable", jobID, old.Reason)
	}

	next, err := s.store.CreateUpdateJob(ctx, models.UpdateJob{
		BatchID: old.BatchID,
		HostID:  old.HostID,
		Target:  old.Target,
		State:   models.UpdateJobQueued,
		Attempt: old.Attempt + 1,
	})
	if err != nil {
		return models.UpdateJob{}, fmt.Errorf("hub: retry update job: create: %w", err)
	}
	return next, nil
}

// cancelUpdateBatch transitions every queued job in batchID to failed
// with UpdateReasonUnknown-free cancellation semantics (SPEC-v0.6 §2:
// "대기 중인 job만 취소" — only queued jobs are affected; in_progress
// jobs run to completion). It returns the number of jobs actually
// canceled.
func (s *Server) cancelUpdateBatch(ctx context.Context, batchID string) (int, error) {
	jobs, err := s.store.ListUpdateJobs(ctx, batchID, models.UpdateJobQueued)
	if err != nil {
		return 0, fmt.Errorf("hub: cancel update batch: list queued jobs: %w", err)
	}
	canceled := 0
	for _, job := range jobs {
		job.State = models.UpdateJobFailed
		job.Reason = models.UpdateReasonUnknown
		job.Error = "canceled"
		if err := s.store.UpdateUpdateJob(ctx, job); err != nil {
			return canceled, fmt.Errorf("hub: cancel update batch: update job %d: %w", job.ID, err)
		}
		canceled++
	}
	return canceled, nil
}
