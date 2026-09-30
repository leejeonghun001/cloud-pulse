package poller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoop_TicksImmediatelyThenOnInterval(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		Loop(ctx, func() time.Duration { return time.Millisecond }, func(context.Context) {
			n := calls.Add(1)
			if n >= 3 {
				cancel()
			}
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return within 5s of cancel")
	}

	if got := calls.Load(); got < 3 {
		t.Errorf("calls = %d, want >= 3", got)
	}
}

func TestLoop_StopsImmediatelyOnCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		Loop(ctx, func() time.Duration { return time.Hour }, func(context.Context) {
			calls.Add(1)
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return promptly on an already-canceled context")
	}
	// The immediate pre-loop tick still fires once even on an
	// already-canceled context (matches billing's "collect on startup"
	// behavior); only the subsequent timer wait is skipped.
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want exactly 1", got)
	}
}

func TestLoop_ZeroOrNegativeIntervalDefaultsToOneMinute(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		Loop(ctx, func() time.Duration { return 0 }, func(context.Context) {
			if calls.Add(1) == 1 {
				cancel()
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not return")
	}
}

func TestRefreshGate_FirstCallAlwaysAllowed(t *testing.T) {
	t.Parallel()
	g := &RefreshGate{MinInterval: time.Hour}
	allowed, retryAfter := g.Allow()
	if !allowed || retryAfter != 0 {
		t.Errorf("first Allow() = (%v, %v), want (true, 0)", allowed, retryAfter)
	}
}

func TestRefreshGate_ThrottlesUntilIntervalElapsed(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	g := &RefreshGate{MinInterval: 10 * time.Minute, Now: func() time.Time { return now }}

	allowed, _ := g.Allow()
	if !allowed {
		t.Fatal("first Allow() should be true")
	}
	g.Record()

	allowed, retryAfter := g.Allow()
	if allowed {
		t.Error("second immediate Allow() should be false")
	}
	if retryAfter != 10*time.Minute {
		t.Errorf("retryAfter = %v, want 10m", retryAfter)
	}

	now = now.Add(5 * time.Minute)
	allowed, retryAfter = g.Allow()
	if allowed {
		t.Error("Allow() after 5m should still be false")
	}
	if retryAfter != 5*time.Minute {
		t.Errorf("retryAfter = %v, want 5m", retryAfter)
	}

	now = now.Add(5 * time.Minute)
	allowed, retryAfter = g.Allow()
	if !allowed || retryAfter != 0 {
		t.Errorf("Allow() after full 10m = (%v, %v), want (true, 0)", allowed, retryAfter)
	}
}

func TestRefreshGate_ZeroMinIntervalNeverThrottles(t *testing.T) {
	t.Parallel()
	g := &RefreshGate{}
	g.Record()
	allowed, retryAfter := g.Allow()
	if !allowed || retryAfter != 0 {
		t.Errorf("Allow() with zero MinInterval = (%v, %v), want (true, 0)", allowed, retryAfter)
	}
}

func TestDailyLogGate_AllowsOncePerUTCDay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	g := &DailyLogGate{Now: func() time.Time { return now }}

	if !g.Allow("aws") {
		t.Error("first Allow(\"aws\") should be true")
	}
	if g.Allow("aws") {
		t.Error("second Allow(\"aws\") same day should be false")
	}
	// A different key is independent.
	if !g.Allow("oci") {
		t.Error("Allow(\"oci\") should be true (different key)")
	}

	// Crossing midnight UTC re-allows.
	now = now.Add(24 * time.Hour)
	if !g.Allow("aws") {
		t.Error("Allow(\"aws\") after a day should be true again")
	}
}

func TestDailyLogGate_ZeroValueUsable(t *testing.T) {
	t.Parallel()
	var g DailyLogGate
	if !g.Allow("x") {
		t.Error("zero-value DailyLogGate should allow the first call")
	}
}
