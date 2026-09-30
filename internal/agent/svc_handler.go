// svc_handler.go implements the platform-independent core of the
// Windows service handler (SPEC-v0.7 §1): translating service
// stop/shutdown control requests into a context cancellation, so the
// normal agent run loop (agent.RunWithOptions) shuts down gracefully
// exactly as it does on SIGINT/SIGTERM under systemd. The actual
// golang.org/x/sys/windows/svc wiring (svc.Run, the Handler interface,
// ChangeRequest/Status types) lives in svc_windows.go, which is
// windows-only and therefore untestable on this development machine —
// this file exists so the request-handling *logic* itself (not the
// Windows API glue) is covered by ordinary, portable Go tests using a
// fake channel-based driver.
package agent

import (
	"context"
	"log/slog"
)

// ServiceChangeCmd is a platform-independent enum mirroring the small
// subset of golang.org/x/sys/windows/svc.Cmd values this handler cares
// about. svc_windows.go maps the real svc.Cmd constants onto these
// before calling ServiceHandler.Handle, so ServiceHandler itself has no
// build-tagged dependency and can be exercised by ordinary tests on
// every platform.
type ServiceChangeCmd int

const (
	// ServiceCmdInterrogate requests the handler report its current
	// status without changing it.
	ServiceCmdInterrogate ServiceChangeCmd = iota
	// ServiceCmdStop and ServiceCmdShutdown both request a graceful
	// shutdown (Stop: SCM-initiated; Shutdown: system shutdown) — this
	// handler treats them identically, matching how the agent's own
	// signal.NotifyContext(SIGINT, SIGTERM) treats both signals
	// identically on Unix.
	ServiceCmdStop
	ServiceCmdShutdown
	// ServiceCmdOther is any control command this handler does not
	// specifically act on (e.g. ParamChange) — reported back as
	// "still running," never causing a state change.
	ServiceCmdOther
)

// ServiceState mirrors the small subset of svc.State values this
// handler reports, for the same build-tag-avoidance reason as
// ServiceChangeCmd.
type ServiceState int

const (
	ServiceStateStartPending ServiceState = iota
	ServiceStateRunning
	ServiceStateStopPending
	ServiceStateStopped
)

// ServiceHandler drives a Windows service's lifecycle: Handle is called
// once per control request the SCM delivers, and StopFunc (set to the
// run loop's context.CancelFunc) is invoked exactly once, the first
// time a Stop or Shutdown request arrives — mirroring
// signal.NotifyContext's own "first signal cancels, subsequent signals
// are no-ops beyond what the context.CancelFunc contract already
// guarantees" behavior.
//
// A zero-value ServiceHandler is not usable; construct with
// NewServiceHandler.
type ServiceHandler struct {
	cancel  context.CancelFunc
	logger  *slog.Logger
	stopped bool
}

// NewServiceHandler returns a ServiceHandler whose Handle method calls
// cancel exactly once, on the first Stop/Shutdown request it observes.
// logger may be nil (defaults to a discarding logger).
func NewServiceHandler(cancel context.CancelFunc, logger *slog.Logger) *ServiceHandler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &ServiceHandler{cancel: cancel, logger: logger}
}

// Handle processes a single control request, returning the ServiceState
// to report back to the SCM. Interrogate and any command in
// ServiceCmdOther leave the handler's own stopped/running bookkeeping
// untouched, always reporting ServiceStateRunning unless a prior
// Stop/Shutdown is already in progress (in which case
// ServiceStateStopPending is reported instead, so a stray Interrogate
// during shutdown doesn't misreport "running").
func (h *ServiceHandler) Handle(cmd ServiceChangeCmd) ServiceState {
	switch cmd {
	case ServiceCmdStop, ServiceCmdShutdown:
		if !h.stopped {
			h.stopped = true
			h.logger.Info("service control request received; shutting down", "cmd", cmd)
			h.cancel()
		}
		return ServiceStateStopPending
	default:
		if h.stopped {
			return ServiceStateStopPending
		}
		return ServiceStateRunning
	}
}

// Stopped reports whether a Stop/Shutdown request has been observed.
func (h *ServiceHandler) Stopped() bool {
	return h.stopped
}
