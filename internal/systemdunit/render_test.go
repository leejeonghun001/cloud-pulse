package systemdunit

import (
	"fmt"
	"strings"
	"testing"
)

// The expected strings below are transcribed verbatim from
// scripts/install-hub.sh's render_unit() and
// scripts/install-agent.sh's render_unit() (the exact `echo` sequences),
// so any drift between the bash heredoc and this package's Render is
// caught here rather than at runtime by a test-install.sh diff.

const wantHubRealUnit = `[Unit]
Description=cloud-pulse hub (metrics ingestion + dashboard)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/cloud-pulse/hub.env
Environment=CP_DATA_DIR=/var/lib/cloud-pulse
ExecStart=/usr/local/bin/cloud-pulse-hub
User=cloud-pulse
Group=cloud-pulse
Restart=on-failure
RestartSec=5
StateDirectory=cloud-pulse

# --- sandboxing / hardening ---
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK

[Install]
WantedBy=multi-user.target
`

const wantHubSandboxUnitTemplate = `[Unit]
Description=cloud-pulse hub (metrics ingestion + dashboard)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=%[1]s/etc/cloud-pulse/hub.env
Environment=CP_DATA_DIR=/var/lib/cloud-pulse
ExecStart=%[1]s/usr/local/bin/cloud-pulse-hub
User=cloud-pulse
Group=cloud-pulse
Restart=on-failure
RestartSec=5
ReadWritePaths=%[1]s/var/lib/cloud-pulse

# --- sandboxing / hardening ---
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK

[Install]
WantedBy=multi-user.target
`

const wantAgentUnit = `[Unit]
Description=cloud-pulse agent (host metrics collector)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/cloud-pulse/agent.env
ExecStart=/usr/local/bin/cloud-pulse-agent
User=cloud-pulse
Group=cloud-pulse
Restart=on-failure
RestartSec=5

# --- sandboxing / hardening ---
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
`

