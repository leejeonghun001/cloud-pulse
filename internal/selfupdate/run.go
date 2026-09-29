package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// verifyTimeout bounds the default Verify implementation's subprocess
// call to "<path> -version".
const verifyTimeout = 10 * time.Second

// restartTimeout bounds the default Restart implementation's
// "systemctl restart <unit>" call.
const restartTimeout = 60 * time.Second

// Options configures a call to Run.
type Options struct {
	// Binary identifies which cloud-pulse binary is being updated:
	// "cloud-pulse-hub" or "cloud-pulse-agent". Required.
	Binary string
	// Current is the running binary's version string (typically
	// version.Version). Required.
	Current string
	// Target, when non-empty, pins the update to this exact release
	// tag instead of resolving the latest release. Setting Target
	// explicitly allows downgrading (Run still succeeds, with a
	// warning message).
	Target string
	// CheckOnly, when true, resolves the latest/target version and
	// reports whether an update is available without downloading or
	// replacing anything.
	CheckOnly bool
	// NoRestart, when true, skips the restart step even if Restart (or
	// its default systemd-based implementation) would otherwise have
	// restarted a running service.
	NoRestart bool
	// ExecPath is the path to the binary to replace. Empty resolves it
	// via os.Executable + filepath.EvalSymlinks.
	ExecPath string
	// GOOS and GOARCH select the release asset platform. Empty values use
	// runtime.GOOS and runtime.GOARCH respectively. They are primarily
	// useful to callers that need to select a supported release platform
	// independently of the process build target.
	GOOS, GOARCH string
	// Source describes where to fetch release info/assets from.
	Source Source
	// Verify validates a downloaded binary at path before it replaces
	// the running executable. nil uses the default: run
	// "<path> -version" with verifyTimeout and require tag to appear
	// in its combined output.
	Verify func(ctx context.Context, path, tag string) error
	// Restart attempts to restart the service running this binary
	// after a successful replace. nil uses the default systemd-based
	// implementation described in Run's doc comment. restarted
	// reports whether a restart was actually performed (as opposed to
	// e.g. "no active service found").
	Restart func(ctx context.Context, unit string) (restarted bool, msg string, err error)
	// Stdout, when non-nil, receives human-readable progress messages
	// as Run executes. nil discards them (callers that just want the
	// Result can ignore this).
	Stdout io.Writer
}

// Result reports the outcome of a Run call.
type Result struct {
	// Current is the version that was running before Run executed.
	Current string
	// Latest is the resolved latest (or explicit Target) version.
	Latest string
	// UpdateAvailable reports whether Latest is newer than Current.
	UpdateAvailable bool
	// Updated reports whether the binary was actually replaced.
	Updated bool
	// Restarted reports whether a service restart was performed.
	Restarted bool
	// Message is a human-readable summary suitable for printing
	// directly to the user.
	Message string
}

