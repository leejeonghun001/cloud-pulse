package models

// AgentOS identifies an agent-supported operating system family, used
// to select the right one-liner in the dashboard's "Add agent" dialog
// (SPEC-v0.7 §1) and to interpret RemoteUpdateCapability.Platform.
type AgentOS string

// Supported agent operating systems.
const (
	AgentOSLinux   AgentOS = "linux"
	AgentOSDarwin  AgentOS = "darwin"
	AgentOSWindows AgentOS = "windows"
)

// AgentInstallCommand is one OS tab's ready-to-run install one-liner,
// returned alongside the existing agent-token reveal endpoint (GET
// /api/v1/settings/agent-token) so the dashboard can render Linux/
// macOS/Windows tabs without hardcoding the script URLs client-side.
// See models.AgentTokenView.InstallCommands (extended in place, kept
// backward compatible with the pre-v0.7.0 single-command shape).
type AgentInstallCommand struct {
	OS AgentOS `json:"os"`
	// Label is a human-readable name for the OS tab, e.g. "Linux
	// (systemd)", "macOS (launchd)", "Windows (service)".
	Label string `json:"label"`
	// Command is the full copy-pasteable one-liner (curl|bash for
	// Linux/macOS, an iex/irm PowerShell invocation for Windows).
	Command string `json:"command"`
}
