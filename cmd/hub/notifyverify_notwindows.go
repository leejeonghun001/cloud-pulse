//go:build !windows

package main

import "fmt"

// checkCredentialsFilePermissionsWindows is unreachable on non-Windows
// systems; it exists so the runtime platform dispatch compiles everywhere.
func checkCredentialsFilePermissionsWindows(path string) error {
	return fmt.Errorf("%s: Windows credential-file checks are not applicable on this platform", path)
}
