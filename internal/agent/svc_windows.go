//go:build windows

package agent

import (
	"context"
	"log/slog"

	"golang.org/x/sys/windows/svc"
)

// windowsSvcHandler adapts ServiceHandler to golang.org/x/sys/windows/svc.Handler,
// translating real svc.ChangeRequest/svc.Status values to/from this
// package's platform-independent ServiceChangeCmd/ServiceState types
// (see svc_handler.go) and running runFunc (the normal agent run loop)
// to completion in its own goroutine, reporting svc.Running once
// started and svc.Stopped once runFunc returns.
type windowsSvcHandler struct {
	runFunc func(ctx context.Context) error
	logger  *slog.Logger
}

// RunAsService starts the Windows service main loop: it blocks until
// the SCM stops the service, at which point it has already canceled
// runFunc's context and waited for runFunc to return. runFunc is the
// normal agent run loop (agent.RunWithOptions via cmd/agent's runAgent),
// taking a context that's canceled on a Stop/Shutdown control request.
// isDebug controls whether svc.Run or svc.debug's interactive runner is
// used — always false in production; only ever true from a manual
// `service debug` invocation for local troubleshooting on a real
// Windows machine (not exercised by any test, since it blocks on
// console input).
func RunAsService(name string, runFunc func(ctx context.Context) error, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	h := &windowsSvcHandler{runFunc: runFunc, logger: logger}
	return svc.Run(name, h)
}

// Execute implements svc.Handler. It starts runFunc in a background
// goroutine immediately (reporting StartPending while doing so, then
// Running), then loops reading control requests from r until a
// Stop/Shutdown is observed and runFunc has returned, at which point it
// reports Stopped and returns.
func (h *windowsSvcHandler) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	s <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- h.runFunc(ctx)
	}()

	handler := NewServiceHandler(cancel, h.logger)
	s <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case req := <-r:
			cmd := mapWindowsCmd(req.Cmd)
			state := handler.Handle(cmd)
			s <- svc.Status{State: mapAgentState(state), Accepts: accepts}
			if handler.Stopped() {
				// Wait for the run loop to actually finish before
				// reporting Stopped, so the SCM never observes this
				// service as stopped while runFunc might still be
				// flushing a final report.
				<-runDone
				s <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-runDone:
			// The run loop exited on its own (not via a Stop request
			// — e.g. an unrecoverable error). Report Stopped
			// immediately; a non-nil err becomes a service-specific
			// exit code so `sc query` / Event Viewer shows failure.
			if err != nil {
				h.logger.Error("agent run loop exited unexpectedly", "error", err)
				s <- svc.Status{State: svc.Stopped}
				return true, 1
			}
			s <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

// mapWindowsCmd converts a real svc.Cmd into this package's
// build-tag-free ServiceChangeCmd.
func mapWindowsCmd(cmd svc.Cmd) ServiceChangeCmd {
	switch cmd {
	case svc.Stop:
		return ServiceCmdStop
	case svc.Shutdown:
		return ServiceCmdShutdown
	case svc.Interrogate:
		return ServiceCmdInterrogate
	default:
		return ServiceCmdOther
	}
}

// mapAgentState converts this package's build-tag-free ServiceState
// into a real svc.State.
func mapAgentState(s ServiceState) svc.State {
	switch s {
	case ServiceStateStartPending:
		return svc.StartPending
	case ServiceStateRunning:
		return svc.Running
	case ServiceStateStopPending:
		return svc.StopPending
	case ServiceStateStopped:
		return svc.Stopped
	default:
		return svc.Running
	}
}

// IsWindowsService reports whether this process is currently running
// under the Windows Service Control Manager (as opposed to an
// interactive console invocation like `cloud-pulse-agent service
// start` or a manual double-click). cmd/agent's main uses this to
// decide whether to call RunAsService instead of the normal run() path.
func IsWindowsService() bool {
	isSvc, err := svc.IsWindowsService()
	return err == nil && isSvc
}
