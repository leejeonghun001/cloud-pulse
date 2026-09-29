package hub

import (
	"sync"
	"testing"
	"time"
)

// mutableClock is a test clock that starts at start and only advances
// when advance is called, letting tests deterministically control
// rateLimiter's time-dependent behavior.
type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMutableClock(start time.Time) *mutableClock {
	return &mutableClock{now: start}
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestRateLimiter_PerIPLockoutAfterFailures(t *testing.T) {
	t.Parallel()
	clock := newMutableClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rl := newRateLimiter(clock.Now)
	const ip = "203.0.113.1:1"

	for i := 0; i < rateLimitMaxFailures-1; i++ {
		if ok, _ := rl.allow(ip); !ok {
			t.Fatalf("attempt %d: expected allow before lockout threshold", i)
		}
		rl.recordFailure(ip)
	}
	// Still allowed: one failure short of the threshold.
	if ok, _ := rl.allow(ip); !ok {
		t.Fatal("expected allow with one failure remaining before lockout")
	}
	rl.recordFailure(ip) // crosses the threshold

	ok, retryAfter := rl.allow(ip)
	if ok {
		t.Fatal("expected lockout after reaching rateLimitMaxFailures")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %d, want > 0", retryAfter)
	}

	// Advance past the lockout: allowed again.
	clock.advance(rateLimitBaseLockout + time.Second)
	if ok, _ := rl.allow(ip); !ok {
		t.Error("expected allow after lockout window elapsed")
	}
}

func TestRateLimiter_DoublingLockout(t *testing.T) {
	t.Parallel()
	clock := newMutableClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rl := newRateLimiter(clock.Now)
	const ip = "203.0.113.2:1"

	triggerLockout := func() {
		for i := 0; i < rateLimitMaxFailures; i++ {
			rl.recordFailure(ip)
		}
	}

	triggerLockout()
	_, retry1 := rl.allow(ip)
	clock.advance(rateLimitMaxLockout * 2) // clear any lockout

	triggerLockout()
	_, retry2 := rl.allow(ip)

	if retry2 <= retry1 {
		t.Errorf("second lockout retryAfter = %d, want > first lockout's %d (doubling)", retry2, retry1)
	}
}

func TestRateLimiter_LockoutCappedAtMax(t *testing.T) {
	t.Parallel()
	clock := newMutableClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rl := newRateLimiter(clock.Now)
	const ip = "203.0.113.3:1"

	for round := 0; round < 6; round++ {
		for i := 0; i < rateLimitMaxFailures; i++ {
			rl.recordFailure(ip)
		}
		_, retry := rl.allow(ip)
		if retry > int(rateLimitMaxLockout.Seconds()) {
			t.Fatalf("round %d: retryAfter = %ds, want <= %ds (capped)", round, retry, int(rateLimitMaxLockout.Seconds()))
		}
		clock.advance(rateLimitMaxLockout + time.Second)
	}
}

func TestRateLimiter_SuccessClearsFailures(t *testing.T) {
	t.Parallel()
	clock := newMutableClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rl := newRateLimiter(clock.Now)
	const ip = "203.0.113.4:1"

	for i := 0; i < rateLimitMaxFailures-1; i++ {
		rl.recordFailure(ip)
	}
	rl.recordSuccess(ip)
	rl.recordFailure(ip) // would have triggered lockout without the reset above

	if ok, _ := rl.allow(ip); !ok {
		t.Error("expected allow: recordSuccess should have cleared prior failures")
	}
}

func TestRateLimiter_FailuresOutsideWindowDontAccumulate(t *testing.T) {
	t.Parallel()
	clock := newMutableClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rl := newRateLimiter(clock.Now)
	const ip = "203.0.113.5:1"

	for i := 0; i < rateLimitMaxFailures-1; i++ {
		rl.recordFailure(ip)
	}
	clock.advance(rateLimitWindow + time.Second) // old failures fall out of the window
	rl.recordFailure(ip)

	if ok, _ := rl.allow(ip); !ok {
		t.Error("expected allow: earlier failures should have aged out of the window")
	}
}

func TestRateLimiter_GlobalLockout(t *testing.T) {
	t.Parallel()
	clock := newMutableClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rl := newRateLimiter(clock.Now)

	// Distribute failures across many distinct IPs so no single IP's
	// per-IP lockout triggers first, isolating the global threshold.
	for i := 0; i < globalRateLimitMaxFailures+1; i++ {
		ip := "203.0.114." + string(rune('A'+i%26)) + ":1"
		rl.recordFailure(ip)
	}

	ok, retryAfter := rl.allow("203.0.115.1:1") // a fresh IP with no history
	if ok {
		t.Fatal("expected global lockout to block even a fresh client IP")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %d, want > 0", retryAfter)
	}

	clock.advance(globalRateLimitLockout + time.Second)
	if ok, _ := rl.allow("203.0.115.1:1"); !ok {
		t.Error("expected allow after global lockout window elapsed")
	}
}

func TestRateLimiter_Cleanup(t *testing.T) {
	t.Parallel()
	clock := newMutableClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	rl := newRateLimiter(clock.Now)
	const ip = "203.0.113.6:1"

	rl.recordFailure(ip)
	clock.advance(rateLimitWindow + time.Second)
	rl.cleanup()

	rl.mu.Lock()
	_, exists := rl.clients[ip]
	rl.mu.Unlock()
	if exists {
		t.Error("expected stale client state to be removed by cleanup")
	}
}

func TestRateLimiter_PBKDF2Semaphore(t *testing.T) {
	t.Parallel()
	rl := newRateLimiter(nil)

	var releases []func()
	for i := 0; i < maxConcurrentPBKDF2; i++ {
		release, ok := rl.acquirePBKDF2()
		if !ok {
			t.Fatalf("acquire %d: expected ok=true within the semaphore limit", i)
		}
		releases = append(releases, release)
	}

	// One more, over the limit: acquirePBKDF2 blocks until
	// pbkdf2WaitTimeout, so run it in a goroutine and release a slot
	// shortly after to prove it unblocks rather than waiting the full
	// timeout.
	done := make(chan bool, 1)
	go func() {
		_, ok := rl.acquirePBKDF2()
		done <- ok
	}()

	select {
	case <-done:
		t.Fatal("acquirePBKDF2 returned before a slot was released")
	case <-time.After(50 * time.Millisecond):
	}

	releases[0]()
	select {
	case ok := <-done:
		if !ok {
			t.Error("expected ok=true once a slot was released")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquirePBKDF2 did not unblock after a slot was released")
	}

	for _, release := range releases[1:] {
		release()
	}
}

func TestCeilSeconds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want int
	}{
		{0, 0},
		{-time.Second, 0},
		{500 * time.Millisecond, 1},
		{1 * time.Second, 1},
		{1500 * time.Millisecond, 2},
		{60 * time.Second, 60},
	}
	for _, tc := range cases {
		if got := ceilSeconds(tc.d); got != tc.want {
			t.Errorf("ceilSeconds(%v) = %d, want %d", tc.d, got, tc.want)
		}
	}
}
