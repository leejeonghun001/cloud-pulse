package hub

import (
	"sync"
	"time"
)

// fleetVersionTracker records, in-memory only, the unix-seconds time
// each currently-outdated host was first observed outdated (SPEC-v0.8
// §3-1's "behind-for" / OutdatedSince display). There is no persisted
// table for this (0007_v08.sql's rollout section only adds an index —
// see the migration's own comment on why: version/OS aggregation is
// computed at read time from hosts + update_jobs, and "since" is best-
// effort display metadata, not data that needs to survive a hub
// restart with full fidelity). A restart simply restarts every
// currently-outdated host's "since" clock at the moment it's first
// observed again post-restart — an acceptable degradation, exactly the
// same tradeoff batchMaxParallel already makes for in-flight batch
// parallelism (see updateengine.go's doc comment on that field).
type fleetVersionTracker struct {
	mu    sync.Mutex
	since map[string]int64 // hostID -> unix seconds first observed outdated
}

// newFleetVersionTracker returns an empty tracker.
func newFleetVersionTracker() *fleetVersionTracker {
	return &fleetVersionTracker{since: make(map[string]int64)}
}

// update reconciles the tracker against outdatedNow (the set of host
// IDs currently classified outdated) as of now, and returns a copy of
// the resulting hostID -> since map. A host present in outdatedNow but
// not yet tracked is recorded with since = now.Unix(); a host absent
// from outdatedNow is removed (see the type doc comment on why a
// regression starts a fresh window rather than reporting a stale one).
func (t *fleetVersionTracker) update(outdatedNow map[string]bool, now time.Time) map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	for hostID := range outdatedNow {
		if _, tracked := t.since[hostID]; !tracked {
			t.since[hostID] = now.Unix()
		}
	}
	for hostID := range t.since {
		if !outdatedNow[hostID] {
			delete(t.since, hostID)
		}
	}

	out := make(map[string]int64, len(t.since))
	for hostID, since := range t.since {
		out[hostID] = since
	}
	return out
}
