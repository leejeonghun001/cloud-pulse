//go:build windows

package selfupdate

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// openNoFollowWindows opens path for reading, refusing to follow a
// reparse point (symlink or junction) at the final path component —
// the Windows equivalent of O_NOFOLLOW on Unix (SPEC-v0.7 §1:
// "Windows: CreateFile에 FILE_FLAG_OPEN_REPARSE_POINT를 주고,
// FILE_ATTRIBUTE_REPARSE_POINT이면 거부한다"). CreateFile with
// FILE_FLAG_OPEN_REPARSE_POINT opens the reparse point itself rather
// than following it to its target; this function then checks the
// returned file's attributes and rejects it if FILE_ATTRIBUTE_REPARSE_POINT
// is set, closing the handle first so no caller can accidentally read
// through a half-validated handle.
func openNoFollowWindows(path string) (*os.File, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: convert path %s: %w", path, err)
	}

	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: open %s: %w", path, err)
	}

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("selfupdate: stat %s: %w", path, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("selfupdate: refusing to open %s: it is a symlink or junction (reparse point)", path)
	}

	return os.NewFile(uintptr(handle), path), nil
}

// openRequestFileNoFollow opens path read-only, refusing a reparse
// point at the final path component (see openNoFollowWindows). This is
// the function fromrequest.go's readRequestFile actually calls;
// nofollow_unix.go provides the Unix equivalent under the same name.
func openRequestFileNoFollow(path string) (*os.File, error) {
	return openNoFollowWindows(path)
}
