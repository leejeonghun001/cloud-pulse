// Package launchd is the single source of truth for rendering, parsing,
// and updating the macOS LaunchDaemon plist cloud-pulse-agent installs
// for itself (SPEC-v0.7 §1), mirroring internal/systemdunit's role for
// the Linux systemd unit. It has no dependencies beyond the standard
// library and uses "path" (not "path/filepath") for every path it
// renders/parses, since a launchd plist always describes POSIX-style
// macOS paths regardless of which OS this package itself is compiled
// for (unit tests run on every CI platform, not just macOS).
//
// Render produces byte-identical output to install-agent.sh's
// render_plist() bash heredoc (see the golden tests in
// render_test.go) — the script calls the freshly downloaded binary's
// "plist print" subcommand to render the plist itself, falling back to
// its own heredoc only when that invocation fails (e.g. an explicit
// --version pin older than the tag that introduced the subcommand).
// ParseExisting and Apply implement the `plist apply` subcommand's job
// of rewriting an already-installed plist in place, used by
// internal/selfupdate's darwin PostUpdate hook so that `update` can
// apply plist changes without requiring a full re-run of the install
// script.
package launchd

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

// Label is the fixed launchd job label cloud-pulse-agent installs
// under.
const Label = "com.cloudpulse.agent"

// PlistPath is the fixed, non-configurable install path for the main
// agent LaunchDaemon plist.
const PlistPath = "/Library/LaunchDaemons/" + Label + ".plist"

// DefaultUser is the dedicated hidden macOS user the agent LaunchDaemon
// runs as (SPEC-v0.7 §1: "_cloudpulse").
const DefaultUser = "_cloudpulse"

// DefaultLogPath is the fixed stdout/stderr log path SPEC-v0.7 §1
// specifies for the agent LaunchDaemon.
const DefaultLogPath = "/Library/Logs/cloud-pulse-agent.log"

// reloadTimeout bounds Apply's launchctl kickstart call.
const reloadTimeout = 30 * time.Second

// Params configures Render. Render's output must match
// install-agent.sh's render_plist() heredoc byte for byte given
// equivalent inputs.
type Params struct {
	// BinPath is the absolute path to the installed binary, e.g.
	// "/usr/local/bin/cloud-pulse-agent". Required.
	BinPath string
	// EnvFile is the absolute path to the KEY=VALUE env file the agent
	// binary reads via its own --env-file flag (launchd has no
	// EnvironmentFile= equivalent — see internal/config's
	// ParseEnvFile). Required.
	EnvFile string
	// UserName overrides the LaunchDaemon's UserName key. Empty
	// defaults to DefaultUser.
	UserName string
	// LogPath overrides StandardOutPath/StandardErrorPath (both use
	// the same path). Empty defaults to DefaultLogPath.
	LogPath string
}

func (p Params) userNameOrDefault() string {
	if p.UserName == "" {
		return DefaultUser
	}
	return p.UserName
}

func (p Params) logPathOrDefault() string {
	if p.LogPath == "" {
		return DefaultLogPath
	}
	return p.LogPath
}

func validateParams(p Params) error {
	if p.BinPath == "" {
		return errors.New("bin-path is required")
	}
	if p.EnvFile == "" {
		return errors.New("env-file is required")
	}
	if err := validatePathValue("bin-path", p.BinPath); err != nil {
		return err
	}
	if err := validatePathValue("env-file", p.EnvFile); err != nil {
		return err
	}
	if err := validatePathValue("log-path", p.logPathOrDefaultForValidate()); err != nil {
		return err
	}
	return nil
}

func (p Params) logPathOrDefaultForValidate() string {
	return p.logPathOrDefault()
}

// validatePathValue mirrors systemdunit's identical helper: reject a
// path-shaped value that is not absolute or contains
// whitespace/control/quote characters.
func validatePathValue(name, value string) error {
	if !path.IsAbs(value) {
		return fmt.Errorf("%s %q must be an absolute path", name, value)
	}
	for _, r := range value {
		switch {
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("%s %q must not contain control characters", name, value)
		case r == ' ' || r == '\t':
			return fmt.Errorf("%s %q must not contain whitespace", name, value)
		case r == '"' || r == '\'':
			return fmt.Errorf("%s %q must not contain quote characters", name, value)
		}
	}
	return nil
}

