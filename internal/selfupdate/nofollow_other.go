//go:build !linux && !darwin && !freebsd && !windows

package selfupdate

import "os"

// unixNoFollowFlag returns 0 on platforms with no O_NOFOLLOW open flag
// and no Windows-specific safe-open path either (SPEC-v0.6 §2's
// request-mode is Linux/macOS/Windows-only per RunFromRequest's own
// platform gate — see fromrequest.go — so this stub is unreachable in
// practice; it exists only so internal/selfupdate builds portably on
// every GOOS the rest of cloud-pulse targets, e.g. freebsd already has
// its own real O_NOFOLLOW via nofollow_unix.go, so this file only ever
// matters for a hypothetical future GOOS with neither).
func unixNoFollowFlag() int {
	return 0
}

// openRequestFileNoFollow falls back to a plain open with no
// reparse-point/symlink protection on a hypothetical platform with
// neither O_NOFOLLOW nor Windows' CreateFile-based equivalent — never
// reached in practice since RunFromRequest's own platform gate (see
// fromrequest.go) refuses everywhere except linux/darwin/windows/freebsd.
func openRequestFileNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY, 0)
}
