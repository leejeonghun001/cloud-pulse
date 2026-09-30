package launchd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// goldenPlist is the exact expected output of Render for the default
// Params (no overrides), kept in sync with install-agent.sh's
// render_plist() heredoc — see the drift test in scripts/test-install.sh.
const goldenPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.cloudpulse.agent</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/cloud-pulse-agent</string>
        <string>--env-file</string>
        <string>/usr/local/etc/cloud-pulse/agent.env</string>
    </array>
    <key>UserName</key>
    <string>_cloudpulse</string>
    <key>KeepAlive</key>
    <true/>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Library/Logs/cloud-pulse-agent.log</string>
    <key>StandardErrorPath</key>
    <string>/Library/Logs/cloud-pulse-agent.log</string>
</dict>
</plist>
`

func defaultParams() Params {
	return Params{
		BinPath: "/usr/local/bin/cloud-pulse-agent",
		EnvFile: "/usr/local/etc/cloud-pulse/agent.env",
	}
}

func TestRender_Golden(t *testing.T) {
	got, err := Render(defaultParams())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != goldenPlist {
		t.Errorf("Render output mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, goldenPlist)
	}
}

func TestRender_CustomUserAndLog(t *testing.T) {
	p := defaultParams()
	p.UserName = "_customuser"
	p.LogPath = "/tmp/custom.log"
	got, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "<string>_customuser</string>") {
		t.Errorf("expected custom UserName in output:\n%s", got)
	}
	if !strings.Contains(got, "<string>/tmp/custom.log</string>") {
		t.Errorf("expected custom log path in output:\n%s", got)
	}
}

func TestRender_XMLEscaping(t *testing.T) {
	p := defaultParams()
	p.UserName = "a&b"
	got, err := Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "a&amp;b") {
		t.Errorf("expected XML-escaped ampersand in output:\n%s", got)
	}
	if strings.Contains(got, "<string>a&b</string>") {
		t.Errorf("unescaped ampersand leaked into output:\n%s", got)
	}
}

func TestRender_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		p    Params
	}{
		{"missing bin path", Params{EnvFile: "/etc/x"}},
		{"missing env file", Params{BinPath: "/usr/bin/x"}},
		{"relative bin path", Params{BinPath: "rel/path", EnvFile: "/etc/x"}},
		{"bin path with space", Params{BinPath: "/usr/bin/x y", EnvFile: "/etc/x"}},
		{"bin path with quote", Params{BinPath: `/usr/bin/x"y`, EnvFile: "/etc/x"}},
		{"env file with control char", Params{BinPath: "/usr/bin/x", EnvFile: "/etc/x\x01"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Render(tc.p); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestParseExisting_RoundTrip(t *testing.T) {
	cases := []Params{
		defaultParams(),
		{BinPath: "/opt/bin/cloud-pulse-agent", EnvFile: "/opt/etc/agent.env", UserName: "_other", LogPath: "/tmp/other.log"},
	}
	for _, p := range cases {
		rendered, err := Render(p)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		parsed, err := ParseExisting(rendered)
		if err != nil {
			t.Fatalf("ParseExisting: %v", err)
		}
		if parsed.BinPath != p.BinPath {
			t.Errorf("BinPath: got %q want %q", parsed.BinPath, p.BinPath)
		}
		if parsed.EnvFile != p.EnvFile {
			t.Errorf("EnvFile: got %q want %q", parsed.EnvFile, p.EnvFile)
		}
		if parsed.userNameOrDefault() != p.userNameOrDefault() {
			t.Errorf("UserName: got %q want %q", parsed.userNameOrDefault(), p.userNameOrDefault())
		}
		if parsed.logPathOrDefault() != p.logPathOrDefault() {
			t.Errorf("LogPath: got %q want %q", parsed.logPathOrDefault(), p.logPathOrDefault())
		}
		// Re-rendering the parsed params must reproduce the same text
		// (idempotent round trip), matching Apply's up-to-date check.
		rerendered, err := Render(parsed)
		if err != nil {
			t.Fatalf("Render(parsed): %v", err)
		}
		if rerendered != rendered {
			t.Errorf("round trip mismatch:\n--- original ---\n%s\n--- rerendered ---\n%s", rendered, rerendered)
		}
	}
}

func TestParseExisting_Malformed(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"not xml", "not xml at all"},
		{"missing program arguments", `<?xml version="1.0"?><plist><dict><key>UserName</key><string>x</string></dict></plist>`},
		{"missing env-file arg", `<?xml version="1.0"?><plist><dict><key>ProgramArguments</key><array><string>/usr/bin/x</string></array></dict></plist>`},
		{"relative bin path", `<?xml version="1.0"?><plist><dict><key>ProgramArguments</key><array><string>rel</string><string>--env-file</string><string>/etc/x</string></array></dict></plist>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseExisting(tc.text); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestApply_NoChangeWhenUpToDate(t *testing.T) {
	dir := t.TempDir()
	plistPath := filepath.Join(dir, "com.cloudpulse.agent.plist")
	rendered, err := Render(defaultParams())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte(rendered), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	changed, err := Apply(context.Background(), plistPath, false, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if changed {
		t.Error("expected changed=false for an already up-to-date plist")
	}
	if _, err := os.Stat(plistPath + ".bak"); !os.IsNotExist(err) {
		t.Error("expected no .bak file to be created when nothing changed")
	}
}

func TestApply_RewritesDriftedPlist(t *testing.T) {
	dir := t.TempDir()
	plistPath := filepath.Join(dir, "com.cloudpulse.agent.plist")

	// Simulate a hand-edited/drifted plist: identical to a normally
	// rendered one except for extra surrounding whitespace, which
	// ParseExisting tolerates (XML) but which does not byte-for-byte
	// match Render's own canonical output — so Apply must detect this
	// as changed and rewrite it to the canonical form.
	stale := Params{BinPath: "/usr/local/bin/cloud-pulse-agent", EnvFile: "/usr/local/etc/cloud-pulse/agent.env", LogPath: "/tmp/stale.log"}
	canonical, err := Render(stale)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	staleRendered := strings.Replace(canonical, "<key>KeepAlive</key>", "<key>KeepAlive</key>\n    <!-- hand-edited comment -->", 1)
	if err := os.WriteFile(plistPath, []byte(staleRendered), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var out bytes.Buffer
	changed, err := Apply(context.Background(), plistPath, false, &out)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}

	after, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Re-applying Render to the *parsed* params of the stale file
	// should exactly match what's now on disk (Apply doesn't silently
	// alter LogPath — it only re-renders exactly what ParseExisting
	// extracted).
	reparsed, err := ParseExisting(string(after))
	if err != nil {
		t.Fatalf("ParseExisting(after): %v", err)
	}
	if reparsed.logPathOrDefault() != "/tmp/stale.log" {
		t.Errorf("expected stale log path to be preserved through apply, got %q", reparsed.logPathOrDefault())
	}

	if _, err := os.Stat(plistPath + ".bak"); err != nil {
		t.Errorf("expected .bak file to exist: %v", err)
	}
	backup, err := os.ReadFile(plistPath + ".bak")
	if err != nil {
		t.Fatalf("ReadFile(.bak): %v", err)
	}
	if string(backup) != staleRendered {
		t.Error("backup content does not match the pre-apply plist")
	}
}

func TestApply_KickstartHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launchctl fake is a POSIX shell script and Windows has no shebang execution")
	}
	dir := t.TempDir()
	plistPath := filepath.Join(dir, "com.cloudpulse.agent.plist")
	stale := Params{BinPath: "/usr/local/bin/cloud-pulse-agent", EnvFile: "/usr/local/etc/cloud-pulse/agent.env", LogPath: "/tmp/stale.log"}
	canonical, err := Render(stale)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	staleRendered := strings.Replace(canonical, "<key>KeepAlive</key>", "<key>KeepAlive</key>\n    <!-- hand-edited comment -->", 1)
	if err := os.WriteFile(plistPath, []byte(staleRendered), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	marker := filepath.Join(dir, "kickstart-called")
	hook := writeFakeLaunchctl(t, dir, marker)
	t.Setenv("CP_LAUNCHCTL", hook)

	changed, err := Apply(context.Background(), plistPath, true, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("expected fake launchctl to have been invoked: %v", err)
	}
}

func TestApply_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := Apply(context.Background(), filepath.Join(dir, "nope.plist"), false, nil)
	if err == nil {
		t.Error("expected an error for a missing plist")
	}
}

// writeFakeLaunchctl writes a tiny shell script standing in for
// launchctl: for "kickstart"/"bootstrap"/"bootout" it touches markerPath
// and exits 0. Requires /bin/sh, present on Linux and macOS CI. The sole
// caller explicitly skips on Windows because Windows cannot execute shebang
// scripts.
func writeFakeLaunchctl(t *testing.T, dir, markerPath string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no /bin/sh available to build the fake launchctl test hook")
	}
	scriptPath := filepath.Join(dir, "fake-launchctl.sh")
	script := "#!/bin/sh\ntouch " + shQuote(markerPath) + "\nexit 0\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return scriptPath
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
