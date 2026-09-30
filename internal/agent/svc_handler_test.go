package agent

import (
	"context"
	"testing"
)

func TestServiceHandler_StopCancelsOnce(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := NewServiceHandler(func() {
		calls++
		cancel()
	}, nil)

	state := h.Handle(ServiceCmdStop)
	if state != ServiceStateStopPending {
		t.Errorf("state = %v, want ServiceStateStopPending", state)
	}
	if calls != 1 {
		t.Fatalf("cancel called %d times, want 1", calls)
	}
	select {
	case <-ctx.Done():
	default:
		t.Error("expected context to be canceled after Handle(Stop)")
	}

	// A second Stop (or a Shutdown) must not call cancel again.
	state = h.Handle(ServiceCmdShutdown)
	if state != ServiceStateStopPending {
		t.Errorf("state = %v, want ServiceStateStopPending", state)
	}
	if calls != 1 {
		t.Errorf("cancel called %d times after second stop request, want still 1", calls)
	}
}

func TestServiceHandler_ShutdownCancelsOnce(t *testing.T) {
	calls := 0
	h := NewServiceHandler(func() { calls++ }, nil)

	h.Handle(ServiceCmdShutdown)
	if calls != 1 {
		t.Fatalf("cancel called %d times, want 1", calls)
	}
	if !h.Stopped() {
		t.Error("expected Stopped() to report true after a Shutdown request")
	}
}

func TestServiceHandler_InterrogateAndOtherDoNotStop(t *testing.T) {
	calls := 0
	h := NewServiceHandler(func() { calls++ }, nil)

	if state := h.Handle(ServiceCmdInterrogate); state != ServiceStateRunning {
		t.Errorf("Interrogate state = %v, want ServiceStateRunning", state)
	}
	if state := h.Handle(ServiceCmdOther); state != ServiceStateRunning {
		t.Errorf("Other state = %v, want ServiceStateRunning", state)
	}
	if calls != 0 {
		t.Errorf("cancel called %d times, want 0", calls)
	}
	if h.Stopped() {
		t.Error("expected Stopped() to report false")
	}
}

func TestServiceHandler_InterrogateAfterStopReportsStopPending(t *testing.T) {
	h := NewServiceHandler(func() {}, nil)
	h.Handle(ServiceCmdStop)

	if state := h.Handle(ServiceCmdInterrogate); state != ServiceStateStopPending {
		t.Errorf("state after stop+interrogate = %v, want ServiceStateStopPending", state)
	}
}

func TestServiceHandler_NilLoggerDoesNotPanic(t *testing.T) {
	h := NewServiceHandler(func() {}, nil)
	h.Handle(ServiceCmdStop)
}
