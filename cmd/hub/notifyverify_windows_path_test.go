package main

import (
	"fmt"
	"testing"
)

func TestWindowsPathIsWithinProfile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		profile string
		path    string
		want    bool
	}{
		{
			name:    "expanded short-name canonical path",
			profile: `C:\Users\runneradmin`,
			path:    `C:\Users\runneradmin\AppData\Local\Temp\creds.env`,
			want:    true,
		},
		{
			name:    "case differences",
			profile: `C:\Users\RunnerAdmin`,
			path:    `c:\users\runneradmin\APPDATA\creds.env`,
			want:    true,
		},
		{
			name:    "trailing separators",
			profile: `C:\Users\runneradmin\\`,
			path:    `C:\Users\runneradmin\AppData\\`,
			want:    true,
		},
		{
			name:    "sibling prefix is not contained",
			profile: `C:\Users\bob`,
			path:    `C:\Users\bob2\creds.env`,
			want:    false,
		},
		{
			name:    "different volume",
			profile: `C:\Users\runneradmin`,
			path:    `D:\Users\runneradmin\creds.env`,
			want:    false,
		},
		{
			name:    "UNC case differences",
			profile: `\\server\profiles\runneradmin`,
			path:    `\\SERVER\PROFILES\RUNNERADMIN\AppData\creds.env`,
			want:    true,
		},
		{
			name:    "UNC different share",
			profile: `\\server\profiles\runneradmin`,
			path:    `\\server\other-profiles\runneradmin\creds.env`,
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := windowsPathIsWithinProfile(tt.profile, tt.path); got != tt.want {
				t.Errorf("windowsPathIsWithinProfile(%q, %q) = %t, want %t", tt.profile, tt.path, got, tt.want)
			}
		})
	}
}

func TestCheckCredentialsFileLocationWindows_CanonicalizesShortName(t *testing.T) {
	t.Parallel()
	profile := `C:\Users\runneradmin`
	shortPath := `C:\Users\RUNNER~1\AppData\Local\Temp\creds.env`
	canonicalPath := `C:\Users\runneradmin\AppData\Local\Temp\creds.env`
	if windowsPathIsWithinProfile(profile, shortPath) {
		t.Fatal("raw short path unexpectedly passed containment; this regression test requires canonicalization")
	}

	canonicalize := func(path string) (string, error) {
		switch path {
		case profile:
			return profile, nil
		case shortPath:
			return canonicalPath, nil
		default:
			return "", fmt.Errorf("unexpected path %q", path)
		}
	}
	if err := checkCredentialsFileLocationWindows(shortPath, profile, canonicalize); err != nil {
		t.Fatalf("checkCredentialsFileLocationWindows() error = %v, want canonical short path accepted", err)
	}
}
