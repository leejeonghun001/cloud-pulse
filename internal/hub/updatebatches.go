package hub

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// defaultBatchListLimit/maxBatchListLimit bound
// GET /api/v1/agents/updates/batches' `limit` query parameter
// (SPEC-v0.8 §3-1).
const (
	defaultBatchListLimit = 20
	maxBatchListLimit     = 200
)

// BuildUpdateBatchSummaries groups jobs by BatchID into one
// models.UpdateBatchSummary per batch, sorted newest-first by
// CreatedAt (ties broken by BatchID descending for a stable order). It
// is a pure function of jobs: no store/context access, matching
// BuildFleetVersionSummary's own shape in fleetversions.go.
func BuildUpdateBatchSummaries(jobs []models.UpdateJob) []models.UpdateBatchSummary {
	type acc struct {
		summary      models.UpdateBatchSummary
		reasonCounts map[models.UpdateJobReason]int
		minCreated   int64
		maxUpdated   int64
		anyPending   bool
	}
	byBatch := make(map[string]*acc)
	var order []string

	for _, j := range jobs {
		a, ok := byBatch[j.BatchID]
		if !ok {
			a = &acc{
				summary:      models.UpdateBatchSummary{BatchID: j.BatchID, Target: j.Target},
				reasonCounts: make(map[models.UpdateJobReason]int),
				minCreated:   j.CreatedAt,
			}
			byBatch[j.BatchID] = a
			order = append(order, j.BatchID)
		}

		a.summary.TargetCount++
		if a.minCreated == 0 || (j.CreatedAt != 0 && j.CreatedAt < a.minCreated) {
			a.minCreated = j.CreatedAt
		}
		if j.UpdatedAt > a.maxUpdated {
			a.maxUpdated = j.UpdatedAt
		}

		switch j.State {
		case models.UpdateJobSucceeded:
			a.summary.Succeeded++
		case models.UpdateJobFailed:
			a.summary.Failed++
			a.reasonCounts[j.Reason]++
		default: // queued, in_progress
			a.summary.Pending++
			a.anyPending = true
		}
	}

	out := make([]models.UpdateBatchSummary, 0, len(order))
	for _, batchID := range order {
		a := byBatch[batchID]
		a.summary.CreatedAt = a.minCreated
		if !a.anyPending {
			a.summary.FinishedAt = a.maxUpdated
			if a.summary.FinishedAt > a.summary.CreatedAt {
				a.summary.DurationSec = a.summary.FinishedAt - a.summary.CreatedAt
			}
		}

		reasons := make([]models.UpdateBatchFailureReasonCount, 0, len(a.reasonCounts))
		for reason, count := range a.reasonCounts {
			reasons = append(reasons, models.UpdateBatchFailureReasonCount{Reason: reason, Count: count})
		}
		sort.Slice(reasons, func(i, j int) bool {
			if reasons[i].Count != reasons[j].Count {
				return reasons[i].Count > reasons[j].Count
			}
			return reasons[i].Reason < reasons[j].Reason
		})
		a.summary.FailureReasons = reasons

		out = append(out, a.summary)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].BatchID > out[j].BatchID
	})
	return out
}

// paginateBatchSummaries applies the `before`/`limit` cursor pagination
// GET /api/v1/agents/updates/batches accepts: before (a CreatedAt unix-
// seconds cursor, 0 meaning "no cursor, start from the newest") and
// limit (clamped to [1, maxBatchListLimit], defaulting to
// defaultBatchListLimit when <= 0). summaries must already be sorted
// newest-first (see BuildUpdateBatchSummaries) for the cursor
// comparison to behave correctly.
func paginateBatchSummaries(summaries []models.UpdateBatchSummary, before int64, limit int) []models.UpdateBatchSummary {
	if limit <= 0 {
		limit = defaultBatchListLimit
	}
	if limit > maxBatchListLimit {
		limit = maxBatchListLimit
	}

	filtered := summaries
	if before > 0 {
		filtered = make([]models.UpdateBatchSummary, 0, len(summaries))
		for _, s := range summaries {
			if s.CreatedAt < before {
				filtered = append(filtered, s)
			}
		}
	}

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}

// handleListUpdateBatches responds with paginated batch summaries:
// GET /api/v1/agents/updates/batches?limit=&before= (any authenticated
// user — read-only, same auth tier as GET /api/v1/fleet/versions and
// GET /api/v1/hosts; see fleetversions.go's registerFleetVersionsRoute
// doc comment for the shared rationale). An invalid limit/before value
// is ignored (falls back to the default/no-cursor behavior) rather
// than rejected with a 400, since these are optional display-tuning
// parameters, not something a malformed value should hard-fail a
// dashboard page load over.
func (s *Server) handleListUpdateBatches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)

	jobs, err := s.store.ListUpdateJobs(ctx, "", "")
	if err != nil {
		s.logger.Error("list update batches: list jobs failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	all := BuildUpdateBatchSummaries(jobs)
	page := paginateBatchSummaries(all, before, limit)
	writeJSON(w, http.StatusOK, map[string]any{"batches": page})
}
