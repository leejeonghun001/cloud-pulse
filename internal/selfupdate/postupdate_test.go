package selfupdate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// TestRun_PostUpdate_CalledBetweenReplaceAndRestart asserts the
// documented ordering: Replace, then PostUpdate, then Restart. It
// injects both PostUpdate and Restart and records the state of the
// replaced binary at each call time.
func TestRun_PostUpdate_CalledBetweenReplaceAndRestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("old binary v0.3.0"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	newContent := []byte("new binary v0.3.1")
	_, source := newFakeReleaseServer(t, "v0.3.1", "cloud-pulse-hub", testGOOS, testGOARCH, newContent)

	var order []string
	postUpdate := func(ctx context.Context, binPath, tag string) (string, error) {
		order = append(order, "postupdate")
		got, err := os.ReadFile(binPath) //nolint:gosec // test-controlled path inside t.TempDir()
		if err != nil {
			t.Fatalf("read binPath in PostUpdate: %v", err)
		}
		if string(got) != string(newContent) {
			t.Errorf("PostUpdate observed pre-replace content %q; want post-replace %q", got, newContent)
		}
		if tag != "v0.3.1" {
			t.Errorf("PostUpdate tag = %q; want v0.3.1", tag)
		}
		return "post-update ok", nil
	}
	restart := func(ctx context.Context, unit string) (bool, string, error) {
		order = append(order, "restart")
		return true, "", nil
	}

	result, err := Run(context.Background(), Options{
		Binary:     "cloud-pulse-hub",
		GOOS:       testGOOS,
		GOARCH:     testGOARCH,
		Current:    "v0.3.0",
		ExecPath:   execPath,
		Source:     source,
		Verify:     func(ctx context.Context, path, tag string) error { return nil },
		PostUpdate: postUpdate,
		Restart:    restart,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !result.Updated {
		t.Fatal("result.Updated = false; want true")
	}
	if len(order) != 2 || order[0] != "postupdate" || order[1] != "restart" {
		t.Fatalf("call order = %v; want [postupdate restart]", order)
	}
}

// TestRun_PostUpdate_FailureDoesNotFailRun asserts that a PostUpdate
// error is surfaced only as a warning message, never as a Run error,
// and Restart still runs afterward.
func TestRun_PostUpdate_FailureDoesNotFailRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	_, source := newFakeReleaseServer(t, "v0.3.1", "cloud-pulse-hub", testGOOS, testGOARCH, []byte("new binary"))

	restartCalled := false
	postUpdate := func(ctx context.Context, binPath, tag string) (string, error) {
		return "", errFakePostUpdate{}
	}
	restart := func(ctx context.Context, unit string) (bool, string, error) {
		restartCalled = true
		return true, "", nil
	}

	var stdout bytes.Buffer
	result, err := Run(context.Background(), Options{
		Binary:     "cloud-pulse-hub",
		GOOS:       testGOOS,
		GOARCH:     testGOARCH,
		Current:    "v0.3.0",
		ExecPath:   execPath,
		Source:     source,
		Verify:     func(ctx context.Context, path, tag string) error { return nil },
		PostUpdate: postUpdate,
		Restart:    restart,
		Stdout:     &stdout,
	})
	if err != nil {
		t.Fatalf("Run() error: %v; PostUpdate failure must not fail Run", err)
	}
	if !result.Updated {
		t.Fatal("result.Updated = false; want true despite PostUpdate failure")
	}
	if !restartCalled {
		t.Fatal("Restart was not called after a PostUpdate failure")
	}
	if !strings.Contains(stdout.String(), "post-update step failed") {
		t.Errorf("stdout = %q; want a post-update failure warning", stdout.String())
	}
}

type errFakePostUpdate struct{}

func (errFakePostUpdate) Error() string { return "fake post-update failure" }

// TestDefaultPostUpdateFor_GatingConditions exercises
// defaultPostUpdateFor's no-op preconditions without requiring root or a
// real systemd unit: non-root always skips regardless of tag/OS, and a
// tag older than version.UnitManagedSince always skips regardless of
// root.
func TestDefaultPostUpdateFor_GatingConditions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(binPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	tests := []struct {
		name string
		tag  string
	}{
		{"tag before UnitManagedSince", "v0.3.0"},
		{"tag at UnitManagedSince", version.UnitManagedSince},
		{"tag after UnitManagedSince", "v0.9.9"},
		{"unparsable tag", "dev"},
	}

	fn := defaultPostUpdateFor("cloud-pulse-hub")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Not running as root in CI/sandbox: every case must be a
			// silent no-op (empty message, nil error) regardless of
			// tag, since defaultPostUpdateFor checks euid before tag.
			if os.Geteuid() == 0 {
				t.Skip("running as root; gating semantics differ, skip to avoid mutating a real systemd unit")
			}
			msg, err := fn(context.Background(), binPath, tc.tag)
			if err != nil {
				t.Fatalf("defaultPostUpdateFor(...)(%q) error = %v; want nil (non-root no-op)", tc.tag, err)
			}
			if msg != "" {
				t.Fatalf("defaultPostUpdateFor(...)(%q) msg = %q; want empty (non-root no-op)", tc.tag, msg)
			}
		})
	}
}

// TestIsUnitManaged covers the tag comparison defaultPostUpdateFor's
// gating relies on.
func TestIsUnitManaged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tag  string
		want bool
	}{
		{"v0.3.0", false},
		{"v0.3.1", true},
		{"v0.3.2", true},
		{"v1.0.0", true},
		{"v0.2.9", false},
		{"dev", false},
		{"", false},
		{"not-a-version", false},
	}
	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			if got := isUnitManaged(tc.tag); got != tc.want {
				t.Errorf("isUnitManaged(%q) = %v; want %v", tc.tag, got, tc.want)
			}
		})
	}
}
