package hub

import (
	"net/http"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestBuildUpdateBatchSummaries(t *testing.T) {
	t.Parallel()

	t.Run("mixed_terminal_batch_computes_duration_and_reasons", func(t *testing.T) {
		t.Parallel()
		jobs := []models.UpdateJob{
			{ID: 1, BatchID: "b1", Target: "latest", State: models.UpdateJobSucceeded, CreatedAt: 100, UpdatedAt: 150},
			{ID: 2, BatchID: "b1", Target: "latest", State: models.UpdateJobFailed, Reason: models.UpdateReasonTimeout, CreatedAt: 100, UpdatedAt: 200},
			{ID: 3, BatchID: "b1", Target: "latest", State: models.UpdateJobFailed, Reason: models.UpdateReasonTimeout, CreatedAt: 100, UpdatedAt: 180},
			{ID: 4, BatchID: "b1", Target: "latest", State: models.UpdateJobFailed, Reason: models.UpdateReasonDownloadFailed, CreatedAt: 100, UpdatedAt: 190},
		}
		got := BuildUpdateBatchSummaries(jobs)
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		b := got[0]
		if b.TargetCount != 4 || b.Succeeded != 1 || b.Failed != 3 || b.Pending != 0 {
			t.Errorf("counts = %+v, want target=4 succeeded=1 failed=3 pending=0", b)
		}
		if b.CreatedAt != 100 {
			t.Errorf("CreatedAt = %d, want 100", b.CreatedAt)
		}
		if b.FinishedAt != 200 {
			t.Errorf("FinishedAt = %d, want 200 (latest UpdatedAt)", b.FinishedAt)
		}
		if b.DurationSec != 100 {
			t.Errorf("DurationSec = %d, want 100", b.DurationSec)
		}
		if len(b.FailureReasons) != 2 {
			t.Fatalf("FailureReasons len = %d, want 2", len(b.FailureReasons))
		}
		if b.FailureReasons[0].Reason != models.UpdateReasonTimeout || b.FailureReasons[0].Count != 2 {
			t.Errorf("FailureReasons[0] = %+v, want timeout x2 first (highest count)", b.FailureReasons[0])
		}
	})

	t.Run("pending_batch_has_no_finished_at_or_duration", func(t *testing.T) {
		t.Parallel()
		jobs := []models.UpdateJob{
			{ID: 1, BatchID: "b1", State: models.UpdateJobSucceeded, CreatedAt: 100, UpdatedAt: 150},
			{ID: 2, BatchID: "b1", State: models.UpdateJobInProgress, CreatedAt: 100, UpdatedAt: 160},
		}
		got := BuildUpdateBatchSummaries(jobs)
		b := got[0]
		if b.Pending != 1 {
			t.Errorf("Pending = %d, want 1", b.Pending)
		}
		if b.FinishedAt != 0 {
			t.Errorf("FinishedAt = %d, want 0 (still pending)", b.FinishedAt)
		}
		if b.DurationSec != 0 {
			t.Errorf("DurationSec = %d, want 0 (still pending)", b.DurationSec)
		}
	})

	t.Run("queued_job_counts_as_pending", func(t *testing.T) {
		t.Parallel()
		jobs := []models.UpdateJob{
			{ID: 1, BatchID: "b1", State: models.UpdateJobQueued, CreatedAt: 100, UpdatedAt: 100},
		}
		got := BuildUpdateBatchSummaries(jobs)
		if got[0].Pending != 1 {
			t.Errorf("Pending = %d, want 1", got[0].Pending)
		}
	})

	t.Run("multiple_batches_sorted_newest_first", func(t *testing.T) {
		t.Parallel()
		jobs := []models.UpdateJob{
			{ID: 1, BatchID: "old", State: models.UpdateJobSucceeded, CreatedAt: 100, UpdatedAt: 100},
			{ID: 2, BatchID: "new", State: models.UpdateJobSucceeded, CreatedAt: 500, UpdatedAt: 500},
			{ID: 3, BatchID: "mid", State: models.UpdateJobSucceeded, CreatedAt: 300, UpdatedAt: 300},
		}
		got := BuildUpdateBatchSummaries(jobs)
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		gotOrder := []string{got[0].BatchID, got[1].BatchID, got[2].BatchID}
		want := []string{"new", "mid", "old"}
		for i := range want {
			if gotOrder[i] != want[i] {
				t.Errorf("order[%d] = %q, want %q (full order %v)", i, gotOrder[i], want[i], gotOrder)
			}
		}
	})

	t.Run("no_jobs_returns_empty_not_nil_reasons", func(t *testing.T) {
		t.Parallel()
		got := BuildUpdateBatchSummaries(nil)
		if len(got) != 0 {
			t.Errorf("len = %d, want 0", len(got))
		}
	})

	t.Run("succeeded_batch_has_empty_failure_reasons_slice", func(t *testing.T) {
		t.Parallel()
		jobs := []models.UpdateJob{
			{ID: 1, BatchID: "b1", State: models.UpdateJobSucceeded, CreatedAt: 100, UpdatedAt: 150},
		}
		got := BuildUpdateBatchSummaries(jobs)
		if got[0].FailureReasons == nil {
			t.Error("FailureReasons is nil, want non-nil empty slice for JSON [] encoding")
		}
		if len(got[0].FailureReasons) != 0 {
			t.Errorf("FailureReasons len = %d, want 0", len(got[0].FailureReasons))
		}
	})
}

func TestPaginateBatchSummaries(t *testing.T) {
	t.Parallel()

	summaries := []models.UpdateBatchSummary{
		{BatchID: "b5", CreatedAt: 500},
		{BatchID: "b4", CreatedAt: 400},
		{BatchID: "b3", CreatedAt: 300},
		{BatchID: "b2", CreatedAt: 200},
		{BatchID: "b1", CreatedAt: 100},
	}

	t.Run("default_limit_applied_when_zero", func(t *testing.T) {
		t.Parallel()
		got := paginateBatchSummaries(summaries, 0, 0)
		if len(got) != 5 {
			t.Errorf("len = %d, want 5 (under default limit)", len(got))
		}
	})

	t.Run("limit_clamped_to_max", func(t *testing.T) {
		t.Parallel()
		got := paginateBatchSummaries(summaries, 0, maxBatchListLimit+1000)
		if len(got) != 5 {
			t.Errorf("len = %d, want 5", len(got))
		}
	})

	t.Run("explicit_limit_truncates", func(t *testing.T) {
		t.Parallel()
		got := paginateBatchSummaries(summaries, 0, 2)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].BatchID != "b5" || got[1].BatchID != "b4" {
			t.Errorf("got = %v, want [b5 b4]", got)
		}
	})

	t.Run("before_cursor_filters_strictly_older", func(t *testing.T) {
		t.Parallel()
		got := paginateBatchSummaries(summaries, 300, 10)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (b2, b1 with CreatedAt < 300)", len(got))
		}
		if got[0].BatchID != "b2" || got[1].BatchID != "b1" {
			t.Errorf("got = %v, want [b2 b1]", got)
		}
	})

	t.Run("before_and_limit_combined", func(t *testing.T) {
		t.Parallel()
		got := paginateBatchSummaries(summaries, 500, 2)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].BatchID != "b4" || got[1].BatchID != "b3" {
			t.Errorf("got = %v, want [b4 b3]", got)
		}
	})

	t.Run("before_cursor_with_no_matches", func(t *testing.T) {
		t.Parallel()
		got := paginateBatchSummaries(summaries, 100, 10)
		if len(got) != 0 {
			t.Errorf("len = %d, want 0 (nothing older than the oldest batch)", len(got))
		}
	})
}

// --- HTTP handler tests ---

func TestHandleListUpdateBatches_RequireUser(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/agents/updates/batches", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (no token)", rec.Code)
	}

	rec2 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/agents/updates/batches", "203.0.113.1:1234", testUIToken, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (with token), body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestHandleListUpdateBatches_PaginationQueryParams(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := t.Context()

	for i, batchID := range []string{"b1", "b2", "b3"} {
		if _, err := store.CreateUpdateJob(ctx, models.UpdateJob{
			BatchID: batchID, HostID: "h1", Target: "latest", State: models.UpdateJobSucceeded,
		}); err != nil {
			t.Fatalf("CreateUpdateJob(%d): %v", i, err)
		}
	}

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/agents/updates/batches?limit=1", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[struct {
		Batches []models.UpdateBatchSummary `json:"batches"`
	}](t, rec.Body)
	if len(got.Batches) != 1 {
		t.Errorf("Batches len = %d, want 1 (limit=1)", len(got.Batches))
	}
}
