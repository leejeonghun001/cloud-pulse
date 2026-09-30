//go:build !windows

package selfupdate

import (
	"context"
	"fmt"
	"runtime"
)

// defaultRestartWindows is unreachable on non-Windows platforms:
// defaultRestart's runtime.GOOS switch (run.go) only calls it when
// GOOS is "windows", which is never true here. Exists so run.go
// compiles cross-platform without its own build tags.
func defaultRestartWindows(ctx context.Context, unit string) (bool, string, error) {
	return false, "", fmt.Errorf("selfupdate: windows restart is not supported on %s", runtime.GOOS)
}
