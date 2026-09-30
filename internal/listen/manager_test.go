package listen

import (
	"context"
	"net"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"
)

func testServer() *http.Server {
	return &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// waitForStatus polls fn until it returns true or the timeout elapses,
// failing the test on timeout. Avoids a fixed sleep for a goroutine-driven
// state change.
func waitForStatus(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

func TestApply_BindsLoopbackRandomPort(t *testing.T) {
	t.Parallel()
	m := NewManager(testServer(), testLogger())

	statuses, err := m.Apply(context.Background(), []string{"127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("len(statuses) = %d, want 1", len(statuses))
	}
	if statuses[0].Status != "listening" {
		t.Fatalf("status = %+v, want listening", statuses[0])
	}

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// freePort asks the OS for a free TCP port on 127.0.0.1 by binding then
// immediately closing a listener, returning "127.0.0.1:<port>".
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("freePort: close: %v", err)
	}
	return addr
}

func TestApply_ServesHTTPOnKnownPort(t *testing.T) {
	t.Parallel()
	addr := freePort(t)
	m := NewManager(testServer(), testLogger())

	statuses, err := m.Apply(context.Background(), []string{addr})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != "listening" {
		t.Fatalf("statuses = %+v, want one listening entry", statuses)
	}

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestApply_EADDRNOTAVAIL_ReportsWaiting(t *testing.T) {
	t.Parallel()
	m := NewManager(testServer(), testLogger())

	// 192.0.2.1 is in TEST-NET-1 (RFC 5737), guaranteed not assigned to
	// any local interface, so binding it fails with EADDRNOTAVAIL on
	// every platform.
	statuses, err := m.Apply(context.Background(), []string{"192.0.2.1:18321"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("len(statuses) = %d, want 1", len(statuses))
	}
	if statuses[0].Status != "waiting" {
		t.Fatalf("status = %+v, want waiting", statuses[0])
	}
	if statuses[0].Error == "" {
		t.Error("expected non-empty Error message")
	}

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestApply_EADDRINUSE_ReportsError(t *testing.T) {
	t.Parallel()
	addr := freePort(t)

	// Occupy the port first with a plain listener.
	occupied, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("occupy: listen: %v", err)
	}
	defer func() { _ = occupied.Close() }()

	m := NewManager(testServer(), testLogger())
	statuses, err := m.Apply(context.Background(), []string{addr})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != "error" {
		t.Fatalf("statuses = %+v, want one error entry", statuses)
	}

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestApply_AddingAndRemovingAddresses(t *testing.T) {
	t.Parallel()
	addr1 := freePort(t)
	addr2 := freePort(t)
	m := NewManager(testServer(), testLogger())

	if _, err := m.Apply(context.Background(), []string{addr1}); err != nil {
		t.Fatalf("Apply 1: %v", err)
	}

	statuses, err := m.Apply(context.Background(), []string{addr1, addr2})
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("len(statuses) = %d, want 2", len(statuses))
	}
	for _, s := range statuses {
		if s.Status != "listening" {
			t.Errorf("addr %s status = %s, want listening", s.Addr, s.Status)
		}
	}

	// Remove addr1, keep addr2.
	statuses, err = m.Apply(context.Background(), []string{addr2})
	if err != nil {
		t.Fatalf("Apply 3: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Addr != addr2 {
		t.Fatalf("statuses = %+v, want only addr2", statuses)
	}

	// addr1 should become free again shortly after Apply's closeDelay.
	waitForStatus(t, 3*time.Second, func() bool {
		ln, err := net.Listen("tcp", addr1)
		if err != nil {
			return false
		}
		_ = ln.Close()
		return true
	})

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestApply_DuplicateAddressesDeduped(t *testing.T) {
	t.Parallel()
	addr := freePort(t)
	m := NewManager(testServer(), testLogger())

	statuses, err := m.Apply(context.Background(), []string{addr, addr, addr})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("len(statuses) = %d, want 1 (deduped)", len(statuses))
	}

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestStatus_ReflectsLastApply(t *testing.T) {
	t.Parallel()
	addr := freePort(t)
	m := NewManager(testServer(), testLogger())

	if _, err := m.Apply(context.Background(), []string{addr}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	statuses := m.Status()
	if len(statuses) != 1 || statuses[0].Addr != addr || statuses[0].Status != "listening" {
		t.Fatalf("Status() = %+v, want one listening entry for %s", statuses, addr)
	}

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestRun_RetriesWaitingAddressUntilBindable(t *testing.T) {
	t.Parallel()
	// Occupy a port so the first Apply fails, then free it and let Run
	// pick it up on its next retry tick. Bind ":0" directly and occupy
	// that listener: a freePort()-then-rebind sequence leaves a window in
	// which a parallel test can take the port first.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy: listen: %v", err)
	}
	addr := occupied.Addr().String()

	m := NewManagerWithOptions(testServer(), testLogger(), ManagerOptions{RetryInterval: 30 * time.Millisecond})
	statuses, err := m.Apply(context.Background(), []string{addr})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if statuses[0].Status != "error" {
		t.Fatalf("initial status = %+v, want error (port occupied)", statuses[0])
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	if err := occupied.Close(); err != nil {
		t.Fatalf("release occupied: %v", err)
	}

	waitForStatus(t, 3*time.Second, func() bool {
		s := m.Status()
		return len(s) == 1 && s[0].Status == "listening"
	})

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestApply_Loopback127002_LinuxOnly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("127.0.0.2 loopback alias binding is Linux-specific behavior")
	}
	t.Parallel()

	m := NewManager(testServer(), testLogger())
	statuses, err := m.Apply(context.Background(), []string{"127.0.0.2:0"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != "listening" {
		t.Fatalf("statuses = %+v, want listening on 127.0.0.2", statuses)
	}

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestShutdown_ClosesAllListeners(t *testing.T) {
	t.Parallel()
	addr1 := freePort(t)
	addr2 := freePort(t)
	m := NewManager(testServer(), testLogger())

	if _, err := m.Apply(context.Background(), []string{addr1, addr2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	for _, addr := range []string{addr1, addr2} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("expected %s to be free after Shutdown: %v", addr, err)
		}
		_ = ln.Close()
	}
}

func TestClassifyBindError(t *testing.T) {
	t.Parallel()
	m := NewManager(testServer(), testLogger())
	defer func() { _ = m.Shutdown(context.Background()) }()

	// EADDRNOTAVAIL via an unassigned address.
	if _, err := m.Apply(context.Background(), []string{"192.0.2.1:18322"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	statuses := m.Status()
	if len(statuses) != 1 || statuses[0].Status != "waiting" {
		t.Fatalf("statuses = %+v, want waiting", statuses)
	}
}

// stickyListener keeps Accept blocked after Close (so http.Server keeps
// tracking it, like a Serve goroutine that has not returned yet) and makes
// every Close after the first report net.ErrClosed.
type stickyListener struct {
	net.Listener
	release     chan struct{}
	releaseOnce sync.Once
	mu          sync.Mutex
	closed      bool
}

func (l *stickyListener) Accept() (net.Conn, error) {
	<-l.release
	return nil, net.ErrClosed
}

func (l *stickyListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		// http.Server.Shutdown's own close: let Serve return, as a real
		// listener's Accept would after the first close.
		l.releaseOnce.Do(func() { close(l.release) })
		return &net.OpError{Op: "close", Net: "tcp", Err: net.ErrClosed}
	}
	l.closed = true
	return l.Listener.Close()
}

// TestShutdown_AlreadyClosedListenerIsNotAnError reproduces the macOS CI
// race: a listener removed by Apply is closed by closeAfterDelay while
// http.Server still tracks it, so srv.Shutdown's own close reports
// net.ErrClosed. That must not surface as a shutdown failure.
// Not parallel: it swaps the package-level listenTCP hook.
func TestShutdown_AlreadyClosedListenerIsNotAnError(t *testing.T) {
	release := make(chan struct{})
	var sticky *stickyListener
	m := NewManagerWithOptions(testServer(), testLogger(), ManagerOptions{
		Listen: func(_ string, addr string) (net.Listener, error) {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return nil, err
			}
			sticky = &stickyListener{Listener: ln, release: release}
			return sticky, nil
		},
	})
	t.Cleanup(func() {
		if sticky != nil {
			sticky.releaseOnce.Do(func() { close(release) })
		}
	})
	statuses, err := m.Apply(context.Background(), []string{"127.0.0.1:0"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Status != "listening" {
		t.Fatalf("statuses = %+v, want one listening", statuses)
	}
	// Wait until Serve has started tracking the listener.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.Dial("tcp", sticky.Addr().String()); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := sticky.Close(); err != nil { // closeAfterDelay wins the race
		t.Fatalf("first close: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}
}
