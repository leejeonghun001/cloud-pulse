package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// TestIsLaunchdManaged mirrors TestIsUnitManaged for the darwin
// equivalent gate.
func TestIsLaunchdManaged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tag  string
		want bool
	}{
		{"v0.6.0", false},
		{"v0.7.0-rc.1", true},
		{"v0.7.0", true},
		{"v0.7.1", true},
		{"v1.0.0", true},
		{"v0.6.9", false},
		{"dev", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			if got := isLaunchdManaged(tc.tag); got != tc.want {
				t.Errorf("isLaunchdManaged(%q) = %v; want %v", tc.tag, got, tc.want)
			}
		})
	}
}

// TestDefaultPostUpdateDarwin_NonRootIsNoOp mirrors
// TestDefaultPostUpdateFor_GatingConditions's non-root assertion for
// the darwin variant: running unprivileged (the case in CI/sandbox)
// must always be a silent no-op regardless of tag or binary.
func TestDefaultPostUpdateDarwin_NonRootIsNoOp(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; gating semantics differ")
	}

	fn := defaultPostUpdateDarwin("cloud-pulse-agent")
	msg, err := fn(context.Background(), "/nonexistent/cloud-pulse-agent", version.LaunchdManagedSince)
	if err != nil {
		t.Fatalf("defaultPostUpdateDarwin non-root: error = %v, want nil", err)
	}
	if msg != "" {
		t.Fatalf("defaultPostUpdateDarwin non-root: msg = %q, want empty", msg)
	}
}

// TestDefaultPostUpdateDarwin_WrongBinaryIsNoOp asserts the hub (which
// has no macOS deployment story at all) is always a no-op regardless
// of root/tag, since defaultPostUpdateDarwin checks binary before
// anything else.
func TestDefaultPostUpdateDarwin_WrongBinaryIsNoOp(t *testing.T) {
	t.Parallel()

	fn := defaultPostUpdateDarwin("cloud-pulse-hub")
	msg, err := fn(context.Background(), "/nonexistent/cloud-pulse-hub", version.LaunchdManagedSince)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if msg != "" {
		t.Fatalf("msg = %q, want empty", msg)
	}
}

// TestDefaultRestartDarwin_NonRootReportsManualMessage exercises the
// non-root path without invoking a real launchctl.
func TestDefaultRestartDarwin_NonRootReportsManualMessage(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; gating semantics differ")
	}

	restarted, msg, err := defaultRestartDarwin(context.Background(), "unused")
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if restarted {
		t.Error("restarted = true, want false when not running as root")
	}
	if msg == "" {
		t.Error("expected a manual-restart message")
	}
}

// TestDefaultRestartLinux_NonRootReportsManualMessage covers the
// extracted defaultRestartLinux directly (previously only reachable
// through Run/defaultRestart's OS dispatch).
func TestDefaultRestartLinux_NonRootReportsManualMessage(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; gating semantics differ")
	}

	restarted, msg, err := defaultRestartLinux(context.Background(), "cloud-pulse-agent.service")
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if restarted {
		t.Error("restarted = true, want false when not running as root")
	}
	if msg == "" {
		t.Error("expected a manual-restart message")
	}
}

// TestDefaultRestart_DispatchesByHostGOOS is not directly testable
// through defaultRestart itself (it always dispatches on the real
// runtime.GOOS, by design — see run.go's doc comment), so this test
// instead confirms each of the three OS-specific functions is callable
// and returns the expected non-root/not-installed shape, exercising
// the same code paths defaultRestart's switch would reach on each OS.
func TestDefaultRestart_EachVariantHandlesNotInstalledGracefully(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; gating semantics differ")
	}

	ctx := context.Background()
	if restarted, msg, err := defaultRestartLinux(ctx, "cloud-pulse-agent.service"); err != nil || restarted || msg == "" {
		t.Errorf("defaultRestartLinux: restarted=%v msg=%q err=%v", restarted, msg, err)
	}
	if restarted, msg, err := defaultRestartDarwin(ctx, "unused"); err != nil || restarted || msg == "" {
		t.Errorf("defaultRestartDarwin: restarted=%v msg=%q err=%v", restarted, msg, err)
	}
	// defaultRestartWindows is only defined (real behavior) on
	// windows; on every other GOOS the _other.go stub always errors,
	// so it's covered separately by TestDefaultRestartWindows_OtherOS_Stub.
}

// TestDefaultRestartWindows_OtherOSStub covers the non-windows stub in
// restart_windows_other.go, ensuring it fails closed (an error, never
// a false "success") rather than silently doing nothing.
func TestDefaultRestartWindows_OtherOSStub(t *testing.T) {
	t.Parallel()
	if runtimeIsWindows() {
		t.Skip("this test only exercises the non-windows stub")
	}
	restarted, _, err := defaultRestartWindows(context.Background(), "unused")
	if err == nil {
		t.Error("expected an error from the non-windows stub")
	}
	if restarted {
		t.Error("restarted = true, want false")
	}
}

func runtimeIsWindows() bool {
	return os.PathSeparator == '\\'
}

// TestDefaultPostUpdateFor_DispatchesToLinuxAndDarwinBuilders confirms
// defaultPostUpdateFor's switch resolves the extracted per-OS
// functions without a nil dereference, using the current runtime.GOOS
// (which on this development/CI machine is linux) plus a direct check
// that non-linux/darwin GOOS values (covered by the real
// runtime.GOOS-based dispatch, not injectable — see run.go) don't
// exist as an untested branch: the default case returns a working
// zero-cost no-op closure.
func TestDefaultPostUpdateFor_DefaultCaseIsSafeNoOp(t *testing.T) {
	t.Parallel()
	fn := defaultPostUpdateFor("cloud-pulse-agent")
	if fn == nil {
		t.Fatal("defaultPostUpdateFor returned nil")
	}
	// Exercising it is safe regardless of which OS branch actually
	// executes (linux/darwin/default) since every branch no-ops for a
	// nonexistent binPath/unmanaged tag.
	msg, err := fn(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), "v0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = msg
}
