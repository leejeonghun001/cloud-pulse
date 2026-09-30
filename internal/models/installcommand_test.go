package models

import (
	"encoding/json"
	"testing"
)

func TestAgentInstallCommand_JSONRoundTrip(t *testing.T) {
	cmd := AgentInstallCommand{
		OS:      AgentOSDarwin,
		Label:   "macOS (launchd)",
		Command: "curl -fsSL https://example.invalid/install-agent.sh | sudo bash",
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got AgentInstallCommand
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != cmd {
		t.Errorf("round-trip mismatch: got %#v, want %#v", got, cmd)
	}
}

func TestAgentTokenView_InstallCommandsOmittedWhenNil(t *testing.T) {
	view := AgentTokenView{AgentToken: "tok", InstallCommand: "curl ..."}
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["install_commands"]; ok {
		t.Errorf("install_commands should be omitted when nil, got %v", raw["install_commands"])
	}
}

func TestAgentTokenView_InstallCommandsPresent(t *testing.T) {
	view := AgentTokenView{
		AgentToken:     "tok",
		InstallCommand: "curl ...",
		InstallCommands: []AgentInstallCommand{
			{OS: AgentOSLinux, Label: "Linux (systemd)", Command: "curl ..."},
			{OS: AgentOSDarwin, Label: "macOS (launchd)", Command: "curl ..."},
			{OS: AgentOSWindows, Label: "Windows (service)", Command: "irm ... | iex"},
		},
	}
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got AgentTokenView
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.InstallCommands) != 3 {
		t.Errorf("InstallCommands len = %d, want 3", len(got.InstallCommands))
	}
}
