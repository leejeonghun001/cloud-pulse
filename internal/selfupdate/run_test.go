package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// newFakeReleaseServer starts an httptest server that serves a "latest"
// redirect to latestTag plus checksums.txt + the binary asset for
// latestTag containing assetContent, using AssetName's real naming
// scheme for goos/goarch so tests exercise the exact path Run itself
// takes for the current platform.
func newFakeReleaseServer(t *testing.T, latestTag, binary, goos, goarch string, assetContent []byte) (*httptest.Server, Source) {
	t.Helper()

	asset, err := AssetName(binary, goos, goarch)
	if err != nil {
		t.Fatalf("AssetName setup: %v", err)
	}
	sum := sha256.Sum256(assetContent)
	sumHex := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://github.com/leejeonghun001/cloud-pulse/releases/tag/"+latestTag)
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/dl/"+latestTag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", sumHex, asset)
	})
	mux.HandleFunc("/dl/"+latestTag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(assetContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	source := Source{
		LatestURL:    srv.URL + "/latest",
		AssetBaseURL: srv.URL + "/dl/" + latestTag,
		Client:       srv.Client(),
	}
	return srv, source
}

// injectedVerify returns a Verify func that records its calls and
// always succeeds, for tests that only need to assert it was invoked
// with the expected arguments.
func injectedVerify(calls *[]string) func(ctx context.Context, path, tag string) error {
	return func(ctx context.Context, path, tag string) error {
		*calls = append(*calls, fmt.Sprintf("%s@%s", filepath.Base(path), tag))
		return nil
	}
}

// noRestart is a Restart func that always reports "not restarted", for
// tests that don't care about restart behavior.
func noRestart(ctx context.Context, unit string) (bool, string, error) {
	return false, "restart skipped (test)", nil
}

func TestRun_UpdateAvailableAndInstalled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("old binary v0.3.0"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	newContent := []byte("new binary v0.3.1")
	_, source := newFakeReleaseServer(t, "v0.3.1", "cloud-pulse-hub", runtime.GOOS, runtime.GOARCH, newContent)

	var verifyCalls []string
	var restartCalls []string
	restart := func(ctx context.Context, unit string) (bool, string, error) {
		restartCalls = append(restartCalls, unit)
		return true, "", nil
	}

	var stdout bytes.Buffer
	result, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.0",
		ExecPath: execPath,
		Source:   source,
		Verify:   injectedVerify(&verifyCalls),
		Restart:  restart,
		Stdout:   &stdout,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if !result.UpdateAvailable {
		t.Error("result.UpdateAvailable = false; want true")
	}
	if !result.Updated {
		t.Error("result.Updated = false; want true")
	}
	if !result.Restarted {
		t.Error("result.Restarted = false; want true")
	}
	if result.Latest != "v0.3.1" {
		t.Errorf("result.Latest = %q; want v0.3.1", result.Latest)
	}

	got, err := os.ReadFile(execPath) //nolint:gosec // test-controlled path inside t.TempDir()
	if err != nil {
		t.Fatalf("read execPath after Run: %v", err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("execPath content = %q; want %q", got, newContent)
	}

	info, err := os.Stat(execPath)
	if err != nil {
		t.Fatalf("stat execPath: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("execPath mode = %o; want 0755", info.Mode().Perm())
	}

	if len(verifyCalls) != 1 {
		t.Fatalf("verify called %d times; want 1 (calls=%v)", len(verifyCalls), verifyCalls)
	}
	if len(restartCalls) != 1 || restartCalls[0] != "cloud-pulse-hub.service" {
		t.Fatalf("restart calls = %v; want exactly [cloud-pulse-hub.service]", restartCalls)
	}

	// No leftover temp update directories.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cloud-pulse-update-") {
			t.Errorf("leftover temp entry %q not cleaned up", e.Name())
		}
	}

	if !strings.Contains(stdout.String(), "v0.3.1") {
		t.Errorf("stdout progress output missing version: %q", stdout.String())
	}
}

func TestRun_AlreadyUpToDate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("current binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	_, source := newFakeReleaseServer(t, "v0.3.0", "cloud-pulse-hub", runtime.GOOS, runtime.GOARCH, []byte("irrelevant"))

	result, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.0",
		ExecPath: execPath,
		Source:   source,
		Restart:  noRestart,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.UpdateAvailable {
		t.Error("result.UpdateAvailable = true; want false")
	}
	if result.Updated {
		t.Error("result.Updated = true; want false")
	}
	if !strings.Contains(result.Message, "already up to date") {
		t.Errorf("result.Message = %q; want mention of up to date", result.Message)
	}

	got, err := os.ReadFile(execPath) //nolint:gosec // test-controlled path inside t.TempDir()
	if err != nil {
		t.Fatalf("read execPath: %v", err)
	}
	if string(got) != "current binary" {
		t.Fatal("execPath was modified even though already up to date")
	}
}

func TestRun_CheckOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		current    string
		latest     string
		wantUpdate bool
	}{
		{"update available", "v0.3.0", "v0.3.1", true},
		{"up to date", "v0.3.1", "v0.3.1", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			execPath := filepath.Join(dir, "cloud-pulse-hub")
			if err := os.WriteFile(execPath, []byte("binary"), 0o755); err != nil {
				t.Fatalf("write fake binary: %v", err)
			}

			_, source := newFakeReleaseServer(t, tc.latest, "cloud-pulse-hub", runtime.GOOS, runtime.GOARCH, []byte("irrelevant"))

			result, err := Run(context.Background(), Options{
				Binary:    "cloud-pulse-hub",
				Current:   tc.current,
				ExecPath:  execPath,
				Source:    source,
				CheckOnly: true,
			})
			if err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			if result.UpdateAvailable != tc.wantUpdate {
				t.Errorf("result.UpdateAvailable = %v; want %v", result.UpdateAvailable, tc.wantUpdate)
			}
			if result.Updated {
				t.Error("result.Updated = true; CheckOnly must never install")
			}

			got, err := os.ReadFile(execPath) //nolint:gosec // test-controlled path inside t.TempDir()
			if err != nil {
				t.Fatalf("read execPath: %v", err)
			}
			if string(got) != "binary" {
				t.Fatal("execPath was modified during CheckOnly")
			}
		})
	}
}

