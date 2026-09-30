package main

import (
	"fmt"
	"strings"
)

// windowsPathIsWithinProfile reports whether path is profile itself or is
// contained below it. Both inputs must already be canonical Windows paths.
// It compares path components, rather than raw string prefixes, so a sibling
// such as C:\\Users\\bob2 is not considered inside C:\\Users\\bob.
func windowsPathIsWithinProfile(profile, path string) bool {
	profileParts := splitWindowsPathComponents(profile)
	pathParts := splitWindowsPathComponents(path)
	if len(profileParts) == 0 || len(pathParts) < len(profileParts) {
		return false
	}
	for i, profilePart := range profileParts {
		if !strings.EqualFold(profilePart, pathParts[i]) {
			return false
		}
	}
	return true
}

// splitWindowsPathComponents splits a canonical Windows drive or UNC path
// into case-preserving components. It accepts either separator so tests can
// exercise Windows paths on every host operating system.
func splitWindowsPathComponents(path string) []string {
	path = strings.ReplaceAll(path, "/", `\`)
	parts := strings.Split(path, `\`)
	components := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			components = append(components, part)
		}
	}
	return components
}

// checkCredentialsFileLocationWindows canonicalizes path and profile using
// the supplied resolver, then checks component containment. Keeping the
// resolver injectable makes the security decision deterministic on all OSes;
// Windows supplies the resolver that expands short names and resolves links.
func checkCredentialsFileLocationWindows(path, profile string, canonicalize func(string) (string, error)) error {
	canonicalPath, err := canonicalize(path)
	if err != nil {
		return fmt.Errorf("%s: resolve canonical credentials-file path: %w", path, err)
	}
	canonicalProfile, err := canonicalize(profile)
	if err != nil {
		return fmt.Errorf("%s: resolve canonical user profile directory: %w", path, err)
	}
	if !windowsPathIsWithinProfile(canonicalProfile, canonicalPath) {
		return fmt.Errorf("%s: must be located inside your user profile directory (%s) on Windows, where NTFS ACLs default to owner-only access", path, profile)
	}
	return nil
}
