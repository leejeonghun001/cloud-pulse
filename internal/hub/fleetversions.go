package hub

import (
	"net/http"
	"sort"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// fleetVersionHostInput is the per-host input BuildFleetVersionSummary
// aggregates, deliberately narrower than models.HostInfo so the pure
// aggregation function's table tests don't need to construct a full
// HostRecord — just the handful of fields the computation actually
// reads. Callers (handleFleetVersions) adapt a models.HostRecord slice
// into this shape.
type fleetVersionHostInput struct {
	HostID        string
	Hostname      string
	Version       string
	OS            string
	Capability    models.RemoteUpdateCapability
	OutdatedSince int64
}

// BuildFleetVersionSummary computes GET /api/v1/fleet/versions' response
// from hosts and the hub's currently known latest release tag (may be
// ""). It is a pure function of its inputs: no store/context access, no
// wall-clock reads (the "outdated" flag is a point-in-time comparison
// against latestVersion, not a duration — see OutdatedSince's doc
// comment on why a "how long" figure is threaded through rather than
// computed here).
//
// A host is "outdated" only when both its own Version and latestVersion
// parse as release tags and the host's version is strictly older
// (version.IsNewer(latestVersion, hostVersion)); an empty/unparseable
// host version or an unknown latestVersion never counts as outdated —
// there is nothing to compare against, so such a host is counted in
// UnknownCount instead. This mirrors decideAgentUpdate's own "both must
// parse" rule (updateengine.go) rather than inventing a second policy.
//
// hostOutdatedSince supplies each outdated host's OutdatedSince value
// (unix seconds, 0 if unknown) — this function has no persisted
// "since" store of its own (SPEC-v0.8 §3-1 doesn't add one; the rollout
// stage tracks this in-memory, see fleetVersionTracker in
// fleetversionstracker.go), so the caller passes whatever it already
// knows for hostID, keyed by host ID. A host missing from the map (or
// not currently outdated) gets OutdatedSince 0.
func BuildFleetVersionSummary(hosts []fleetVersionHostInput, latestVersion string, hostOutdatedSince map[string]int64) models.FleetVersionSummary {
	summary := models.FleetVersionSummary{
		LatestVersion: latestVersion,
		TotalHosts:    len(hosts),
	}

	type bucketKey struct {
		os      string
		version string
	}
	bucketCounts := make(map[bucketKey]int)
	var bucketOrder []bucketKey

	rows := make([]models.FleetVersionHost, 0, len(hosts))
	for _, h := range hosts {
		row := models.FleetVersionHost{
			HostID:        h.HostID,
			Hostname:      h.Hostname,
			Version:       h.Version,
			OS:            h.OS,
			Capability:    classifyRemoteUpdateCapability(h.Capability),
			ManualCommand: agentUpdateCommand(remoteUpdateSelfUpdateCapable(h.Version), normalizeGOOS(h.OS)),
		}

		switch classifyVersionState(h.Version, latestVersion) {
		case versionStateUpToDate:
			summary.UpToDateCount++
		case versionStateOutdated:
			row.Outdated = true
			row.OutdatedSince = hostOutdatedSince[h.HostID]
			summary.OutdatedCount++
		default:
			summary.UnknownCount++
		}

		switch row.Capability {
		case models.RemoteUpdateClassCapable:
			summary.CapableCount++
		case models.RemoteUpdateClassNotOptedIn:
			summary.NotOptedInCount++
		default:
			summary.UnsupportedCount++
		}

		rows = append(rows, row)

		key := bucketKey{os: displayOS(h.OS), version: displayVersion(h.Version)}
		if _, seen := bucketCounts[key]; !seen {
			bucketOrder = append(bucketOrder, key)
		}
		bucketCounts[key]++
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Hostname != rows[j].Hostname {
			return rows[i].Hostname < rows[j].Hostname
		}
		return rows[i].HostID < rows[j].HostID
	})
	summary.Hosts = rows

	buckets := make([]models.FleetVersionOSBucket, 0, len(bucketOrder))
	for _, key := range bucketOrder {
		buckets = append(buckets, models.FleetVersionOSBucket{OS: key.os, Version: key.version, Count: bucketCounts[key]})
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		if buckets[i].OS != buckets[j].OS {
			return buckets[i].OS < buckets[j].OS
		}
		return compareVersionsForDisplay(buckets[i].Version, buckets[j].Version)
	})
	summary.Distribution = buckets

	return summary
}

// versionState classifies a host's own version against the fleet's
// latest known release.
type versionState int

const (
	versionStateUnknown versionState = iota
	versionStateUpToDate
	versionStateOutdated
)

// classifyVersionState implements the "both must parse" comparison
// documented on BuildFleetVersionSummary.
func classifyVersionState(hostVersion, latestVersion string) versionState {
	if _, ok := version.Parse(hostVersion); !ok {
		return versionStateUnknown
	}
	if _, ok := version.Parse(latestVersion); !ok {
		return versionStateUnknown
	}
	if version.IsNewer(latestVersion, hostVersion) {
		return versionStateOutdated
	}
	return versionStateUpToDate
}