func TestRun_TamperedChecksum_BinaryUntouched(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	original := []byte("original binary bytes")
	if err := os.WriteFile(execPath, original, 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	asset, err := AssetName("cloud-pulse-hub", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("AssetName: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://github.com/leejeonghun001/cloud-pulse/releases/tag/v0.3.1")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/dl/v0.3.1/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		// Deliberately wrong checksum for the asset content served
		// below (simulates a tampered/corrupted checksums.txt or a
		// MITM'd asset).
		_, _ = fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), asset)
	})
	mux.HandleFunc("/dl/v0.3.1/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("new binary bytes"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	source := Source{LatestURL: srv.URL + "/latest", AssetBaseURL: srv.URL + "/dl/v0.3.1", Client: srv.Client()}

	_, err = Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.0",
		ExecPath: execPath,
		Source:   source,
	})
	if err == nil {
		t.Fatal("Run() = nil error; want ErrChecksum for tampered checksums.txt")
	}
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("Run() error = %v; want wrapping ErrChecksum", err)
	}

	got, readErr := os.ReadFile(execPath) //nolint:gosec // test-controlled path inside t.TempDir()
	if readErr != nil {
		t.Fatalf("read execPath: %v", readErr)
	}
	if string(got) != string(original) {
		t.Fatal("execPath was modified despite checksum verification failure")
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != filepath.Base(execPath) {
			t.Errorf("leftover file %q after failed update", e.Name())
		}
	}
}

func TestRun_VerifyFailure_BinaryUntouched(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	original := []byte("original binary bytes")
	if err := os.WriteFile(execPath, original, 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	_, source := newFakeReleaseServer(t, "v0.3.1", "cloud-pulse-hub", runtime.GOOS, runtime.GOARCH, []byte("new binary bytes"))

	verifyErr := errors.New("injected verify failure")
	_, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.0",
		ExecPath: execPath,
		Source:   source,
		Verify: func(ctx context.Context, path, tag string) error {
			return verifyErr
		},
	})
	if err == nil {
		t.Fatal("Run() = nil error; want verify failure surfaced")
	}
	if !strings.Contains(err.Error(), "injected verify failure") {
		t.Fatalf("Run() error = %v; want it to mention injected verify failure", err)
	}

	got, readErr := os.ReadFile(execPath) //nolint:gosec // test-controlled path inside t.TempDir()
	if readErr != nil {
		t.Fatalf("read execPath: %v", readErr)
	}
	if string(got) != string(original) {
		t.Fatal("execPath was modified despite Verify failure")
	}
}

