package agent

import (
	"log/slog"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// updateNoticeThrottle is how often the "a newer cloud-pulse-agent is
// available" notice is allowed to repeat for the same latest version.
const updateNoticeThrottle = 24 * time.Hour

// updateNotifier logs a one-time-per-24h-per-version notice when the hub
// reports a newer cloud-pulse-agent release than the one currently
// running. It is safe for concurrent use.
type updateNotifier struct {
	mu sync.Mutex

	// now returns the current time; defaults to time.Now. Tests inject a
	// fake clock to exercise the throttle window deterministically.
	now func() time.Time

	lastVersion  string
	lastLoggedAt time.Time
}

// newUpdateNotifier constructs an updateNotifier. now may be nil, in
// which case time.Now is used.
func newUpdateNotifier(now func() time.Time) *updateNotifier {
	if now == nil {
		now = time.Now
	}
	return &updateNotifier{now: now}
}

// Observe records that the hub reported latestVersion in a report
// response and, if latestVersion is newer than the agent's own
// version.Version, logs a notice via logger — at most once per
// updateNoticeThrottle window for the same latestVersion. An empty
// latestVersion, or one that isn't newer than the running version, is a
// no-op.
func (n *updateNotifier) Observe(logger *slog.Logger, latestVersion string) {
	if latestVersion == "" || !version.IsNewer(latestVersion, version.Version) {
		return
	}

	n.mu.Lock()
	now := n.now()
	shouldLog := latestVersion != n.lastVersion || now.Sub(n.lastLoggedAt) >= updateNoticeThrottle
	if shouldLog {
		n.lastVersion = latestVersion
		n.lastLoggedAt = now
	}
	n.mu.Unlock()

	if !shouldLog {
		return
	}

	logger.Info("a newer cloud-pulse-agent is available",
		"latest", latestVersion,
		"current", version.Version,
		"command", "sudo cloud-pulse-agent update",
	)
}
