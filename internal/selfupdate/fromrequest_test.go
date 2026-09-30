package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeRequestFixture writes a request file at dir/"update-request.json"
// with the given raw content (already-marshaled or deliberately
// malformed), returning the full path.
func writeRequestFixture(t *testing.T, dir string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, "update-request.json")
	if err := os.WriteFile(path, content, 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write request fixture: %v", err)
	}
	return path
}

func marshalRequest(t *testing.T, req UpdateRequestFile) []byte {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request fixture: %v", err)
	}
	return data
}

func readResultFixture(t *testing.T, dir string) UpdateResultFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "result.json")) //nolint:gosec // test tempdir fixture
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var rf UpdateResultFile
	if err := json.Unmarshal(data, &rf); err != nil {
		t.Fatalf("unmarshal result.json: %v", err)
	}
	return rf
}

func TestRunFromRequest_Success(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "cloud-pulse-agent")
	if err := os.WriteFile(execPath, []byte("old binary v0.6.0"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fake binary: %v", err)
	}

	newContent := []byte("new binary v0.6.1")
	_, source := newFakeReleaseServer(t, "v0.6.1", "cloud-pulse-agent", testGOOS, testGOARCH, newContent)

	reqPath := writeRequestFixture(t, dir, marshalRequest(t, UpdateRequestFile{JobID: 42, Target: "v0.6.1"}))
	resultDir := filepath.Join(dir, "result")

	var stdout bytes.Buffer
	result, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   resultDir,
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
		GOOS:        testGOOS,
		GOARCH:      testGOARCH,
		ExecPath:    execPath,
		Source:      source,
		Verify:      injectedVerify(&[]string{}),
		Restart:     noRestart,
		Stdout:      &stdout,
	})
	if err != nil {
		t.Fatalf("RunFromRequest: %v", err)
	}
	if !result.Updated {
		t.Error("result.Updated = false, want true")
	}

	rf := readResultFixture(t, resultDir)
	if rf.JobID != 42 {
		t.Errorf("result JobID = %d, want 42", rf.JobID)
	}
	if rf.State != ResultSucceeded {
		t.Errorf("result State = %q, want %q", rf.State, ResultSucceeded)
	}
	if rf.Version != "v0.6.1" {
		t.Errorf("result Version = %q, want v0.6.1", rf.Version)
	}
	if rf.ErrorCode != "" {
		t.Errorf("result ErrorCode = %q, want empty", rf.ErrorCode)
	}

	got, err := os.ReadFile(execPath) //nolint:gosec // test tempdir fixture
	if err != nil {
		t.Fatalf("read execPath: %v", err)
	}
	if string(got) != string(newContent) {
		t.Errorf("execPath content = %q, want %q (binary should have been replaced)", got, newContent)
	}
}

func TestRunFromRequest_LatestResolvedByAgentItself(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "cloud-pulse-agent")
	if err := os.WriteFile(execPath, []byte("old binary v0.6.0"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fake binary: %v", err)
	}

	newContent := []byte("new binary v0.6.5")
	_, source := newFakeReleaseServer(t, "v0.6.5", "cloud-pulse-agent", testGOOS, testGOARCH, newContent)

	// target: "latest" — the request never names an exact tag; the
	// agent resolves it itself via o.Source.Latest, using its own
	// environment's release source, exactly as SPEC-v0.6 §2 requires.
	reqPath := writeRequestFixture(t, dir, marshalRequest(t, UpdateRequestFile{JobID: 1, Target: "latest"}))
	resultDir := filepath.Join(dir, "result")

	result, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   resultDir,
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
		GOOS:        testGOOS,
		GOARCH:      testGOARCH,
		ExecPath:    execPath,
		Source:      source,
		Verify:      injectedVerify(&[]string{}),
		Restart:     noRestart,
	})
	if err != nil {
		t.Fatalf("RunFromRequest: %v", err)
	}
	if result.Latest != "v0.6.5" {
		t.Errorf("result.Latest = %q, want v0.6.5 (resolved by agent's own Source.Latest)", result.Latest)
	}

	rf := readResultFixture(t, resultDir)
	if rf.Version != "v0.6.5" {
		t.Errorf("result Version = %q, want v0.6.5", rf.Version)
	}
	if rf.State != ResultSucceeded {
		t.Errorf("result State = %q, want %q", rf.State, ResultSucceeded)
	}
}