// Render returns the full plist XML text for p, matching
// install-agent.sh's render_plist() heredoc exactly (same lines, same
// order, same indentation).
func Render(p Params) (string, error) {
	if err := validateParams(p); err != nil {
		return "", fmt.Errorf("launchd: render: %w", err)
	}

	var b strings.Builder
	writeLine(&b, `<?xml version="1.0" encoding="UTF-8"?>`)
	writeLine(&b, `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`)
	writeLine(&b, `<plist version="1.0">`)
	writeLine(&b, `<dict>`)
	writeLine(&b, `    <key>Label</key>`)
	writeLine(&b, `    <string>`+Label+`</string>`)
	writeLine(&b, `    <key>ProgramArguments</key>`)
	writeLine(&b, `    <array>`)
	writeLine(&b, `        <string>`+xmlEscape(p.BinPath)+`</string>`)
	writeLine(&b, `        <string>--env-file</string>`)
	writeLine(&b, `        <string>`+xmlEscape(p.EnvFile)+`</string>`)
	writeLine(&b, `    </array>`)
	writeLine(&b, `    <key>UserName</key>`)
	writeLine(&b, `    <string>`+xmlEscape(p.userNameOrDefault())+`</string>`)
	writeLine(&b, `    <key>KeepAlive</key>`)
	writeLine(&b, `    <true/>`)
	writeLine(&b, `    <key>RunAtLoad</key>`)
	writeLine(&b, `    <true/>`)
	writeLine(&b, `    <key>StandardOutPath</key>`)
	writeLine(&b, `    <string>`+xmlEscape(p.logPathOrDefault())+`</string>`)
	writeLine(&b, `    <key>StandardErrorPath</key>`)
	writeLine(&b, `    <string>`+xmlEscape(p.logPathOrDefault())+`</string>`)
	writeLine(&b, `</dict>`)
	writeLine(&b, `</plist>`)
	return b.String(), nil
}

// xmlEscape escapes the five XML predefined entities in a plist
// <string> value. Params are always operator/installer-controlled
// absolute paths (validated above), never end-user request input, but
// escaping is applied unconditionally as defense in depth and so
// Render's output is always well-formed XML regardless of input.
func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		// xml.EscapeText only errors on a write failure to b, which
		// strings.Builder never returns.
		return s
	}
	return b.String()
}

func writeLine(b *strings.Builder, s string) {
	b.WriteString(s)
	b.WriteByte('\n')
}

// plistDict is the minimal decode target for ParseExisting: only the
// keys/values Render itself ever produces, decoded via encoding/xml's
// generic element walk rather than a strict plist library (the
// standard library has none, and this project bans new third-party
// dependencies).
type plistDict struct {
	XMLName xml.Name  `xml:"plist"`
	Dict    plistBody `xml:"dict"`
}

type plistBody struct {
	Keys   []string `xml:"key"`
	Values []rawTag `xml:",any"`
}

// rawTag captures each non-<key> child of <dict> generically so both
// <string>text</string> and <array>...</array> (ProgramArguments) can
// be distinguished by tag name without a fixed schema per key.
type rawTag struct {
	XMLName xml.Name
	Content string   `xml:",innerxml"`
	Strings []string `xml:"string"`
}

// ParseExisting extracts Params from the text of an already-installed
// plist, for Apply's up-to-date comparison. It validates every
// extracted path-shaped value the same way Render's own validation
// does, so a corrupted or hand-edited plist is rejected rather than
// silently accepted and re-rendered with attacker-influenced paths.
func ParseExisting(plist string) (Params, error) {
	var doc plistDict
	if err := xml.Unmarshal([]byte(plist), &doc); err != nil {
		return Params{}, fmt.Errorf("launchd: parse: invalid XML: %w", err)
	}

	if len(doc.Dict.Keys) != len(doc.Dict.Values) {
		return Params{}, errors.New("launchd: parse: mismatched <key>/value pairs")
	}

	var p Params
	var programArgsSeen bool
	for i, key := range doc.Dict.Keys {
		val := doc.Dict.Values[i]
		switch key {
		case "ProgramArguments":
			programArgsSeen = true
			if len(val.Strings) == 0 {
				return Params{}, errors.New("launchd: parse: ProgramArguments has no entries")
			}
			p.BinPath = val.Strings[0]
			for j := 0; j < len(val.Strings)-1; j++ {
				if val.Strings[j] == "--env-file" {
					p.EnvFile = val.Strings[j+1]
				}
			}
		case "UserName":
			p.UserName = strings.TrimSpace(val.Content)
		case "StandardOutPath":
			p.LogPath = strings.TrimSpace(val.Content)
		}
	}

	if !programArgsSeen || p.BinPath == "" {
		return Params{}, errors.New("launchd: parse: missing ProgramArguments")
	}
	if p.EnvFile == "" {
		return Params{}, errors.New("launchd: parse: missing --env-file argument")
	}

	if err := validatePathValue("bin-path", p.BinPath); err != nil {
		return Params{}, fmt.Errorf("launchd: parse: %w", err)
	}
	if err := validatePathValue("env-file", p.EnvFile); err != nil {
		return Params{}, fmt.Errorf("launchd: parse: %w", err)
	}
	if p.LogPath != "" {
		if err := validatePathValue("log-path", p.LogPath); err != nil {
			return Params{}, fmt.Errorf("launchd: parse: %w", err)
		}
	}

	return p, nil
}

