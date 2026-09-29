package hub

import (
	"context"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// LatestResolver resolves the latest published cloud-pulse release tag.
// Implementations live in internal/selfupdate (selfupdate.Source); a nil
// LatestResolver in Options.UpdateSource disables the background update
// check entirely.
type LatestResolver interface {
	// Latest returns the latest release tag (e.g. "v0.3.1").
	Latest(ctx context.Context) (tag string, err error)
}

// firstUpdateCheckDelay is how long RunBackground waits after starting
// before performing the first release check.
const firstUpdateCheckDelay = 30 * time.Second

// updateCheckInterval is how often RunBackground re-checks for a new
// release after the first check.
const updateCheckInterval = 24 * time.Hour

// updateCheckTimeout bounds a single release-check HTTP round trip.
const updateCheckTimeout = 15 * time.Second

// releaseURLBase is the GitHub releases page base used to build
// VersionInfo.ReleaseURL.
const releaseURLBase = "https://github.com/leejeonghun001/cloud-pulse/releases/tag/"

// updateStatus holds the in-memory result of the most recent (and most
// recently successful) background release check. All access is guarded
// by Server.updateMu.
type updateStatus struct {
	// latestVersion is the latest known release tag, or "" if no check
	// has ever succeeded.
	latestVersion string
	// checkedAt is the unix-seconds time of the last check attempt
	// (successful or not), or 0 if none has run yet.
	checkedAt int64
	// checkError is the most recent failure's message; "" if the last
	// check succeeded or none has run yet.
	checkError string
	// consecutiveFailures counts checks failed in a row since the last
	// success, used to log a warning only once per failure streak
	// rather than on every retry.
	consecutiveFailures int
}

// errInvalidLatestTag is recorded as the check error when a
// LatestResolver returns a tag that fails version.ValidTag.
type errInvalidLatestTag struct{}

func (errInvalidLatestTag) Error() string { return "resolved tag failed validation" }

// recordUpdateCheck stores the result of a single background release
// check.
func (s *Server) recordUpdateCheck(now time.Time, tag string, err error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	s.updateStatusV.checkedAt = now.Unix()
	if err != nil {
		s.updateStatusV.checkError = err.Error()
		s.updateStatusV.consecutiveFailures++
		return
	}
	s.updateStatusV.latestVersion = tag
	s.updateStatusV.checkError = ""
	s.updateStatusV.consecutiveFailures = 0
}

// updateStatusSnapshot returns a copy of the current update status.
func (s *Server) updateStatusSnapshot() updateStatus {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	return s.updateStatusV
}

// runUpdateCheckLoop runs the background release-check loop until ctx is
// canceled: first check after firstDelay, then every interval. A nil
// resolver (Options.UpdateSource) makes this a no-op — the loop returns
// immediately without starting a timer.
func (s *Server) runUpdateCheckLoop(ctx context.Context, firstDelay, interval time.Duration) {
	if s.opts.UpdateSource == nil {
		return
	}

	timer := time.NewTimer(firstDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.checkForUpdate(ctx)
			timer.Reset(interval)
		}
	}
}

// checkForUpdate performs a single release check against
// s.opts.UpdateSource, bounded by updateCheckTimeout, and records the
// result. A failure logs a warning only on the first failure of a new
// streak (tracked via consecutiveFailures), so a prolonged GitHub outage
// doesn't produce a warning on every subsequent retry.
func (s *Server) checkForUpdate(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()

	tag, err := s.opts.UpdateSource.Latest(checkCtx)
	now := s.opts.now()

	if err != nil {
		wasFirstFailure := s.updateStatusSnapshot().consecutiveFailures == 0
		s.recordUpdateCheck(now, "", err)
		if wasFirstFailure {
			s.logger.Warn("update check: failed to resolve latest release", "error", err)
		}
		return
	}

	if !version.ValidTag(tag) {
		wasFirstFailure := s.updateStatusSnapshot().consecutiveFailures == 0
		s.recordUpdateCheck(now, "", errInvalidLatestTag{})
		if wasFirstFailure {
			s.logger.Warn("update check: resolver returned an invalid tag", "tag", tag)
		}
		return
	}

	s.recordUpdateCheck(now, tag, nil)
}

