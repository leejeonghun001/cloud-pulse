//go:build windows

package listen

import (
	"errors"
	"syscall"
)

// wsaEADDRNOTAVAIL is Winsock's WSAEADDRNOTAVAIL (10049). Go's
// syscall.EADDRNOTAVAIL on Windows is an invented constant that
// net.Listen never returns, so the Winsock code must be checked directly.
const wsaEADDRNOTAVAIL = syscall.Errno(10049)

// isAddrNotAvailable reports whether err is the OS error for binding to an
// address that is not (yet) assigned to any local interface.
func isAddrNotAvailable(err error) bool {
	return errors.Is(err, wsaEADDRNOTAVAIL) || errors.Is(err, syscall.EADDRNOTAVAIL)
}