// Run resolves the target release, and — unless CheckOnly is set —
// downloads, verifies, and installs it in place of the currently running
// binary, then attempts a best-effort service restart.
//
// Default restart behavior (used when Options.Restart is nil): only on
// GOOS=linux, running as root (euid 0), with "systemctl" present in
// PATH, and a unit file at "/etc/systemd/system/<binary>.service": if
// `systemctl is-active --quiet <unit>` reports active, run
// `systemctl restart <unit>` (60s timeout) and report Restarted=true; if
// the unit exists but is inactive, it is deliberately left stopped
// (Restarted=false, explanatory message) rather than started, since Run
// has no way to know whether "inactive" was intentional. In every case
// where none of the above preconditions hold, the message instructs the
// operator to restart the binary manually.
func Run(ctx context.Context, o Options) (Result, error) {
	printf(o.Stdout, "cloud-pulse: checking for updates...")

	execPath, err := resolveExecPath(o.ExecPath)
	if err != nil {
		return Result{}, fmt.Errorf("selfupdate: resolve running executable: %w", err)
	}

	latest, err := resolveTarget(ctx, o)
	if err != nil {
		return Result{}, err
	}

	_, currentIsRelease := version.Parse(o.Current)
	updateAvailable := version.IsNewer(latest, o.Current)

	result := Result{Current: o.Current, Latest: latest, UpdateAvailable: updateAvailable}

	// A dev build (unparsable Current) may only update with an explicit
	// Target; otherwise there's no meaningful "is newer" comparison to
	// make and Run refuses rather than guessing.
	if !currentIsRelease && o.Target == "" {
		result.Message = "development build; use --version vX.Y.Z to install a release"
		return result, nil
	}

	if o.Target == "" && !updateAvailable {
		result.Message = fmt.Sprintf("already up to date (%s)", o.Current)
		return result, nil
	}

	warnIfDowngradeBelowSelfUpdate(o.Stdout, latest)

	if o.CheckOnly {
		if updateAvailable || o.Target != "" {
			result.Message = fmt.Sprintf("update available: %s -> %s", o.Current, latest)
		} else {
			result.Message = fmt.Sprintf("already up to date (%s)", o.Current)
		}
		return result, nil
	}

	if err := checkPermission(execPath); err != nil {
		return result, err
	}

	goos, goarch := o.GOOS, o.GOARCH
	runtimeGOOS, runtimeGOARCH := currentGOOSGOARCH()
	if goos == "" {
		goos = runtimeGOOS
	}
	if goarch == "" {
		goarch = runtimeGOARCH
	}
	asset, err := AssetName(o.Binary, goos, goarch)
	if err != nil {
		return result, err
	}

	dir := filepath.Dir(execPath)
	tmpDir, err := os.MkdirTemp(dir, ".cloud-pulse-update-*")
	if err != nil {
		return result, fmt.Errorf("selfupdate: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	printf(o.Stdout, "cloud-pulse: downloading %s %s...", asset, latest)
	downloadedPath, err := o.Source.Fetch(ctx, latest, asset, tmpDir)
	if err != nil {
		return result, err
	}

	// Verify (default implementation) executes the downloaded file, so
	// it must be executable before Verify runs, not only once Replace
	// chmods it as part of installing it onto execPath.
	if err := os.Chmod(downloadedPath, 0o755); err != nil {
		return result, fmt.Errorf("selfupdate: chmod downloaded binary: %w", err)
	}

	verify := o.Verify
	if verify == nil {
		verify = defaultVerify
	}
	printf(o.Stdout, "cloud-pulse: verifying downloaded binary...")
	if err := verify(ctx, downloadedPath, latest); err != nil {
		return result, fmt.Errorf("selfupdate: verify downloaded binary: %w", err)
	}

	// Replace requires newFile to be in the same directory as target;
	// tmpDir is created as a sibling directory of execPath, but the
	// file itself must be moved to be a direct sibling file, not nested
	// in a subdirectory. Move it up one level (still same filesystem).
	replacementPath := filepath.Join(dir, filepath.Base(downloadedPath))
	if err := os.Rename(downloadedPath, replacementPath); err != nil {
		return result, fmt.Errorf("selfupdate: stage replacement binary: %w", err)
	}
	defer func() { _ = os.Remove(replacementPath) }() // no-op after a successful Replace (file already moved onto execPath)

	if err := Replace(execPath, replacementPath); err != nil {
		return result, err
	}
	result.Updated = true
	printf(o.Stdout, "cloud-pulse: installed %s", latest)

	restart := o.Restart
	if restart == nil {
		restart = defaultRestart
	}

	if o.NoRestart {
		result.Message = fmt.Sprintf("%s: %s -> %s installed; restart manually to run the new version", o.Binary, o.Current, latest)
		return result, nil
	}

	unit := o.Binary + ".service"
	restarted, msg, err := restart(ctx, unit)
	if err != nil {
		// A restart failure does not undo the already-successful
		// binary replacement; report it but don't treat Run as
		// failed overall (the new binary is correctly installed).
		result.Message = fmt.Sprintf("%s: %s -> %s installed; restart failed: %v", o.Binary, o.Current, latest, err)
		return result, nil
	}
	result.Restarted = restarted
	if restarted {
		result.Message = fmt.Sprintf("%s: %s -> %s installed; restarted %s", o.Binary, o.Current, latest, unit)
	} else {
		result.Message = fmt.Sprintf("%s: %s -> %s installed; %s", o.Binary, o.Current, latest, msg)
	}
	return result, nil
}

// resolveTarget returns o.Target if set (after validating it), otherwise
// resolves and returns the latest release tag from o.Source.
func resolveTarget(ctx context.Context, o Options) (string, error) {
	if o.Target != "" {
		if !version.ValidTag(o.Target) {
			return "", fmt.Errorf("selfupdate: invalid --version %q", o.Target)
		}
		return o.Target, nil
	}
	latest, err := o.Source.Latest(ctx)
	if err != nil {
		return "", fmt.Errorf("selfupdate: resolve latest release: %w", err)
	}
	return latest, nil
}

// warnIfDowngradeBelowSelfUpdate prints a warning to out when target is
// older than version.SelfUpdateSince, since a binary at that version has
// no `update` subcommand to upgrade itself back with.
func warnIfDowngradeBelowSelfUpdate(out io.Writer, target string) {
	since, ok := version.Parse(version.SelfUpdateSince)
	if !ok {
		return
	}
	t, ok := version.Parse(target)
	if !ok {
		return
	}
	if version.Compare(t, since) < 0 {
		printf(out, "cloud-pulse: warning: %s predates %s and has no `update` subcommand; "+
			"you will need to re-run the install script to upgrade again", target, version.SelfUpdateSince)
	}
}

// checkPermission verifies that execPath's directory is writable,
// returning ErrPermission (wrapped with a hint) if not. This runs before
// any network activity so a permission problem is reported immediately.
func checkPermission(execPath string) error {
	dir := filepath.Dir(execPath)
	if !dirWritable(dir) {
		return fmt.Errorf("%w: %s is not writable", ErrPermission, dir)
	}
	return nil
}

// resolveExecPath returns explicit if non-empty, otherwise
// os.Executable() resolved through filepath.EvalSymlinks (so a symlinked
// invocation, e.g. via PATH, resolves to the real underlying file that
// Replace should overwrite).
func resolveExecPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("filepath.EvalSymlinks(%s): %w", p, err)
	}
	return resolved, nil
}

