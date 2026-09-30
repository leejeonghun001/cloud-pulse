package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// timeSyncPath is the hub endpoint queried for an initial clock offset
// estimate, relative to a Reporter's HubURL.
const timeSyncPath = "/api/v1/agent/time"

// timeSyncAttempts is the number of GET attempts made against
// timeSyncPath during initial sync before giving up for that call.
const timeSyncAttempts = 3

// timeSyncRequestTimeout bounds each initial-sync HTTP request.
const timeSyncRequestTimeout = 5 * time.Second

// maxClockObservations is the number of most recent Observe samples a
// HubClock retains for its min-RTT clock filter (NTP clock-filter idea).
const maxClockObservations = 8

// maxAcceptableRTT is the largest round-trip time an Observe sample may
// have and still be considered; larger values likely reflect network
// congestion or a stalled request rather than a fair sample of the
// current offset, so they are ignored entirely (not just deprioritized).
const maxAcceptableRTT = 5 * time.Second

// clockWarnThreshold is the minimum |offset| that triggers a warning log
// that the agent's local clock differs from the hub's.
const clockWarnThreshold = 1 * time.Second

// clockWarnInterval is the minimum time between repeated clock-offset
// warning log lines.
const clockWarnInterval = 10 * time.Minute

// clockObservation is a single offset/RTT sample recorded by Observe.
type clockObservation struct {
	offset time.Duration
	rtt    time.Duration
}

// HubClock estimates the offset between the local system clock and a
// cloud-pulse hub's wall clock using round-trip samples, NTP-style: each
// sample yields offset_i = serverMs - (t0+t1)/2 and rtt = t1-t0, and the
// clock reports the offset of whichever of the last few samples had the
// smallest RTT (least likely to be skewed by queueing/network delay).
// A zero-value HubClock is valid and behaves as an unsynced local clock
// (Now returns the local time, Synced reports false).
//
// HubClock is safe for concurrent use by multiple goroutines.
type HubClock struct {
	mu     sync.Mutex
	obs    []clockObservation
	synced bool

	// lastWarn is the local time of the most recent clock-offset warning
	// log line, used to rate-limit repeated warnings. Zero means none yet.
	lastWarn time.Time

	// now returns the current local time. If nil, time.Now is used.
	now func() time.Time
}

// Now returns the current time corrected by the clock's estimated offset
// from the hub (local time + Offset()). Before any successful
// observation, it returns the local time unmodified.
func (c *HubClock) Now() time.Time {
	return c.localNow().Add(c.Offset())
}

// Offset returns the current best estimate of hub-time minus local-time.
// It is zero until the first successful Observe call.
func (c *HubClock) Offset() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bestOffsetLocked()
}

// Synced reports whether at least one usable Observe sample has been
// recorded.
func (c *HubClock) Synced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.synced
}

// Observe records a single round-trip clock sample: t0 is the local time
// just before a request was sent, t1 is the local time just after its
// response was received, and serverMs is the hub's wall clock (unix
// milliseconds) as reported in that response. Samples with a negative or
// implausibly large (> 5s) RTT are ignored as unreliable. logWarn, if
// non-nil, is invoked (rate-limited to once per 10 minutes) when the
// resulting best-offset estimate has a magnitude of at least one second.
func (c *HubClock) Observe(t0, t1 time.Time, serverMs int64, logWarn func(offset time.Duration)) {
	rtt := t1.Sub(t0)
	if rtt < 0 || rtt > maxAcceptableRTT {
		return
	}

	mid := t0.Add(rtt / 2)
	serverTime := time.UnixMilli(serverMs)
	offset := serverTime.Sub(mid)

	c.mu.Lock()
	c.obs = append(c.obs, clockObservation{offset: offset, rtt: rtt})
	if len(c.obs) > maxClockObservations {
		c.obs = c.obs[len(c.obs)-maxClockObservations:]
	}
	c.synced = true
	best := c.bestOffsetLocked()

	shouldWarn := false
	if best >= clockWarnThreshold || -best >= clockWarnThreshold {
		if c.lastWarn.IsZero() || c.localNow().Sub(c.lastWarn) >= clockWarnInterval {
			c.lastWarn = c.localNow()
			shouldWarn = true
		}
	}
	c.mu.Unlock()

	if shouldWarn && logWarn != nil {
		logWarn(best)
	}
}

