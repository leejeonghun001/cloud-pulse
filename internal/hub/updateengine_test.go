package hub

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// updateTestHost inserts a host record into store directly (bypassing
// ingest) with the given remote-update capability/opt-in, for tests
// that only care about job-engine/routing behavior, not ingest itself.
func updateTestHost(t *testing.T, store *fakeStore, id string, capability models.RemoteUpdateCapability, agentVersion string) {
	t.Helper()
	err := store.UpsertHost(context.Background(), models.HostInfo{
		ID:           id,
		Hostname:     id,
		OS:           "linux",
		AgentVersion: agentVersion,
		RemoteUpdate: capability,
	}, time.Now().Unix())
	if err != nil {
		t.Fatalf("upsert host %s: %v", id, err)
	}
}

func capableOptedIn() models.RemoteUpdateCapability {
	return models.RemoteUpdateCapability{Supported: true, OptedIn: true}
}

// TestRemoteUpdateCapable_PerPlatformVersionGate covers SPEC-v0.7 §6's
// widened gate: Linux >= v0.6.0, macOS/Windows >= v0.7.0. Platform is
// set explicitly on RemoteUpdateCapability (the v0.7.0+ agent-reported
// field); OS is set to something deliberately different in some cases
// to prove Platform (not the legacy OS field) drives the decision once
// it's populated.
func TestRemoteUpdateCapable_PerPlatformVersionGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		platform     string
		os           string
		agentVersion string
		supported    bool
		want         bool
	}{
		{"linux v0.6.0 capable", "linux", "linux", "v0.6.0", true, true},
		{"linux v0.5.9 too old", "linux", "linux", "v0.5.9", true, false},
		{"darwin v0.7.0 capable", "darwin", "darwin", "v0.7.0", true, true},
		{"darwin v0.6.0 too old (pre-v0.7.0 macOS never existed, but gate still enforces)", "darwin", "darwin", "v0.6.0", true, false},
		{"windows v0.7.0 capable", "windows", "windows", "v0.7.0", true, true},
		{"windows v0.6.9 too old", "windows", "windows", "v0.6.9", true, false},
		{"not supported at all", "linux", "linux", "v0.6.0", false, false},
		{"unknown platform refused", "plan9", "plan9", "v9.9.9", true, false},
		{"empty platform falls back to OS=linux", "", "linux", "v0.6.0", true, true},
		{"empty platform falls back to OS=darwin", "", "darwin", "v0.7.0", true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host := models.HostInfo{
				OS:           tc.os,
				AgentVersion: tc.agentVersion,
				RemoteUpdate: models.RemoteUpdateCapability{Supported: tc.supported, Platform: tc.platform},
			}
			if got := remoteUpdateCapable(host); got != tc.want {
				t.Errorf("remoteUpdateCapable(platform=%q os=%q version=%q supported=%v) = %v, want %v",
					tc.platform, tc.os, tc.agentVersion, tc.supported, got, tc.want)
			}
		})
	}
}

// TestCreateUpdateBatch_CopiesPlatformFromHostInfo guards SPEC-v0.7
// §1's "hub이 HostInfo.RemoteUpdate.Platform을 UpdateJob.Platform으로
// 복사" requirement.
func TestCreateUpdateBatch_CopiesPlatformFromHostInfo(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	if err := store.UpsertHost(ctx, models.HostInfo{
		ID:           "mac1",
		Hostname:     "mac1",
		OS:           "darwin",
		AgentVersion: "v0.7.0",
		RemoteUpdate: models.RemoteUpdateCapability{Supported: true, OptedIn: true, Platform: "darwin"},
	}, time.Now().Unix()); err != nil {
		t.Fatalf("upsert host: %v", err)
	}

	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"mac1"}, Target: "v0.7.1"})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}
	if len(batch.Jobs) != 1 {
		t.Fatalf("len(batch.Jobs) = %d, want 1", len(batch.Jobs))
	}
	if got := batch.Jobs[0].Platform; got != "darwin" {
		t.Errorf("job.Platform = %q, want %q", got, "darwin")
	}
}