// classifyRemoteUpdateCapability buckets a models.RemoteUpdateCapability
// into the three coarse classes the rollout dashboard displays,
// matching remoteUpdateCapable's own supported/opted-in rules
// (updateengine.go) rather than re-deriving a second policy: Supported
// == false is always "unsupported" regardless of Reason (an old agent
// that never reports Reason at all still isn't remote-update capable);
// Supported == true with OptedIn == false is "not_opted_in"; both true
// is "capable".
func classifyRemoteUpdateCapability(cap models.RemoteUpdateCapability) models.RemoteUpdateCapabilityClass {
	if !cap.Supported {
		return models.RemoteUpdateClassUnsupported
	}
	if !cap.OptedIn {
		return models.RemoteUpdateClassNotOptedIn
	}
	return models.RemoteUpdateClassCapable
}

// remoteUpdateSelfUpdateCapable mirrors decideAgentUpdate's SelfUpdate
// computation (updateengine.go) for the manual-command text: an
// unparseable version (dev build) is treated as self-update capable so
// the manual command shown is the modern `... update` form rather than
// the legacy installer one-liner, matching pages/updates.js's existing
// manualUpdateCommand fallback behavior for a host with no reported
// update info at all.
func remoteUpdateSelfUpdateCapable(agentVersion string) bool {
	if _, ok := version.Parse(agentVersion); !ok {
		return true
	}
	return version.AtLeastIncludingPrerelease(agentVersion, version.SelfUpdateSince)
}

// displayOS normalizes an empty/unrecognized OS value to "unknown" for
// grouping/display purposes (distinct from normalizeGOOS's "default to
// linux" behavior, which is used only for choosing manual-command
// text — grouping an OS-less host into the "linux" bucket would
// misrepresent the fleet's actual OS distribution).
func displayOS(osName string) string {
	switch osName {
	case "linux", "darwin", "windows", "freebsd":
		return osName
	default:
		return "unknown"
	}
}

// displayVersion normalizes an empty version to "unknown" for grouping/
// display purposes; a non-empty-but-unparseable version (e.g. "dev",
// or a git-describe dev build string) is shown as-is so a fleet running
// local dev builds still gets a meaningful (if unsortable-by-semver)
// bucket label instead of being lumped in with truly-unreported hosts.
func displayVersion(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// compareVersionsForDisplay orders two displayVersion outputs for the
// distribution chart: parseable release tags sort by descending semver
// (newest first), and every unparseable label ("unknown", "dev",
// git-describe strings) sorts after every parseable one, then
// lexically among themselves for a deterministic order.
func compareVersionsForDisplay(a, b string) bool {
	aSem, aOK := version.Parse(a)
	bSem, bOK := version.Parse(b)
	switch {
	case aOK && bOK:
		return version.Compare(aSem, bSem) > 0
	case aOK && !bOK:
		return true
	case !aOK && bOK:
		return false
	default:
		return a < b
	}
}

// registerFleetVersionsRoute registers GET /api/v1/fleet/versions
// (SPEC-v0.8 §3-1). Read-only, so it uses the same requireUser auth
// tier as GET /api/v1/hosts rather than requireAdmin — see
// DECISIONS_LOG.md's rollout-stage entry for the rationale (every
// other read-only fleet-wide GET endpoint already uses requireUser;
// this endpoint discloses nothing beyond what GET /api/v1/hosts already
// exposes to any signed-in user, just aggregated differently).
func (s *Server) registerFleetVersionsRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/fleet/versions", s.requireUser(s.handleFleetVersions))
}

// handleFleetVersions responds with the fleet-wide version/OS
// distribution and per-host breakdown: GET /api/v1/fleet/versions (any
// authenticated user — read-only, same auth tier as GET /api/v1/hosts).
func (s *Server) handleFleetVersions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	records, err := s.store.ListHosts(ctx)
	if err != nil {
		s.logger.Error("fleet versions: list hosts failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	inputs := make([]fleetVersionHostInput, 0, len(records))
	for _, rec := range records {
		inputs = append(inputs, fleetVersionHostInput{
			HostID:     rec.Info.ID,
			Hostname:   rec.Info.Hostname,
			Version:    rec.Info.AgentVersion,
			OS:         rec.Info.OS,
			Capability: rec.Info.RemoteUpdate,
		})
	}

	st := s.updateStatusSnapshot()
	since := s.outdatedSinceSnapshot(inputs, st.latestVersion, s.opts.now())

	summary := BuildFleetVersionSummary(inputs, st.latestVersion, since)
	writeJSON(w, http.StatusOK, summary)
}

// outdatedSinceSnapshot returns a hostID -> OutdatedSince (unix seconds)
// map for every currently-outdated host in hosts, updating and reading
// through s.fleetOutdatedTracker (see fleetversionstracker.go) so the
// "since" timestamp survives repeated calls within this process's
// lifetime — the first time a host is observed outdated, "since" is
// recorded as now; the timestamp is preserved on every subsequent call
// while the host remains outdated, and cleared once it catches up
// (a later request that observes it as no longer outdated deletes the
// entry, so a possible future regression starts a fresh "since" window
// rather than reporting a stale one from months ago).
func (s *Server) outdatedSinceSnapshot(hosts []fleetVersionHostInput, latestVersion string, now time.Time) map[string]int64 {
	outdatedNow := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		if classifyVersionState(h.Version, latestVersion) == versionStateOutdated {
			outdatedNow[h.HostID] = true
		}
	}
	return s.fleetOutdatedTracker.update(outdatedNow, now)
}
