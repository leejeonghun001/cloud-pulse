//go:build windows

// Package selfupdate: restart_windows.go implements Run's default
// restart behavior on Windows (SPEC-v0.7 §1): stop then start the
// cloud-pulse-agent service via `sc.exe`, invoked with a fixed
// argument array (never a shell string, per this project's exec-rules
// convention — see CODING_CONVENTIONS.md's "Exec rules" section), only
// when the service is actually installed. This file deliberately
// avoids importing internal/agent's golang.org/x/sys/windows/svc/mgr-based
// control functions to keep internal/selfupdate's own dependency
// surface unchanged (sc.exe is already present on every Windows
// install, no new dependency); it is windows-only since `sc.exe` and
// Windows service semantics don't exist elsewhere.
package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// windowsServiceName is the fixed Windows service name the agent
// registers itself under (mirrors internal/agent.WindowsServiceName;
// duplicated as a literal here rather than importing internal/agent,
// matching this project's stated preference — see
// selfupdate/fromrequest.go's own doc comment — for independently
// defined constants over cross-package coupling for a value that must
// change, if ever, as a deliberate decision visible in both places).
const windowsServiceName = "cloud-pulse-agent"

// defaultRestartWindows implements Run's documented default restart
// behavior on Windows: queries the service; if not installed, returns
// the manual-restart message; if installed, stops it (best-effort —
// "already stopped" is not an error) and starts it again, bounded by
// restartTimeout. unit is accepted for signature symmetry with
// defaultRestartLinux/Darwin but unused (the Windows service name is
// fixed).
func defaultRestartWindows(ctx context.Context, unit string) (bool, string, error) {
	_ = unit

	if _, err := exec.LookPath("sc.exe"); err != nil {
		return false, "restart the cloud-pulse-agent service manually to run the new version (sc.exe not found)", nil
	}

	restartCtx, cancel := context.WithTimeout(ctx, restartTimeout)
	defer cancel()

	queryCmd := exec.CommandContext(restartCtx, "sc.exe", "query", windowsServiceName) //nolint:gosec // fixed argv, no user input
	if err := queryCmd.Run(); err != nil {
		return false, "restart the cloud-pulse-agent service manually to run the new version (service not installed)", nil
	}

	// Best-effort stop: an already-stopped service returns a non-zero
	// exit code from `sc stop`, which is not itself an error condition
	// here — the subsequent `sc start` is what actually matters.
	stopCmd := exec.CommandContext(restartCtx, "sc.exe", "stop", windowsServiceName) //nolint:gosec // fixed argv, no user input
	_ = stopCmd.Run()

	startCmd := exec.CommandContext(restartCtx, "sc.exe", "start", windowsServiceName) //nolint:gosec // fixed argv, no user input
	var buf bytes.Buffer
	startCmd.Stdout = &buf
	startCmd.Stderr = &buf
	if err := startCmd.Run(); err != nil {
		return false, "", fmt.Errorf("sc.exe start %s: %w (output: %s)", windowsServiceName, err, buf.String())
	}
	return true, "", nil
}