// TestCreateUpdateBatch_PlatformFallsBackToHostOS covers a host whose
// RemoteUpdate.Platform is empty (a pre-v0.7.0 agent report) — the
// job's Platform should fall back to HostInfo.OS rather than being
// left empty.
func TestCreateUpdateBatch_PlatformFallsBackToHostOS(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	if err := store.UpsertHost(ctx, models.HostInfo{
		ID:           "old1",
		Hostname:     "old1",
		OS:           "linux",
		AgentVersion: "v0.6.0",
		RemoteUpdate: models.RemoteUpdateCapability{Supported: true, OptedIn: true}, // Platform unset
	}, time.Now().Unix()); err != nil {
		t.Fatalf("upsert host: %v", err)
	}

	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"old1"}, Target: "v0.6.1"})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}
	if got := batch.Jobs[0].Platform; got != "linux" {
		t.Errorf("job.Platform = %q, want %q (fallback to HostInfo.OS)", got, "linux")
	}
}

func TestCreateUpdateBatch_CreatesQueuedJobsPerHost(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")
	updateTestHost(t, store, "h2", capableOptedIn(), "v0.6.0")

	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1", "h2"}, Target: "v0.6.1"})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}
	if len(batch.Jobs) != 2 {
		t.Fatalf("len(batch.Jobs) = %d, want 2", len(batch.Jobs))
	}
	for _, job := range batch.Jobs {
		if job.State != models.UpdateJobQueued {
			t.Errorf("job %d state = %q, want queued", job.ID, job.State)
		}
		if job.Target != "v0.6.1" {
			t.Errorf("job %d target = %q, want v0.6.1", job.ID, job.Target)
		}
		if job.BatchID != batch.BatchID {
			t.Errorf("job %d batch_id = %q, want %q", job.ID, job.BatchID, batch.BatchID)
		}
	}
	if batch.MaxParallel != defaultMaxParallel {
		t.Errorf("MaxParallel = %d, want default %d", batch.MaxParallel, defaultMaxParallel)
	}
}

func TestCreateUpdateBatch_DefaultTargetIsLatest(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}
	if batch.Target != "latest" {
		t.Errorf("Target = %q, want latest", batch.Target)
	}
}

func TestCreateUpdateBatch_InvalidTargetRejected(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	_, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "; rm -rf /"})
	if err == nil {
		t.Fatal("createUpdateBatch: want an error for an invalid target")
	}
}

func TestCreateUpdateBatch_SkipsUnknownHosts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1", "does-not-exist"}, Target: "v0.6.1"})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}
	if len(batch.Jobs) != 1 {
		t.Fatalf("len(batch.Jobs) = %d, want 1 (unknown host skipped)", len(batch.Jobs))
	}
}

func TestPendingUpdateRequestForHost_NotOptedIn(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	host := models.HostInfo{ID: "h1", OS: "linux", AgentVersion: "v0.6.0", RemoteUpdate: models.RemoteUpdateCapability{Supported: true, OptedIn: false}}
	if got := s.pendingUpdateRequestForHost(ctx, host); got != nil {
		t.Errorf("pendingUpdateRequestForHost = %+v, want nil (not opted in)", got)
	}
}

func TestPendingUpdateRequestForHost_Unsupported(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	host := models.HostInfo{ID: "h1", OS: "windows", AgentVersion: "v0.6.0", RemoteUpdate: models.RemoteUpdateCapability{Supported: false, OptedIn: false}}
	if got := s.pendingUpdateRequestForHost(ctx, host); got != nil {
		t.Errorf("pendingUpdateRequestForHost = %+v, want nil (unsupported)", got)
	}
}

