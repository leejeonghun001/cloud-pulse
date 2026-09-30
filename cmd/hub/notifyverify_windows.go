//go:build windows

// notifyverify_windows.go provides the Windows stub for
// checkCredentialsFilePermissionsUnix, which is never actually called on
// Windows (checkCredentialsFilePermissions branches on runtime.GOOS
// before calling either half) but must still exist at compile time since
// Go compiles every file matching the build constraints for the target
// GOOS regardless of which runtime branch would be taken.
package main

import "fmt"

// checkCredentialsFilePermissionsUnix is unreachable on Windows; see
// checkCredentialsFilePermissions in notifyverify.go.
func checkCredentialsFilePermissionsUnix(path string) error {
	return fmt.Errorf("%s: unix permission checks are not applicable on windows", path)
}
