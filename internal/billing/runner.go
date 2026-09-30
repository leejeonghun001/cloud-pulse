// Package billing implements SPEC-v0.6 §1's periodic cloud billing
// collection: shelling out to the aws/oci CLIs (never a shell, fixed
// argv, minimal sandboxed environment) to fetch each configured
// provider's month-to-date cost and month-end forecast, classifying
// every quiet-skip condition (CLI missing, unauthenticated, permission
// denied, timed out) without ever logging raw CLI stderr, and combining
// the result with host<->cloud-resource matching for the dashboard's
// Costs page.
package billing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CallTimeout bounds every individual aws/oci CLI invocation (SPEC-v0.6
// §1: "호출마다 60초 타임아웃").
const CallTimeout = 60 * time.Second

// MaxOutputBytes caps how much of a CLI call's stdout/stderr is read
// into memory (SPEC-v0.6 §1: "출력 읽기는 4 MiB로 제한한다").
const MaxOutputBytes = 4 << 20 // 4 MiB

// ErrOutputTruncated is wrapped into a CommandRunner result's error (or
// reported via Result.StdoutTruncated/StderrTruncated, see Result) when
// a stream exceeded MaxOutputBytes. Exec's real implementation reports
// truncation via the Result fields rather than failing the call outright
// — a truncated-but-parseable JSON prefix should still fail cleanly at
// the JSON-decode step with a normal parse error, not a distinct "output
// too big" error class.
var ErrOutputTruncated = errors.New("billing: command output exceeded size limit")

// Result is the outcome of one CommandRunner.Run call.
type Result struct {
	// Stdout is the command's standard output, truncated to
	// MaxOutputBytes.
	Stdout []byte
	// Stderr is the command's standard error, truncated to
	// MaxOutputBytes. Callers must never log this raw (SPEC-v0.6 §1) —
	// only a classified status/StatusDetail derived from it.
	Stderr []byte
	// StdoutTruncated/StderrTruncated report whether the corresponding
	// stream exceeded MaxOutputBytes and was cut off.
	StdoutTruncated bool
	StderrTruncated bool
	// ExitCode is the process exit code, or -1 if the process never
	// started (e.g. binary not found) or was killed by a signal.
	ExitCode int
	// TimedOut reports whether the call was terminated because it
	// exceeded its timeout.
	TimedOut bool
	// NotFound reports whether the named binary could not be located on
	// PATH (wraps exec.ErrNotFound at the CommandRunner level so callers
	// never need to inspect the raw error type).
	NotFound bool
}

// CommandRunner executes an external command with a fixed argv (never a
// shell), a bounded lifetime, and an explicitly constructed environment
// — injected so internal/billing's collectors can be tested with a fake
// instead of ever invoking a real aws/oci binary.
type CommandRunner interface {
	// Run executes name with args (no shell interpretation) in working
	// directory dir with exactly env as the child's environment
	// (implementations must not merge in the parent process's own
	// environment). The call is bounded to CallTimeout regardless of
	// ctx's own deadline (Run applies the shorter of the two). Run
	// itself never returns a non-nil error for a normal nonzero exit,
	// timeout, or missing binary — those are reported via the returned
	// Result; a non-nil error indicates Run's own bookkeeping failed
	// (e.g. dir could not be statted), which should be rare.
	Run(ctx context.Context, dir string, env []string, name string, args ...string) (Result, error)
}

// ExecRunner is the production CommandRunner: os/exec with a fixed argv,
// CallTimeout, and MaxOutputBytes-capped output capture.
type ExecRunner struct{}

// Run implements CommandRunner using os/exec.CommandContext. The binary
// named by name is resolved against env's own PATH entry (not the
// calling process's environment) via lookPathIn, so a test-injected PATH
// is what actually determines which binary runs — exec.Command itself
// only consults os.Getenv("PATH") for lookup, which would silently
// ignore an injected env's PATH otherwise.
func (ExecRunner) Run(ctx context.Context, dir string, env []string, name string, args ...string) (Result, error) {
	callCtx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	resolved, err := lookPathIn(name, envValue(env, "PATH"))
	if err != nil {
		return Result{NotFound: true, ExitCode: -1}, nil
	}

	cmd := exec.CommandContext(callCtx, resolved, args...)
	cmd.Dir = dir
	cmd.Env = env
	// WaitDelay bounds how long Wait() blocks on the stdout/stderr
	// pipe-copy goroutines after the context is canceled and the direct
	// child is killed — without it, a shell script whose own child
	// (e.g. `sleep`) inherits the pipe fd can keep Wait() blocked until
	// that grandchild exits on its own, defeating CallTimeout entirely.
	// See https://pkg.go.dev/os/exec#Cmd.WaitDelay.
	cmd.WaitDelay = 2 * time.Second

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutTrunc, err := attachCappedWriter(cmd, &stdoutBuf, false)
	if err != nil {
		return Result{}, fmt.Errorf("billing: exec runner: stdout pipe: %w", err)
	}
	stderrTrunc, err := attachCappedWriter(cmd, &stderrBuf, true)
	if err != nil {
		return Result{}, fmt.Errorf("billing: exec runner: stderr pipe: %w", err)
	}

	runErr := cmd.Run()

	res := Result{
		Stdout:          stdoutBuf.Bytes(),
		Stderr:          stderrBuf.Bytes(),
		StdoutTruncated: *stdoutTrunc,
		StderrTruncated: *stderrTrunc,
		ExitCode:        -1,
	}

	if runErr != nil {
		if errors.Is(runErr, exec.ErrNotFound) {
			res.NotFound = true
			return res, nil
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		if callCtx.Err() == context.DeadlineExceeded {
			res.TimedOut = true
		}
		return res, nil
	}
	res.ExitCode = 0
	return res, nil
}

// envValue returns the value of key within env (a "KEY=value" slice, the
// same shape passed to exec.Cmd.Env), or "" if key is absent.
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):]
		}
	}
	return ""
}