func TestPendingUpdateRequestForHost_OldAgentVersionRejected(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	updateTestHost(t, store, "h1", capableOptedIn(), "v0.5.0")
	if _, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"}); err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}

	host := models.HostInfo{ID: "h1", OS: "linux", AgentVersion: "v0.5.0", RemoteUpdate: capableOptedIn()}
	if got := s.pendingUpdateRequestForHost(ctx, host); got != nil {
		t.Errorf("pendingUpdateRequestForHost = %+v, want nil (agent version < v0.6.0)", got)
	}
}

// TestPendingUpdateRequestForHost_QueuedJobFailsNotEnabled is a
// regression test for SPEC-v0.6 §2's "옵트인하지 않은 에이전트는
// failed(사유 not_enabled)로 표시" requirement: a queued job for a host
// that hasn't opted in must actively transition to failed/not_enabled
// once observed via a report, not remain queued forever (there is no
// other scheduler path that would ever revisit a queued-but-ineligible
// job — checkUpdateJobTimeouts only scans in_progress jobs).
func TestPendingUpdateRequestForHost_QueuedJobFailsNotEnabled(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	updateTestHost(t, store, "h1", models.RemoteUpdateCapability{Supported: true, OptedIn: false}, "v0.6.0")
	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}
	jobID := batch.Jobs[0].ID

	host := models.HostInfo{ID: "h1", OS: "linux", AgentVersion: "v0.6.0", RemoteUpdate: models.RemoteUpdateCapability{Supported: true, OptedIn: false}}
	if got := s.pendingUpdateRequestForHost(ctx, host); got != nil {
		t.Errorf("pendingUpdateRequestForHost = %+v, want nil (not opted in)", got)
	}

	job, err := store.GetUpdateJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if job.State != models.UpdateJobFailed {
		t.Errorf("job.State = %q, want failed", job.State)
	}
	if job.Reason != models.UpdateReasonNotEnabled {
		t.Errorf("job.Reason = %q, want not_enabled", job.Reason)
	}
}

// TestPendingUpdateRequestForHost_QueuedJobFailsUnsupported mirrors
// TestPendingUpdateRequestForHost_QueuedJobFailsNotEnabled for the
// unsupported (non-Linux/no-systemd) case.
func TestPendingUpdateRequestForHost_QueuedJobFailsUnsupported(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	updateTestHost(t, store, "h1", models.RemoteUpdateCapability{Supported: false, OptedIn: false}, "v0.6.0")
	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}
	jobID := batch.Jobs[0].ID

	host := models.HostInfo{ID: "h1", OS: "windows", AgentVersion: "v0.6.0", RemoteUpdate: models.RemoteUpdateCapability{Supported: false, OptedIn: false}}
	if got := s.pendingUpdateRequestForHost(ctx, host); got != nil {
		t.Errorf("pendingUpdateRequestForHost = %+v, want nil (unsupported)", got)
	}

	job, err := store.GetUpdateJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if job.State != models.UpdateJobFailed {
		t.Errorf("job.State = %q, want failed", job.State)
	}
	if job.Reason != models.UpdateReasonUnsupported {
		t.Errorf("job.Reason = %q, want unsupported", job.Reason)
	}
}

// TestPendingUpdateRequestForHost_NoJobIneligibleHostStaysNil verifies
// an ineligible host with no job at all (the original NotOptedIn/
// Unsupported test scenario, no createUpdateBatch call) still simply
// returns nil without erroring — failIneligibleUpdateJob must only ever
// be reached when a real queued job exists.
func TestPendingUpdateRequestForHost_NoJobIneligibleHostStaysNil(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	host := models.HostInfo{ID: "h1", OS: "linux", AgentVersion: "v0.6.0", RemoteUpdate: models.RemoteUpdateCapability{Supported: true, OptedIn: false}}
	if got := s.pendingUpdateRequestForHost(ctx, host); got != nil {
		t.Errorf("pendingUpdateRequestForHost = %+v, want nil", got)
	}
}

