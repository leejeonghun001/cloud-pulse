// updater.go implements the polling loop for the Windows update-helper
// service, cloud-pulse-agent-updater (SPEC-v0.7 §1): unlike Linux's
// systemd .path unit (an inotify-backed watch) or macOS's launchd
// WatchPaths key (an FSEvents-backed watch), Windows has no equivalent
// file-watch trigger wired into this project's service model, so the
// updater instead polls the request file's existence on a fixed
// interval — "Windows에는 파일 감시 트리거가 없어 폴링이 가장 단순하고
// 안전하다" per SPEC-v0.7 §1.
//
// This file is deliberately platform-independent (no windows build
// tag): the polling loop itself — "wait, check for a request file,
// invoke a callback, repeat until canceled" — has no Windows API
// dependency, so it is fully covered by ordinary Go tests using an
// injected clock and a fake filesystem-check function, matching this
// project's "inject clocks/hooks, never sleep on wall-clock granularity"
// testing convention. The actual Windows service registration for
// cloud-pulse-agent-updater (a second, LocalSystem, always-running
// service distinct from the main agent service) is wired through the
// same svcmgr_windows.go/svc_windows.go machinery as the main agent
// service, parameterized by service name.
package agent

import (
	"context"
	"log/slog"
	"time"
)

// DefaultUpdaterPollInterval is the polling interval SPEC-v0.7 §1
// specifies for the Windows update-helper service ("30초 간격").
const DefaultUpdaterPollInterval = 30 * time.Second

// UpdaterOptions configures RunUpdaterLoop.
type UpdaterOptions struct {
	// PollInterval overrides DefaultUpdaterPollInterval. Zero uses the
	// default; tests set this to a much smaller value alongside an
	// injected Clock/Sleep so the loop advances immediately rather
	// than depending on wall-clock sleep granularity (which differs
	// significantly on Windows, ~15.6ms — see this project's own CI
	// lessons).
	PollInterval time.Duration
	// RequestExists reports whether an update request file is
	// currently present. Required.
	RequestExists func() bool
	// ProcessRequest is invoked once each time RequestExists reports
	// true, expected to run the same "update --from-request"-equivalent
	// pipeline the Linux/macOS paths use (selfupdate.RunFromRequest) and
	// remove/consume the request file itself so the next poll doesn't
	// reprocess it. A returned error is logged but never stops the
	// loop — a single failed update attempt should not wedge the
	// updater service, since the *next* hub-delivered request (a fresh
	// job ID) still deserves an attempt.
	ProcessRequest func(ctx context.Context) error
	// Sleep overrides the loop's wait step; nil uses a real
	// time.Timer against PollInterval. Tests inject a fast
	// channel-driven stand-in instead of relying on any real
	// wall-clock sleep.
	Sleep func(ctx context.Context, d time.Duration) bool
	// Logger receives progress/error logs; nil discards them.
	Logger *slog.Logger
}

// RunUpdaterLoop polls o.RequestExists every o.PollInterval (or
// DefaultUpdaterPollInterval) until ctx is canceled, calling
// o.ProcessRequest each time a request is observed. It returns when ctx
// is done (nil error — cancellation is the loop's normal, expected
// termination, not a failure).
func RunUpdaterLoop(ctx context.Context, o UpdaterOptions) error {
	interval := o.PollInterval
	if interval <= 0 {
		interval = DefaultUpdaterPollInterval
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	sleep := o.Sleep
	if sleep == nil {
		sleep = realSleep
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		if o.RequestExists != nil && o.RequestExists() {
			logger.Info("update request file detected; processing")
			if o.ProcessRequest != nil {
				if err := o.ProcessRequest(ctx); err != nil {
					logger.Error("update request processing failed", "error", err)
				}
			}
		}
		if !sleep(ctx, interval) {
			return nil
		}
	}
}

// realSleep waits for d or ctx cancellation, returning false iff ctx
// was canceled first (mirroring the sentinel RunUpdaterLoop's loop
// checks to decide whether to exit).
func realSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