func TestRun_RestartFailure_StillReportsUpdated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	_, source := newFakeReleaseServer(t, "v0.3.1", "cloud-pulse-hub", runtime.GOOS, runtime.GOARCH, []byte("new"))

	restartErr := errors.New("injected restart failure")
	result, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.0",
		ExecPath: execPath,
		Source:   source,
		Verify:   func(ctx context.Context, path, tag string) error { return nil },
		Restart: func(ctx context.Context, unit string) (bool, string, error) {
			return false, "", restartErr
		},
	})
	if err != nil {
		t.Fatalf("Run() returned error for a restart failure; want nil error, err=%v", err)
	}
	if !result.Updated {
		t.Error("result.Updated = false; binary replacement should have succeeded despite restart failure")
	}
	if result.Restarted {
		t.Error("result.Restarted = true; want false when restart injection failed")
	}
	if !strings.Contains(result.Message, "restart failed") {
		t.Errorf("result.Message = %q; want it to mention restart failure", result.Message)
	}
}

func TestRun_NoRestartOption(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	_, source := newFakeReleaseServer(t, "v0.3.1", "cloud-pulse-hub", runtime.GOOS, runtime.GOARCH, []byte("new"))

	restartCalled := false
	result, err := Run(context.Background(), Options{
		Binary:    "cloud-pulse-hub",
		Current:   "v0.3.0",
		ExecPath:  execPath,
		Source:    source,
		Verify:    func(ctx context.Context, path, tag string) error { return nil },
		NoRestart: true,
		Restart: func(ctx context.Context, unit string) (bool, string, error) {
			restartCalled = true
			return true, "", nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if restartCalled {
		t.Error("Restart callback invoked despite NoRestart=true")
	}
	if !result.Updated {
		t.Error("result.Updated = false; want true")
	}
	if result.Restarted {
		t.Error("result.Restarted = true; want false with NoRestart")
	}
	if !strings.Contains(result.Message, "restart manually") {
		t.Errorf("result.Message = %q; want restart-manually hint", result.Message)
	}
}

func TestRun_ExplicitTargetAllowsDowngrade(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("v0.3.5 binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	asset, err := AssetName("cloud-pulse-hub", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("AssetName: %v", err)
	}
	downgradeContent := []byte("v0.3.0 binary")
	sum := sha256.Sum256(downgradeContent)
	sumHex := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/dl/v0.3.0/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", sumHex, asset)
	})
	mux.HandleFunc("/dl/v0.3.0/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(downgradeContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	source := Source{AssetBaseURL: srv.URL + "/dl/v0.3.0", Client: srv.Client()}

	var stdout bytes.Buffer
	result, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.5",
		Target:   "v0.3.0",
		ExecPath: execPath,
		Source:   source,
		Verify:   func(ctx context.Context, path, tag string) error { return nil },
		Restart:  noRestart,
		Stdout:   &stdout,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !result.Updated {
		t.Fatal("result.Updated = false; want true for explicit-target downgrade")
	}
	if result.Latest != "v0.3.0" {
		t.Fatalf("result.Latest = %q; want v0.3.0", result.Latest)
	}

	got, readErr := os.ReadFile(execPath) //nolint:gosec // test-controlled path inside t.TempDir()
	if readErr != nil {
		t.Fatalf("read execPath: %v", readErr)
	}
	if string(got) != string(downgradeContent) {
		t.Fatal("downgrade did not install the requested older asset")
	}
}

func TestRun_DowngradeBelowSelfUpdateSince_Warns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("v0.3.5 binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	asset, err := AssetName("cloud-pulse-hub", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("AssetName: %v", err)
	}
	content := []byte("v0.2.0 binary")
	sum := sha256.Sum256(content)
	sumHex := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/dl/v0.2.0/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", sumHex, asset)
	})
	mux.HandleFunc("/dl/v0.2.0/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	source := Source{AssetBaseURL: srv.URL + "/dl/v0.2.0", Client: srv.Client()}

	var stdout bytes.Buffer
	_, err = Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.5",
		Target:   "v0.2.0",
		ExecPath: execPath,
		Source:   source,
		Verify:   func(ctx context.Context, path, tag string) error { return nil },
		Restart:  noRestart,
		Stdout:   &stdout,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !strings.Contains(stdout.String(), "warning") || !strings.Contains(stdout.String(), version.SelfUpdateSince) {
		t.Errorf("stdout = %q; want a downgrade warning mentioning %s", stdout.String(), version.SelfUpdateSince)
	}
}

