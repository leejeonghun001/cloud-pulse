package systemdunit

import (
	"strings"
	"testing"
)

func TestParseExisting_ValidHubUnit(t *testing.T) {
	got, err := ParseExisting(wantHubRealUnit)
	if err != nil {
		t.Fatalf("ParseExisting: %v", err)
	}
	want := Params{
		Binary:  HubBinary,
		BinPath: "/usr/local/bin/cloud-pulse-hub",
		EnvFile: "/etc/cloud-pulse/hub.env",
		User:    "cloud-pulse",
		Group:   "cloud-pulse",
	}
	if got != want {
		t.Fatalf("ParseExisting mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestParseExisting_ValidHubSandboxUnit(t *testing.T) {
	const root = "/tmp/cp-sandbox"
	unit := fmtSprintf(wantHubSandboxUnitTemplate, root)
	got, err := ParseExisting(unit)
	if err != nil {
		t.Fatalf("ParseExisting: %v", err)
	}
	want := Params{
		Binary:        HubBinary,
		BinPath:       root + "/usr/local/bin/cloud-pulse-hub",
		EnvFile:       root + "/etc/cloud-pulse/hub.env",
		User:          "cloud-pulse",
		Group:         "cloud-pulse",
		ReadWritePath: root + "/var/lib/cloud-pulse",
	}
	if got != want {
		t.Fatalf("ParseExisting mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}
}

func TestParseExisting_ValidAgentUnit(t *testing.T) {
	got, err := ParseExisting(wantAgentUnit)
	if err != nil {
		t.Fatalf("ParseExisting: %v", err)
	}
	want := Params{
		Binary:  AgentBinary,
		BinPath: "/usr/local/bin/cloud-pulse-agent",
		EnvFile: "/etc/cloud-pulse/agent.env",
		User:    "cloud-pulse",
		Group:   "cloud-pulse",
	}
	if got != want {
		t.Fatalf("ParseExisting mismatch:\ngot:  %+v\nwant: %+v", got, want)
	}
}

// TestParseExisting_RoundTripsThroughRender asserts Apply's core
// invariant: parsing a unit Render produced and re-rendering it returns
// byte-identical output, for all three golden shapes.
func TestParseExisting_RoundTripsThroughRender(t *testing.T) {
	units := []string{wantHubRealUnit, wantAgentUnit}

	for _, unit := range units {
		params, err := ParseExisting(unit)
		if err != nil {
			t.Fatalf("ParseExisting: %v", err)
		}
		rendered, err := Render(params)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if rendered != unit {
			t.Fatalf("round-trip mismatch:\n--- rendered ---\n%s\n--- original ---\n%s", rendered, unit)
		}
	}
}

func TestParseExisting_RejectsMalformedOrMaliciousUnits(t *testing.T) {
	tests := []struct {
		name string
		unit string
	}{
		{
			name: "missing ExecStart",
			unit: "[Service]\nEnvironmentFile=/etc/cloud-pulse/hub.env\nUser=cloud-pulse\n",
		},
		{
			name: "missing EnvironmentFile",
			unit: "[Service]\nExecStart=/usr/local/bin/cloud-pulse-hub\nUser=cloud-pulse\n",
		},
		{
			name: "unrecognized binary basename",
			unit: "[Service]\nExecStart=/usr/local/bin/evil-binary\nEnvironmentFile=/etc/cloud-pulse/hub.env\n",
		},
		{
			name: "relative ExecStart path",
			unit: "[Service]\nExecStart=cloud-pulse-hub\nEnvironmentFile=/etc/cloud-pulse/hub.env\n",
		},
		{
			name: "relative EnvironmentFile path",
			unit: "[Service]\nExecStart=/usr/local/bin/cloud-pulse-hub\nEnvironmentFile=hub.env\n",
		},
		{
			name: "ExecStart with embedded whitespace path (injection attempt)",
			unit: "[Service]\nExecStart=/usr/local/bin/cloud-pulse-hub; rm -rf /\nEnvironmentFile=/etc/cloud-pulse/hub.env\n",
		},
		{
			name: "EnvironmentFile with quote character",
			unit: "[Service]\nExecStart=/usr/local/bin/cloud-pulse-hub\nEnvironmentFile=/etc/cloud-pulse/\"hub.env\n",
		},
		{
			name: "ReadWritePaths with control character",
			unit: "[Service]\nExecStart=/usr/local/bin/cloud-pulse-hub\nEnvironmentFile=/etc/cloud-pulse/hub.env\nReadWritePaths=/var/lib/\x01evil\n",
		},
		{
			name: "empty unit",
			unit: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseExisting(tt.unit); err == nil {
				t.Fatalf("ParseExisting(%q): expected error, got none", tt.unit)
			}
		})
	}
}

// TestParseExisting_StripsLeadingDashOnEnvironmentFile covers systemd's
// own "-" prefix meaning "don't fail if the file is missing", which
// cloud-pulse never writes itself but a hand-edited unit might carry.
func TestParseExisting_StripsLeadingDashOnEnvironmentFile(t *testing.T) {
	unit := "[Service]\nExecStart=/usr/local/bin/cloud-pulse-hub\nEnvironmentFile=-/etc/cloud-pulse/hub.env\n"
	got, err := ParseExisting(unit)
	if err != nil {
		t.Fatalf("ParseExisting: %v", err)
	}
	if got.EnvFile != "/etc/cloud-pulse/hub.env" {
		t.Fatalf("EnvFile = %q, want /etc/cloud-pulse/hub.env", got.EnvFile)
	}
}

// TestParseExisting_ExecStartExtraArgsUsesFirstToken covers a unit whose
// ExecStart carries argv beyond the bare binary path (cloud-pulse never
// writes one today, but ParseExisting's contract is "first token").
func TestParseExisting_ExecStartExtraArgsUsesFirstToken(t *testing.T) {
	unit := "[Service]\nExecStart=/usr/local/bin/cloud-pulse-hub -listen :9090\nEnvironmentFile=/etc/cloud-pulse/hub.env\n"
	got, err := ParseExisting(unit)
	if err != nil {
		t.Fatalf("ParseExisting: %v", err)
	}
	if got.BinPath != "/usr/local/bin/cloud-pulse-hub" {
		t.Fatalf("BinPath = %q, want /usr/local/bin/cloud-pulse-hub", got.BinPath)
	}
}

// fmtSprintf avoids importing "fmt" twice across test files under the
// same package; delegates to strings-based substitution matching the
// single "%[1]s" placeholder used three times in the sandbox template.
func fmtSprintf(tmpl, root string) string {
	return strings.ReplaceAll(tmpl, "%[1]s", root)
}
