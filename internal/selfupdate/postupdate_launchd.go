// postupdate_launchd.go implements Run's default PostUpdate behavior on
// macOS (SPEC-v0.7 §1): re-render and kickstart the
// com.cloudpulse.agent LaunchDaemon plist via "<binPath> plist apply",
// mirroring defaultPostUpdateLinux's "<binPath> systemd-unit apply"
// role for Linux. This file has no darwin build tag — the logic itself
// (root check via os.Geteuid, tag-gating, invoking the just-replaced
// binary's own "plist apply" subcommand) has nothing OS-specific about
// its Go code, so it is covered by ordinary cross-platform unit tests;
// only the *decision* to call it (defaultPostUpdateFor's runtime.GOOS
// switch in run.go) is darwin-specific. This mirrors defaultPostUpdateLinux's
// own lack of a linux build tag for the same reason.
package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/leejeonghun001/cloud-pulse/internal/launchd"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// defaultPostUpdateDarwin returns Run's default PostUpdate
// implementation for macOS: it applies LaunchDaemon plist changes via
// "<binPath> plist apply" — but only when every one of the following
// holds, so that a downgrade or an unmanaged host is always a safe
// no-op rather than an error:
//
//   - running as root (euid 0): the plist under
//     /Library/LaunchDaemons requires root to write
//   - the plist file exists at launchd.PlistPath: an unmanaged/dev
//     invocation (no installed LaunchDaemon) has nothing to apply
//   - tag (the version just installed) is at or after
//     version.LaunchdManagedSince: only a binary from that tag onward
//     is guaranteed to itself understand "plist apply"
func defaultPostUpdateDarwin(binary string) func(ctx context.Context, binPath, tag string) (string, error) {
	return func(ctx context.Context, binPath, tag string) (string, error) {
		if binary != "cloud-pulse-agent" {
			// Only the agent ships a launchd plist; the hub has no
			// macOS deployment story at all (SPEC-v0.7 §1 is agent-only).
			return "", nil
		}
		if os.Geteuid() != 0 {
			return "", nil
		}
		if !isLaunchdManaged(tag) {
			return "", nil
		}
		if _, err := os.Stat(launchd.PlistPath); err != nil {
			return "", nil
		}

		applyCtx, cancel := context.WithTimeout(ctx, postUpdateTimeout)
		defer cancel()

		cmd := exec.CommandContext(applyCtx, binPath, "plist", "apply") //nolint:gosec // binPath is our own just-replaced, checksum-verified binary
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%s plist apply: %w (output: %s)", binPath, err, buf.String())
		}
		return strings.TrimRight(buf.String(), "\n"), nil
	}
}

// isLaunchdManaged reports whether tag is at or after
// version.LaunchdManagedSince. An unparsable tag is conservatively
// treated as not launchd-managed.
func isLaunchdManaged(tag string) bool {
	return version.AtLeastIncludingPrerelease(tag, version.LaunchdManagedSince)
}
