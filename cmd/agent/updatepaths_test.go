package main

import (
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/updatepaths"
)

func TestRemoteUpdatePathDefaultsUseCanonicalContract(t *testing.T) {
	t.Parallel()
	linux, _ := updatepaths.For("linux")
	windows, _ := updatepaths.For("windows")
	if got := defaultLinuxRequestPath(); got != linux.RequestPath {
		t.Errorf("defaultLinuxRequestPath() = %q, want %q", got, linux.RequestPath)
	}
	if got := defaultLinuxResultDir(); got != linux.ResultDir {
		t.Errorf("defaultLinuxResultDir() = %q, want %q", got, linux.ResultDir)
	}
	if got := defaultWindowsRequestDir(); got != windows.RequestDir {
		t.Errorf("defaultWindowsRequestDir() = %q, want %q", got, windows.RequestDir)
	}
	if got := defaultWindowsResultDir(); got != windows.ResultDir {
		t.Errorf("defaultWindowsResultDir() = %q, want %q", got, windows.ResultDir)
	}
}
