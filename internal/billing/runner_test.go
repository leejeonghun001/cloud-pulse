package billing

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestExecRunner_StubBinaryOnPATH writes a tiny shell script named
// "aws" into a fresh temp dir, prepends that dir to a runner-injected
// PATH (never the process's own os.Environ()), and verifies ExecRunner
// finds and executes it purely via the exec-lookup-on-PATH mechanism —
// proving the real production code path (fixed argv, explicit env)
// works end to end, without ever touching a real aws/oci binary. Unix
// only: Windows has no #!/bin/sh shebang support and this project skips
// exec-path tests there per this stage's task description.
func TestExecRunner_StubBinaryOnPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binaries require a POSIX shell")
	}
	t.Parallel()

	dir := t.TempDir()
	stubPath := filepath.Join(dir, "aws")
	script := "#!/bin/sh\necho '{\"stub\":true,\"args\":\"'\"$*\"'\"}'\n"
	if err := os.WriteFile(stubPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	runner := ExecRunner{}
	homeDir := t.TempDir()
	env := []string{"HOME=" + homeDir, "PATH=" + dir}

	res, err := runner.Run(context.Background(), homeDir, env, "aws", "ce", "get-cost-and-usage")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.NotFound {
		t.Fatal("Run reported NotFound for a stub binary that exists on the injected PATH")
	}
	if res.ExitCode != 0 {
		t.Fatalf("Run ExitCode = %d, want 0; stderr=%q", res.ExitCode, res.Stderr)
	}
	if !bytes.Contains(res.Stdout, []byte(`"stub":true`)) {
		t.Errorf("Run Stdout = %q, want it to contain the stub's JSON marker", res.Stdout)
	}
	if !bytes.Contains(res.Stdout, []byte("ce get-cost-and-usage")) {
		t.Errorf("Run Stdout = %q, want it to echo the exact argv passed", res.Stdout)
	}
}

// TestExecRunner_NotFound verifies a binary absent from the injected
// PATH is reported via Result.NotFound, not a Go error.
func TestExecRunner_NotFound(t *testing.T) {
	t.Parallel()

	runner := ExecRunner{}
	dir := t.TempDir()
	env := []string{"HOME=" + dir, "PATH=" + dir}

	res, err := runner.Run(context.Background(), dir, env, "definitely-not-a-real-binary-xyz")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.NotFound {
		t.Error("Run did not report NotFound for a nonexistent binary")
	}
}

// TestExecRunner_NonZeroExit verifies stderr/exit code are captured for
// a failing stub binary, without Run itself erroring.
func TestExecRunner_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binaries require a POSIX shell")
	}
	t.Parallel()

	dir := t.TempDir()
	stubPath := filepath.Join(dir, "aws")
	script := "#!/bin/sh\necho 'An error occurred (AccessDeniedException) when calling the GetCostAndUsage operation' >&2\nexit 254\n"
	if err := os.WriteFile(stubPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	runner := ExecRunner{}
	env := []string{"HOME=" + dir, "PATH=" + dir}
	res, err := runner.Run(context.Background(), dir, env, "aws", "ce", "get-cost-and-usage")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.ExitCode != 254 {
		t.Errorf("ExitCode = %d, want 254", res.ExitCode)
	}
	if !strings.Contains(string(res.Stderr), "AccessDeniedException") {
		t.Errorf("Stderr = %q, want it to contain AccessDeniedException", res.Stderr)
	}
}

// TestExecRunner_Timeout verifies a call exceeding CallTimeout is
// reported via Result.TimedOut. Uses a short injected context deadline
// rather than waiting for the real 60s CallTimeout.
func TestExecRunner_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binaries require a POSIX shell")
	}
	t.Parallel()

	dir := t.TempDir()
	stubPath := filepath.Join(dir, "aws")
	script := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(stubPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	runner := ExecRunner{}
	env := []string{"HOME=" + dir, "PATH=" + dir + ":/usr/bin:/bin"}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	res, err := runner.Run(ctx, dir, env, "aws")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.TimedOut {
		t.Error("Run did not report TimedOut for a call exceeding its context deadline")
	}
}

// TestExecRunner_OutputCappedAt4MiB verifies stdout beyond MaxOutputBytes
// is truncated rather than growing unbounded.
func TestExecRunner_OutputCappedAt4MiB(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub binaries require a POSIX shell")
	}
	t.Parallel()

	dir := t.TempDir()
	stubPath := filepath.Join(dir, "aws")
	// Print well over MaxOutputBytes (4 MiB) worth of 'x' characters.
	script := "#!/bin/sh\nyes x | head -c 5000000\n"
	if err := os.WriteFile(stubPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	runner := ExecRunner{}
	env := []string{"HOME=" + dir, "PATH=" + dir + ":/usr/bin:/bin"}
	res, err := runner.Run(context.Background(), dir, env, "aws")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !res.StdoutTruncated {
		t.Error("Run did not report StdoutTruncated for output exceeding MaxOutputBytes")
	}
	if len(res.Stdout) > MaxOutputBytes {
		t.Errorf("len(Stdout) = %d, want <= %d", len(res.Stdout), MaxOutputBytes)
	}
}
