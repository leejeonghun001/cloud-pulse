package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReplace_Success(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	target := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	newFile := filepath.Join(dir, "cloud-pulse-hub.download")
	if err := os.WriteFile(newFile, []byte("new binary"), 0o644); err != nil {
		t.Fatalf("write newFile: %v", err)
	}

	if err := Replace(target, newFile); err != nil {
		t.Fatalf("Replace() error: %v", err)
	}

	got, err := os.ReadFile(target) //nolint:gosec // test-controlled path inside t.TempDir()
	if err != nil {
		t.Fatalf("read target after replace: %v", err)
	}
	if string(got) != "new binary" {
		t.Fatalf("target content = %q; want %q", got, "new binary")
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode = %o; want 0755", info.Mode().Perm())
	}

	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatalf("newFile still exists after Replace (err=%v); want it moved away", err)
	}
}

func TestReplace_RejectsDifferentDirectory(t *testing.T) {
	t.Parallel()
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	target := filepath.Join(dir1, "cloud-pulse-hub")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	newFile := filepath.Join(dir2, "cloud-pulse-hub.download")
	if err := os.WriteFile(newFile, []byte("new"), 0o644); err != nil {
		t.Fatalf("write newFile: %v", err)
	}

	err := Replace(target, newFile)
	if err == nil {
		t.Fatal("Replace() = nil error; want error for cross-directory newFile")
	}

	got, readErr := os.ReadFile(target) //nolint:gosec // test-controlled path inside t.TempDir()
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(got) != "old" {
		t.Fatal("target was modified despite Replace() returning an error")
	}
}

func TestReplace_MissingNewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	target := filepath.Join(dir, "cloud-pulse-hub")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	newFile := filepath.Join(dir, "does-not-exist")

	if err := Replace(target, newFile); err == nil {
		t.Fatal("Replace() = nil error; want error for missing newFile")
	}
}

func TestDirWritable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if !dirWritable(dir) {
		t.Fatalf("dirWritable(%q) = false; want true for a fresh TempDir", dir)
	}
	if dirWritable(filepath.Join(dir, "does-not-exist-subdir")) {
		t.Fatal("dirWritable() = true for a nonexistent directory; want false")
	}
}
