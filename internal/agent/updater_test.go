package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStepper drives RunUpdaterLoop's injected Sleep deterministically:
// each call blocks until the test sends on step (advance one iteration)
// or ctx is canceled. This avoids any dependency on real wall-clock
// sleep granularity per this project's CI-lessons testing convention.
type fakeStepper struct {
	step chan struct{}
	done chan struct{}
	n    atomic.Int32
}

func newFakeStepper() *fakeStepper {
	return &fakeStepper{step: make(chan struct{}), done: make(chan struct{})}
}

func (f *fakeStepper) sleep(ctx context.Context, _ time.Duration) bool {
	f.n.Add(1)
	select {
	case <-f.step:
		return true
	case <-ctx.Done():
		return false
	case <-f.done:
		return false
	}
}

// advance unblocks exactly one pending sleep call.
func (f *fakeStepper) advance() {
	f.step <- struct{}{}
}

func TestRunUpdaterLoop_ProcessesRequestWhenPresent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var exists atomic.Bool
	exists.Store(true)
	var processed atomic.Int32

	stepper := newFakeStepper()
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- RunUpdaterLoop(ctx, UpdaterOptions{
			RequestExists: exists.Load,
			ProcessRequest: func(context.Context) error {
				processed.Add(1)
				exists.Store(false) // simulate consuming/removing the request file
				return nil
			},
			Sleep: stepper.sleep,
		})
	}()

	// Wait for the first iteration to reach the sleep call (meaning
	// RequestExists+ProcessRequest already ran for iteration 1).
	waitForSleepCall(t, stepper, 1)
	if got := processed.Load(); got != 1 {
		t.Fatalf("processed = %d after first iteration, want 1", got)
	}

	stepper.advance() // let iteration 2 run: exists is now false, so no further processing
	waitForSleepCall(t, stepper, 2)
	if got := processed.Load(); got != 1 {
		t.Fatalf("processed = %d after second iteration, want still 1 (request already consumed)", got)
	}

	cancel()
	if err := <-loopDone; err != nil {
		t.Errorf("RunUpdaterLoop returned error %v, want nil on cancellation", err)
	}
}

func TestRunUpdaterLoop_SkipsProcessingWhenAbsent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var processed atomic.Int32
	stepper := newFakeStepper()
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- RunUpdaterLoop(ctx, UpdaterOptions{
			RequestExists: func() bool { return false },
			ProcessRequest: func(context.Context) error {
				processed.Add(1)
				return nil
			},
			Sleep: stepper.sleep,
		})
	}()

	waitForSleepCall(t, stepper, 1)
	stepper.advance()
	waitForSleepCall(t, stepper, 2)

	cancel()
	<-loopDone
	if got := processed.Load(); got != 0 {
		t.Errorf("processed = %d, want 0 when RequestExists always false", got)
	}
}

func TestRunUpdaterLoop_ProcessErrorDoesNotStopLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempts atomic.Int32
	stepper := newFakeStepper()
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- RunUpdaterLoop(ctx, UpdaterOptions{
			RequestExists: func() bool { return true },
			ProcessRequest: func(context.Context) error {
				attempts.Add(1)
				return errors.New("simulated failure")
			},
			Sleep: stepper.sleep,
		})
	}()

	waitForSleepCall(t, stepper, 1)
	stepper.advance()
	waitForSleepCall(t, stepper, 2)
	stepper.advance()
	waitForSleepCall(t, stepper, 3)

	cancel()
	if err := <-loopDone; err != nil {
		t.Errorf("RunUpdaterLoop returned error %v, want nil despite ProcessRequest failures", err)
	}
	if got := attempts.Load(); got < 3 {
		t.Errorf("attempts = %d, want at least 3 (loop must keep retrying after each failure)", got)
	}
}

func TestRunUpdaterLoop_DefaultIntervalUsedWhenZero(t *testing.T) {
	// Not a timing test (per this project's convention of never
	// depending on sleep granularity) — just confirms a zero
	// PollInterval doesn't panic or busy-loop forever without ever
	// reaching Sleep: the fake sleep still gets called, proving the
	// default value path executed rather than e.g. dividing by zero
	// or spinning without ever yielding to Sleep.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stepper := newFakeStepper()
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- RunUpdaterLoop(ctx, UpdaterOptions{
			RequestExists: func() bool { return false },
			Sleep:         stepper.sleep,
		})
	}()

	waitForSleepCall(t, stepper, 1)
	cancel()
	<-loopDone
}

func TestRunUpdaterLoop_NilProcessRequestIsSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stepper := newFakeStepper()
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- RunUpdaterLoop(ctx, UpdaterOptions{
			RequestExists: func() bool { return true },
			Sleep:         stepper.sleep,
		})
	}()

	waitForSleepCall(t, stepper, 1)
	cancel()
	if err := <-loopDone; err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunUpdaterLoop_ExitsImmediatelyIfAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := RunUpdaterLoop(ctx, UpdaterOptions{
		RequestExists: func() bool { return true },
		ProcessRequest: func(context.Context) error {
			t.Error("ProcessRequest must not be called when ctx is already canceled")
			return nil
		},
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRealSleep_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		cancel()
	}()
	// A long duration proves the return is due to cancellation, not
	// the timer firing — this is the only place a real timer is used
	// (realSleep's own unit test), never in the loop tests above.
	if got := realSleep(ctx, 10*time.Second); got {
		t.Error("realSleep returned true, want false on context cancellation")
	}
}

// waitForSleepCall blocks until stepper has recorded at least n calls
// to sleep, or fails the test after a generous timeout. This is a
// synchronization wait on an atomic counter (polled), not a sleep-
// granularity-dependent timing assertion — the timeout exists only to
// fail fast on a genuine deadlock/bug, not to assert real elapsed time.
func waitForSleepCall(t *testing.T, f *fakeStepper, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.n.Load() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for sleep call #%d", n)
}
