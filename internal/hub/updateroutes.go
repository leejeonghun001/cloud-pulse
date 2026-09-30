package hub

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// maxUpdateBodyBytes bounds the size of remote-update admin request
// bodies.
const maxUpdateBodyBytes = 16 << 10 // 16 KiB

// maxUpdateBatchHosts caps how many host IDs a single
// POST /api/v1/agents/updates call may target, defense-in-depth against
// an accidental or malicious request enqueueing an unbounded number of
// jobs in one call.
const maxUpdateBatchHosts = 1000

// Audit actions recorded by this stage's routes (SPEC-v0.6 §2, audit
// slog line per request). Free-form "<entity>.<verb>" strings per
// models.AuditAction's doc comment, consistent with the network-cost
// stage's own constants in audit.go.
const (
	AuditActionUpdateBatchCreate models.AuditAction = "update_batch.create"
	AuditActionUpdateJobRetry    models.AuditAction = "update_job.retry"
	AuditActionUpdateBatchCancel models.AuditAction = "update_batch.cancel"
)

// registerUpdateRoutes registers the remote agent update API
// (SPEC-v0.6 §2): batch create/list, retry, cancel.
func (s *Server) registerUpdateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/agents/updates", s.requireAdmin(s.handleListUpdateJobs))
	mux.HandleFunc("POST /api/v1/agents/updates", s.requireAdmin(s.handleCreateUpdateBatch))
	mux.HandleFunc("POST /api/v1/agents/updates/{job_id}/retry", s.requireAdmin(s.handleRetryUpdateJob))
	mux.HandleFunc("POST /api/v1/agents/updates/{batch_id}/cancel", s.requireAdmin(s.handleCancelUpdateBatch))
}

// handleListUpdateJobs responds with remote-update jobs, optionally
// filtered by batch: GET /api/v1/agents/updates?batch= (admin).
func (s *Server) handleListUpdateJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	batchID := r.URL.Query().Get("batch")

	jobs, err := s.store.ListUpdateJobs(ctx, batchID, "")
	if err != nil {
		s.logger.Error("list update jobs failed", "batch_id", batchID, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// handleCreateUpdateBatch creates a batch of remote-update jobs:
// POST /api/v1/agents/updates (admin). Logs an audit slog.Info line
// naming the actor, host count, and target per SPEC-v0.6 §2.
func (s *Server) handleCreateUpdateBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxUpdateBodyBytes)
	var req models.UpdateBatchCreate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	if len(req.HostIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "host_ids must not be empty"})
		return
	}
	if len(req.HostIDs) > maxUpdateBatchHosts {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "host_ids exceeds maximum batch size"})
		return
	}
	if req.MaxParallel < 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "max_parallel must be >= 0"})
		return
	}

	batch, err := s.createUpdateBatch(ctx, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: err.Error()})
		return
	}
	s.setBatchMaxParallel(batch.BatchID, batch.MaxParallel)

	s.logger.Info("audit: remote update batch created",
		"actor", auditActorFor(ctx),
		"remote", clientIP(r),
		"batch_id", batch.BatchID,
		"host_count", len(batch.Jobs),
		"requested_host_count", len(req.HostIDs),
		"target", batch.Target,
		"max_parallel", batch.MaxParallel,
	)
	s.recordAudit(ctx, r, AuditActionUpdateBatchCreate, "update_batch", batch.BatchID, nil, batch)

	writeJSON(w, http.StatusCreated, batch)
}

// handleRetryUpdateJob creates a new queued job cloned from a failed
// one: POST /api/v1/agents/updates/{job_id}/retry (admin).
func (s *Server) handleRetryUpdateJob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := r.PathValue("job_id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid job id"})
		return
	}

	job, err := s.retryUpdateJob(ctx, id)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "job not found"})
			return
		}
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: err.Error()})
		return
	}

	s.logger.Info("audit: remote update job retried",
		"actor", auditActorFor(ctx),
		"remote", clientIP(r),
		"original_job_id", id,
		"new_job_id", job.ID,
		"host_id", job.HostID,
		"target", job.Target,
	)
	s.recordAudit(ctx, r, AuditActionUpdateJobRetry, "update_job", strconv.FormatInt(job.ID, 10), map[string]any{"original_job_id": id}, job)

	writeJSON(w, http.StatusOK, job)
}

// handleCancelUpdateBatch cancels every queued (not yet in_progress)
// job in a batch: POST /api/v1/agents/updates/{batch_id}/cancel (admin).
func (s *Server) handleCancelUpdateBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	batchID := r.PathValue("batch_id")

	canceled, err := s.cancelUpdateBatch(ctx, batchID)
	if err != nil {
		s.logger.Error("cancel update batch failed", "batch_id", batchID, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.logger.Info("audit: remote update batch canceled",
		"actor", auditActorFor(ctx),
		"remote", clientIP(r),
		"batch_id", batchID,
		"canceled_count", canceled,
	)
	s.recordAudit(ctx, r, AuditActionUpdateBatchCancel, "update_batch", batchID, nil, map[string]any{"canceled": canceled})

	writeJSON(w, http.StatusOK, map[string]any{"canceled": canceled})
}
