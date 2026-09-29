package hub

import (
	"context"
	"time"
)

// alertNotifyTimeout bounds how long a single legacy webhook Notify call
// (PUT/POST /api/v1/settings/alerts and its /test endpoint, see admin.go)
// may run.
const alertNotifyTimeout = 10 * time.Second

// afterIngest is called after successfully ingesting at least one valid
// sample for host. Since SPEC-v0.5 §B, egress/CPU/memory/disk/load1/
// host_down alerting is entirely owned by the alerting engine
// (Options.Alerting): this just re-evaluates every enabled rule scoped
// to this host so a threshold crossed by the sample just ingested is
// noticed immediately rather than waiting for the next 30s scheduler
// tick (see scheduler.go's runAlertEvalLoop). A nil Alerting (no engine
// wired, e.g. older tests in this package) makes this a no-op — it
// never fails the ingest request either way, matching the pre-v0.5.0
// behavior of never letting alert delivery affect the ingest response.
func (s *Server) afterIngest(hostID string, now time.Time) {
	if s.opts.Alerting == nil {
		return
	}

	ctx := context.Background()
	snapshot, err := s.hostSnapshotFor(ctx, now, hostID)
	if err != nil {
		s.logger.Error("alerts: build host snapshot failed", "host_id", hostID, "error", err)
		return
	}
	if len(snapshot) == 0 {
		return
	}
	if err := s.opts.Alerting.Evaluate(ctx, now, snapshot); err != nil {
		s.logger.Error("alerts: evaluate failed", "host_id", hostID, "error", err)
	}
}