func TestPendingUpdateRequestForHost_ReturnsQueuedJob(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")
	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}
	s.setBatchMaxParallel(batch.BatchID, 3)

	host := models.HostInfo{ID: "h1", OS: "linux", AgentVersion: "v0.6.0", RemoteUpdate: capableOptedIn()}
	req := s.pendingUpdateRequestForHost(ctx, host)
	if req == nil {
		t.Fatal("pendingUpdateRequestForHost = nil, want a request")
	}
	if req.JobID != batch.Jobs[0].ID || req.Target != "v0.6.1" {
		t.Errorf("request = %+v, want job_id=%d target=v0.6.1", req, batch.Jobs[0].ID)
	}
}

func TestPendingUpdateRequestForHost_ParallelLimitBlocksExtraJobs(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()

	for _, id := range []string{"h1", "h2", "h3"} {
		updateTestHost(t, store, id, capableOptedIn(), "v0.6.0")
	}
	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1", "h2", "h3"}, Target: "v0.6.1", MaxParallel: 2})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}
	s.setBatchMaxParallel(batch.BatchID, 2)

	// Ack the first two jobs to in_progress, saturating the limit.
	for i := 0; i < 2; i++ {
		s.ackUpdateJob(ctx, batch.Jobs[i])
	}

	host3 := models.HostInfo{ID: "h3", OS: "linux", AgentVersion: "v0.6.0", RemoteUpdate: capableOptedIn()}
	if got := s.pendingUpdateRequestForHost(ctx, host3); got != nil {
		t.Errorf("pendingUpdateRequestForHost(h3) = %+v, want nil (parallel limit saturated)", got)
	}

	// The already in_progress jobs should still be re-returned
	// (idempotent re-delivery), not blocked by the limit against
	// themselves.
	host1 := models.HostInfo{ID: "h1", OS: "linux", AgentVersion: "v0.6.0", RemoteUpdate: capableOptedIn()}
	if got := s.pendingUpdateRequestForHost(ctx, host1); got == nil {
		t.Error("pendingUpdateRequestForHost(h1) = nil, want re-delivery of its own in_progress job")
	}
}

func TestAckUpdateJob_TransitionsQueuedToInProgress(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])

	got, err := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.State != models.UpdateJobInProgress {
		t.Errorf("state = %q, want in_progress", got.State)
	}
}

func TestHandleAgentUpdateStatus_SucceededWithMatchingVersion(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])

	s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{JobID: batch.Jobs[0].ID, State: models.UpdateJobSucceeded, Version: "v0.6.1"})

	got, err := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.State != models.UpdateJobSucceeded {
		t.Errorf("state = %q, want succeeded", got.State)
	}
}

func TestHandleAgentUpdateStatus_SucceededWithVersionMismatchBecomesFailed(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])

	// Agent claims success but the version doesn't match the requested
	// target — must not be trusted blindly.
	s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{JobID: batch.Jobs[0].ID, State: models.UpdateJobSucceeded, Version: "v0.6.0"})

	got, err := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.State != models.UpdateJobFailed {
		t.Errorf("state = %q, want failed (version mismatch)", got.State)
	}
	if got.Reason != models.UpdateReasonVerifyFailed {
		t.Errorf("reason = %q, want %q", got.Reason, models.UpdateReasonVerifyFailed)
	}
}

func TestHandleAgentUpdateStatus_LatestTargetAcceptsAnyVersion(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "latest"})
	s.ackUpdateJob(ctx, batch.Jobs[0])

	s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{JobID: batch.Jobs[0].ID, State: models.UpdateJobSucceeded, Version: "v0.9.9"})

	got, err := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.State != models.UpdateJobSucceeded {
		t.Errorf("state = %q, want succeeded (target=latest accepts any reported version)", got.State)
	}
}

