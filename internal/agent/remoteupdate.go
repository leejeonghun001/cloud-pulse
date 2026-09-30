// remoteupdate.go implements the agent-side plumbing for SPEC-v0.6 §2's
// remote batch-update feature: reporting this agent's capability/opt-in
// (resolveRemoteUpdateCapability), atomically writing a hub-delivered
// UpdateRequest to the state directory the systemd path unit watches
// (handleUpdateRequest), and reading back the result the root-owned
// `cloud-pulse-agent update --from-request` invocation wrote
// (pendingUpdateStatus).
//
// Security model (SPEC-v0.6 §2, mandatory): the agent process itself
// never runs the actual update — it only writes a small JSON file
// naming a job ID and a target tag/"latest". The real work (download,
// checksum verification, binary replacement, restart) happens in a
// separate `cloud-pulse-agent update --from-request` invocation started
// by a root oneshot systemd service reacting to a systemd .path unit,
// using the *agent's own* agent.env (root-owned, unwritable by the
// unprivileged agent process) for its release source. This means a
// compromised/malicious hub can, at most, ask this agent to move to a
// different tag of the *official* release feed the agent's own
// environment already trusts — it can never supply a URL, a checksum,
// or an arbitrary binary itself.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// RequestStateDir is the systemd StateDirectory the agent unit is
// granted (SPEC-v0.6 §2 step 3): "StateDirectory=cloud-pulse-agent"
// resolves to this path at runtime. Exported as a var (not const) only
// so tests can point it at a temp directory; production code never
// reassigns it.
var RequestStateDir = "/var/lib/cloud-pulse-agent"

// RequestFileName is the file the agent atomically writes inside
// RequestStateDir, watched by the cloud-pulse-agent-update.path systemd
// unit.
const RequestFileName = "update-request.json"

// ResultDir is the root-owned directory `cloud-pulse-agent update
// --from-request` writes its outcome to (SPEC-v0.6 §2 step 4: "root
// 소유 디렉터리 ... 에이전트가 쓸 수 없는 위치라 symlink 공격을 차단").
// Exported as a var for the same test-only reason as RequestStateDir.
var ResultDir = "/var/lib/cloud-pulse-agent-update"

// ResultFileName is the result file inside ResultDir that the agent
// process reads back on a subsequent report cycle.
const ResultFileName = "result.json"

// maxRequestFileBytes/maxResultFileBytes bound how much of a
// request/result file this package will ever read, defense-in-depth
// against a corrupted or maliciously oversized file (SPEC-v0.6 §2: "크기
// 제한").
const (
	maxRequestFileBytes = 4 << 10  // 4 KiB: {job_id, target} is tiny
	maxResultFileBytes  = 16 << 10 // 16 KiB: result + short error text
)

// requestFilePerm/resultFilePerm match the spec's exact modes: the
// request file is written by the unprivileged agent process for a root
// oneshot service to read (world-readable is fine, nothing secret is
// in it); the result file's 0644 is set by the *writer*
// (`update --from-request`, run as root — see cmd/agent/update.go),
// documented here for reference by both sides.
const requestFilePerm = 0o644

// resolveRemoteUpdateCapability reports this agent's remote-update
// support/opt-in for HostInfo.RemoteUpdate (SPEC-v0.6 §2). Support is
// restricted to Linux with systemd actually present as the running init
// system (probed via the presence of /run/systemd/system, the standard
// detection method systemd itself documents — see systemd's own
// sd_booted(3)); a Linux host running a different init system (e.g. a
// minimal container, sysvinit, OpenRC) correctly reports Supported=false
// with reason "unsupported" rather than optimistically claiming support
// it cannot actually deliver on. optedIn mirrors the agent's own
// CP_REMOTE_UPDATE setting (config.Agent.RemoteUpdate) — the hub can
// never turn this on remotely, only reflect what the agent itself
// reports.
func resolveRemoteUpdateCapability(optedIn bool) models.RemoteUpdateCapability {
	if !systemdPresent() {
		return models.RemoteUpdateCapability{Supported: false, Reason: models.UpdateReasonUnsupported}
	}
	if !optedIn {
		return models.RemoteUpdateCapability{Supported: true, OptedIn: false, Reason: models.UpdateReasonNotEnabled}
	}
	return models.RemoteUpdateCapability{Supported: true, OptedIn: true}
}

// systemdPresent reports whether this host is Linux with systemd as its
// running init system: runtime.GOOS is "linux" and /run/systemd/system
// exists (a directory systemd itself creates and documents as the
// standard "is systemd running" check — see systemd's sd_booted(3) and
// systemd.exec(5)). A stat error (including "not found") is treated as
// "systemd not present" rather than propagated, since this function
// has no error return and a probe failure should degrade to
// Supported=false, not panic or block agent startup.
func systemdPresent() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	info, err := os.Stat("/run/systemd/system")
	return err == nil && info.IsDir()
}

// requestFile is the strict on-disk schema for update-request.json,
// mirroring models.UpdateRequest but decoded independently (not by
// reusing models.UpdateRequest for decoding) so that a future field
// added to the wire type does not silently loosen this file's schema
// without an explicit decision.
type requestFile struct {
	JobID  int64  `json:"job_id"`
	Target string `json:"target"`
}