// bestOffsetLocked returns the offset of the retained observation with the
// smallest RTT; ties favor the most recently recorded observation (freshest
// data). Returns zero if there are none. c.mu must be held.
func (c *HubClock) bestOffsetLocked() time.Duration {
	if len(c.obs) == 0 {
		return 0
	}
	best := c.obs[0]
	for _, o := range c.obs[1:] {
		if o.rtt <= best.rtt {
			best = o
		}
	}
	return best.offset
}

// localNow returns the local time, using the injected now func if set.
func (c *HubClock) localNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// SyncInitial performs the initial hub time sync described in
// SPEC-v0.2: it issues up to timeSyncAttempts GET requests against
// {hubURL}/api/v1/agent/time (each bounded by timeSyncRequestTimeout,
// authenticated with token as a bearer credential — GET
// /api/v1/agent/time is agent-token-gated, same as the report endpoint)
// and records the best result via Observe. It is non-fatal: any failure
// (network error, non-2xx status) is logged at debug/info level and
// SyncInitial returns without error, leaving the clock unsynced so Run
// falls back to the local clock. A 404 response is logged once at info
// level ("hub does not support time sync; using local clock") and no
// further attempts are made for this call. logWarn is forwarded to
// Observe (see its docs).
func (c *HubClock) SyncInitial(ctx context.Context, client *http.Client, hubURL, token string, logger *slog.Logger, logWarn func(offset time.Duration)) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if client == nil {
		client = &http.Client{Timeout: timeSyncRequestTimeout}
	}

	for attempt := 0; attempt < timeSyncAttempts; attempt++ {
		if ctx.Err() != nil {
			return
		}
		serverMs, t0, t1, status, err := c.fetchServerTime(ctx, client, hubURL, token)
		if err != nil {
			logger.DebugContext(ctx, "time sync attempt failed", "attempt", attempt+1, "error", err)
			continue
		}
		if status == http.StatusNotFound {
			logger.InfoContext(ctx, "hub does not support time sync; using local clock")
			return
		}
		if status < 200 || status >= 300 {
			logger.DebugContext(ctx, "time sync attempt failed", "attempt", attempt+1, "status", status)
			continue
		}
		c.Observe(t0, t1, serverMs, logWarn)
	}
}

// fetchServerTime issues a single GET against {hubURL}/api/v1/agent/time
// (authenticated with token, matching the report endpoint's own bearer
// auth — GET /api/v1/agent/time is gated by the same requireAgentToken
// middleware) and returns the parsed server_time_ms, the local send/
// receive timestamps used for RTT estimation, and the HTTP status code.
// err is non-nil only for transport-level failures or an unparsable 2xx
// body; non-2xx/non-404 responses are reported via status with a nil
// error.
func (c *HubClock) fetchServerTime(ctx context.Context, client *http.Client, hubURL, token string) (serverMs int64, t0, t1 time.Time, status int, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeSyncRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, hubURL+timeSyncPath, nil)
	if err != nil {
		return 0, time.Time{}, time.Time{}, 0, fmt.Errorf("agent: build time sync request: %w", err)
	}
	req.Header.Set("User-Agent", "cloud-pulse-agent/"+version.Version)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	t0 = c.localNow()
	resp, err := client.Do(req)
	t1 = c.localNow()
	if err != nil {
		return 0, t0, t1, 0, fmt.Errorf("agent: time sync request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close() // best-effort close; body fully decoded or discarded below
	}()

	if resp.StatusCode != http.StatusOK {
		return 0, t0, t1, resp.StatusCode, nil
	}

	var tr struct {
		ServerTimeMs int64 `json:"server_time_ms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return 0, t0, t1, resp.StatusCode, fmt.Errorf("agent: decode time sync response: %w", err)
	}
	return tr.ServerTimeMs, t0, t1, resp.StatusCode, nil
}
