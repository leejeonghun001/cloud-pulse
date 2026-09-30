package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestResolveRemoteUpdateCapability(t *testing.T) {
	t.Parallel()

	optedIn := resolveRemoteUpdateCapability(true)
	notOptedIn := resolveRemoteUpdateCapability(false)

	if !systemdPresent() {
		for _, rc := range []models.RemoteUpdateCapability{optedIn, notOptedIn} {
			if rc.Supported {
				t.Errorf("Supported = true without systemd, want false")
			}
			if rc.Reason != models.UpdateReasonUnsupported {
				t.Errorf("Reason = %q, want %q", rc.Reason, models.UpdateReasonUnsupported)
			}
		}
		return
	}

	if !optedIn.Supported {
		t.Error("Supported = false with systemd present, want true")
	}
	if !optedIn.OptedIn {
		t.Error("OptedIn = false for optedIn=true, want true")
	}
	if optedIn.Reason != "" {
		t.Errorf("Reason = %q, want empty when supported and opted in", optedIn.Reason)
	}

	if !notOptedIn.Supported {
		t.Error("Supported = false with systemd present, want true")
	}
	if notOptedIn.OptedIn {
		t.Error("OptedIn = true for optedIn=false, want false")
	}
	if notOptedIn.Reason != models.UpdateReasonNotEnabled {
		t.Errorf("Reason = %q, want %q", notOptedIn.Reason, models.UpdateReasonNotEnabled)
	}
}

func TestSystemdPresent_NonLinuxAlwaysFalse(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "linux" {
		t.Skip("this assertion only applies off-Linux")
	}
	if systemdPresent() {
		t.Error("systemdPresent() = true on non-Linux, want false")
	}
}

func TestHandleUpdateRequest_NilIsNoOp(t *testing.T) {
	t.Parallel()
	// Must not panic with a nil request or a nil logger.
	handleUpdateRequest(context.Background(), nil, nil)
}

func TestHandleUpdateRequest_LogsReceipt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handleUpdateRequestAt(context.Background(), logger, &models.UpdateRequest{JobID: 42, Target: "v0.6.0"}, dir)

	out := buf.String()
	if out == "" {
		t.Fatal("expected a log line for a non-nil update request")
	}
	if !bytes.Contains(buf.Bytes(), []byte("42")) {
		t.Errorf("log output %q does not mention job_id 42", out)
	}
}

func TestHandleUpdateRequest_WritesRequestFileAtomically(t *testing.T) {
	dir := t.TempDir()
	handleUpdateRequestAt(context.Background(), nil, &models.UpdateRequest{JobID: 7, Target: "v0.6.1"}, dir)

	path := filepath.Join(dir, RequestFileName)
	data, err := os.ReadFile(path) //nolint:gosec // test reads its own tempdir fixture
	if err != nil {
		t.Fatalf("read request file: %v", err)
	}

	var got requestFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal request file: %v", err)
	}
	if got.JobID != 7 || got.Target != "v0.6.1" {
		t.Errorf("request file = %+v, want {JobID:7 Target:v0.6.1}", got)
	}

	// No leftover .tmp file after a successful write.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected no leftover .tmp file, stat err = %v", err)
	}
}

func TestHandleUpdateRequest_OverwritesPreviousRequest(t *testing.T) {
	dir := t.TempDir()
	handleUpdateRequestAt(context.Background(), nil, &models.UpdateRequest{JobID: 1, Target: "v0.6.0"}, dir)
	handleUpdateRequestAt(context.Background(), nil, &models.UpdateRequest{JobID: 2, Target: "v0.6.2"}, dir)

	data, err := os.ReadFile(filepath.Join(dir, RequestFileName)) //nolint:gosec // test tempdir fixture
	if err != nil {
		t.Fatalf("read request file: %v", err)
	}
	var got requestFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.JobID != 2 {
		t.Errorf("JobID = %d, want 2 (latest request wins)", got.JobID)
	}
}

func TestHandleUpdateRequest_MissingDirIsCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state", "dir")
	handleUpdateRequestAt(context.Background(), nil, &models.UpdateRequest{JobID: 1, Target: "latest"}, dir)

	if _, err := os.Stat(filepath.Join(dir, RequestFileName)); err != nil {
		t.Errorf("expected request file to exist after MkdirAll, stat err: %v", err)
	}
}

func TestPendingUpdateStatus_StubAlwaysNilWhenNoResultDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "does-not-exist")

	if got := pendingUpdateStatusAt(context.Background(), nil, dir); got != nil {
		t.Errorf("pendingUpdateStatus = %+v, want nil (no result file)", got)
	}
}

