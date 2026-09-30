package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// fakeRolloutAlertEngine is a minimal AlertEngine + RolloutAlertEvaluator
// test double recording every call, used to verify
// evaluateAgentOutdatedAlerts/scanUpdateBatchesForAlerts build correct
// inputs from hub Store data without needing a real
// *alerting.Engine (which would require its own Store adapter).
type fakeRolloutAlertEngine struct {
	mu sync.Mutex

	outdatedCalls []outdatedCall
	batchCalls    []alerting.UpdateBatchOutcome
}

type outdatedCall struct {
	now   time.Time
	hosts []alerting.AgentOutdatedInput
}

func (f *fakeRolloutAlertEngine) Evaluate(context.Context, time.Time, []models.HostSnapshot) error {
	return nil
}

func (f *fakeRolloutAlertEngine) Preview(context.Context, time.Time, models.AlertRule, []models.HostSnapshot) (map[string]bool, error) {
	return nil, nil
}

func (f *fakeRolloutAlertEngine) EvaluateAgentOutdated(_ context.Context, now time.Time, hosts []alerting.AgentOutdatedInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outdatedCalls = append(f.outdatedCalls, outdatedCall{now: now, hosts: hosts})
	return nil
}

func (f *fakeRolloutAlertEngine) EvaluateUpdateBatch(_ context.Context, _ time.Time, outcome alerting.UpdateBatchOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCalls = append(f.batchCalls, outcome)
	return nil
}

func (f *fakeRolloutAlertEngine) lastOutdatedHosts() []alerting.AgentOutdatedInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.outdatedCalls) == 0 {
		return nil
	}
	return f.outdatedCalls[len(f.outdatedCalls)-1].hosts
}

func (f *fakeRolloutAlertEngine) batchOutcomes() []alerting.UpdateBatchOutcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]alerting.UpdateBatchOutcome, len(f.batchCalls))
	copy(out, f.batchCalls)
	return out
}

func TestEvaluateAgentOutdatedAlerts_BuildsInputsFromHosts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	engine := &fakeRolloutAlertEngine{}
	opts := testOptions()
	opts.Alerting = engine
	s := New(opts, store, nil, nil, nil, testLogger())

	now := opts.now()
	if err := store.UpsertHost(t.Context(), models.HostInfo{ID: "h1", Hostname: "host-one", AgentVersion: "v0.6.0"}, now.Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	s.recordUpdateCheck(now, "v0.7.0", nil)

	s.evaluateAgentOutdatedAlerts(t.Context(), now)

	hosts := engine.lastOutdatedHosts()
	if len(hosts) != 1 {
		t.Fatalf("outdated hosts len = %d, want 1", len(hosts))
	}
	if hosts[0].HostID != "h1" || !hosts[0].Outdated {
		t.Errorf("hosts[0] = %+v, want h1 outdated=true", hosts[0])
	}
	if hosts[0].OutdatedSince == 0 {
		t.Error("hosts[0].OutdatedSince = 0, want non-zero once observed outdated")
	}
}

func TestEvaluateAgentOutdatedAlerts_NoOpWithoutEvaluatorCapability(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store) // no Alerting configured
	// Must not panic when Options.Alerting is nil.
	s.evaluateAgentOutdatedAlerts(t.Context(), time.Now())
}

func TestScanUpdateBatchesForAlerts_FinishedBatchWithFailurePassedThrough(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	engine := &fakeRolloutAlertEngine{}
	opts := testOptions()
	opts.Alerting = engine
	s := New(opts, store, nil, nil, nil, testLogger())
	ctx := t.Context()

	if err := store.UpsertHost(ctx, models.HostInfo{ID: "h1", Hostname: "host-one"}, opts.now().Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	if _, err := store.CreateUpdateJob(ctx, models.UpdateJob{BatchID: "batch-1", HostID: "h1", Target: "latest", State: models.UpdateJobFailed, Reason: models.UpdateReasonTimeout}); err != nil {
		t.Fatalf("CreateUpdateJob: %v", err)
	}
	if _, err := store.CreateUpdateJob(ctx, models.UpdateJob{BatchID: "batch-2", HostID: "h1", Target: "latest", State: models.UpdateJobInProgress}); err != nil {
		t.Fatalf("CreateUpdateJob (pending batch): %v", err)
	}

	s.scanUpdateBatchesForAlerts(ctx, opts.now())

	outcomes := engine.batchOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("batchOutcomes len = %d, want 1 (only the finished batch)", len(outcomes))
	}
	if outcomes[0].BatchID != "batch-1" {
		t.Errorf("BatchID = %q, want batch-1", outcomes[0].BatchID)
	}
	if len(outcomes[0].FailedHosts) != 1 || outcomes[0].FailedHosts[0].Hostname != "host-one" {
		t.Errorf("FailedHosts = %+v, want one entry with hostname host-one", outcomes[0].FailedHosts)
	}
}

func TestScanUpdateBatchesForAlerts_NoOpWithoutEvaluatorCapability(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	s.scanUpdateBatchesForAlerts(t.Context(), time.Now())
}