func TestRunFromRequest_VerifyFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "cloud-pulse-agent")
	if err := os.WriteFile(execPath, []byte("old binary v0.6.0"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fake binary: %v", err)
	}
	beforeSha := mustReadFile(t, execPath)

	_, source := newFakeReleaseServer(t, "v0.6.1", "cloud-pulse-agent", testGOOS, testGOARCH, []byte("expected content"))

	reqPath := writeRequestFixture(t, dir, marshalRequest(t, UpdateRequestFile{JobID: 7, Target: "v0.6.1"}))
	resultDir := filepath.Join(dir, "result")

	verifyErr := errors.New("boom: simulated verify failure")
	_, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   resultDir,
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
		GOOS:        testGOOS,
		GOARCH:      testGOARCH,
		ExecPath:    execPath,
		Source:      source,
		Verify: func(ctx context.Context, path, tag string) error {
			return verifyErr
		},
		Restart: noRestart,
	})
	if err != nil {
		t.Fatalf("RunFromRequest returned a process-level error (want a result-file failure instead): %v", err)
	}

	rf := readResultFixture(t, resultDir)
	if rf.State != ResultFailed {
		t.Errorf("result State = %q, want %q", rf.State, ResultFailed)
	}
	if rf.JobID != 7 {
		t.Errorf("result JobID = %d, want 7", rf.JobID)
	}

	afterSha := mustReadFile(t, execPath)
	if string(beforeSha) != string(afterSha) {
		t.Error("execPath content changed after a failed update; binary must be left untouched")
	}
}

func TestRunFromRequest_RealChecksumMismatchClassifiedCorrectly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "cloud-pulse-agent")
	if err := os.WriteFile(execPath, []byte("old binary v0.6.0"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fake binary: %v", err)
	}

	tampered := corruptedAssetSource(t, "cloud-pulse-agent", "v0.6.1", testGOOS, testGOARCH)

	reqPath := writeRequestFixture(t, dir, marshalRequest(t, UpdateRequestFile{JobID: 3, Target: "v0.6.1"}))
	resultDir := filepath.Join(dir, "result")

	_, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   resultDir,
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
		GOOS:        testGOOS,
		GOARCH:      testGOARCH,
		ExecPath:    execPath,
		Source:      tampered,
		Restart:     noRestart,
	})
	if err != nil {
		t.Fatalf("RunFromRequest returned a process-level error: %v", err)
	}

	rf := readResultFixture(t, resultDir)
	if rf.State != ResultFailed {
		t.Fatalf("result State = %q, want %q", rf.State, ResultFailed)
	}
	if rf.ErrorCode != ReasonChecksumMismatch {
		t.Errorf("result ErrorCode = %q, want %q", rf.ErrorCode, ReasonChecksumMismatch)
	}
}

func TestRunFromRequest_DowngradeRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "cloud-pulse-agent")
	if err := os.WriteFile(execPath, []byte("current binary v0.6.5"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fake binary: %v", err)
	}
	beforeSha := mustReadFile(t, execPath)

	// No fake server needed: a downgrade is refused before any network
	// call, using version.ValidTag+version.IsNewer alone.
	reqPath := writeRequestFixture(t, dir, marshalRequest(t, UpdateRequestFile{JobID: 9, Target: "v0.6.0"}))
	resultDir := filepath.Join(dir, "result")

	result, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   resultDir,
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.5",
		ExecPath:    execPath,
	})
	if err != nil {
		t.Fatalf("RunFromRequest: %v", err)
	}
	if result.Updated {
		t.Error("result.Updated = true, want false for a refused downgrade")
	}

	rf := readResultFixture(t, resultDir)
	if rf.State != ResultFailed {
		t.Errorf("result State = %q, want %q", rf.State, ResultFailed)
	}
	if rf.ErrorCode != ReasonDowngradeRefused {
		t.Errorf("result ErrorCode = %q, want %q", rf.ErrorCode, ReasonDowngradeRefused)
	}

	afterSha := mustReadFile(t, execPath)
	if string(beforeSha) != string(afterSha) {
		t.Error("execPath content changed despite a refused downgrade")
	}
}