func TestHandleAgentUpdateStatus_Failed(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])

	s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{
		JobID: batch.Jobs[0].ID, State: models.UpdateJobFailed,
		ErrorCode: models.UpdateReasonChecksumMismatch, Error: "sha256 mismatch",
	})

	got, err := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.State != models.UpdateJobFailed {
		t.Errorf("state = %q, want failed", got.State)
	}
	if got.Reason != models.UpdateReasonChecksumMismatch {
		t.Errorf("reason = %q, want %q", got.Reason, models.UpdateReasonChecksumMismatch)
	}
}

func TestHandleAgentUpdateStatus_AlreadyUpToDateBecomesSucceeded(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "latest"})
	s.ackUpdateJob(ctx, batch.Jobs[0])

	s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{
		JobID: batch.Jobs[0].ID, State: models.UpdateJobFailed,
		ErrorCode: models.UpdateReasonAlreadyUpToDate,
	})

	got, err := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.State != models.UpdateJobSucceeded {
		t.Errorf("state = %q, want succeeded (already_up_to_date is completion, not failure)", got.State)
	}
}

func TestHandleAgentUpdateStatus_HostMismatchIgnored(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])

	// A different host reports on h1's job — must be ignored.
	s.handleAgentUpdateStatus(ctx, "h2", &models.AgentUpdateStatus{JobID: batch.Jobs[0].ID, State: models.UpdateJobSucceeded, Version: "v0.6.1"})

	got, err := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.State != models.UpdateJobInProgress {
		t.Errorf("state = %q, want unchanged in_progress after a host-mismatched report", got.State)
	}
}

func TestHandleAgentUpdateStatus_TerminalJobNotOverwritten(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])
	s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{JobID: batch.Jobs[0].ID, State: models.UpdateJobSucceeded, Version: "v0.6.1"})

	// A duplicate/late report must not flip a terminal job back.
	s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{JobID: batch.Jobs[0].ID, State: models.UpdateJobFailed, ErrorCode: models.UpdateReasonUnknown})

	got, err := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.State != models.UpdateJobSucceeded {
		t.Errorf("state = %q, want succeeded to remain terminal", got.State)
	}
}

func TestCheckUpdateJobTimeouts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = func() time.Time { return now }
	s := newTestServer(t, opts, store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])

	// The fake store's UpdateUpdateJob stamps UpdatedAt from the real
	// wall clock (per the Store interface's contract — it takes no
	// injected `now`), so pin it directly to a known instant here
	// rather than relying on test execution speed for determinism.
	job, err := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	job.UpdatedAt = now.Unix()
	store.forceSetUpdateJob(job)

	// Not yet timed out at 10 minutes.
	s.checkUpdateJobTimeouts(ctx, now.Add(10*time.Minute))
	got, _ := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if got.State != models.UpdateJobInProgress {
		t.Fatalf("state = %q after 10m, want still in_progress", got.State)
	}

	// Timed out at 16 minutes. Re-pin UpdatedAt: the "not yet timed
	// out" check above ran checkUpdateJobTimeouts, which — for a job
	// that didn't time out — leaves the row untouched (no
	// UpdateUpdateJob call), so UpdatedAt is still now.Unix() here;
	// this second pin is defensive documentation of that invariant,
	// not a workaround for a real problem.
	job.UpdatedAt = now.Unix()
	store.forceSetUpdateJob(job)
	s.checkUpdateJobTimeouts(ctx, now.Add(16*time.Minute))
	got, _ = store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if got.State != models.UpdateJobFailed {
		t.Fatalf("state = %q after 16m, want failed (timeout)", got.State)
	}
	if got.Reason != models.UpdateReasonTimeout {
		t.Errorf("reason = %q, want %q", got.Reason, models.UpdateReasonTimeout)
	}
}