// handleUpdateRequest processes an IngestResponse.UpdateRequest received
// on a report response (SPEC-v0.6 §2 step 3): atomically writing the
// request file for the systemd path unit to pick up, and returning
// immediately (never blocking the report loop on the update actually
// running, which happens out-of-process via
// `cloud-pulse-agent update --from-request`).
//
// The write is atomic (temp file in the same directory, then rename)
// so the path unit's watcher never observes a partially written file.
// A failure to write (e.g. RequestStateDir missing because
// CP_REMOTE_UPDATE=off and the unit was never granted a
// StateDirectory) is logged at warn level and otherwise swallowed —
// the next report will simply re-deliver the same UpdateRequest since
// the hub has not yet seen an ack.
func handleUpdateRequest(_ context.Context, logger *slog.Logger, req *models.UpdateRequest) {
	if req == nil {
		return
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger.Debug("received remote update request", "job_id", req.JobID, "target", req.Target)

	if err := writeRequestFileAtomic(RequestStateDir, requestFile{JobID: req.JobID, Target: req.Target}); err != nil {
		logger.Warn("failed to write remote update request file", "job_id", req.JobID, "error", err)
	}
}

// writeRequestFileAtomic marshals req and writes it to
// dir/RequestFileName via a temp-file-then-rename, so a concurrent
// reader (the systemd path unit's watcher, or a second agent process
// during a race) never observes a partially written file.
func writeRequestFileAtomic(dir string, req requestFile) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("agent: marshal update request: %w", err)
	}

	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // dir is a fixed, non-configurable state directory (RequestStateDir), not user input
		return fmt.Errorf("agent: create request dir %s: %w", dir, err)
	}

	final := filepath.Join(dir, RequestFileName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, requestFilePerm); err != nil { //nolint:gosec // tmp is derived from the same fixed directory
		return fmt.Errorf("agent: write temp request file %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("agent: rename request file to %s: %w", final, err)
	}
	return nil
}

// resultFile is the strict on-disk schema for result.json, written by
// `cloud-pulse-agent update --from-request` (always run as root, per
// SPEC-v0.6 §2 step 4) and read back here by the unprivileged agent
// process.
type resultFile struct {
	JobID     int64                  `json:"job_id"`
	State     models.UpdateJobState  `json:"state"`
	Version   string                 `json:"version"`
	ErrorCode models.UpdateJobReason `json:"error_code,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

// validResultState reports whether s is one of the two states an agent
// is ever allowed to self-report (SPEC-v0.6 §2 step 5: "에이전트는
// queued/in_progress를 보고하지 않는다").
func validResultState(s models.UpdateJobState) bool {
	return s == models.UpdateJobSucceeded || s == models.UpdateJobFailed
}

// pendingUpdateStatus returns the AgentUpdateStatus to attach to the
// next outbound AgentReport (SPEC-v0.6 §2 step 5), or nil when there is
// nothing to report.
//
// It reads ResultDir/ResultFileName defensively:
//   - opened with O_NOFOLLOW (never follows a symlink placed at the
//     expected path — SPEC-v0.6 §2's explicit symlink-attack defense,
//     meaningful because ResultDir's parent may be more permissively
//     writable than ResultDir itself on some deployments even though
//     ResultDir itself is root-owned);
//   - size-limited to maxResultFileBytes;
//   - strictly schema-validated (unknown/missing required fields,
//     invalid State, and a JobID <= 0 are all rejected).
//
// On any read/parse/validation failure the file is logged at warn
// level and otherwise ignored (returns nil) — a malformed or malicious
// result file must never crash or block the report loop. On success
// the file is removed so the same result is never reported twice; a
// removal failure is logged but does not prevent the status from being
// returned for this cycle.
func pendingUpdateStatus(_ context.Context, logger *slog.Logger) *models.AgentUpdateStatus {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	path := filepath.Join(ResultDir, ResultFileName)
	data, err := readFileNoFollow(path, maxResultFileBytes)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("failed to read remote update result file", "path", path, "error", err)
		}
		return nil
	}

	var rf resultFile
	if err := json.Unmarshal(data, &rf); err != nil {
		logger.Warn("remote update result file has invalid JSON", "path", path, "error", err)
		return nil
	}
	if rf.JobID <= 0 {
		logger.Warn("remote update result file has invalid job_id", "path", path, "job_id", rf.JobID)
		return nil
	}
	if !validResultState(rf.State) {
		logger.Warn("remote update result file has invalid state", "path", path, "state", rf.State)
		return nil
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("failed to remove consumed remote update result file", "path", path, "error", err)
	}

	return &models.AgentUpdateStatus{
		JobID:     rf.JobID,
		State:     rf.State,
		Version:   rf.Version,
		ErrorCode: rf.ErrorCode,
		Error:     rf.Error,
	}
}

// readFileNoFollow opens path with O_NOFOLLOW (refusing to follow a
// symlink at the final path component — the parent directories are
// still resolved normally, matching the standard meaning of
// O_NOFOLLOW) and reads at most maxBytes+1 bytes, returning an error if
// the file is larger than maxBytes.
func readFileNoFollow(path string, maxBytes int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unixNoFollowFlag(), 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	limited := io.LimitReader(f, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds maximum size of %d bytes", path, maxBytes)
	}
	return data, nil
}
