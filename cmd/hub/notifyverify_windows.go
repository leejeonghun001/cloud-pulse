//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// checkCredentialsFilePermissionsUnix is unreachable on Windows; see
// checkCredentialsFilePermissions in notifyverify.go.
func checkCredentialsFilePermissionsUnix(path string) error {
	return fmt.Errorf("%s: unix permission checks are not applicable on windows", path)
}

// checkCredentialsFilePermissionsWindows requires credentials files to be
// inside the current user's canonical profile path. NTFS ACLs are not
// independently inspected; see SPEC-v0.7's Windows limitation.
func checkCredentialsFilePermissionsWindows(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("%s: could not resolve user profile directory to check credentials-file location: %w", path, err)
	}
	if err := checkCredentialsFileLocationWindows(path, home, canonicalizeWindowsPath); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stderr, "warning: cloud-pulse-hub notify verify: on Windows, only the credentials file's location (inside your user profile) is checked — its NTFS ACLs are not independently verified; ensure it is not shared with other accounts")
	return nil
}

// canonicalizeWindowsPath returns an absolute canonical path. EvalSymlinks
// normally normalizes path case and expands 8.3 short names. GetLongPathName
// provides the same short-name expansion when link resolution is unavailable.
func canonicalizeWindowsPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	resolved, evalErr := filepath.EvalSymlinks(absolute)
	if evalErr == nil {
		return resolved, nil
	}
	longPath, longErr := getLongWindowsPath(absolute)
	if longErr == nil {
		return longPath, nil
	}
	return "", fmt.Errorf("canonicalize %q: %w", absolute, errors.Join(
		fmt.Errorf("filepath.EvalSymlinks: %w", evalErr),
		fmt.Errorf("GetLongPathName fallback: %w", longErr),
	))
}

func getLongWindowsPath(path string) (string, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("encode path: %w", err)
	}
	for size := uint32(260); ; {
		buffer := make([]uint16, size)
		n, err := windows.GetLongPathName(pathPtr, &buffer[0], size)
		if err != nil {
			return "", fmt.Errorf("GetLongPathName: %w", err)
		}
		if n == 0 {
			return "", fmt.Errorf("GetLongPathName returned an empty path")
		}
		if n < size {
			return windows.UTF16ToString(buffer[:n]), nil
		}
		if n == ^uint32(0) {
			return "", fmt.Errorf("GetLongPathName returned an invalid required length")
		}
		size = n + 1
	}
}
