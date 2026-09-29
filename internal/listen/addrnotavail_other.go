//go:build !windows

package listen

import (
	"errors"
	"syscall"
)

// isAddrNotAvailable reports whether err is the OS error for binding to an
// address that is not (yet) assigned to any local interface, e.g. an
// interface such as tailscale0 that is not up at boot.
func isAddrNotAvailable(err error) bool {
	return errors.Is(err, syscall.EADDRNOTAVAIL)
}