func TestPendingUpdateStatus_ParsesValidResultAndRemovesFile(t *testing.T) {
	dir := t.TempDir()
	writeResultFixture(t, dir, resultFile{
		JobID:   99,
		State:   models.UpdateJobSucceeded,
		Version: "v0.6.0",
	})

	got := pendingUpdateStatusAt(context.Background(), nil, dir)
	if got == nil {
		t.Fatal("pendingUpdateStatus = nil, want a status")
	}
	if got.JobID != 99 || got.State != models.UpdateJobSucceeded || got.Version != "v0.6.0" {
		t.Errorf("status = %+v, want {JobID:99 State:succeeded Version:v0.6.0}", got)
	}

	if _, err := os.Stat(filepath.Join(dir, ResultFileName)); !os.IsNotExist(err) {
		t.Errorf("expected result file to be removed after a successful read, stat err = %v", err)
	}
}

func TestPendingUpdateStatus_FailedResultWithErrorCode(t *testing.T) {
	dir := t.TempDir()
	writeResultFixture(t, dir, resultFile{
		JobID:     5,
		State:     models.UpdateJobFailed,
		ErrorCode: models.UpdateReasonChecksumMismatch,
		Error:     "sha256 mismatch",
	})

	got := pendingUpdateStatusAt(context.Background(), nil, dir)
	if got == nil {
		t.Fatal("pendingUpdateStatus = nil, want a status")
	}
	if got.ErrorCode != models.UpdateReasonChecksumMismatch {
		t.Errorf("ErrorCode = %q, want %q", got.ErrorCode, models.UpdateReasonChecksumMismatch)
	}
}

func TestPendingUpdateStatus_RejectsInvalidJobID(t *testing.T) {
	dir := t.TempDir()
	writeResultFixture(t, dir, resultFile{JobID: 0, State: models.UpdateJobSucceeded, Version: "v0.6.0"})

	if got := pendingUpdateStatusAt(context.Background(), nil, dir); got != nil {
		t.Errorf("pendingUpdateStatus = %+v, want nil for job_id <= 0", got)
	}
}

func TestPendingUpdateStatus_RejectsInvalidState(t *testing.T) {
	dir := t.TempDir()
	// queued/in_progress are not states an agent is allowed to report.
	writeResultFixture(t, dir, resultFile{JobID: 1, State: models.UpdateJobQueued})

	if got := pendingUpdateStatusAt(context.Background(), nil, dir); got != nil {
		t.Errorf("pendingUpdateStatus = %+v, want nil for state=queued", got)
	}
}

func TestPendingUpdateStatus_RejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ResultFileName), []byte("{not json"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fixture: %v", err)
	}

	if got := pendingUpdateStatusAt(context.Background(), nil, dir); got != nil {
		t.Errorf("pendingUpdateStatus = %+v, want nil for malformed JSON", got)
	}
}

func TestPendingUpdateStatus_RejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	oversized := make([]byte, maxResultFileBytes+1024)
	for i := range oversized {
		oversized[i] = ' '
	}
	if err := os.WriteFile(filepath.Join(dir, ResultFileName), oversized, 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fixture: %v", err)
	}

	if got := pendingUpdateStatusAt(context.Background(), nil, dir); got != nil {
		t.Errorf("pendingUpdateStatus = %+v, want nil for oversized file", got)
	}
	// The oversized file must be left in place (never removed on a
	// rejected read) so a human/monitoring can diagnose it.
	if _, err := os.Stat(filepath.Join(dir, ResultFileName)); err != nil {
		t.Errorf("expected oversized result file to remain on disk, stat err = %v", err)
	}
}

func TestPendingUpdateStatus_RefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("O_NOFOLLOW is a no-op stub on windows")
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "evil-target.json")
	writeResultFixtureAt(t, target, resultFile{JobID: 1, State: models.UpdateJobSucceeded, Version: "v0.6.0"})

	link := filepath.Join(dir, ResultFileName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if got := pendingUpdateStatusAt(context.Background(), nil, dir); got != nil {
		t.Errorf("pendingUpdateStatus = %+v, want nil when result path is a symlink", got)
	}
}

func TestPendingUpdateStatus_NoFileIsNilWithoutError(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if got := pendingUpdateStatusAt(context.Background(), logger, dir); got != nil {
		t.Errorf("pendingUpdateStatus = %+v, want nil", got)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no warning log for a simply-absent result file, got: %s", buf.String())
	}
}

func writeResultFixture(t *testing.T, dir string, rf resultFile) {
	t.Helper()
	writeResultFixtureAt(t, filepath.Join(dir, ResultFileName), rf)
}

func writeResultFixtureAt(t *testing.T, path string, rf resultFile) {
	t.Helper()
	data, err := json.Marshal(rf)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fixture %s: %v", path, err)
	}
}
