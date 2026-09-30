package systemdunit

import (
	"os"
	"strings"
	"testing"
)

const wantUpdatePathUnit = `[Unit]
Description=Watch for cloud-pulse-agent remote update requests

[Path]
PathModified=/var/lib/cloud-pulse-agent/update-request.json
PathExists=/var/lib/cloud-pulse-agent/update-request.json
Unit=cloud-pulse-agent-update.service

[Install]
WantedBy=multi-user.target
`

const wantUpdateServiceUnit = `[Unit]
Description=Apply a hub-requested cloud-pulse-agent update

[Service]
Type=oneshot
ExecStart=/usr/local/bin/cloud-pulse-agent update --from-request /var/lib/cloud-pulse-agent/update-request.json --result-dir /var/lib/cloud-pulse-agent-update
`

func testUpdateUnitParams() UpdateUnitParams {
	return UpdateUnitParams{
		BinPath:     "/usr/local/bin/cloud-pulse-agent",
		RequestPath: "/var/lib/cloud-pulse-agent/update-request.json",
		ResultDir:   "/var/lib/cloud-pulse-agent-update",
	}
}

func TestRenderUpdatePath(t *testing.T) {
	got, err := RenderUpdatePath(testUpdateUnitParams())
	if err != nil {
		t.Fatalf("RenderUpdatePath: %v", err)
	}
	if got != wantUpdatePathUnit {
		t.Fatalf("update path unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, wantUpdatePathUnit)
	}
}

func TestRenderUpdateService(t *testing.T) {
	got, err := RenderUpdateService(testUpdateUnitParams())
	if err != nil {
		t.Fatalf("RenderUpdateService: %v", err)
	}
	if got != wantUpdateServiceUnit {
		t.Fatalf("update service unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, wantUpdateServiceUnit)
	}
}

func TestRenderUpdatePath_HasNoUserGroupOverride(t *testing.T) {
	got, err := RenderUpdatePath(testUpdateUnitParams())
	if err != nil {
		t.Fatalf("RenderUpdatePath: %v", err)
	}
	if strings.Contains(got, "User=") || strings.Contains(got, "Group=") {
		t.Errorf("path unit should not set User=/Group=, got:\n%s", got)
	}
}

func TestRenderUpdateService_RunsAsRoot(t *testing.T) {
	got, err := RenderUpdateService(testUpdateUnitParams())
	if err != nil {
		t.Fatalf("RenderUpdateService: %v", err)
	}
	// No User=/Group= line at all means systemd runs it as root
	// (the default for a system unit with no User= override) — this
	// is deliberate (SPEC-v0.6 §2: writes to a root-owned ResultDir).
	if strings.Contains(got, "User=") || strings.Contains(got, "Group=") {
		t.Errorf("service unit should have no User=/Group= override (must run as root), got:\n%s", got)
	}
	if strings.Contains(got, "[Install]") {
		t.Errorf("service unit should have no [Install] section (only triggered by the .path unit), got:\n%s", got)
	}
}

func TestRenderUpdateUnits_ValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		p    UpdateUnitParams
	}{
		{"missing bin-path", UpdateUnitParams{RequestPath: "/var/lib/cloud-pulse-agent/update-request.json", ResultDir: "/var/lib/cloud-pulse-agent-update"}},
		{"missing request-path", UpdateUnitParams{BinPath: "/usr/local/bin/cloud-pulse-agent", ResultDir: "/var/lib/cloud-pulse-agent-update"}},
		{"missing result-dir", UpdateUnitParams{BinPath: "/usr/local/bin/cloud-pulse-agent", RequestPath: "/var/lib/cloud-pulse-agent/update-request.json"}},
		{"relative bin-path", UpdateUnitParams{BinPath: "cloud-pulse-agent", RequestPath: "/var/lib/cloud-pulse-agent/update-request.json", ResultDir: "/var/lib/cloud-pulse-agent-update"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := RenderUpdatePath(tt.p); err == nil {
				t.Error("RenderUpdatePath: expected an error, got none")
			}
			if _, err := RenderUpdateService(tt.p); err == nil {
				t.Error("RenderUpdateService: expected an error, got none")
			}
		})
	}
}

