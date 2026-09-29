package agent

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// hostInfoRefreshInterval is how often Run refreshes the host info sent
// with each flush (static/slow-changing metadata such as uptime/boot
// time).
const hostInfoRefreshInterval = 10 * time.Minute

// timeSource abstracts wall-clock time and sleeping so Run's aligned
// scheduling loop can be driven by a fake clock in tests instead of real
// time. realTimeSource (the zero value's effective behavior, see
// newRealTimeSource) wraps time.Now/time.After.
type timeSource interface {
	// Now returns the current time (hub-corrected, when a HubClock is in
	// use; see RunOptions.Clock).
	Now() time.Time
	// After returns a channel that receives the current time after d has
	// elapsed, analogous to time.After.
	After(d time.Duration) <-chan time.Time
}

// realTimeSource is the production timeSource, optionally corrected by a
// HubClock.
type realTimeSource struct {
	clock *HubClock
}

func (r realTimeSource) Now() time.Time {
	if r.clock != nil {
		return r.clock.Now()
	}
	return time.Now()
}

func (r realTimeSource) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// RunOptions configures Run. The zero value runs with a plain, unsynced
// real-time source, no jitter, and a discard logger — equivalent to
// v0.1's ticker-based behavior except that sample timestamps are still
// aligned to interval boundaries.
type RunOptions struct {
	// Logger receives warn/info/debug logs. If nil, a discard logger is
	// used.
	Logger *slog.Logger
	// Clock, if non-nil, is used to correct Now() against the hub's
	// clock (see HubClock). If nil, Run uses the unsynced local clock.
	Clock *HubClock
	// Jitter is the maximum random delay inserted between collecting a
	// sample and flushing it, spreading simultaneous sends across a
	// fleet. 0 (the default) disables jitter. Must be <= interval/2;
	// callers (config validation) are responsible for enforcing that.
	Jitter time.Duration
	// RandomJitter returns a random duration in [0, Jitter). If nil,
	// math/rand/v2 is used. Exposed for deterministic tests.
	RandomJitter func(jitter time.Duration) time.Duration
	// timeSrc overrides the time source entirely, for tests. Unexported:
	// only this package's tests construct one directly.
	timeSrc timeSource
}

// Run drives the agent's collect/report loop: it waits for the next
// hub-clock-aligned interval boundary (e.g. :00/:15/:30/:45 for a 15s
// interval), collects a Sample stamped with that boundary, enqueues it,
// optionally sleeps a random jitter delay, then flushes, repeating until
// ctx is canceled. Aligning every agent's sample Timestamp to the same
// wall-clock boundary (in hub time) lets samples from many hosts share
// identical ts values for the same collection round. Rates/deltas inside
// each Sample are computed from the real monotonic time elapsed since the
// previous collection (Collector.CollectAt does this internally), not
// from the boundary-to-boundary difference, so a missed or delayed
// boundary never distorts a rate calculation.
//
// If a boundary is missed entirely (e.g. the process was suspended past
// it), Run skips forward to the next future boundary rather than firing a
// burst of catch-up samples.
//
// hostInfo is invoked at startup and every 10 minutes thereafter to
// refresh the reported host metadata. Flush failures are logged and
// retried on the next tick; the in-memory buffer absorbs hub outages.
// Run returns nil when ctx is canceled.
func Run(ctx context.Context, c *Collector, r *Reporter, hostInfo func(context.Context) models.HostInfo, interval time.Duration, logger *slog.Logger) error {
	return RunWithOptions(ctx, c, r, hostInfo, interval, RunOptions{Logger: logger})
}

// RunWithOptions is Run with full control over clock source, jitter, and
// (for tests) the underlying time source. See Run and RunOptions for
// details.
func RunWithOptions(ctx context.Context, c *Collector, r *Reporter, hostInfo func(context.Context) models.HostInfo, interval time.Duration, opts RunOptions) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ts := opts.timeSrc
	if ts == nil {
		ts = realTimeSource{clock: opts.Clock}
	}
	randomJitter := opts.RandomJitter
	if randomJitter == nil {
		randomJitter = defaultRandomJitter
	}

	info := hostInfo(ctx)
	lastInfoRefresh := ts.Now()

	nextBoundary := nextAlignedBoundary(ts.Now(), interval)

	for {
		if !sleepUntil(ctx, ts, nextBoundary) {
			return nil
		}

		// If we overslept past one or more boundaries (e.g. suspend),
		// skip forward without bursting catch-up samples.
		now := ts.Now()
		boundary := nextBoundary
		if now.Sub(boundary) >= interval {
			boundary = nextAlignedBoundary(now, interval)
			nextBoundary = boundary
		}
		nextBoundary = nextBoundary.Add(interval)

		if ts.Now().Sub(lastInfoRefresh) >= hostInfoRefreshInterval {
			info = hostInfo(ctx)
			lastInfoRefresh = ts.Now()
		}

		sample, err := c.CollectAt(ctx, boundary)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			logger.WarnContext(ctx, "collect failed", "error", err)
			continue
		}
		r.Enqueue(sample)

		if opts.Jitter > 0 {
			if !sleepUntil(ctx, ts, ts.Now().Add(randomJitter(opts.Jitter))) {
				return nil
			}
		}

		if err := r.Flush(ctx, info); err != nil {
			if errors.Is(err, ErrUnauthorized) {
				logger.WarnContext(ctx, "hub rejected report: unauthorized", "error", err)
				continue
			}
			logger.WarnContext(ctx, "flush failed, will retry next tick", "error", err)
		}

		if ctx.Err() != nil {
			return nil
		}
	}
}

// nextAlignedBoundary returns the smallest multiple of interval (in unix
// time) that is >= now, i.e. ceil(now/interval)*interval, as a time.Time.
// This aligns every caller using the same interval and clock to identical
// boundaries (e.g. :00/:15/:30/:45 for a 15s interval), independent of
// when Run happened to start.
func nextAlignedBoundary(now time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		return now
	}
	unitNanos := interval.Nanoseconds()
	nowNanos := now.UnixNano()
	rem := nowNanos % unitNanos
	if rem == 0 {
		return now
	}
	return time.Unix(0, nowNanos+(unitNanos-rem)).UTC()
}

// sleepUntil blocks until deadline (per ts) or ctx cancellation, whichever
// comes first, re-checking the remaining wait after each wake to absorb
// timer drift. It returns false if ctx was canceled before deadline.
func sleepUntil(ctx context.Context, ts timeSource, deadline time.Time) bool {
	for {
		remaining := deadline.Sub(ts.Now())
		if remaining <= 0 {
			return ctx.Err() == nil
		}
		select {
		case <-ctx.Done():
			return false
		case <-ts.After(remaining):
			// loop and re-check: the fake/real clock may have advanced by
			// more or less than `remaining` depending on scheduler slack.
		}
	}
}

// defaultRandomJitter returns a uniform random duration in [0, jitter).
func defaultRandomJitter(jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(jitter)))
}