// Apply reads the plist at plistPath, parses it via ParseExisting,
// re-renders it via Render, and — if the rendered text differs from
// what's on disk — writes the new text to plistPath+".tmp", preserves
// the previous content at plistPath+".bak", and renames the ".tmp"
// file over plistPath (atomic on the same filesystem). If kickstart is
// true and a change was made, it runs `launchctl kickstart -k
// system/<Label>` bounded by a 30s timeout so the running daemon picks
// up the new plist immediately. Apply reports changed=false and takes
// no action (including no kickstart) when the existing plist already
// matches Render's output.
//
// Progress and any kickstart output are written to out if non-nil.
func Apply(ctx context.Context, plistPath string, kickstart bool, out interface{ Write([]byte) (int, error) }) (bool, error) {
	existing, err := os.ReadFile(plistPath) //nolint:gosec // plistPath is an operator-supplied LaunchDaemon path, not user request input
	if err != nil {
		return false, fmt.Errorf("launchd: apply: read %s: %w", plistPath, err)
	}

	params, err := ParseExisting(string(existing))
	if err != nil {
		return false, fmt.Errorf("launchd: apply: parse %s: %w", plistPath, err)
	}

	rendered, err := Render(params)
	if err != nil {
		return false, fmt.Errorf("launchd: apply: render: %w", err)
	}

	if rendered == string(existing) {
		printfln(out, "plist up to date: %s", plistPath)
		return false, nil
	}

	backupPath := plistPath + ".bak"
	if err := os.WriteFile(backupPath, existing, 0o644); err != nil { //nolint:gosec // plist files are world-readable by design (launchd requirement)
		return false, fmt.Errorf("launchd: apply: write backup %s: %w", backupPath, err)
	}

	tmpPath := plistPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(rendered), 0o644); err != nil { //nolint:gosec // plist files are world-readable by design (launchd requirement)
		return false, fmt.Errorf("launchd: apply: write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, plistPath); err != nil {
		return false, fmt.Errorf("launchd: apply: rename %s to %s: %w", tmpPath, plistPath, err)
	}
	printfln(out, "updated plist: %s (previous content saved to %s)", plistPath, backupPath)

	if kickstart {
		if err := Kickstart(ctx, out); err != nil {
			return true, fmt.Errorf("launchd: apply: %w", err)
		}
	}

	return true, nil
}

// Kickstart runs `launchctl kickstart -k system/<Label>` bounded by a
// 30s timeout, writing progress to out if non-nil. launchctlPath
// defaults to "launchctl" but is overridable via the CP_LAUNCHCTL
// environment variable (a test hook mirroring install-agent.sh's own
// CP_LAUNCHCTL sandbox hook, so unit tests never invoke the real
// launchctl binary).
func Kickstart(ctx context.Context, out interface{ Write([]byte) (int, error) }) error {
	bin := launchctlBin()
	kickCtx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()

	cmd := exec.CommandContext(kickCtx, bin, "kickstart", "-k", "system/"+Label)
	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		printfln(out, "%s", strings.TrimRight(string(output), "\n"))
	}
	if err != nil {
		return fmt.Errorf("launchctl kickstart: %w", err)
	}
	printfln(out, "launchctl kickstart: OK")
	return nil
}

// Bootstrap runs `launchctl bootstrap system <plistPath>`, loading a
// not-yet-loaded LaunchDaemon into the system domain. Test hook: see
// Kickstart's doc comment on CP_LAUNCHCTL.
func Bootstrap(ctx context.Context, plistPath string, out interface{ Write([]byte) (int, error) }) error {
	bin := launchctlBin()
	bootCtx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()

	cmd := exec.CommandContext(bootCtx, bin, "bootstrap", "system", plistPath) //nolint:gosec // plistPath is an operator-supplied LaunchDaemon path
	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		printfln(out, "%s", strings.TrimRight(string(output), "\n"))
	}
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	printfln(out, "launchctl bootstrap: OK")
	return nil
}

// Bootout runs `launchctl bootout system/<Label>`, unloading the
// LaunchDaemon. A "no such process"-style failure (nothing currently
// loaded) is not treated as an error by callers that expect this to be
// idempotent; Bootout itself just reports what launchctl returned. Test
// hook: see Kickstart's doc comment on CP_LAUNCHCTL.
func Bootout(ctx context.Context, out interface{ Write([]byte) (int, error) }) error {
	bin := launchctlBin()
	bootCtx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()

	cmd := exec.CommandContext(bootCtx, bin, "bootout", "system/"+Label)
	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		printfln(out, "%s", strings.TrimRight(string(output), "\n"))
	}
	if err != nil {
		return fmt.Errorf("launchctl bootout: %w", err)
	}
	printfln(out, "launchctl bootout: OK")
	return nil
}

// launchctlBin returns CP_LAUNCHCTL if set (a sandbox test hook, same
// convention as install-agent.sh's own CP_LAUNCHCTL), else "launchctl".
func launchctlBin() string {
	if v := os.Getenv("CP_LAUNCHCTL"); v != "" {
		return v
	}
	return "launchctl"
}

// printfln writes a formatted, newline-terminated message to out if
// non-nil, matching systemdunit's own helper's shape.
func printfln(out interface{ Write([]byte) (int, error) }, format string, args ...any) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, format+"\n", args...)
}