func TestParseUpdateUnitParams_RoundTrip(t *testing.T) {
	p := testUpdateUnitParams()
	pathUnit, err := RenderUpdatePath(p)
	if err != nil {
		t.Fatalf("RenderUpdatePath: %v", err)
	}
	serviceUnit, err := RenderUpdateService(p)
	if err != nil {
		t.Fatalf("RenderUpdateService: %v", err)
	}

	got, err := ParseUpdateUnitParams(pathUnit, serviceUnit)
	if err != nil {
		t.Fatalf("ParseUpdateUnitParams: %v", err)
	}
	if got != p {
		t.Errorf("ParseUpdateUnitParams = %+v, want %+v", got, p)
	}
}

func TestParseUpdateUnitParams_RejectsMismatchedPaths(t *testing.T) {
	p := testUpdateUnitParams()
	pathUnit, _ := RenderUpdatePath(p)
	p2 := p
	p2.RequestPath = "/var/lib/cloud-pulse-agent/other-request.json"
	serviceUnit, _ := RenderUpdateService(p2)

	if _, err := ParseUpdateUnitParams(pathUnit, serviceUnit); err == nil {
		t.Error("ParseUpdateUnitParams: expected an error for mismatched request paths between the two units")
	}
}

func TestParseUpdateUnitParams_RejectsMissingFields(t *testing.T) {
	if _, err := ParseUpdateUnitParams("[Unit]\n", "[Unit]\n[Service]\nType=oneshot\n"); err == nil {
		t.Error("ParseUpdateUnitParams: expected an error for a unit with no PathModified=/ExecStart=")
	}
}

func TestApplyUpdateUnits_CreatesBothUnitsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	pathPath := dir + "/cloud-pulse-agent-update.path"
	servicePath := dir + "/cloud-pulse-agent-update.service"

	changed, err := ApplyUpdateUnits(pathPath, servicePath, testUpdateUnitParams(), nil)
	if err != nil {
		t.Fatalf("ApplyUpdateUnits: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true when both unit files are newly created")
	}

	gotPath := mustReadUnitFile(t, pathPath)
	if gotPath != wantUpdatePathUnit {
		t.Errorf("path unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", gotPath, wantUpdatePathUnit)
	}
	gotService := mustReadUnitFile(t, servicePath)
	if gotService != wantUpdateServiceUnit {
		t.Errorf("service unit mismatch:\n--- got ---\n%s\n--- want ---\n%s", gotService, wantUpdateServiceUnit)
	}
}

func TestApplyUpdateUnits_NoOpWhenAlreadyCurrent(t *testing.T) {
	dir := t.TempDir()
	pathPath := dir + "/cloud-pulse-agent-update.path"
	servicePath := dir + "/cloud-pulse-agent-update.service"

	if _, err := ApplyUpdateUnits(pathPath, servicePath, testUpdateUnitParams(), nil); err != nil {
		t.Fatalf("first ApplyUpdateUnits: %v", err)
	}

	changed, err := ApplyUpdateUnits(pathPath, servicePath, testUpdateUnitParams(), nil)
	if err != nil {
		t.Fatalf("second ApplyUpdateUnits: %v", err)
	}
	if changed {
		t.Error("changed = true on a no-op re-apply, want false")
	}
}

func TestApplyUpdateUnits_RewritesDriftedUnitWithBackup(t *testing.T) {
	dir := t.TempDir()
	pathPath := dir + "/cloud-pulse-agent-update.path"
	servicePath := dir + "/cloud-pulse-agent-update.service"

	stalePath := "[Unit]\nDescription=stale\n"
	if err := os.WriteFile(pathPath, []byte(stalePath), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write stale fixture: %v", err)
	}

	changed, err := ApplyUpdateUnits(pathPath, servicePath, testUpdateUnitParams(), nil)
	if err != nil {
		t.Fatalf("ApplyUpdateUnits: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true (path unit drifted)")
	}

	gotPath := mustReadUnitFile(t, pathPath)
	if gotPath != wantUpdatePathUnit {
		t.Errorf("path unit not rewritten to current Render output:\n%s", gotPath)
	}
	bak := mustReadUnitFile(t, pathPath+".bak")
	if bak != stalePath {
		t.Errorf(".bak content = %q, want the stale pre-apply content %q", bak, stalePath)
	}
}

func mustReadUnitFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test tempdir fixture
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
