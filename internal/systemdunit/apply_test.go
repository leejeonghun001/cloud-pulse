package systemdunit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApply_UpToDate_NoChange(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "cloud-pulse-hub.service")
	if err := os.WriteFile(unitPath, []byte(wantHubRealUnit), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var out bytes.Buffer
	changed, err := Apply(context.Background(), unitPath, false, &out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if changed {
		t.Fatalf("Apply reported changed=true for an already up-to-date unit")
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Fatalf("expected 'up to date' message, got: %s", out.String())
	}

	// No .bak/.tmp should have been created.
	if _, err := os.Stat(unitPath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf(".bak file should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(unitPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf(".tmp file should not exist, stat err = %v", err)
	}

	// Content on disk is untouched.
	got, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != wantHubRealUnit {
		t.Fatalf("unit content changed unexpectedly")
	}
}

func TestApply_Outdated_RewritesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "cloud-pulse-hub.service")

	// An "outdated" unit: same Params but missing the hardening block
	// text this package's Render always adds back — simulate drift by
	// starting from an old rendering that used a different EnvFile,
	// which Render will normalize into the new canonical text.
	outdated := strings.Replace(wantHubRealUnit, "RestartSec=5\n", "RestartSec=3\n", 1)
	if err := os.WriteFile(unitPath, []byte(outdated), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var out bytes.Buffer
	changed, err := Apply(context.Background(), unitPath, false, &out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !changed {
		t.Fatalf("Apply reported changed=false for a modified unit")
	}
	if !strings.Contains(out.String(), "updated systemd unit") {
		t.Fatalf("expected 'updated systemd unit' message, got: %s", out.String())
	}

	gotBackup, err := os.ReadFile(unitPath + ".bak")
	if err != nil {
		t.Fatalf("ReadFile .bak: %v", err)
	}
	if string(gotBackup) != outdated {
		t.Fatalf(".bak content = %q, want original outdated content", gotBackup)
	}

	gotNew, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(gotNew) != wantHubRealUnit {
		t.Fatalf("rewritten unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", gotNew, wantHubRealUnit)
	}

	if _, err := os.Stat(unitPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf(".tmp file should not remain after a successful rename, stat err = %v", err)
	}
}

func TestApply_SecondRunAfterRewriteIsUpToDate(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "cloud-pulse-agent.service")

	outdated := strings.Replace(wantAgentUnit, "RestartSec=5\n", "RestartSec=1\n", 1)
	if err := os.WriteFile(unitPath, []byte(outdated), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx := context.Background()
	if _, err := Apply(ctx, unitPath, false, nil); err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	var out bytes.Buffer
	changed, err := Apply(ctx, unitPath, false, &out)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if changed {
		t.Fatalf("second Apply should report changed=false (already up to date)")
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Fatalf("expected 'up to date' message on second run, got: %s", out.String())
	}
}

func TestApply_ReloadFalse_NeverInvokesSystemctl(t *testing.T) {
	// This test's real assertion is implicit: if daemonReload were
	// invoked in a sandboxed CI/test environment without systemctl
	// available (or without permission), Apply would return an error.
	// reload=false must never attempt it regardless of environment.
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "cloud-pulse-hub.service")
	outdated := strings.Replace(wantHubRealUnit, "RestartSec=5\n", "RestartSec=9\n", 1)
	if err := os.WriteFile(unitPath, []byte(outdated), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Apply(context.Background(), unitPath, false, nil); err != nil {
		t.Fatalf("Apply with reload=false: %v", err)
	}
}

func TestApply_MissingUnitFile(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "does-not-exist.service")

	if _, err := Apply(context.Background(), unitPath, false, nil); err == nil {
		t.Fatalf("Apply on a missing unit file: expected error, got none")
	}
}

func TestApply_MalformedExistingUnit(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "cloud-pulse-hub.service")
	if err := os.WriteFile(unitPath, []byte("not a valid unit file\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Apply(context.Background(), unitPath, false, nil); err == nil {
		t.Fatalf("Apply on a malformed unit file: expected error, got none")
	}
}
