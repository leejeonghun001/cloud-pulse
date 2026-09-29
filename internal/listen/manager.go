// Package listen manages the hub's actual HTTP listen addresses so the
// Network settings page (SPEC-v0.4 §2) can change which
// interfaces/addresses the hub is reachable on without restarting the
// process. It depends only on the standard library.
//
// Manager owns a set of net.Listener values, one http.Server.Serve
// goroutine per listener, and a status map reporting whether each
// desired address is currently "listening", "waiting" (a retryable bind
// failure, e.g. an interface that isn't up yet), or "error" (an
// unretryable-looking bind failure, e.g. EADDRINUSE — still retried,
// since an admin might free the port). Run periodically retries any
// address not currently listening.
package listen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// RetryInterval is how often Run attempts to (re)bind addresses that are
// not currently listening. Exposed as a var (not a parameter) so tests
// can shorten it; production code never changes it.
var RetryInterval = 5 * time.Second

// closeDelay is how long Apply waits before closing a listener that is
// no longer desired, giving any in-flight HTTP response on that
// listener (notably the PUT /api/v1/settings/network response itself)
// a chance to flush before the connection's listener disappears.
var closeDelay = 1 * time.Second

// listenTCP opens a TCP listener; tests replace it to inject listener
// behaviour (e.g. to reproduce close races deterministically).
var listenTCP = func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }

// entry tracks one desired address's listener (if bound) and status.
type entry struct {
	ln     net.Listener // nil while not listening
	status models.ListenerStatus
	// stopServe cancels the Serve goroutine's context when the listener
	// is closed by us (as opposed to a Serve error), so the goroutine
	// doesn't report a spurious error status for an intentional close.
	closing bool
}

// Manager reconciles a desired set of listen addresses against a set of
// real net.Listeners, serving srv.Handler on each. All exported methods
// are safe for concurrent use.
type Manager struct {
	srv    *http.Server
	logger *slog.Logger

	mu      sync.Mutex
	entries map[string]*entry // key: addr string as passed to Apply
}

// NewManager constructs a Manager that serves srv.Handler on every
// listener it opens. srv itself is never Serve'd directly by the
// caller; Manager calls srv.Serve(ln) once per listener it opens.
// logger must not be nil.
func NewManager(srv *http.Server, logger *slog.Logger) *Manager {
	return &Manager{
		srv:     srv,
		logger:  logger,
		entries: make(map[string]*entry),
	}
}

// Apply reconciles the manager's active listeners with addrs (each
// "ip:port" or ":port", as accepted by net.Listen("tcp", addr)). It
// opens listeners for newly desired addresses before closing listeners
// for addresses no longer desired (closeDelay after Apply returns, so
// an in-flight HTTP response on a removed listener can still flush).
// Duplicate entries in addrs are treated as one address. Apply returns
// the status of every address in addrs after this reconciliation
// (including ones that failed to bind and are now "waiting"/"error").
func (m *Manager) Apply(ctx context.Context, addrs []string) ([]models.ListenerStatus, error) {
	wanted := dedupe(addrs)
	wantedSet := make(map[string]bool, len(wanted))
	for _, a := range wanted {
		wantedSet[a] = true
	}

	m.mu.Lock()

	// Open (or attempt to open) every newly desired address first.
	for _, addr := range wanted {
		if _, ok := m.entries[addr]; ok {
			continue
		}
		e := &entry{}
		m.entries[addr] = e
		m.bindLocked(addr, e)
	}

	// Collect entries no longer desired; close them after releasing the
	// lock (bindLocked/serve goroutines also take m.mu).
	var toClose []struct {
		addr string
		e    *entry
	}
	for addr, e := range m.entries {
		if !wantedSet[addr] {
			e.closing = true
			toClose = append(toClose, struct {
				addr string
				e    *entry
			}{addr, e})
		}
	}
	for _, tc := range toClose {
		delete(m.entries, tc.addr)
	}

	statuses := m.statusLocked(wanted)
	m.mu.Unlock()

	for _, tc := range toClose {
		m.closeAfterDelay(tc.addr, tc.e)
	}

	return statuses, nil
}

// closeAfterDelay closes e's listener (if any) after closeDelay,
// off the calling goroutine so Apply never blocks on it.
func (m *Manager) closeAfterDelay(addr string, e *entry) {
	go func() {
		time.Sleep(closeDelay)
		if e.ln != nil {
			if err := e.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				m.logger.Debug("listen: close removed listener failed", "addr", addr, "error", err)
			}
		}
	}()
}

