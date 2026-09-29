package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Replace atomically installs newFile as target. newFile must reside in
// the same directory as target so the rename is a single filesystem
// operation (guaranteed atomic on the same volume) rather than a
// cross-device copy. The resulting file is chmod'd 0755.
//
// On Unix, this is a single rename(2) of newFile onto target: there is
// no window where target is missing or truncated, and a process that
// already has the old file open via its existing file descriptor keeps
// running against the old bytes until it restarts (see D-030's rationale
// in DECISIONS_LOG.md for the same pattern in the install scripts).
//
// On Windows, a rename cannot replace an open file in place, so target
// is first renamed to target+".old" (freeing the target path even while
// the running process holds the old file open), then newFile is renamed
// to target; the ".old" file is removed on a best-effort basis (it may
// still be open by the running process and fail to delete — that's not
// an error for Replace's purposes, just a leftover cleaned up on the
// next successful update or manually).
func Replace(target, newFile string) error {
	if filepath.Dir(newFile) != filepath.Dir(target) {
		return fmt.Errorf("selfupdate: replace: newFile %q must be in the same directory as target %q", newFile, target)
	}

	if err := os.Chmod(newFile, 0o755); err != nil {
		return fmt.Errorf("selfupdate: replace: chmod new binary: %w", err)
	}

	if runtime.GOOS == "windows" {
		return replaceWindows(target, newFile)
	}
	return replaceUnix(target, newFile)
}

// replaceUnix performs the single-rename atomic replace used on every
// non-Windows target.
func replaceUnix(target, newFile string) error {
	if err := os.Rename(newFile, target); err != nil {
		return fmt.Errorf("selfupdate: replace: rename %q to %q: %w", newFile, target, err)
	}
	return nil
}

// replaceWindows performs the rename-away-then-rename-in two-step
// replace Windows requires, since it cannot rename over a file that's
// open (a running executable's own image, in particular).
func replaceWindows(target, newFile string) error {
	oldPath := target + ".old"
	// Best-effort: a stale .old from a previous update attempt should
	// not block this one.
	_ = os.Remove(oldPath)

	if err := os.Rename(target, oldPath); err != nil {
		return fmt.Errorf("selfupdate: replace: rename current binary aside: %w", err)
	}
	if err := os.Rename(newFile, target); err != nil {
		// Attempt to restore the original binary so a failed update
		// doesn't leave the target path missing.
		_ = os.Rename(oldPath, target)
		return fmt.Errorf("selfupdate: replace: rename new binary into place: %w", err)
	}
	// Best-effort cleanup; the running process may still hold oldPath
	// open, in which case this simply leaves a harmless leftover file.
	_ = os.Remove(oldPath)
	return nil
}

// dirWritable reports whether dir appears writable by the current
// process, by attempting to create and immediately remove a temp file
// inside it. This is used as a pre-flight permission check before
// downloading anything, so a permission failure is reported quickly
// with an actionable message instead of after a multi-MB download.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".cloud-pulse-update-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