// defaultVerify runs "<path> -version" with verifyTimeout and requires
// tag to appear somewhere in its combined stdout+stderr output.
func defaultVerify(ctx context.Context, path, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "-version") //nolint:gosec // path is our own just-downloaded, checksum-verified binary
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s -version: %w (output: %s)", path, err, buf.String())
	}
	if !strings.Contains(buf.String(), tag) {
		return fmt.Errorf("output of %s -version does not mention %s (output: %s)", path, tag, buf.String())
	}
	return nil
}

// defaultRestart implements Run's documented default restart behavior:
// systemctl-based restart, only on Linux, only as root, only for a unit
// that exists and is already active.
func defaultRestart(ctx context.Context, unit string) (bool, string, error) {
	if runtime.GOOS != "linux" {
		return false, fmt.Sprintf("restart %s manually to run the new version", unit), nil
	}
	if os.Geteuid() != 0 {
		return false, fmt.Sprintf("restart %s manually to run the new version (not running as root)", unit), nil
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false, fmt.Sprintf("restart %s manually to run the new version (systemctl not found)", unit), nil
	}

	unitPath := filepath.Join("/etc/systemd/system", unit)
	if _, err := os.Stat(unitPath); err != nil {
		return false, fmt.Sprintf("restart %s manually to run the new version (no systemd unit installed)", unit), nil
	}

	restartCtx, cancel := context.WithTimeout(ctx, restartTimeout)
	defer cancel()

	isActiveCmd := exec.CommandContext(restartCtx, "systemctl", "is-active", "--quiet", unit) //nolint:gosec // unit is derived from a fixed Options.Binary value, not user input
	active := isActiveCmd.Run() == nil
	if !active {
		return false, fmt.Sprintf("%s is not active; not starting it automatically", unit), nil
	}

	restartCmd := exec.CommandContext(restartCtx, "systemctl", "restart", unit) //nolint:gosec // unit is derived from a fixed Options.Binary value, not user input
	var buf bytes.Buffer
	restartCmd.Stdout = &buf
	restartCmd.Stderr = &buf
	if err := restartCmd.Run(); err != nil {
		return false, "", fmt.Errorf("systemctl restart %s: %w (output: %s)", unit, err, buf.String())
	}
	return true, "", nil
}

// printf writes a formatted, newline-terminated progress message to out
// if out is non-nil.
func printf(out io.Writer, format string, args ...any) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, format+"\n", args...)
}
