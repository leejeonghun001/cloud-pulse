//go:build !windows

package main

import (
	"os"
	"syscall"
)

// statOwner returns path's owning uid/gid on POSIX platforms, ok=false
// if path can't be stat'd or the platform doesn't expose numeric
// ownership via Stat_t.
func statOwner(path string) (uid, gid int, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
