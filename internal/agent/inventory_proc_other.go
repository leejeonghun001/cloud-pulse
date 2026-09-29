//go:build !linux

package agent

// processNameForPID is unimplemented on non-Linux platforms: the agent
// always returns "" for ListeningPort.Process there (documented on
// models.ListeningPort). Cross-platform process-name resolution would
// require gopsutil's process subpackage; SPEC-v0.5 §C scopes that to a
// future enhancement, not v0.5.0.
func processNameForPID(_ int32) string {
	return ""
}