func TestRetryUpdateJob_CreatesNewQueuedJob(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])
	s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{
		JobID: batch.Jobs[0].ID, State: models.UpdateJobFailed, ErrorCode: models.UpdateReasonDownloadFailed,
	})

	next, err := s.retryUpdateJob(ctx, batch.Jobs[0].ID)
	if err != nil {
		t.Fatalf("retryUpdateJob: %v", err)
	}
	if next.State != models.UpdateJobQueued {
		t.Errorf("state = %q, want queued", next.State)
	}
	if next.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", next.Attempt)
	}
	if next.HostID != "h1" || next.Target != "v0.6.1" {
		t.Errorf("new job = %+v, want host_id=h1 target=v0.6.1", next)
	}
}

func TestRetryUpdateJob_RefusesNotEnabledAndUnsupported(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	for _, reason := range []models.UpdateJobReason{models.UpdateReasonNotEnabled, models.UpdateReasonUnsupported} {
		batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
		s.ackUpdateJob(ctx, batch.Jobs[0])
		s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{JobID: batch.Jobs[0].ID, State: models.UpdateJobFailed, ErrorCode: reason})

		if _, err := s.retryUpdateJob(ctx, batch.Jobs[0].ID); err == nil {
			t.Errorf("retryUpdateJob with reason=%q: want an error, got none", reason)
		}
	}
}

func TestRetryUpdateJob_RefusesNonFailedJob(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})

	if _, err := s.retryUpdateJob(ctx, batch.Jobs[0].ID); err == nil {
		t.Error("retryUpdateJob on a queued (non-failed) job: want an error")
	}
}

func TestCancelUpdateBatch_OnlyCancelsQueuedJobs(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	for _, id := range []string{"h1", "h2"} {
		updateTestHost(t, store, id, capableOptedIn(), "v0.6.0")
	}

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1", "h2"}, Target: "v0.6.1"})
	// h1's job progresses to in_progress; h2 stays queued.
	s.ackUpdateJob(ctx, batch.Jobs[0])

	canceled, err := s.cancelUpdateBatch(ctx, batch.BatchID)
	if err != nil {
		t.Fatalf("cancelUpdateBatch: %v", err)
	}
	if canceled != 1 {
		t.Fatalf("canceled = %d, want 1 (only the queued job)", canceled)
	}

	inProgressJob, _ := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if inProgressJob.State != models.UpdateJobInProgress {
		t.Errorf("in_progress job state = %q, want unchanged in_progress", inProgressJob.State)
	}
	queuedJob, _ := store.GetUpdateJob(ctx, batch.Jobs[1].ID)
	if queuedJob.State != models.UpdateJobFailed {
		t.Errorf("previously-queued job state = %q, want failed (canceled)", queuedJob.State)
	}
}

// --- HTTP route tests ---

func TestHandleCreateUpdateBatch_ViaHTTP(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	body := []byte(`{"host_ids":["h1"],"target":"v0.6.1"}`)
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agents/updates", "203.0.113.1:1234", testUIToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.UpdateBatch](t, rec.Body)
	if len(got.Jobs) != 1 {
		t.Fatalf("len(Jobs) = %d, want 1", len(got.Jobs))
	}
}

func TestHandleCreateUpdateBatch_EmptyHostIDsRejected(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	body := []byte(`{"host_ids":[],"target":"v0.6.1"}`)
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agents/updates", "203.0.113.1:1234", testUIToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleListUpdateJobs_ViaHTTP(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")
	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/agents/updates?batch="+batch.BatchID, "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON[map[string][]models.UpdateJob](t, rec.Body)
	if len(got["jobs"]) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(got["jobs"]))
	}
}

func TestHandleRetryUpdateJob_ViaHTTP(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")
	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])
	s.handleAgentUpdateStatus(ctx, "h1", &models.AgentUpdateStatus{JobID: batch.Jobs[0].ID, State: models.UpdateJobFailed, ErrorCode: models.UpdateReasonDownloadFailed})

	path := "/api/v1/agents/updates/" + itoa64(batch.Jobs[0].ID) + "/retry"
	rec := doRequest(t, s.Handler(), http.MethodPost, path, "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.UpdateJob](t, rec.Body)
	if got.State != models.UpdateJobQueued {
		t.Errorf("state = %q, want queued", got.State)
	}
}

