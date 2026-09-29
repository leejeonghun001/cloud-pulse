package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHubClock_ZeroValueIsUnsyncedLocalClock(t *testing.T) {
	t.Parallel()

	var c HubClock
	if c.Synced() {
		t.Error("Synced() = true on zero value, want false")
	}
	if c.Offset() != 0 {
		t.Errorf("Offset() = %v, want 0", c.Offset())
	}
	before := time.Now()
	got := c.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Errorf("Now() = %v, want within [%v, %v]", got, before, after)
	}
}

func TestHubClock_ObserveSetsOffset(t *testing.T) {
	t.Parallel()

	var c HubClock
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Local send at base, receive at base+100ms (rtt=100ms), server clock
	// reports base+3s+50ms (midpoint of the request window): so the hub
	// is running 3s ahead of the local clock.
	t0 := base
	t1 := base.Add(100 * time.Millisecond)
	serverMs := base.Add(3*time.Second + 50*time.Millisecond).UnixMilli()

	c.Observe(t0, t1, serverMs, nil)

	if !c.Synced() {
		t.Fatal("Synced() = false after a valid Observe")
	}
	got := c.Offset()
	want := 3 * time.Second
	if diff := got - want; diff > time.Millisecond || diff < -time.Millisecond {
		t.Errorf("Offset() = %v, want ~%v", got, want)
	}
}

func TestHubClock_Now_AppliesOffset(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := HubClock{now: func() time.Time { return base }}

	c.Observe(base, base, base.Add(5*time.Second).UnixMilli(), nil)

	got := c.Now()
	want := base.Add(5 * time.Second)
	if !got.Equal(want) {
		t.Errorf("Now() = %v, want %v", got, want)
	}
}

func TestHubClock_Observe_IgnoresNegativeOrHugeRTT(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("negative_rtt_ignored", func(t *testing.T) {
		t.Parallel()
		var c HubClock
		c.Observe(base.Add(time.Second), base, base.UnixMilli(), nil)
		if c.Synced() {
			t.Error("Synced() = true, want false (negative RTT sample must be ignored)")
		}
	})

	t.Run("huge_rtt_ignored", func(t *testing.T) {
		t.Parallel()
		var c HubClock
		c.Observe(base, base.Add(10*time.Second), base.UnixMilli(), nil)
		if c.Synced() {
			t.Error("Synced() = true, want false (RTT > 5s sample must be ignored)")
		}
	})

	t.Run("rtt_exactly_5s_ignored", func(t *testing.T) {
		t.Parallel()
		var c HubClock
		c.Observe(base, base.Add(5*time.Second+time.Nanosecond), base.UnixMilli(), nil)
		if c.Synced() {
			t.Error("Synced() = true, want false (RTT just over 5s must be ignored)")
		}
	})
}

func TestHubClock_MinRTTPick(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var c HubClock

	// Sample 1: high RTT (800ms), offset ~+10s -- should be outweighed.
	c.Observe(base, base.Add(800*time.Millisecond), base.Add(10*time.Second+400*time.Millisecond).UnixMilli(), nil)
	// Sample 2: low RTT (10ms), offset ~+2s -- should win (smallest RTT).
	c.Observe(base, base.Add(10*time.Millisecond), base.Add(2*time.Second+5*time.Millisecond).UnixMilli(), nil)
	// Sample 3: medium RTT (200ms), offset ~+7s -- should not win.
	c.Observe(base, base.Add(200*time.Millisecond), base.Add(7*time.Second+100*time.Millisecond).UnixMilli(), nil)

	got := c.Offset()
	want := 2 * time.Second
	if diff := got - want; diff > 10*time.Millisecond || diff < -10*time.Millisecond {
		t.Errorf("Offset() = %v, want ~%v (offset of the min-RTT sample)", got, want)
	}
}

func TestHubClock_RetainsOnlyLastEightObservations(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var c HubClock

	// First observation: very low RTT, offset +100s -- should be evicted
	// once more than maxClockObservations samples have been recorded.
	c.Observe(base, base.Add(time.Millisecond), base.Add(100*time.Second).UnixMilli(), nil)

	// Push maxClockObservations more observations with higher RTT and a
	// different, consistent offset, so if the first is evicted the
	// result reflects only the later batch.
	for i := 0; i < maxClockObservations; i++ {
		c.Observe(base, base.Add(50*time.Millisecond), base.Add(3*time.Second).UnixMilli(), nil)
	}

	got := c.Offset()
	want := 3*time.Second - 25*time.Millisecond // offset accounts for the 50ms RTT's midpoint correction
	if diff := got - want; diff > time.Millisecond || diff < -time.Millisecond {
		t.Errorf("Offset() = %v, want ~%v (oldest observation must have been evicted)", got, want)
	}
}