func TestRunFromRequest_AlreadyUpToDate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "cloud-pulse-agent")
	if err := os.WriteFile(execPath, []byte("current binary v0.6.5"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fake binary: %v", err)
	}

	reqPath := writeRequestFixture(t, dir, marshalRequest(t, UpdateRequestFile{JobID: 11, Target: "v0.6.5"}))
	resultDir := filepath.Join(dir, "result")

	result, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   resultDir,
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.5",
		ExecPath:    execPath,
	})
	if err != nil {
		t.Fatalf("RunFromRequest: %v", err)
	}
	if result.Updated {
		t.Error("result.Updated = true, want false when target equals current")
	}

	rf := readResultFixture(t, resultDir)
	if rf.State != ResultSucceeded {
		t.Errorf("result State = %q, want %q (already up to date is success, not failure)", rf.State, ResultSucceeded)
	}
	if rf.ErrorCode != ReasonAlreadyUpToDate {
		t.Errorf("result ErrorCode = %q, want %q", rf.ErrorCode, ReasonAlreadyUpToDate)
	}
}

func TestRunFromRequest_MalformedRequestFile_ErrorsAndWritesNoResult(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	reqPath := writeRequestFixture(t, dir, []byte("{not valid json"))
	resultDir := filepath.Join(dir, "result")

	_, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   resultDir,
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
	})
	if err == nil {
		t.Fatal("RunFromRequest: want an error for malformed request JSON")
	}
	if _, statErr := os.Stat(filepath.Join(resultDir, "result.json")); !os.IsNotExist(statErr) {
		t.Error("expected no result.json to be written for a request-file-level failure")
	}
}

func TestRunFromRequest_RejectsInvalidJobID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	reqPath := writeRequestFixture(t, dir, marshalRequest(t, UpdateRequestFile{JobID: 0, Target: "v0.6.1"}))

	_, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   filepath.Join(dir, "result"),
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
	})
	if err == nil {
		t.Fatal("RunFromRequest: want an error for job_id <= 0")
	}
}

func TestRunFromRequest_RejectsInvalidTargetTag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	reqPath := writeRequestFixture(t, dir, marshalRequest(t, UpdateRequestFile{JobID: 1, Target: "; rm -rf /"}))

	_, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   filepath.Join(dir, "result"),
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
	})
	if err == nil {
		t.Fatal("RunFromRequest: want an error for a malicious/invalid target tag")
	}
}

func TestRunFromRequest_RejectsOversizedRequestFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	oversized := bytes.Repeat([]byte("x"), maxRequestFileBytesForUpdate+1024)
	reqPath := writeRequestFixture(t, dir, oversized)

	_, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   filepath.Join(dir, "result"),
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
	})
	if err == nil {
		t.Fatal("RunFromRequest: want an error for an oversized request file")
	}
}

func TestRunFromRequest_RefusesSymlinkRequestFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("O_NOFOLLOW does not exist on windows; request mode refuses non-linux hosts")
	}
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "evil.json")
	if err := os.WriteFile(target, marshalRequest(t, UpdateRequestFile{JobID: 1, Target: "v0.6.1"}), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write symlink target: %v", err)
	}
	link := filepath.Join(dir, "update-request.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: link,
		ResultDir:   filepath.Join(dir, "result"),
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
	})
	if err == nil {
		t.Fatal("RunFromRequest: want an error when RequestPath is a symlink")
	}
}

func TestRunFromRequest_MissingRequestPath(t *testing.T) {
	t.Parallel()

	_, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: "",
		ResultDir:   t.TempDir(),
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
	})
	if err == nil {
		t.Fatal("RunFromRequest: want an error for an empty RequestPath")
	}
}

