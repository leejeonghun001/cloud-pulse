package hub

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/agent"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/selfupdate"
)

// TestRemoteUpdateRequestResultCycle exercises the cross-package hand-off
// without a real service: the hub delivers a request, the agent writes it to
// injected directories, the platform helper consumes it, the agent reads the
// result, and the hub records success. HostGOOS makes every platform case run
// deterministically on every CI worker.
func TestRemoteUpdateRequestResultCycle(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		platform := platform
		t.Run(platform, func(t *testing.T) {
			ctx := context.Background()
			store := newFakeStore()
			s := newTestServer(t, testOptions(), store)
			host := models.HostInfo{
				ID: "cycle-" + platform, OS: platform, AgentVersion: "v0.7.0",
				RemoteUpdate: models.RemoteUpdateCapability{Platform: platform, Supported: true, OptedIn: true},
			}
			if err := store.UpsertHost(ctx, host, 1); err != nil {
				t.Fatalf("upsert host: %v", err)
			}
			batch, err := s.createUpdateBatch(ctx, models.UpdateBatchCreate{HostIDs: []string{host.ID}, Target: "v0.7.0"})
			if err != nil {
				t.Fatalf("create update batch: %v", err)
			}
			s.setBatchMaxParallel(batch.BatchID, batch.MaxParallel)

			req := s.applyUpdateIngest(ctx, models.AgentReport{Host: host})
			if req == nil {
				t.Fatal("hub delivered no update request")
			}
			requestDir := filepath.Join(t.TempDir(), "request")
			resultDir := filepath.Join(t.TempDir(), "result")
			if err := agent.WriteRemoteUpdateRequest(requestDir, req); err != nil {
				t.Fatalf("agent write request: %v", err)
			}
			if _, err := selfupdate.RunFromRequest(ctx, selfupdate.FromRequestOptions{
				HostGOOS: platform, RequestPath: filepath.Join(requestDir, agent.RequestFileName),
				ResultDir: resultDir, Binary: "cloud-pulse-agent", Current: "v0.7.0",
			}); err != nil {
				t.Fatalf("platform helper RunFromRequest: %v", err)
			}
			status := agent.ReadRemoteUpdateStatus(resultDir)
			if status == nil || status.State != models.UpdateJobSucceeded {
				t.Fatalf("agent status = %+v, want succeeded", status)
			}
			s.applyUpdateIngest(ctx, models.AgentReport{Host: host, UpdateStatus: status})
			job, err := store.GetUpdateJob(ctx, req.JobID)
			if err != nil {
				t.Fatalf("get update job: %v", err)
			}
			if job.State != models.UpdateJobSucceeded {
				t.Errorf("job state = %q, want succeeded (job=%+v)", job.State, job)
			}
		})
	}
}