func TestHandleCancelUpdateBatch_ViaHTTP(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")
	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agents/updates/"+batch.BatchID+"/cancel", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON[map[string]int](t, rec.Body)
	if got["canceled"] != 1 {
		t.Errorf("canceled = %d, want 1", got["canceled"])
	}
}

// --- Ingest integration tests ---

func TestApplyUpdateIngest_DeliversRequestAndAcks(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})

	report := models.AgentReport{Host: models.HostInfo{ID: "h1", OS: "linux", AgentVersion: "v0.6.0", RemoteUpdate: capableOptedIn()}}
	req := s.applyUpdateIngest(ctx, report)
	if req == nil {
		t.Fatal("applyUpdateIngest returned nil, want an UpdateRequest")
	}
	if req.JobID != batch.Jobs[0].ID {
		t.Errorf("JobID = %d, want %d", req.JobID, batch.Jobs[0].ID)
	}

	got, _ := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if got.State != models.UpdateJobInProgress {
		t.Errorf("job state after applyUpdateIngest = %q, want in_progress (acked)", got.State)
	}
}

func TestApplyUpdateIngest_AppliesReportedStatus(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	ctx := context.Background()
	updateTestHost(t, store, "h1", capableOptedIn(), "v0.6.0")

	batch, _ := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	s.ackUpdateJob(ctx, batch.Jobs[0])

	report := models.AgentReport{
		Host:         models.HostInfo{ID: "h1", OS: "linux", AgentVersion: "v0.6.1", RemoteUpdate: capableOptedIn()},
		UpdateStatus: &models.AgentUpdateStatus{JobID: batch.Jobs[0].ID, State: models.UpdateJobSucceeded, Version: "v0.6.1"},
	}
	s.applyUpdateIngest(ctx, report)

	got, _ := store.GetUpdateJob(ctx, batch.Jobs[0].ID)
	if got.State != models.UpdateJobSucceeded {
		t.Errorf("state = %q, want succeeded", got.State)
	}
}

func TestHandleIngest_EmbedsUpdateRequestInResponse(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	s := newTestServer(t, opts, store)
	ctx := context.Background()

	// A host must already exist for createUpdateBatch to accept it;
	// upsert it first via a real ingest so the flow matches production
	// (agent reports before any job exists).
	body0 := []byte(`{"host":{"id":"h1","hostname":"h1","os":"linux","agent_version":"v0.6.0","remote_update":{"supported":true,"opted_in":true}},"samples":[]}`)
	rec0 := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body0)
	if rec0.Code != http.StatusOK {
		t.Fatalf("initial ingest status = %d, want 200 (body=%s)", rec0.Code, rec0.Body.String())
	}

	batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{"h1"}, Target: "v0.6.1"})
	if err != nil {
		t.Fatalf("createUpdateBatch: %v", err)
	}

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body0)
	if rec.Code != http.StatusOK {
		t.Fatalf("second ingest status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.IngestResponse](t, rec.Body)
	if got.UpdateRequest == nil {
		t.Fatal("IngestResponse.UpdateRequest = nil, want the queued job")
	}
	if got.UpdateRequest.JobID != batch.Jobs[0].ID {
		t.Errorf("UpdateRequest.JobID = %d, want %d", got.UpdateRequest.JobID, batch.Jobs[0].ID)
	}
}

// itoa64 is a tiny local helper to avoid importing strconv just for one
// call site in a few HTTP-path-building tests above. Named itoa64 (not
// itoa) to avoid colliding with networkroutes_test.go's own itoa(int)
// helper in this same test package.
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
