//go:build windows

package main

// statOwner is a no-op on Windows, which has no numeric uid/gid
// ownership model compatible with os.Chown; chownToDataDirOwner's
// runtime.GOOS check already skips calling this, but it's provided so
// the package still builds on windows/386, windows/amd64, etc.
func statOwner(_ string) (uid, gid int, ok bool) {
	return 0, 0, false
}
