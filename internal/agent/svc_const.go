package agent

// WindowsServiceName is the fixed Windows service name
// cloud-pulse-agent registers itself under (SPEC-v0.7 §1). Defined in
// this build-tag-free file (not svc_windows.go/svc_other.go) so it
// resolves identically on every OS — cmd/agent/service.go references
// it in non-Windows-tagged code, guarded at runtime by a runtime.GOOS
// check rather than a build tag.
const WindowsServiceName = "cloud-pulse-agent"
