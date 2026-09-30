//go:build linux || darwin || freebsd

package agent

import "syscall"

// unixNoFollowFlag returns syscall.O_NOFOLLOW on platforms that define
// it (all POSIX targets cloud-pulse builds for except Windows). See
// remoteupdate.go's readFileNoFollow.
func unixNoFollowFlag() int {
	return syscall.O_NOFOLLOW
}