func TestRun_DevBuildWithoutTarget_Refuses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("dev binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	_, source := newFakeReleaseServer(t, "v0.3.1", "cloud-pulse-hub", runtime.GOOS, runtime.GOARCH, []byte("new"))

	result, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "dev",
		ExecPath: execPath,
		Source:   source,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Updated {
		t.Error("result.Updated = true; dev build without explicit Target must not update")
	}
	if !strings.Contains(result.Message, "development build") {
		t.Errorf("result.Message = %q; want development-build hint", result.Message)
	}

	got, readErr := os.ReadFile(execPath) //nolint:gosec // test-controlled path inside t.TempDir()
	if readErr != nil {
		t.Fatalf("read execPath: %v", readErr)
	}
	if string(got) != "dev binary" {
		t.Fatal("execPath was modified for a dev build without an explicit Target")
	}
}

func TestRun_DevBuildWithTarget_Updates(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("dev binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	asset, err := AssetName("cloud-pulse-hub", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("AssetName: %v", err)
	}
	content := []byte("v0.3.1 binary")
	sum := sha256.Sum256(content)
	sumHex := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/dl/v0.3.1/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", sumHex, asset)
	})
	mux.HandleFunc("/dl/v0.3.1/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	source := Source{AssetBaseURL: srv.URL + "/dl/v0.3.1", Client: srv.Client()}

	result, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "dev",
		Target:   "v0.3.1",
		ExecPath: execPath,
		Source:   source,
		Verify:   func(ctx context.Context, path, tag string) error { return nil },
		Restart:  noRestart,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !result.Updated {
		t.Fatal("result.Updated = false; dev build with explicit Target should update")
	}
}

func TestRun_InvalidExplicitTarget(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	_, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.0",
		Target:   "not-a-valid-tag; rm -rf /",
		ExecPath: execPath,
		Source:   Source{},
	})
	if err == nil {
		t.Fatal("Run() = nil error; want error for invalid --version value")
	}
}

func TestRun_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit based read-only directories behave differently on Windows")
	}
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // restore so t.TempDir() cleanup can remove it

	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission bits")
	}

	_, source := newFakeReleaseServer(t, "v0.3.1", "cloud-pulse-hub", runtime.GOOS, runtime.GOARCH, []byte("new"))

	_, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.0",
		ExecPath: execPath,
		Source:   source,
	})
	if err == nil {
		t.Fatal("Run() = nil error; want ErrPermission for a read-only directory")
	}
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("Run() error = %v; want wrapping ErrPermission", err)
	}
}

func TestRun_UnsupportedPlatformAsset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	_, err := Run(context.Background(), Options{
		Binary:   "totally-unknown-binary",
		Current:  "v0.3.0",
		Target:   "v0.3.1",
		ExecPath: execPath,
		Source:   Source{},
	})
	if err == nil {
		t.Fatal("Run() = nil error; want ErrUnsupported for an unknown binary name")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Run() error = %v; want wrapping ErrUnsupported", err)
	}
}

func TestRun_LatestResolutionFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(execPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := Run(context.Background(), Options{
		Binary:   "cloud-pulse-hub",
		Current:  "v0.3.0",
		ExecPath: execPath,
		Source:   Source{LatestURL: srv.URL, Client: srv.Client()},
	})
	if err == nil {
		t.Fatal("Run() = nil error; want error when latest-release resolution fails")
	}

	got, readErr := os.ReadFile(execPath) //nolint:gosec // test-controlled path inside t.TempDir()
	if readErr != nil {
		t.Fatalf("read execPath: %v", readErr)
	}
	if string(got) != "binary" {
		t.Fatal("execPath was modified despite latest-resolution failure")
	}
}

// TestRun_DefaultVerify_RejectsWrongTagOutput exercises the real
// (non-injected) default Verify path using the current test binary
// itself as a stand-in "downloaded" executable, on non-Windows where
// exec-based verification is portable to run in CI. On Windows this is
// skipped per the task's guidance to skip exec-based verify there.
func TestRun_DefaultVerify_RejectsWrongTagOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec-based default Verify is not exercised on windows; Options.Verify is always injected there")
	}
	t.Parallel()

	// A tiny shell-less "binary": use /bin/echo (present on all
	// POSIX-ish CI runners we target for this specific skip-gated
	// test) is avoided for portability; instead build nothing and
	// just call defaultVerify against `go` itself is unreliable across
	// environments too. Simplest portable choice: use os.Args[0] (the
	// test binary), which prints go test flags on "-version" and will
	// not contain the tag string, giving a deterministic failure path
	// without depending on any external executable.
	selfPath, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}

	err = defaultVerify(context.Background(), selfPath, "v99.99.99-definitely-not-present")
	if err == nil {
		t.Fatal("defaultVerify() = nil error; want failure since the test binary's -version output can't contain this tag")
	}
}
