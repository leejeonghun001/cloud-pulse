//go:build linux || darwin || freebsd

package selfupdate

import (
	"os"
	"syscall"
)

// unixNoFollowFlag returns syscall.O_NOFOLLOW on platforms that define
// it. See fromrequest.go's readRequestFile.
func unixNoFollowFlag() int {
	return syscall.O_NOFOLLOW
}

// openRequestFileNoFollow opens path read-only with O_NOFOLLOW on Unix
// platforms. See nofollow_windows.go for the Windows equivalent.
func openRequestFileNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unixNoFollowFlag(), 0)
}
