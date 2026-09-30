// Package poller implements the generic periodic-collection framework
// shared by internal/billing (SPEC-v0.6 §1) and internal/storageusage
// (SPEC-v0.7 §3): an interval timer driven by an injected clock, a
// rate-limited manual-refresh gate, and a once-per-day log-reason
// throttle. It intentionally does NOT define a generic Snapshot type —
// billing's CloudCostSnapshot and storageusage's StorageAccountSnapshot
// each keep their own domain-specific fields; only the scheduling
// mechanics below are shared.
package poller

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Loop runs tick on a timer until ctx is canceled, re-resolving the
// interval via intervalFunc before every wait so a live interval change
// (e.g. a hub-side settings override) takes effect on the very next
// scheduling decision without a restart. tick is invoked once
// immediately before the first wait, matching billing/storageusage's
// existing "collect on startup, then on a timer" behavior. It blocks
// until ctx is done.
func Loop(ctx context.Context, intervalFunc func() time.Duration, tick func(ctx context.Context)) {
	tick(ctx)

	for {
		interval := intervalFunc()
		if interval <= 0 {
			interval = time.Minute
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			tick(ctx)
		}
	}
}

// RefreshGate throttles manual "refresh now" requests to at most once
// per MinInterval, using an injected clock for deterministic tests. The
// zero value is usable (first call to Allow always succeeds).
type RefreshGate struct {
	// MinInterval is the minimum time between two successful Allow
	// calls. Zero means never throttle.
	MinInterval time.Duration
	// Now returns the current time; nil defaults to time.Now.
	Now func() time.Time

	last atomic.Int64 // unix nanoseconds of the last allowed call, 0 = never
}

func (g *RefreshGate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Allow reports whether a refresh should proceed right now, and if not,
// how long the caller must wait. It does NOT itself record the attempt
// — callers that proceed must call Record after the attempt completes
// (successfully or not), mirroring the existing billing/refresh
// behavior of stamping lastRefresh regardless of outcome.
func (g *RefreshGate) Allow() (allowed bool, retryAfter time.Duration) {
	last := g.last.Load()
	if last == 0 || g.MinInterval <= 0 {
		return true, 0
	}
	elapsed := g.now().Sub(time.Unix(0, last))
	if elapsed >= g.MinInterval {
		return true, 0
	}
	return false, g.MinInterval - elapsed
}

// Record stamps the current time as the last refresh attempt.
func (g *RefreshGate) Record() {
	g.last.Store(g.now().UnixNano())
}

// RunMutex serializes collector runs so a manual refresh and the
// background scheduler tick never race against each other. Embedding
// this (rather than a bare sync.Mutex) documents intent at call sites.
type RunMutex struct {
	mu sync.Mutex
}

// Guard runs fn while holding the mutex.
func (m *RunMutex) Guard(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn()
}

// DailyLogGate suppresses repeated log lines for the same key to at
// most once per UTC calendar day, so a persistently failing background
// collector (e.g. an unconfigured provider polled every 15 minutes)
// doesn't flood logs with an identical reason every tick. Safe for
// concurrent use.
type DailyLogGate struct {
	// Now returns the current time; nil defaults to time.Now.
	Now func() time.Time

	mu      sync.Mutex
	lastDay map[string]string // key -> "YYYY-MM-DD" of the last day this key was allowed to log
}

func (g *DailyLogGate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Allow reports whether a log line for key should be emitted now: true
// at most once per UTC calendar day per key.
func (g *DailyLogGate) Allow(key string) bool {
	day := g.now().UTC().Format("2006-01-02")

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastDay == nil {
		g.lastDay = make(map[string]string)
	}
	if g.lastDay[key] == day {
		return false
	}
	g.lastDay[key] = day
	return true
}
