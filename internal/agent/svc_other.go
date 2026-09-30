//go:build !windows

package agent

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
)

// IsWindowsService always reports false on non-Windows platforms. See
// svc_windows.go for the real Windows implementation.
func IsWindowsService() bool {
	return false
}

// RunAsService is unreachable on non-Windows platforms: main.go only
// ever calls it after IsWindowsService() has reported true, which
// never happens here. It exists so cmd/agent's service.go compiles
// identically on every OS without its own build tags.
func RunAsService(name string, runFunc func(ctx context.Context) error, logger *slog.Logger) error {
	return fmt.Errorf("agent: RunAsService is only supported on windows (running on %s)", runtime.GOOS)
}

// SimpleServiceStatus mirrors the windows-only type of the same name
// (see svcmgr_windows.go) so cmd/agent's service.go compiles on every
// OS without its own build tags.
type SimpleServiceStatus struct {
	State     uint32
	ProcessID uint32
}

// InstallWindowsService, UninstallWindowsService, StartWindowsService,
// StopWindowsService, and WindowsServiceStatus are all Windows-only
// (see svcmgr_windows.go); on every other OS they report a fixed
// "unsupported" error. cmd/agent's `service` subcommand itself already
// refuses to reach these on a non-Windows runtime.GOOS (see
// cmd/agent/service.go's own check) — these stubs exist purely so the
// package compiles cross-platform, not because they are ever expected
// to be called in practice off Windows.
func InstallWindowsService(binaryPath string) error {
	return windowsOnlyErr("install windows service")
}

func UninstallWindowsService() error {
	return windowsOnlyErr("uninstall windows service")
}

func StartWindowsService() error {
	return windowsOnlyErr("start windows service")
}

func StopWindowsService() error {
	return windowsOnlyErr("stop windows service")
}

func WindowsServiceStatus() (SimpleServiceStatus, error) {
	return SimpleServiceStatus{}, windowsOnlyErr("windows service status")
}

func windowsOnlyErr(op string) error {
	return fmt.Errorf("agent: %s: only supported on windows (running on %s)", op, runtime.GOOS)
}