func TestRender_HubReal_MatchesInstallScriptHeredoc(t *testing.T) {
	got, err := Render(Params{
		Binary:  HubBinary,
		BinPath: "/usr/local/bin/cloud-pulse-hub",
		EnvFile: "/etc/cloud-pulse/hub.env",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != wantHubRealUnit {
		t.Fatalf("hub real unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, wantHubRealUnit)
	}
}

func TestRender_HubSandbox_MatchesInstallScriptHeredoc(t *testing.T) {
	const sandboxRoot = "/tmp/cp-sandbox-XXXX"
	got, err := Render(Params{
		Binary:        HubBinary,
		BinPath:       sandboxRoot + "/usr/local/bin/cloud-pulse-hub",
		EnvFile:       sandboxRoot + "/etc/cloud-pulse/hub.env",
		ReadWritePath: sandboxRoot + "/var/lib/cloud-pulse",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := fmt.Sprintf(wantHubSandboxUnitTemplate, sandboxRoot)
	if got != want {
		t.Fatalf("hub sandbox unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRender_Agent_MatchesInstallScriptHeredoc(t *testing.T) {
	got, err := Render(Params{
		Binary:  AgentBinary,
		BinPath: "/usr/local/bin/cloud-pulse-agent",
		EnvFile: "/etc/cloud-pulse/agent.env",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != wantAgentUnit {
		t.Fatalf("agent unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, wantAgentUnit)
	}
}

// TestRender_Agent_IgnoresReadWritePath asserts the spec's "print for
// the agent ignores --read-write-path" requirement: passing one has no
// effect on the rendered agent unit.
func TestRender_Agent_IgnoresReadWritePath(t *testing.T) {
	got, err := Render(Params{
		Binary:        AgentBinary,
		BinPath:       "/usr/local/bin/cloud-pulse-agent",
		EnvFile:       "/etc/cloud-pulse/agent.env",
		ReadWritePath: "/some/sandbox/path",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != wantAgentUnit {
		t.Fatalf("agent unit with ReadWritePath set should be unaffected:\n--- got ---\n%s\n--- want ---\n%s", got, wantAgentUnit)
	}
	if strings.Contains(got, "some/sandbox/path") {
		t.Fatalf("agent unit unexpectedly contains ReadWritePath value: %s", got)
	}
}

func TestRender_CustomUserGroup(t *testing.T) {
	got, err := Render(Params{
		Binary:  HubBinary,
		BinPath: "/usr/local/bin/cloud-pulse-hub",
		EnvFile: "/etc/cloud-pulse/hub.env",
		User:    "customuser",
		Group:   "customgroup",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "User=customuser\n") {
		t.Fatalf("expected custom User= line, got:\n%s", got)
	}
	if !strings.Contains(got, "Group=customgroup\n") {
		t.Fatalf("expected custom Group= line, got:\n%s", got)
	}
}

// TestRender_Agent_SupplementaryGroups covers SPEC-v0.5 §C's
// install-agent.sh --docker flag: a SupplementaryGroups= line appears
// right after Group=, space-joined, only when non-empty.
func TestRender_Agent_SupplementaryGroups(t *testing.T) {
	got, err := Render(Params{
		Binary:              AgentBinary,
		BinPath:             "/usr/local/bin/cloud-pulse-agent",
		EnvFile:             "/etc/cloud-pulse/agent.env",
		SupplementaryGroups: []string{"docker"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	const want = `[Unit]
Description=cloud-pulse agent (host metrics collector)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/cloud-pulse/agent.env
ExecStart=/usr/local/bin/cloud-pulse-agent
User=cloud-pulse
Group=cloud-pulse
SupplementaryGroups=docker
Restart=on-failure
RestartSec=5

# --- sandboxing / hardening ---
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
`
	if got != want {
		t.Fatalf("agent unit with SupplementaryGroups mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRender_Agent_SupplementaryGroups_MultipleAndSpaceJoined covers
// multiple groups rendered space-joined on one line.
func TestRender_Agent_SupplementaryGroups_MultipleAndSpaceJoined(t *testing.T) {
	got, err := Render(Params{
		Binary:              AgentBinary,
		BinPath:             "/usr/local/bin/cloud-pulse-agent",
		EnvFile:             "/etc/cloud-pulse/agent.env",
		SupplementaryGroups: []string{"docker", "adm"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "SupplementaryGroups=docker adm\n") {
		t.Fatalf("expected space-joined SupplementaryGroups= line, got:\n%s", got)
	}
}

// TestRender_Hub_SupplementaryGroups_Ignored covers the doc comment's
// claim that SupplementaryGroups is ignored for the hub binary.
func TestRender_Hub_SupplementaryGroups_Ignored(t *testing.T) {
	got, err := Render(Params{
		Binary:              HubBinary,
		BinPath:             "/usr/local/bin/cloud-pulse-hub",
		EnvFile:             "/etc/cloud-pulse/hub.env",
		SupplementaryGroups: []string{"docker"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != wantHubRealUnit {
		t.Fatalf("hub unit should ignore SupplementaryGroups:\n--- got ---\n%s\n--- want ---\n%s", got, wantHubRealUnit)
	}
}

// TestRender_RejectsInvalidSupplementaryGroupNames covers
// validateGroupName's whitespace/control-character/empty rejection.
func TestRender_RejectsInvalidSupplementaryGroupNames(t *testing.T) {
	tests := []struct {
		name   string
		groups []string
	}{
		{"empty string", []string{""}},
		{"embedded space", []string{"doc ker"}},
		{"embedded newline", []string{"docker\nCP_EVIL=1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Render(Params{
				Binary:              AgentBinary,
				BinPath:             "/usr/local/bin/cloud-pulse-agent",
				EnvFile:             "/etc/cloud-pulse/agent.env",
				SupplementaryGroups: tt.groups,
			})
			if err == nil {
				t.Fatalf("Render with groups %q: expected error, got none", tt.groups)
			}
		})
	}
}

// TestRender_Agent_RemoteUpdate covers SPEC-v0.6 §2's
// StateDirectory=cloud-pulse-agent line, rendered only when
// Params.RemoteUpdate is true.
func TestRender_Agent_RemoteUpdate(t *testing.T) {
	got, err := Render(Params{
		Binary:       AgentBinary,
		BinPath:      "/usr/local/bin/cloud-pulse-agent",
		EnvFile:      "/etc/cloud-pulse/agent.env",
		RemoteUpdate: true,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	const want = `[Unit]
Description=cloud-pulse agent (host metrics collector)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/cloud-pulse/agent.env
ExecStart=/usr/local/bin/cloud-pulse-agent
User=cloud-pulse
Group=cloud-pulse
StateDirectory=cloud-pulse-agent
Restart=on-failure
RestartSec=5

# --- sandboxing / hardening ---
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
`
	if got != want {
		t.Fatalf("agent unit with RemoteUpdate=true mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRender_Agent_RemoteUpdateFalse_NoStateDirectory covers the
// default (RemoteUpdate=false, e.g. CP_REMOTE_UPDATE=off) case: no
// StateDirectory= line at all, matching the pre-v0.6.0 agent unit
// exactly (see wantAgentUnit above).
func TestRender_Agent_RemoteUpdateFalse_NoStateDirectory(t *testing.T) {
	got, err := Render(Params{
		Binary:       AgentBinary,
		BinPath:      "/usr/local/bin/cloud-pulse-agent",
		EnvFile:      "/etc/cloud-pulse/agent.env",
		RemoteUpdate: false,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != wantAgentUnit {
		t.Fatalf("agent unit with RemoteUpdate=false should be unaffected:\n--- got ---\n%s\n--- want ---\n%s", got, wantAgentUnit)
	}
	if strings.Contains(got, "StateDirectory") {
		t.Errorf("agent unit unexpectedly contains StateDirectory: %s", got)
	}
}

// TestRender_Hub_RemoteUpdate_Ignored covers RemoteUpdate's doc comment
// claim that it is meaningless (ignored) for the hub binary.
func TestRender_Hub_RemoteUpdate_Ignored(t *testing.T) {
	got, err := Render(Params{
		Binary:       HubBinary,
		BinPath:      "/usr/local/bin/cloud-pulse-hub",
		EnvFile:      "/etc/cloud-pulse/hub.env",
		RemoteUpdate: true,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != wantHubRealUnit {
		t.Fatalf("hub unit should ignore RemoteUpdate:\n--- got ---\n%s\n--- want ---\n%s", got, wantHubRealUnit)
	}
}

func TestRender_ValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		p    Params
	}{
		{"missing binary", Params{BinPath: "/usr/local/bin/cloud-pulse-hub", EnvFile: "/etc/cloud-pulse/hub.env"}},
		{"unknown binary", Params{Binary: "cloud-pulse-evil", BinPath: "/usr/local/bin/cloud-pulse-hub", EnvFile: "/etc/cloud-pulse/hub.env"}},
		{"missing bin-path", Params{Binary: HubBinary, EnvFile: "/etc/cloud-pulse/hub.env"}},
		{"missing env-file", Params{Binary: HubBinary, BinPath: "/usr/local/bin/cloud-pulse-hub"}},
		{"relative bin-path", Params{Binary: HubBinary, BinPath: "cloud-pulse-hub", EnvFile: "/etc/cloud-pulse/hub.env"}},
		{"relative env-file", Params{Binary: HubBinary, BinPath: "/usr/local/bin/cloud-pulse-hub", EnvFile: "hub.env"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Render(tt.p); err == nil {
				t.Fatalf("Render(%+v): expected error, got none", tt.p)
			}
		})
	}
}