// lookPathIn resolves name to an absolute path by searching pathEnv (a
// colon-separated PATH value, semicolon on Windows) — deliberately not
// exec.LookPath, which only ever consults the calling process's own
// os.Getenv("PATH") and would ignore a runner-injected PATH entirely.
func lookPathIn(name, pathEnv string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		// Already a path (relative or absolute); exec.LookPath's own
		// rule applies: verify it's an executable regular file.
		return exec.LookPath(name)
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && isExecutable(info) {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

// isExecutable reports whether info's permission bits include an
// execute bit for owner, group, or other (Unix semantics; on Windows
// every regular file found by lookPathIn is treated as executable,
// matching os/exec's own platform convention).
func isExecutable(info os.FileInfo) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

// attachCappedWriter wires an io.Writer capped at MaxOutputBytes (plus
// one sentinel byte to detect truncation) into cmd's Stdout or Stderr,
// returning a pointer to the bool that will be set once Run completes.
func attachCappedWriter(cmd *exec.Cmd, buf *bytes.Buffer, stderr bool) (*bool, error) {
	truncated := new(bool)
	w := &cappedWriter{buf: buf, limit: MaxOutputBytes, truncated: truncated}
	if stderr {
		cmd.Stderr = w
	} else {
		cmd.Stdout = w
	}
	return truncated, nil
}

// cappedWriter writes into buf up to limit bytes, silently discarding
// (but counting) anything beyond that and setting *truncated once.
type cappedWriter struct {
	buf       *bytes.Buffer
	limit     int
	truncated *bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.limit {
		*w.truncated = true
		return len(p), nil // report full consumption to the writer (io.MultiWriter-safe), drop the data
	}
	remaining := w.limit - w.buf.Len()
	if len(p) > remaining {
		*w.truncated = true
		_, err := w.buf.Write(p[:remaining])
		return len(p), err
	}
	return w.buf.Write(p)
}
