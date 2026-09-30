package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalizeWindowsPath_TempDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := canonicalizeWindowsPath(dir)
	if err != nil {
		t.Fatalf("canonicalizeWindowsPath(%q) error = %v", dir, err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("canonicalizeWindowsPath(%q) = %q, want absolute path", dir, got)
	}
}

func TestCheckCredentialsFilePermissionsWindows_TempDir(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "creds.env")
	if err := os.WriteFile(path, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}
	if err := checkCredentialsFilePermissionsWindows(path); err != nil {
		t.Fatalf("checkCredentialsFilePermissionsWindows(%q) error = %v, want temporary profile path accepted", path, err)
	}
}
