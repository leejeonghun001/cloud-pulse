package agent

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// mutableClock is a simple mutable-time.Now stand-in for tests that need
// to advance time deterministically without sleeping.
type mutableClock struct {
	t time.Time
}

func (f *mutableClock) now() time.Time { return f.t }

// withTestVersion temporarily overrides version.Version for the duration
// of a test, restoring the original value on cleanup. version.Version is
// the one documented exception to the "no global mutable state" rule
// (set once via -ldflags before main runs); tests that need it to parse
// as a release (it defaults to "dev" in `go test` builds) must not run
// in parallel with other tests doing the same, since the mutation is
// process-global.
func withTestVersion(t *testing.T, v string) {
	t.Helper()
	original := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = original })
}

func TestUpdateNotifier_LogsOnceThenThrottles(t *testing.T) {
	withTestVersion(t, "v0.3.0")

	clock := &mutableClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	n := newUpdateNotifier(clock.now)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	latest := "v0.3.1" // newer than the v0.3.0 set above

	n.Observe(logger, latest)
	if !strings.Contains(buf.String(), "a newer cloud-pulse-agent is available") {
		t.Fatalf("expected notice on first Observe, got log: %q", buf.String())
	}

	buf.Reset()
	n.Observe(logger, latest)
	if buf.Len() != 0 {
		t.Errorf("expected no second notice within the throttle window, got: %q", buf.String())
	}

	// Advance time by less than the throttle window: still suppressed.
	clock.t = clock.t.Add(23 * time.Hour)
	n.Observe(logger, latest)
	if buf.Len() != 0 {
		t.Errorf("expected notice still throttled at 23h, got: %q", buf.String())
	}

	// Advance past the throttle window: notice repeats.
	clock.t = clock.t.Add(2 * time.Hour) // total 25h since first log
	n.Observe(logger, latest)
	if !strings.Contains(buf.String(), "a newer cloud-pulse-agent is available") {
		t.Errorf("expected notice again after throttle window elapsed, got: %q", buf.String())
	}
}

func TestUpdateNotifier_NewVersionResetsThrottleImmediately(t *testing.T) {
	withTestVersion(t, "v0.3.0")

	clock := &mutableClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	n := newUpdateNotifier(clock.now)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	n.Observe(logger, "v0.3.1")
	if !strings.Contains(buf.String(), "latest=v0.3.1") {
		t.Fatalf("expected first notice with latest=v0.3.1, got: %q", buf.String())
	}

	buf.Reset()
	// A different (even newer) latest version logs immediately, without
	// waiting for the 24h window, since it's new information.
	n.Observe(logger, "v0.4.0")
	if !strings.Contains(buf.String(), "latest=v0.4.0") {
		t.Errorf("expected immediate notice for a new latest version, got: %q", buf.String())
	}
}

func TestUpdateNotifier_NoNoticeWhenNotNewer(t *testing.T) {
	withTestVersion(t, "v0.3.0")

	n := newUpdateNotifier(nil)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	cases := []string{"", "dev", "v0.3.0", "v0.2.0"}
	for _, latest := range cases {
		n.Observe(logger, latest)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no notice for empty/current/older/unparsable latest, got: %q", buf.String())
	}
}

func TestUpdateNotifier_UnparsableCurrentVersionNeverNotifies(t *testing.T) {
	withTestVersion(t, "dev")

	n := newUpdateNotifier(nil)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	n.Observe(logger, "v99.99.99")
	if buf.Len() != 0 {
		t.Errorf("expected no notice when the running version doesn't parse, got: %q", buf.String())
	}
}

func TestUpdateNotifier_NilClockDefaultsToTimeNow(t *testing.T) {
	withTestVersion(t, "v0.3.0")

	// Just verifies newUpdateNotifier(nil) doesn't panic and produces a
	// working notifier (uses real time.Now internally).
	n := newUpdateNotifier(nil)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	n.Observe(logger, "v0.3.1")
	if !strings.Contains(buf.String(), "a newer cloud-pulse-agent is available") {
		t.Errorf("expected notice, got: %q", buf.String())
	}
}
