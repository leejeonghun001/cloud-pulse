// Package updatepaths defines the fixed remote-update request and result
// locations for each supported agent platform. It is the Go-side source of
// truth used by the agent, privileged helpers, and service renderers.
package updatepaths

// File names are shared by every platform's remote-update hand-off.
const (
	RequestFileName = "update-request.json"
	ResultFileName  = "result.json"
)

// Paths identifies the complete request and result locations for one agent
// platform. RequestPath is deliberately explicit rather than assembled with
// filepath.Join: callers and tests can inspect a Windows path while running
// on a non-Windows host.
type Paths struct {
	RequestDir  string
	RequestPath string
	ResultDir   string
}

// For returns the fixed locations for goos. The bool is false for platforms
// that do not implement privileged remote-update helpers.
func For(goos string) (Paths, bool) {
	switch goos {
	case "linux":
		return Paths{
			RequestDir:  "/var/lib/cloud-pulse-agent",
			RequestPath: "/var/lib/cloud-pulse-agent/" + RequestFileName,
			ResultDir:   "/var/lib/cloud-pulse-agent-update",
		}, true
	case "darwin":
		return Paths{
			RequestDir:  "/Library/Application Support/cloud-pulse-agent",
			RequestPath: "/Library/Application Support/cloud-pulse-agent/" + RequestFileName,
			ResultDir:   "/Library/Application Support/cloud-pulse-agent-update",
		}, true
	case "windows":
		return Paths{
			RequestDir:  `C:\ProgramData\cloud-pulse-agent`,
			RequestPath: `C:\ProgramData\cloud-pulse-agent\` + RequestFileName,
			ResultDir:   `C:\ProgramData\cloud-pulse-agent-update`,
		}, true
	default:
		return Paths{}, false
	}
}