// bindLocked attempts to bind addr and, on success, starts a Serve
// goroutine for it. Must be called with m.mu held; on success e.ln and
// e.status are updated to "listening", on failure e.status is updated
// to "waiting" or "error" per classifyBindError.
func (m *Manager) bindLocked(addr string, e *entry) {
	ln, err := listenTCP(addr)
	now := time.Now().Unix()
	if err != nil {
		status, msg := classifyBindError(err)
		e.status = models.ListenerStatus{Addr: addr, Status: status, Error: msg, Since: now}
		m.logger.Debug("listen: bind failed", "addr", addr, "status", status, "error", msg)
		return
	}
	e.ln = ln
	// Report the listener's actual bound address (resolves a ":0"
	// request, or a bare-port request, to its concrete host:port) while
	// keying entries by the originally requested addr string, so Apply's
	// reconciliation against a caller's desired-address set is unaffected.
	e.status = models.ListenerStatus{Addr: ln.Addr().String(), Status: "listening", Since: now}
	m.logger.Info("listen: listening", "addr", addr, "bound_addr", ln.Addr().String())
	go m.serve(addr, e, ln)
}

// serve runs srv.Serve(ln) until it returns, then — unless the listener
// was closed intentionally by Apply (e.closing) — records the error as
// this address's new status so the next Run tick retries it.
func (m *Manager) serve(addr string, e *entry, ln net.Listener) {
	err := m.srv.Serve(ln)
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if e.closing {
		// Apply already removed/replaced this entry; nothing to update.
		return
	}
	// The entry might have been replaced by a subsequent Apply/retry
	// cycle (e.g. via bindLocked creating a new *entry for the same
	// addr key); only update if it's still the current entry for addr.
	if cur, ok := m.entries[addr]; !ok || cur != e {
		return
	}
	status, msg := classifyBindError(err)
	m.logger.Warn("listen: serve error", "addr", addr, "error", err)
	e.ln = nil
	e.status = models.ListenerStatus{Addr: addr, Status: status, Error: msg, Since: time.Now().Unix()}
}

// Status returns the current status of every address the manager knows
// about (the most recent Apply's addrs), sorted by Addr.
func (m *Manager) Status() []models.ListenerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	addrs := make([]string, 0, len(m.entries))
	for addr := range m.entries {
		addrs = append(addrs, addr)
	}
	return m.statusLocked(addrs)
}

// statusLocked returns the status of each addr in addrs (in sorted
// order), skipping any addr no longer tracked. Must be called with m.mu
// held.
func (m *Manager) statusLocked(addrs []string) []models.ListenerStatus {
	sorted := append([]string(nil), addrs...)
	sort.Strings(sorted)
	out := make([]models.ListenerStatus, 0, len(sorted))
	for _, addr := range sorted {
		if e, ok := m.entries[addr]; ok {
			out = append(out, e.status)
		}
	}
	return out
}

// Run retries every currently non-listening address every RetryInterval
// until ctx is done. It never returns until ctx.Done() fires.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(RetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.retryFailed()
		}
	}
}

// retryFailed attempts to bind every entry not currently listening.
func (m *Manager) retryFailed() {
	m.mu.Lock()
	var toRetry []struct {
		addr string
		e    *entry
	}
	for addr, e := range m.entries {
		if e.ln == nil {
			toRetry = append(toRetry, struct {
				addr string
				e    *entry
			}{addr, e})
		}
	}
	for _, tr := range toRetry {
		m.bindLocked(tr.addr, tr.e)
	}
	m.mu.Unlock()
}

// Shutdown gracefully shuts down srv (stopping new accepts, waiting for
// in-flight requests up to ctx's deadline) and then closes every
// remaining listener the manager holds directly, as a backstop for any
// listener srv.Shutdown didn't already close (e.g. one still in the
// process of being bound).
func (m *Manager) Shutdown(ctx context.Context) error {
	var shutdownErr error
	if m.srv != nil {
		// http.Server.Shutdown also closes every listener it is serving and
		// reports the first close error; a listener already closed by
		// closeAfterDelay (whose Serve goroutine has not untracked it yet)
		// yields net.ErrClosed, which is not a shutdown failure.
		if err := m.srv.Shutdown(ctx); err != nil && !errors.Is(err, net.ErrClosed) {
			shutdownErr = fmt.Errorf("listen: shutdown: %w", err)
		}
	}

	m.mu.Lock()
	entries := m.entries
	m.entries = make(map[string]*entry)
	m.mu.Unlock()

	for addr, e := range entries {
		e.closing = true
		if e.ln != nil {
			if err := e.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) && shutdownErr == nil {
				shutdownErr = fmt.Errorf("listen: close %s: %w", addr, err)
			}
		}
	}
	return shutdownErr
}

// dedupe returns addrs with duplicates removed, preserving first
// occurrence order (order doesn't matter to callers here, but keeps
// behavior deterministic).
func dedupe(addrs []string) []string {
	seen := make(map[string]bool, len(addrs))
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// classifyBindError maps a net.Listen error to a models.ListenerStatus
// Status value: "waiting" for errors that are plausibly transient (the
// address isn't available yet, e.g. an interface not up at boot) and
// "error" for everything else (e.g. EADDRINUSE, EACCES). Both classes
// are retried by Run; the distinction is purely informational for the
// dashboard. The message returned is err's own text.
func classifyBindError(err error) (status, message string) {
	if isAddrNotAvailable(err) {
		return "waiting", err.Error()
	}
	return "error", err.Error()
}
