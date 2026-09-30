// fromrequest.go implements `cloud-pulse-agent update --from-request
// FILE` (SPEC-v0.6 §2 step 4): the root oneshot systemd service the
// cloud-pulse-agent-update.path unit triggers reads a small request
// file the unprivileged agent process wrote, strictly validates it,
// and — using this binary's own Run (the exact same download/verify/
// replace/restart pipeline `update` already uses) — installs the
// requested tag, writing its outcome to a root-owned result file the
// agent process reads back on its next report.
//
// Security model (mirrors remoteupdate.go's doc comment on the agent
// side): RunFromRequest never trusts the request file for anything
// beyond a job ID and a target tag/"latest" — Options.Source (release
// URL/checksum source) always comes from the caller's own environment
// (agent.env, root-owned), never from the request file, so a
// compromised hub can only ever move this agent between tags of the
// release feed its own environment already trusts.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// maxRequestFileBytesForUpdate bounds how much of the request file
// RunFromRequest will ever read (SPEC-v0.6 §2: "크기 제한").
const maxRequestFileBytesForUpdate = 4 << 10 // 4 KiB

// resultFilePerm is the exact mode SPEC-v0.6 §2 specifies for the
// result file this function writes ("0644").
const resultFilePerm = 0o644

// UpdateRequestFile is the on-disk schema RunFromRequest reads,
// identical in shape to internal/agent's own requestFile type (kept as
// a separate, independently-defined type per that package's own doc
// comment rationale: a schema change to one must be a deliberate,
// visible decision in both places, never an accidental shared-type
// drift).
type UpdateRequestFile struct {
	JobID  int64  `json:"job_id"`
	Target string `json:"target"`
}