// buildVersionInfo assembles the GET /api/v1/version response from build
// metadata (version.Version/Commit/Date), the current update status, and
// whether update checking is enabled at all.
func buildVersionInfo(updateCheckEnabled bool, st updateStatus) models.VersionInfo {
	info := models.VersionInfo{
		Version:            version.Version,
		Commit:             version.Commit,
		Date:               version.Date,
		LatestVersion:      st.latestVersion,
		UpdateCheckEnabled: updateCheckEnabled,
		CheckedAt:          st.checkedAt,
		CheckError:         st.checkError,
		UpdateCommand:      "sudo cloud-pulse-hub update",
	}
	if st.latestVersion != "" {
		info.ReleaseURL = releaseURLBase + st.latestVersion
		info.UpdateAvailable = version.IsNewer(st.latestVersion, version.Version)
	}
	return info
}

// legacyInstallCommand is the install-agent.sh one-liner printed for
// hosts running an agent older than version.SelfUpdateSince on linux.
// It passes `-s -- --reinstall` so the piped script takes the explicit
// Reinstall action (SPEC-v0.3.1 section A) rather than falling into its
// argument-less "auto" behavior: --reinstall requires an existing
// install, re-downloads the latest release, re-renders the systemd
// unit via the new binary's own `systemd-unit print` (see
// internal/systemdunit), and preserves every existing agent.env value
// (tokens, settings) — the correct one-shot migration path for a
// v0.1.x/v0.2.x/v0.3.0 agent onto v0.3.1+, after which
// `sudo cloud-pulse-agent update` alone is sufficient for every future
// upgrade (binary and unit changes both).
const legacyInstallCommand = "curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh | sudo bash -s -- --reinstall"

// decideAgentUpdate computes the AgentUpdate for one host, given its
// reported agent version, the hub's own build version (used as a
// fallback reference when no release check has ever succeeded), the
// latest known release tag (may be ""), and the agent's reported OS
// (used only to pick OS-specific command text).
//
// Reference version precedence: the latest known release if known,
// otherwise the hub's own version if it parses as a release. If neither
// parses, or the agent's own version doesn't parse, no update
// information is available and decideAgentUpdate returns nil.
//
// SelfUpdate (and therefore which Command is produced) is decided solely
// from the agent's own version against version.SelfUpdateSince, per
// SPEC-v0.3 D-U4:
//   - agent >= SelfUpdateSince: "sudo cloud-pulse-agent update" on
//     linux/darwin/freebsd, or "cloud-pulse-agent update (run as
//     Administrator)" on windows.
//   - agent < SelfUpdateSince (legacy): the install-agent.sh one-liner on
//     linux, or a "download from the release page" instruction elsewhere.
func decideAgentUpdate(agentVersion, hubVersion, latestKnown, agentGOOS string) *models.AgentUpdate {
	reference := latestKnown
	if reference == "" {
		reference = hubVersion
	}
	if _, ok := version.Parse(reference); !ok {
		return nil
	}
	if _, ok := version.Parse(agentVersion); !ok {
		return nil
	}

	selfUpdate := !version.IsNewer(version.SelfUpdateSince, agentVersion) // agentVersion >= SelfUpdateSince
	available := version.IsNewer(reference, agentVersion)

	return &models.AgentUpdate{
		Available:  available,
		Latest:     reference,
		SelfUpdate: selfUpdate,
		Command:    agentUpdateCommand(selfUpdate, normalizeGOOS(agentGOOS)),
	}
}

// normalizeGOOS lowercases known OS values and defaults an
// empty/unrecognized value to "linux", the platform the legacy
// install-agent.sh command targets and the overwhelming majority of
// cloud-pulse hosts run.
func normalizeGOOS(goos string) string {
	switch goos {
	case "linux", "darwin", "windows", "freebsd":
		return goos
	default:
		return "linux"
	}
}

// agentUpdateCommand returns the ready-to-run (or ready-to-follow, for
// the legacy non-linux case) update instruction for an agent, given
// whether it's new enough to have the built-in updater and its
// normalized OS.
func agentUpdateCommand(selfUpdate bool, goos string) string {
	if selfUpdate {
		if goos == "windows" {
			return "cloud-pulse-agent update (run as Administrator)"
		}
		return "sudo cloud-pulse-agent update"
	}
	if goos == "linux" {
		return legacyInstallCommand
	}
	return "download the new binary from the release page"
}

// agentUpdateFor computes the AgentUpdate to embed in a HostSummary for
// host, using the hub's current update status and its own build version
// as the fallback reference.
func (s *Server) agentUpdateFor(host models.HostInfo) *models.AgentUpdate {
	st := s.updateStatusSnapshot()
	return decideAgentUpdate(host.AgentVersion, version.Version, st.latestVersion, host.OS)
}