func TestHubClock_WarnCallback_RateLimitedAndThresholded(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	localNow := base
	c := HubClock{now: func() time.Time { return localNow }}

	var warnCount atomic.Int32
	warn := func(time.Duration) { warnCount.Add(1) }

	// Below threshold (500ms): must not warn.
	c.Observe(base, base, base.Add(500*time.Millisecond).UnixMilli(), warn)
	if warnCount.Load() != 0 {
		t.Fatalf("warnCount = %d after sub-threshold offset, want 0", warnCount.Load())
	}

	// At/above threshold (2s): must warn once.
	c.Observe(base, base, base.Add(2*time.Second).UnixMilli(), warn)
	if warnCount.Load() != 1 {
		t.Fatalf("warnCount = %d after first over-threshold offset, want 1", warnCount.Load())
	}

	// Immediately again: rate-limited, must not warn a second time.
	c.Observe(base, base, base.Add(2*time.Second).UnixMilli(), warn)
	if warnCount.Load() != 1 {
		t.Fatalf("warnCount = %d after second over-threshold offset within window, want 1 (rate-limited)", warnCount.Load())
	}

	// Advance local time past the rate-limit window: must warn again.
	localNow = base.Add(clockWarnInterval + time.Second)
	c.Observe(base, base, base.Add(2*time.Second).UnixMilli(), warn)
	if warnCount.Load() != 2 {
		t.Errorf("warnCount = %d after window elapsed, want 2", warnCount.Load())
	}
}

func TestHubClock_SyncInitial_Success(t *testing.T) {
	t.Parallel()

	serverTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != timeSyncPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, timeSyncPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"server_time_ms": serverTime.UnixMilli()})
	}))
	defer srv.Close()

	var c HubClock
	c.SyncInitial(t.Context(), srv.Client(), srv.URL, nil, nil)

	if !c.Synced() {
		t.Fatal("Synced() = false after successful SyncInitial")
	}
}

func TestHubClock_SyncInitial_404FallsBackWithoutRetry(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var c HubClock
	c.SyncInitial(t.Context(), srv.Client(), srv.URL, nil, nil)

	if c.Synced() {
		t.Error("Synced() = true after a 404 response, want false")
	}
	if requests.Load() != 1 {
		t.Errorf("requests = %d, want exactly 1 (no retry on 404)", requests.Load())
	}
}

func TestHubClock_SyncInitial_RetriesOnTransientFailureThenSucceeds(t *testing.T) {
	t.Parallel()

	serverTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int64{"server_time_ms": serverTime.UnixMilli()})
	}))
	defer srv.Close()

	var c HubClock
	c.SyncInitial(t.Context(), srv.Client(), srv.URL, nil, nil)

	if !c.Synced() {
		t.Fatal("Synced() = false, want true after eventual success within timeSyncAttempts")
	}
	if got := requests.Load(); got != timeSyncAttempts {
		t.Errorf("requests = %d, want %d", got, timeSyncAttempts)
	}
}

func TestHubClock_SyncInitial_AllAttemptsFail(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var c HubClock
	c.SyncInitial(t.Context(), srv.Client(), srv.URL, nil, nil)

	if c.Synced() {
		t.Error("Synced() = true, want false when every attempt fails")
	}
	if got := requests.Load(); got != timeSyncAttempts {
		t.Errorf("requests = %d, want %d", got, timeSyncAttempts)
	}
}

func TestHubClock_SyncInitial_ContextCanceledStopsEarly(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var c HubClock
	c.SyncInitial(ctx, srv.Client(), srv.URL, nil, nil)

	if requests.Load() != 0 {
		t.Errorf("requests = %d, want 0 (canceled before any attempt)", requests.Load())
	}
}

// TestHubClock_TwoAgentsWithDifferentLocalOffsetsConverge simulates two
// agents whose local clocks are skewed from the hub's real clock by +3s
// and -2s respectively. After each observes the same (fake) hub via
// Observe, HubClock.Now() must agree with the hub's real clock closely
// enough that both agents compute an IDENTICAL aligned sample timestamp
// (see TestNextAlignedBoundary_IdenticalAcrossSkewedAgents in run_test.go
// for the full boundary-alignment scenario); this test isolates just the
// offset-estimation half of that guarantee.
func TestHubClock_TwoAgentsWithDifferentLocalOffsetsConverge(t *testing.T) {
	t.Parallel()

	hubTime := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	agentAOffset := 3 * time.Second  // agent A's local clock is 3s ahead
	agentBOffset := -2 * time.Second // agent B's local clock is 2s behind

	// Each agent's local clock reading "now" at the moment of the
	// round trip.
	agentALocalNow := hubTime.Add(agentAOffset)
	agentBLocalNow := hubTime.Add(agentBOffset)

	// Simulate a negligible-RTT round trip for both: t0 == t1 == local
	// now, server reports its real time.
	var clockA, clockB HubClock
	clockA.Observe(agentALocalNow, agentALocalNow, hubTime.UnixMilli(), nil)
	clockB.Observe(agentBLocalNow, agentBLocalNow, hubTime.UnixMilli(), nil)

	// Now both agents ask "what time is it (in hub time)" at the same
	// real-world instant. clockA.Now() should be computed from
	// agentALocalNow + clockA.Offset(), etc.
	correctedA := agentALocalNow.Add(clockA.Offset())
	correctedB := agentBLocalNow.Add(clockB.Offset())

	if !correctedA.Equal(hubTime) {
		t.Errorf("agent A corrected time = %v, want %v", correctedA, hubTime)
	}
	if !correctedB.Equal(hubTime) {
		t.Errorf("agent B corrected time = %v, want %v", correctedB, hubTime)
	}
	if !correctedA.Equal(correctedB) {
		t.Errorf("corrected times differ: A=%v B=%v, want identical", correctedA, correctedB)
	}
}
