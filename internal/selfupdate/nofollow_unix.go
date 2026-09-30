//go:build linux || darwin || freebsd

package selfupdate

import "syscall"

// unixNoFollowFlag returns syscall.O_NOFOLLOW on platforms that define
// it. See fromrequest.go's readRequestFile.
func unixNoFollowFlag() int {
	return syscall.O_NOFOLLOW
}