// UpdateResultFile is the on-disk schema RunFromRequest writes,
// mirroring internal/agent's resultFile type for the same reason.
type UpdateResultFile struct {
	JobID     int64  `json:"job_id"`
	State     string `json:"state"` // "succeeded" or "failed"
	Version   string `json:"version"`
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Result states written to UpdateResultFile.State — string constants
// (not internal/models.UpdateJobState) so this package has no
// dependency on internal/models, matching its existing "standard
// library + internal/version only" import discipline.
const (
	ResultSucceeded = "succeeded"
	ResultFailed    = "failed"
)

// Reason codes written to UpdateResultFile.ErrorCode, matching
// internal/models.UpdateJobReason's vocabulary exactly (duplicated as
// plain strings for the same import-discipline reason as the State
// constants above).
const (
	ReasonUnsupported      = "unsupported"
	ReasonDownloadFailed   = "download_failed"
	ReasonChecksumMismatch = "checksum_mismatch"
	ReasonVerifyFailed     = "verify_failed"
	ReasonRestartFailed    = "restart_failed"
	ReasonDowngradeRefused = "downgrade_refused"
	ReasonAlreadyUpToDate  = "already_up_to_date"
	ReasonUnknown          = "unknown"
)

// FromRequestOptions configures RunFromRequest.
type FromRequestOptions struct {
	// RequestPath is the request file to read (SPEC-v0.6 §2:
	// "/var/lib/cloud-pulse-agent/update-request.json"), passed via
	// `--from-request FILE`.
	RequestPath string
	// ResultDir is the root-owned directory the result file is written
	// into (SPEC-v0.6 §2: "/var/lib/cloud-pulse-agent-update"). Created
	// with mode 0750 if missing.
	ResultDir string
	// Binary, Current, GOOS, GOARCH, Source, Verify, Restart, PostUpdate,
	// Stdout are passed straight through to Run for the actual
	// download/verify/replace/restart pipeline. Source in particular
	// must always be built from the caller's own environment (see this
	// file's package doc comment) — RunFromRequest itself never reads
	// anything URL/checksum-related from the request file.
	Binary  string
	Current string
	GOOS    string
	GOARCH  string
	// ExecPath overrides the binary path to replace, passed straight
	// through to Run (empty resolves via os.Executable, same as Run's
	// own default — tests set this to a temp file).
	ExecPath   string
	Source     Source
	Verify     func(ctx context.Context, path, tag string) error
	Restart    func(ctx context.Context, unit string) (bool, string, error)
	PostUpdate func(ctx context.Context, binPath, tag string) (string, error)
	Stdout     io.Writer
}

// RunFromRequest implements the full request-mode flow: read + validate
// the request file, refuse a downgrade, resolve "latest" itself via
// o.Source, run the same pipeline Run uses, and atomically write the
// result file (0644, in ResultDir, never following a symlink at the
// final path component). It returns the Result from the underlying Run
// call (zero value on a validation failure that never reached Run) and
// an error only for conditions that should make the `update
// --from-request` process itself exit non-zero (a missing/malformed
// request file, or a failure to write the result file at all) — a
// failure of the update *itself* (checksum mismatch, download failure,
// etc.) is reported via the written result file's State=failed, not a
// non-nil error, so the systemd oneshot service's own exit code doesn't
// need to be inspected by anything.
func RunFromRequest(ctx context.Context, o FromRequestOptions) (Result, error) {
	// Request mode relies on O_NOFOLLOW reads and a root-owned result
	// directory, which only the Linux + systemd deployment provides
	// (SPEC-v0.6 §2). Refuse everywhere else instead of running with
	// weaker file-handling guarantees.
	if hostGOOS() != "linux" {
		return Result{}, fmt.Errorf("selfupdate: from-request: remote updates require linux (running on %s): %w", hostGOOS(), ErrUnsupported)
	}
	req, err := readRequestFile(o.RequestPath)
	if err != nil {
		return Result{}, fmt.Errorf("selfupdate: from-request: %w", err)
	}

	result, resultFile := runFromValidatedRequest(ctx, o, req)

	if err := writeResultFileAtomic(o.ResultDir, resultFile); err != nil {
		return result, fmt.Errorf("selfupdate: from-request: write result file: %w", err)
	}
	return result, nil
}

// runFromValidatedRequest performs the actual update attempt for a
// syntactically valid request, always returning a result file to write
// regardless of outcome (never an error) — every failure mode from this
// point on is reported through the result file's State=failed, per
// RunFromRequest's doc comment.
func runFromValidatedRequest(ctx context.Context, o FromRequestOptions, req UpdateRequestFile) (Result, UpdateResultFile) {
	target := req.Target
	if target != "latest" {
		if !version.ValidTag(target) {
			return Result{}, UpdateResultFile{
				JobID: req.JobID, State: ResultFailed, Version: o.Current,
				ErrorCode: ReasonUnknown, Error: fmt.Sprintf("invalid target tag %q", target),
			}
		}
		if !version.IsNewer(target, o.Current) {
			// Not newer: either exactly equal (nothing to do — treat
			// as success, SPEC-v0.6 §2's already_up_to_date rule) or
			// older (a downgrade, which request-mode always refuses
			// regardless of an explicit --version flag's normal
			// downgrade-allowing behavior — SPEC-v0.6 §2: "다운그레이드를
			// 거부한다").
			curSem, curOK := version.Parse(o.Current)
			targetSem, targetOK := version.Parse(target)
			if curOK && targetOK && version.Compare(targetSem, curSem) == 0 {
				return Result{Current: o.Current, Latest: target, Message: "already up to date"},
					UpdateResultFile{JobID: req.JobID, State: ResultSucceeded, Version: o.Current, ErrorCode: ReasonAlreadyUpToDate}
			}
			return Result{Current: o.Current, Latest: target},
				UpdateResultFile{
					JobID: req.JobID, State: ResultFailed, Version: o.Current,
					ErrorCode: ReasonDowngradeRefused,
					Error:     fmt.Sprintf("refusing downgrade: current %s, requested %s", o.Current, target),
				}
		}
	}

	runOpts := Options{
		Binary:  o.Binary,
		Current: o.Current,
		// Target is left as the caller's exact request only for an
		// explicit tag; "latest" is resolved by Run itself via
		// o.Source.Latest, exactly like a normal `update` invocation
		// (SPEC-v0.6 §2: "target 'latest'는 에이전트 자신이 해석").
		GOOS:       o.GOOS,
		GOARCH:     o.GOARCH,
		ExecPath:   o.ExecPath,
		Source:     o.Source,
		Verify:     o.Verify,
		Restart:    o.Restart,
		PostUpdate: o.PostUpdate,
		Stdout:     o.Stdout,
	}
	if target != "latest" {
		runOpts.Target = target
	}

	result, err := Run(ctx, runOpts)
	if err != nil {
		return result, UpdateResultFile{
			JobID: req.JobID, State: ResultFailed, Version: o.Current,
			ErrorCode: classifyRunError(err), Error: err.Error(),
		}
	}

	newVersion := result.Latest
	if !result.Updated {
		// Run reported no update needed/performed (e.g. resolved
		// "latest" turned out to equal Current) — report the
		// already-running version, not the (possibly different)
		// resolved target string, since that's what will actually be
		// observed on the next agent report.
		newVersion = o.Current
	}

	if !result.Updated && !result.UpdateAvailable {
		return result, UpdateResultFile{JobID: req.JobID, State: ResultSucceeded, Version: newVersion, ErrorCode: ReasonAlreadyUpToDate}
	}
	if !result.Updated {
		// Updated=false but UpdateAvailable=true only occurs for
		// CheckOnly, which RunFromRequest never sets — defensive
		// fallback, treated as an unknown failure rather than silently
		// reporting success for an update that didn't actually happen.
		return result, UpdateResultFile{JobID: req.JobID, State: ResultFailed, Version: o.Current, ErrorCode: ReasonUnknown, Error: "update did not run to completion"}
	}

	return result, UpdateResultFile{JobID: req.JobID, State: ResultSucceeded, Version: newVersion}
}

// classifyRunError maps a Run error to one of this package's
// SPEC-v0.6 §2 reason codes on a best-effort basis: checksum failures
// and permission failures have dedicated sentinel errors to match
// against; everything else (network failures, verify failures, replace
// failures) falls back to ReasonDownloadFailed for a fetch-stage error
// or ReasonUnknown otherwise, since Run's error wrapping does not
// otherwise expose a machine-readable stage.
func classifyRunError(err error) string {
	switch {
	case errors.Is(err, ErrChecksum):
		return ReasonChecksumMismatch
	case errors.Is(err, ErrPermission):
		return ReasonUnknown
	case errors.Is(err, ErrUnsupported):
		return ReasonUnsupported
	default:
		return ReasonDownloadFailed
	}
}

// readRequestFile opens path with O_NOFOLLOW (refusing a symlink at the
// final path component), reads at most maxRequestFileBytesForUpdate+1
// bytes, and strictly decodes+validates it: job_id must be > 0, target
// must be "latest" or pass version.ValidTag.
func readRequestFile(path string) (UpdateRequestFile, error) {
	if path == "" {
		return UpdateRequestFile{}, errors.New("--from-request requires a file path")
	}

	f, err := os.OpenFile(path, os.O_RDONLY|unixNoFollowFlag(), 0)
	if err != nil {
		return UpdateRequestFile{}, fmt.Errorf("open request file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	limited := io.LimitReader(f, maxRequestFileBytesForUpdate+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return UpdateRequestFile{}, fmt.Errorf("read request file %s: %w", path, err)
	}
	if int64(len(data)) > maxRequestFileBytesForUpdate {
		return UpdateRequestFile{}, fmt.Errorf("request file %s exceeds maximum size of %d bytes", path, maxRequestFileBytesForUpdate)
	}

	var req UpdateRequestFile
	if err := json.Unmarshal(data, &req); err != nil {
		return UpdateRequestFile{}, fmt.Errorf("request file %s has invalid JSON: %w", path, err)
	}
	if req.JobID <= 0 {
		return UpdateRequestFile{}, fmt.Errorf("request file %s has invalid job_id %d", path, req.JobID)
	}
	if req.Target != "latest" && !version.ValidTag(req.Target) {
		return UpdateRequestFile{}, fmt.Errorf("request file %s has invalid target %q", path, req.Target)
	}
	return req, nil
}

// writeResultFileAtomic marshals rf and writes it to
// dir/result.json (SPEC-v0.6 §2's fixed filename) at mode 0644 via a
// temp-file-then-rename within dir, creating dir (mode 0750) if it does
// not already exist.
func writeResultFileAtomic(dir string, rf UpdateResultFile) error {
	if dir == "" {
		return errors.New("result dir is required")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create result dir %s: %w", dir, err)
	}

	data, err := json.Marshal(rf)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	final := filepath.Join(dir, "result.json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, resultFilePerm); err != nil { //nolint:gosec // tmp is derived from the caller-controlled, non-user-facing ResultDir
		return fmt.Errorf("write temp result file %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, resultFilePerm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod temp result file %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename result file to %s: %w", final, err)
	}
	return nil
}

// hostGOOS reports the operating system the process is actually running
// on. It is a variable (not FromRequestOptions.GOOS, which only selects the
// release asset) so tests can exercise the non-linux refusal on any host.
var hostGOOS = func() string { return runtime.GOOS }
