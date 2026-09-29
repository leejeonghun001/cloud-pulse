package hub

import (
	"sync"
	"time"
)

// rateLimitMaxFailures is the number of failures within
// rateLimitWindow that trigger a per-IP lockout.
const rateLimitMaxFailures = 5

// rateLimitWindow is the sliding window over which per-IP failures are
// counted.
const rateLimitWindow = 15 * time.Minute

// rateLimitBaseLockout is the first lockout duration; each subsequent
// lockout (while failures keep occurring) doubles, up to
// rateLimitMaxLockout.
const rateLimitBaseLockout = 1 * time.Minute

// rateLimitMaxLockout caps the doubling lockout duration.
const rateLimitMaxLockout = 15 * time.Minute

// globalRateLimitMaxFailures is the fleet-wide failure threshold within
// globalRateLimitWindow that triggers a global lockout.
const globalRateLimitMaxFailures = 30

// globalRateLimitWindow is the sliding window for the global failure
// count.
const globalRateLimitWindow = 1 * time.Minute

// globalRateLimitLockout is how long every login attempt is rejected
// once the global threshold is crossed.
const globalRateLimitLockout = 60 * time.Second

// maxConcurrentPBKDF2 bounds how many PBKDF2 computations (login
// attempts actively hashing a password) may run at once, so a burst of
// login requests can't turn into an unbounded CPU spike.
const maxConcurrentPBKDF2 = 2

// pbkdf2WaitTimeout is how long a caller waits for a free PBKDF2
// semaphore slot before giving up and reporting rate-limited.
const pbkdf2WaitTimeout = 5 * time.Second

// clientLimiterState tracks one client IP's recent login failures and
// any active lockout.
type clientLimiterState struct {
	failures     []time.Time
	lockedUntil  time.Time
	lockoutCount int // number of consecutive lockouts, for doubling
}

// rateLimiter implements the login rate limiting described in
// SPEC-v0.4 §1: a per-client-IP failure/lockout tracker, a global
// failure tracker, and a semaphore bounding concurrent PBKDF2
// computations. All time-dependent behavior goes through now, which
// tests override with a fixed/advancing clock.
type rateLimiter struct {
	now func() time.Time

	mu      sync.Mutex
	clients map[string]*clientLimiterState
	global  []time.Time
	// globalLockedUntil is non-zero while a global lockout (>30
	// failures/min across all clients) is active.
	globalLockedUntil time.Time

	sem chan struct{}
}

// newRateLimiter constructs a rateLimiter. now defaults to time.Now
// when nil.
func newRateLimiter(now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{
		now:     now,
		clients: make(map[string]*clientLimiterState),
		sem:     make(chan struct{}, maxConcurrentPBKDF2),
	}
}

// allow reports whether a login attempt from clientIP may proceed
// (neither a per-IP nor a global lockout is active), and if not, how
// many seconds the caller should wait before retrying.
func (rl *rateLimiter) allow(clientIP string) (ok bool, retryAfterSeconds int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()

	if now.Before(rl.globalLockedUntil) {
		return false, ceilSeconds(rl.globalLockedUntil.Sub(now))
	}

	st := rl.clients[clientIP]
	if st == nil {
		return true, 0
	}
	if now.Before(st.lockedUntil) {
		return false, ceilSeconds(st.lockedUntil.Sub(now))
	}
	return true, 0
}

// recordFailure records a failed login attempt from clientIP, applying
// or extending a per-IP lockout once rateLimitMaxFailures failures have
// occurred within rateLimitWindow, and separately tracking the global
// failure count for the fleet-wide lockout.
func (rl *rateLimiter) recordFailure(clientIP string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()

	st := rl.clients[clientIP]
	if st == nil {
		st = &clientLimiterState{}
		rl.clients[clientIP] = st
	}
	st.failures = pruneOlderThan(append(st.failures, now), now, rateLimitWindow)
	if len(st.failures) >= rateLimitMaxFailures {
		lockout := rateLimitBaseLockout << st.lockoutCount
		if lockout > rateLimitMaxLockout || lockout <= 0 {
			lockout = rateLimitMaxLockout
		}
		st.lockedUntil = now.Add(lockout)
		st.lockoutCount++
		st.failures = nil
	}

	rl.global = pruneOlderThan(append(rl.global, now), now, globalRateLimitWindow)
	if len(rl.global) > globalRateLimitMaxFailures {
		rl.globalLockedUntil = now.Add(globalRateLimitLockout)
	}
}

// recordSuccess clears clientIP's failure/lockout state after a
// successful login.
func (rl *rateLimiter) recordSuccess(clientIP string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.clients, clientIP)
}

// cleanup removes per-client state that has no recent failures and no
// active lockout, bounding the limiter's memory usage across a long
// uptime. Callers run this periodically (e.g. alongside Store.Prune).
func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	for ip, st := range rl.clients {
		st.failures = pruneOlderThan(st.failures, now, rateLimitWindow)
		if len(st.failures) == 0 && now.After(st.lockedUntil) {
			delete(rl.clients, ip)
		}
	}
	rl.global = pruneOlderThan(rl.global, now, globalRateLimitWindow)
}

// acquirePBKDF2 blocks until a PBKDF2 semaphore slot is free or
// pbkdf2WaitTimeout elapses, returning a release function and ok=true on
// success. Callers that get ok=false must not perform the PBKDF2
// computation and should report rate-limited instead.
func (rl *rateLimiter) acquirePBKDF2() (release func(), ok bool) {
	select {
	case rl.sem <- struct{}{}:
		return func() { <-rl.sem }, true
	case <-time.After(pbkdf2WaitTimeout):
		return nil, false
	}
}

// pruneOlderThan returns the subset of times within window of now,
// preserving order. It reuses times' backing array.
func pruneOlderThan(times []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	out := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

// ceilSeconds rounds d up to the nearest whole second, never returning
// less than 1 for a positive duration.
func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	secs := int(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	if secs < 1 {
		secs = 1
	}
	return secs
}
