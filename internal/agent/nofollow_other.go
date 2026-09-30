//go:build !linux && !darwin && !freebsd

package agent

// unixNoFollowFlag returns 0 on platforms with no O_NOFOLLOW open flag
// (e.g. Windows). Remote update is a Linux+systemd-only feature (SPEC-v0.6
// §2), so ResultDir/RequestStateDir are never populated on such
// platforms in practice; this stub exists only so internal/agent builds
// portably everywhere the rest of cloud-pulse does.
func unixNoFollowFlag() int {
	return 0
}
