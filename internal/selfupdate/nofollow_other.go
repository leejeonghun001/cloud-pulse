//go:build !linux && !darwin && !freebsd

package selfupdate

// unixNoFollowFlag returns 0 on platforms with no O_NOFOLLOW open flag
// (e.g. Windows). Remote update request-mode is a Linux+systemd-only
// feature (SPEC-v0.6 §2); this stub exists only so internal/selfupdate
// builds portably everywhere the rest of cloud-pulse does.
func unixNoFollowFlag() int {
	return 0
}
