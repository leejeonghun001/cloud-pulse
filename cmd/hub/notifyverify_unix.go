//go:build !windows

// notifyverify_unix.go implements the Unix half of
// checkCredentialsFilePermissions: owner + 0600-or-stricter mode check,
// identical to scripts/verify-notify.py's
// _check_credentials_file_permissions.
package main

import (
	"fmt"
	"os"
	"syscall"
)

// checkCredentialsFilePermissionsUnix rejects path if it is not owned by
// the current effective user, or if its mode grants any permission to
// group/other.
func checkCredentialsFilePermissionsUnix(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat credentials file: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: could not determine file ownership on this platform", path)
	}
	if int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s: not owned by the current user", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s: must not be readable/writable by group or other (chmod 600)", path)
	}
	return nil
}
