package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

// TestBuildAlerting_UpdateBatchFailedDeliversToWebhook is the SPEC-v0.8
// §3-1 Go-level equivalent of a smoke-test scenario ("batch with a
// failing host -> update_batch_failed delivered to the local fake
// webhook"): a real *storage.DB + real *alerting.Engine (via
// buildAlerting, the exact same wiring cmd/hub's main() uses) end to
// end through a local httptest webhook server — no fixed ports, safe
// to run in a feature-stage worktree in parallel with other stages
// (unlike scripts/smoke.sh, which the Part B rules reserve for the
// integrate stage). Mirrors
// TestBuildAlerting_WiresEngineToHubOptions's existing pattern for the
// cpu metric, extended to update_batch_failed's RolloutAlertEvaluator
// path.
func TestBuildAlerting_UpdateBatchFailedDeliversToWebhook(t *testing.T) {
	t.Setenv("CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS", "1")

	var delivered []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered = append(delivered, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cloud-pulse.db")
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})

	logger := testDiscardLogger()
	engine, wrapped, notifyFactory := buildAlerting(store, logger)
	t.Cleanup(engine.Close)

	var opts hub.Options
	opts.Alerting = wrapped
	opts.NotifyFactory = notifyFactory
	if opts.Alerting == nil || opts.NotifyFactory == nil {
		t.Fatal("buildAlerting returned nil Alerting/NotifyFactory")
	}

	evaluator, ok := wrapped.(hub.RolloutAlertEvaluator)
	if !ok {
		t.Fatal("wrapped AlertEngine does not implement hub.RolloutAlertEvaluator")
	}

	ch, err := store.CreateNotifyChannel(ctx, models.NotifyChannel{
		Name: "ops-webhook", Type: models.NotifyChannelWebhook, Enabled: true,
		Config: map[string]string{"url": srv.URL},
	})
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}
	if _, err := store.CreateAlertRule(ctx, models.AlertRule{
		Name: "Remote update batch has a failure", Enabled: true,
		Metric: models.AlertMetricUpdateBatchFailed, ChannelIDs: []int64{ch.ID},
	}); err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}

	now := time.Now()
	outcome := alerting.UpdateBatchOutcome{
		BatchID: "batch-smoke-test", Target: "latest",
		Succeeded: 2, Failed: 1, Pending: 0,
		FailedHosts: []alerting.FailedUpdateHost{
			{HostID: "h3", Hostname: "host-three", Reason: models.UpdateReasonTimeout},
		},
	}
	if err := evaluator.EvaluateUpdateBatch(ctx, now, outcome); err != nil {
		t.Fatalf("EvaluateUpdateBatch: %v", err)
	}
	engine.Wait()

	if len(delivered) != 1 {
		t.Fatalf("delivered = %v, want exactly one delivery to the fake webhook", delivered)
	}

	// Re-evaluating the same (already-finished, already-notified) batch
	// must not deliver a second time — the update_batch_failed
	// idempotency guarantee (see internal/alerting/rollout.go) holds
	// end to end through the real store, not just against the
	// in-package fakeStore rollout_test.go already covers.
	if err := evaluator.EvaluateUpdateBatch(ctx, now, outcome); err != nil {
		t.Fatalf("EvaluateUpdateBatch (re-eval): %v", err)
	}
	engine.Wait()
	if len(delivered) != 1 {
		t.Fatalf("delivered after re-eval = %v, want still exactly one (no duplicate)", delivered)
	}
}