func TestRunFromRequest_ResultFileWrittenAtomicallyWithCorrectMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no unix permission bits; request mode refuses non-linux hosts")
	}
	t.Parallel()

	dir := t.TempDir()
	execDir := t.TempDir()
	execPath := filepath.Join(execDir, "cloud-pulse-agent")
	if err := os.WriteFile(execPath, []byte("current binary v0.6.5"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fake binary: %v", err)
	}

	reqPath := writeRequestFixture(t, dir, marshalRequest(t, UpdateRequestFile{JobID: 1, Target: "v0.6.5"}))
	resultDir := filepath.Join(dir, "result")

	if _, err := RunFromRequest(context.Background(), FromRequestOptions{
		HostGOOS:    "linux",
		RequestPath: reqPath,
		ResultDir:   resultDir,
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.5",
		ExecPath:    execPath,
	}); err != nil {
		t.Fatalf("RunFromRequest: %v", err)
	}

	resultPath := filepath.Join(resultDir, "result.json")
	info, err := os.Stat(resultPath)
	if err != nil {
		t.Fatalf("stat result.json: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("result.json mode = %o, want 0644", got)
	}
	if _, err := os.Stat(resultPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("expected no leftover result.json.tmp after a successful write")
	}
}

// mustReadFile reads path or fails the test.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test tempdir fixture
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// corruptedAssetSource starts an httptest server that serves a valid
// "latest" redirect and a checksums.txt entry for asset, but whose
// asset response body deliberately does not match that checksum —
// reliably reproducing ErrChecksum's exact real-world trigger (a
// checksums.txt/asset mismatch) rather than simulating the failure via
// an injected Verify hook.
func corruptedAssetSource(t *testing.T, binary, tag, goos, goarch string) Source {
	t.Helper()

	asset, err := AssetName(binary, goos, goarch)
	if err != nil {
		t.Fatalf("AssetName setup: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://github.com/leejeonghun001/cloud-pulse/releases/tag/"+tag)
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/dl/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		// A checksum for content that is NOT what /dl/<tag>/<asset>
		// below actually serves.
		_, _ = fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), asset)
	})
	mux.HandleFunc("/dl/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this does not match the all-zero checksum above"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return Source{
		LatestURL:    srv.URL + "/latest",
		AssetBaseURL: srv.URL + "/dl/" + tag,
		Client:       srv.Client(),
	}
}

// TestRunFromRequest_RefusesUnsupportedHost guards the SPEC-v0.6
// §2/SPEC-v0.7 §1 rule that request mode runs only on Linux, macOS, or
// Windows, where a platform-appropriate safe-open (O_NOFOLLOW or
// CreateFile+FILE_FLAG_OPEN_REPARSE_POINT) and a root/Administrators-owned
// result directory are guaranteed.
func TestRunFromRequest_RefusesUnsupportedHost(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	reqPath := filepath.Join(dir, "update-request.json")
	if err := os.WriteFile(reqPath, []byte(`{"job_id":1,"target":"latest"}`), 0o600); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resultDir := filepath.Join(dir, "result")
	_, err := RunFromRequest(context.Background(), FromRequestOptions{
		RequestPath: reqPath,
		ResultDir:   resultDir,
		Binary:      "cloud-pulse-agent",
		Current:     "v0.6.0",
		HostGOOS:    "plan9",
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("RunFromRequest error = %v, want ErrUnsupported", err)
	}
	if _, statErr := os.Stat(filepath.Join(resultDir, "result.json")); !os.IsNotExist(statErr) {
		t.Fatalf("result.json must not be written on a refused platform (stat err = %v)", statErr)
	}
}

// TestRunFromRequest_AcceptsDarwinAndWindowsHosts guards SPEC-v0.7 §1's
// widened platform gate: darwin and windows must pass the initial
// HostGOOS check (i.e. never return ErrUnsupported), unlike plan9
// above. This uses an already-satisfied request (target == Current) so
// the flow completes without needing a real Source/network call,
// isolating the assertion to the platform gate itself rather than the
// full download pipeline.
func TestRunFromRequest_AcceptsDarwinAndWindowsHosts(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			dir := t.TempDir()
			reqPath := filepath.Join(dir, "update-request.json")
			if err := os.WriteFile(reqPath, []byte(`{"job_id":1,"target":"v0.6.0"}`), 0o600); err != nil {
				t.Fatalf("write request: %v", err)
			}
			resultDir := filepath.Join(dir, "result")
			result, err := RunFromRequest(context.Background(), FromRequestOptions{
				RequestPath: reqPath,
				ResultDir:   resultDir,
				Binary:      "cloud-pulse-agent",
				Current:     "v0.6.0",
				HostGOOS:    goos,
			})
			if errors.Is(err, ErrUnsupported) {
				t.Fatalf("RunFromRequest error = %v, want anything but ErrUnsupported for GOOS=%s", err, goos)
			}
			if err != nil {
				t.Fatalf("RunFromRequest unexpected error for GOOS=%s: %v", goos, err)
			}
			if result.Message == "" {
				t.Errorf("expected an already-up-to-date result message for GOOS=%s", goos)
			}
			data, readErr := os.ReadFile(filepath.Join(resultDir, "result.json"))
			if readErr != nil {
				t.Fatalf("expected result.json to be written for GOOS=%s: %v", goos, readErr)
			}
			if !strings.Contains(string(data), `"succeeded"`) {
				t.Errorf("result.json for GOOS=%s = %s, want state=succeeded (already up to date)", goos, data)
			}
		})
	}
}
