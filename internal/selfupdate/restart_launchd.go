// restart_launchd.go implements Run's default restart behavior on
// macOS (SPEC-v0.7 §1): `launchctl kickstart -k
// system/com.cloudpulse.agent`, only as root, only when the
// LaunchDaemon plist is actually installed. No darwin build tag for
// the same reason as postupdate_launchd.go: the logic has nothing
// platform-specific about its Go code, only defaultRestart's dispatch
// (in run.go) decides to call it on darwin.
package selfupdate

import (
	"context"
	"fmt"
	"os"

	"github.com/leejeonghun001/cloud-pulse/internal/launchd"
)

// defaultRestartDarwin implements Run's documented default restart
// behavior on macOS: only as root, and only when
// launchd.PlistPath exists, runs `launchctl kickstart -k
// system/com.cloudpulse.agent` (bounded by restartTimeout). unit is
// accepted for signature symmetry with defaultRestartLinux/Windows but
// unused — the LaunchDaemon's identity (launchd.Label) is fixed,
// unlike systemd's per-binary unit name.
func defaultRestartDarwin(ctx context.Context, unit string) (bool, string, error) {
	_ = unit
	if os.Geteuid() != 0 {
		return false, "restart the cloud-pulse-agent LaunchDaemon manually to run the new version (not running as root)", nil
	}
	if _, err := os.Stat(launchd.PlistPath); err != nil {
		return false, "restart the cloud-pulse-agent LaunchDaemon manually to run the new version (no LaunchDaemon installed)", nil
	}

	restartCtx, cancel := context.WithTimeout(ctx, restartTimeout)
	defer cancel()

	var buf writerCapture
	if err := launchd.Kickstart(restartCtx, &buf); err != nil {
		return false, "", fmt.Errorf("launchctl kickstart: %w (output: %s)", err, buf.String())
	}
	return true, "", nil
}

// writerCapture is a minimal io.Writer -> string capture, avoiding a
// bytes.Buffer import just for this one small use (launchd.Kickstart's
// out parameter only needs Write).
type writerCapture struct {
	data []byte
}

func (w *writerCapture) Write(p []byte) (int, error) {
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *writerCapture) String() string {
	return string(w.data)
}
