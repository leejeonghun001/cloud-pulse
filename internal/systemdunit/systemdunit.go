// Package systemdunit is the single source of truth for rendering,
// parsing, and updating the systemd unit files cloud-pulse-hub and
// cloud-pulse-agent install for themselves. It has no dependencies
// beyond the standard library.
//
// Render produces byte-identical output to the bash heredocs in
// scripts/install-hub.sh's render_unit() and scripts/install-agent.sh's
// render_unit() (see the golden tests in render_test.go) — those scripts
// call the freshly downloaded binary's "systemd-unit print" subcommand
// to render units themselves, falling back to their own heredoc only
// when that invocation fails (e.g. an explicit --version pin older than
// v0.3.1). ParseExisting and Apply implement the `systemd-unit apply`
// subcommand's job of rewriting an already-installed unit file in
// place, used by internal/selfupdate's default PostUpdate hook so that
// `update` can apply unit-file changes without requiring a full
// re-run of the install script.
package systemdunit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

// HubBinary and AgentBinary are the two valid values for Params.Binary /
// the basename ParseExisting expects in a unit's ExecStart line.
const (
	HubBinary   = "cloud-pulse-hub"
	AgentBinary = "cloud-pulse-agent"
)

// DefaultUser and DefaultGroup are the system user/group both installers
// create and run their service as.
const (
	DefaultUser  = "cloud-pulse"
	DefaultGroup = "cloud-pulse"
)

// hubDataDir is the fixed, non-configurable data directory real (non-sandbox)
// hub installs use, matching install-hub.sh's setup_paths()/render_unit().
const hubDataDir = "/var/lib/cloud-pulse"

// reloadTimeout bounds Apply's `systemctl daemon-reload` call.
const reloadTimeout = 30 * time.Second

// Params configures Render. See individual field docs for exact
// semantics; Render's output must match the corresponding bash script's
// render_unit() heredoc byte for byte given equivalent inputs.
type Params struct {
	// Binary is which cloud-pulse binary this unit runs: HubBinary or
	// AgentBinary. Required.
	Binary string
	// BinPath is the absolute path to the installed binary, e.g.
	// "/usr/local/bin/cloud-pulse-hub". Required.
	BinPath string
	// EnvFile is the absolute path to the EnvironmentFile, e.g.
	// "/etc/cloud-pulse/hub.env". Required.
	EnvFile string
	// User and Group override the service's User=/Group= lines.
	// Empty defaults to DefaultUser/DefaultGroup.
	User, Group string
	// ReadWritePath is meaningful only for Binary == HubBinary. Empty
	// (the real-install default) renders "StateDirectory=cloud-pulse"
	// exactly as install-hub.sh does for a non-sandbox install. A
	// non-empty value renders "ReadWritePaths=<value>" instead, exactly
	// as install-hub.sh does for CP_INSTALL_ROOT sandbox installs (the
	// value is expected to be the sandboxed data directory). Ignored
	// for Binary == AgentBinary (the agent unit never had a
	// StateDirectory/ReadWritePaths line).
	ReadWritePath string
	// SupplementaryGroups is meaningful only for Binary == AgentBinary
	// (SPEC-v0.5 §C): when non-empty, renders a
	// "SupplementaryGroups=<space-joined groups>" line right after
	// Group=, letting the agent's unprivileged system user join
	// additional groups — in practice just "docker", so the Docker
	// Engine API's unix socket (owned by root:docker) becomes
	// readable/writable without running the agent as root. Rendered
	// verbatim (space-joined, no quoting) since systemd's
	// SupplementaryGroups= takes a whitespace-separated list; callers
	// (install-agent.sh's --docker flag, the CLI validating group
	// names) are responsible for ensuring values contain no whitespace
	// of their own. Ignored for Binary == HubBinary (the hub has no
	// equivalent use case).
	SupplementaryGroups []string
	// RemoteUpdate is meaningful only for Binary == AgentBinary
	// (SPEC-v0.6 §2): when true, renders "StateDirectory=cloud-pulse-agent"
	// in the agent's [Service] section, granting write access to
	// /var/lib/cloud-pulse-agent for the atomic update-request.json
	// write described in internal/agent/remoteupdate.go. Ignored for
	// Binary == HubBinary (the hub already has its own unconditional
	// StateDirectory/ReadWritePaths handling via ReadWritePath).
	RemoteUpdate bool
}

// userOrDefault and groupOrDefault apply Params' defaulting rule.
func (p Params) userOrDefault() string {
	if p.User == "" {
		return DefaultUser
	}
	return p.User
}

func (p Params) groupOrDefault() string {
	if p.Group == "" {
		return DefaultGroup
	}
	return p.Group
}

// Render returns the full unit file text for p, matching
// install-hub.sh's / install-agent.sh's render_unit() heredocs exactly
// (same lines, same order, same blank lines, same comments).
func Render(p Params) (string, error) {
	if err := validateParams(p); err != nil {
		return "", fmt.Errorf("systemdunit: render: %w", err)
	}
	switch p.Binary {
	case HubBinary:
		return renderHub(p), nil
	case AgentBinary:
		return renderAgent(p), nil
	default:
		return "", fmt.Errorf("systemdunit: render: unknown binary %q", p.Binary)
	}
}

func validateParams(p Params) error {
	if p.Binary != HubBinary && p.Binary != AgentBinary {
		return fmt.Errorf("binary must be %q or %q, got %q", HubBinary, AgentBinary, p.Binary)
	}
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
	if p.ReadWritePath != "" {
		if err := validatePathValue("read-write-path", p.ReadWritePath); err != nil {
			return err
		}
	}
	for _, g := range p.SupplementaryGroups {
		if err := validateGroupName(g); err != nil {
			return err
		}
	}
	return nil
}

// validateGroupName rejects a supplementary group name that is empty or
// contains whitespace/control characters — systemd's
// SupplementaryGroups= is a whitespace-separated list, so any such
// character in a single group name would silently split it into two
// (or corrupt the rendered unit line) rather than erroring visibly.
func validateGroupName(name string) error {
	if name == "" {
		return errors.New("supplementary group name must not be empty")
	}
	for _, r := range name {
		if r <= 0x20 || r == 0x7f {
			return fmt.Errorf("supplementary group name %q must not contain whitespace or control characters", name)
		}
	}
	return nil
}

// renderHub builds cloud-pulse-hub.service, matching
// install-hub.sh's render_unit() exactly.
func renderHub(p Params) string {
	var b strings.Builder
	writeLine(&b, "[Unit]")
	writeLine(&b, "Description=cloud-pulse hub (metrics ingestion + dashboard)")
	writeLine(&b, "After=network-online.target")
	writeLine(&b, "Wants=network-online.target")
	writeLine(&b, "")
	writeLine(&b, "[Service]")
	writeLine(&b, "Type=simple")
	writeLine(&b, "EnvironmentFile="+p.EnvFile)
	writeLine(&b, "Environment=CP_DATA_DIR="+hubDataDir)
	writeLine(&b, "ExecStart="+p.BinPath)
	writeLine(&b, "User="+p.userOrDefault())
	writeLine(&b, "Group="+p.groupOrDefault())
	writeLine(&b, "Restart=on-failure")
	writeLine(&b, "RestartSec=5")
	if p.ReadWritePath == "" {
		writeLine(&b, "StateDirectory=cloud-pulse")
	} else {
		writeLine(&b, "ReadWritePaths="+p.ReadWritePath)
	}
	writeLine(&b, "")
	writeHardening(&b)
	writeLine(&b, "")
	writeLine(&b, "[Install]")
	writeLine(&b, "WantedBy=multi-user.target")
	return b.String()
}

// renderAgent builds cloud-pulse-agent.service, matching
// install-agent.sh's render_unit() exactly.
func renderAgent(p Params) string {
	var b strings.Builder
	writeLine(&b, "[Unit]")
	writeLine(&b, "Description=cloud-pulse agent (host metrics collector)")
	writeLine(&b, "After=network-online.target")
	writeLine(&b, "Wants=network-online.target")
	writeLine(&b, "")
	writeLine(&b, "[Service]")
	writeLine(&b, "Type=simple")
	writeLine(&b, "EnvironmentFile="+p.EnvFile)
	writeLine(&b, "ExecStart="+p.BinPath)
	writeLine(&b, "User="+p.userOrDefault())
	writeLine(&b, "Group="+p.groupOrDefault())
	if len(p.SupplementaryGroups) > 0 {
		writeLine(&b, "SupplementaryGroups="+strings.Join(p.SupplementaryGroups, " "))
	}
	if p.RemoteUpdate {
		writeLine(&b, "StateDirectory=cloud-pulse-agent")
	}
	writeLine(&b, "Restart=on-failure")
	writeLine(&b, "RestartSec=5")
	writeLine(&b, "")
	writeLine(&b, "# --- sandboxing / hardening ---")
	writeLine(&b, "NoNewPrivileges=yes")
	writeLine(&b, "ProtectSystem=strict")
	writeLine(&b, "ProtectHome=read-only")
	writeLine(&b, "PrivateTmp=yes")
	// PrivateDevices=yes hides real /dev nodes but leaves /proc and
	// /sys mounted; gopsutil's disk/net/cpu collectors read
	// /proc/diskstats, /proc/net/dev, /proc/stat and statfs(2) on
	// mountpoints, none of which require device node access, so
	// PrivateDevices is safe here and does not break any collected
	// metric.
	//
	// These are bash-comment-only explanations in install-agent.sh's
	// render_unit_fallback() heredoc (real `#` shell comments, never
	// `echo`'d), so — to stay byte-identical, see the drift test in
	// scripts/test-install.sh — they are Go comments here too, not
	// written to the rendered unit text via writeLine.
	writeLine(&b, "PrivateDevices=yes")
	writeLine(&b, "ProtectKernelTunables=yes")
	writeLine(&b, "ProtectControlGroups=yes")
	writeLine(&b, "RestrictSUIDSGID=yes")
	writeLine(&b, "LockPersonality=yes")
	writeLine(&b, "CapabilityBoundingSet=")
	writeLine(&b, "AmbientCapabilities=")
	// gopsutil's net.IOCounters reads /proc/net/dev directly on Linux
	// (no netlink socket involved), so AF_NETLINK is not required; the
	// agent only needs outbound TCP to the hub. Same rationale as
	// above: bash-comment-only in the heredoc, Go-comment-only here.
	writeLine(&b, "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX")
	writeLine(&b, "")
	writeLine(&b, "[Install]")
	writeLine(&b, "WantedBy=multi-user.target")
	return b.String()
}

// writeHardening writes the shared hub hardening block (identical text
// to install-hub.sh's render_unit(), which has no explanatory comments
// unlike the agent's). RestrictAddressFamilies includes AF_NETLINK (in
// addition to AF_INET/AF_INET6/AF_UNIX) — unlike the agent, the hub's
// Network settings page calls net.Interfaces() (GET
// /api/v1/settings/network), which requires an AF_NETLINK socket on
// Linux to enumerate adapters; the agent has no such feature and keeps
// its narrower RestrictAddressFamilies (see renderAgent).
func writeHardening(b *strings.Builder) {
	writeLine(b, "# --- sandboxing / hardening ---")
	writeLine(b, "NoNewPrivileges=yes")
	writeLine(b, "ProtectSystem=strict")
	writeLine(b, "ProtectHome=read-only")
	writeLine(b, "PrivateTmp=yes")
	writeLine(b, "PrivateDevices=yes")
	writeLine(b, "ProtectKernelTunables=yes")
	writeLine(b, "ProtectControlGroups=yes")
	writeLine(b, "RestrictSUIDSGID=yes")
	writeLine(b, "LockPersonality=yes")
	writeLine(b, "CapabilityBoundingSet=")
	writeLine(b, "AmbientCapabilities=")
	writeLine(b, "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK")
}

// writeLine appends s followed by a newline, matching each `echo`
// invocation in the bash heredocs (including bare `echo` for a blank
// line).
func writeLine(b *strings.Builder, s string) {
	b.WriteString(s)
	b.WriteByte('\n')
}

// ParseExisting extracts Params from the text of an already-installed
// unit file, for Apply's up-to-date comparison. It validates every
// extracted path-shaped value the same way Render's own validation does
// (absolute, no whitespace/control characters/quotes), so a corrupted
// or hand-edited unit file is rejected rather than silently accepted
// and re-rendered with attacker-influenced paths.
func ParseExisting(unit string) (Params, error) {
	var p Params
	var execStartSeen, envFileSeen bool

	for _, rawLine := range strings.Split(unit, "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case strings.HasPrefix(line, "ExecStart="):
			execStartSeen = true
			value := strings.TrimSpace(strings.TrimPrefix(line, "ExecStart="))
			// ExecStart may be followed by argv tokens; Params.BinPath
			// is only the first whitespace-separated token.
			fields := strings.Fields(value)
			if len(fields) == 0 {
				return Params{}, errors.New("systemdunit: parse: ExecStart= has no value")
			}
			p.BinPath = fields[0]
		case strings.HasPrefix(line, "EnvironmentFile="):
			envFileSeen = true
			value := strings.TrimSpace(strings.TrimPrefix(line, "EnvironmentFile="))
			// systemd allows a leading '-' to mean "don't fail if
			// missing"; cloud-pulse never writes one, but strip it
			// defensively so a hand-edited unit still parses.
			value = strings.TrimPrefix(value, "-")
			p.EnvFile = value
		case strings.HasPrefix(line, "User="):
			p.User = strings.TrimSpace(strings.TrimPrefix(line, "User="))
		case strings.HasPrefix(line, "Group="):
			p.Group = strings.TrimSpace(strings.TrimPrefix(line, "Group="))
		case strings.HasPrefix(line, "SupplementaryGroups="):
			value := strings.TrimSpace(strings.TrimPrefix(line, "SupplementaryGroups="))
			if value != "" {
				p.SupplementaryGroups = strings.Fields(value)
			}
		case strings.HasPrefix(line, "ReadWritePaths="):
			p.ReadWritePath = strings.TrimSpace(strings.TrimPrefix(line, "ReadWritePaths="))
		case strings.HasPrefix(line, "StateDirectory="):
			value := strings.TrimSpace(strings.TrimPrefix(line, "StateDirectory="))
			// The hub's own StateDirectory=cloud-pulse (no
			// RemoteUpdate field involved — see ReadWritePath's
			// handling) is parsed by Render's own hub branch, which
			// never reads Params.RemoteUpdate; only the agent's
			// distinct StateDirectory=cloud-pulse-agent value is this
			// field's concern.
			if value == "cloud-pulse-agent" {
				p.RemoteUpdate = true
			}
		}
	}

	if !execStartSeen || p.BinPath == "" {
		return Params{}, errors.New("systemdunit: parse: missing ExecStart=")
	}
	if !envFileSeen || p.EnvFile == "" {
		return Params{}, errors.New("systemdunit: parse: missing EnvironmentFile=")
	}

	// Unit files describe Linux paths, so POSIX path semantics apply
	// regardless of the OS this code is compiled for.
	base := path.Base(p.BinPath)
	switch base {
	case HubBinary, AgentBinary:
		p.Binary = base
	default:
		return Params{}, fmt.Errorf("systemdunit: parse: ExecStart basename %q is not %q or %q", base, HubBinary, AgentBinary)
	}

	if err := validatePathValue("bin-path", p.BinPath); err != nil {
		return Params{}, fmt.Errorf("systemdunit: parse: %w", err)
	}
	if err := validatePathValue("env-file", p.EnvFile); err != nil {
		return Params{}, fmt.Errorf("systemdunit: parse: %w", err)
	}
	if p.ReadWritePath != "" {
		if err := validatePathValue("read-write-path", p.ReadWritePath); err != nil {
			return Params{}, fmt.Errorf("systemdunit: parse: %w", err)
		}
	}
	for _, g := range p.SupplementaryGroups {
		if err := validateGroupName(g); err != nil {
			return Params{}, fmt.Errorf("systemdunit: parse: %w", err)
		}
	}

	return p, nil
}

// validatePathValue rejects a path-shaped value that is not absolute,
// or that contains whitespace, control characters, or quote characters
// — any of which could indicate a corrupted/tampered unit file or, if
// ever fed back into a shell/exec call, an injection vector. Values
// extracted here only ever flow into Go's os/exec (argv elements, never
// a shell string) and back into Render's own strings.Builder output, so
// this is a defense-in-depth sanity check, not a strict requirement for
// safety — but a malformed value here is itself a sign the file has
// been hand-edited or corrupted, worth surfacing as an error rather than
// silently re-rendering it.
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

// Apply reads the unit file at unitPath, parses it via ParseExisting,
// re-renders it via Render, and — if the rendered text differs from
// what's on disk — writes the new text to unitPath+".tmp", preserves the
// previous content at unitPath+".bak", and renames the ".tmp" file over
// unitPath (atomic on the same filesystem, mirroring the install
// scripts' own install-to-".new"-then-rename pattern). If reload is
// true and a change was made, it runs `systemctl daemon-reload` bounded
// by a 30s timeout. Apply reports changed=false and takes no action
// (including no reload) when the existing unit already matches Render's
// output.
//
// Progress and any reload output are written to out if non-nil.
func Apply(ctx context.Context, unitPath string, reload bool, out interface{ Write([]byte) (int, error) }) (bool, error) {
	existing, err := os.ReadFile(unitPath) //nolint:gosec // unitPath is an operator-supplied systemd unit path, not user request input
	if err != nil {
		return false, fmt.Errorf("systemdunit: apply: read %s: %w", unitPath, err)
	}

	params, err := ParseExisting(string(existing))
	if err != nil {
		return false, fmt.Errorf("systemdunit: apply: parse %s: %w", unitPath, err)
	}

	rendered, err := Render(params)
	if err != nil {
		return false, fmt.Errorf("systemdunit: apply: render: %w", err)
	}

	if rendered == string(existing) {
		printfln(out, "systemd unit up to date: %s", unitPath)
		return false, nil
	}

	backupPath := unitPath + ".bak"
	if err := os.WriteFile(backupPath, existing, 0o644); err != nil { //nolint:gosec // unit files are world-readable by design (systemd requirement)
		return false, fmt.Errorf("systemdunit: apply: write backup %s: %w", backupPath, err)
	}

	tmpPath := unitPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(rendered), 0o644); err != nil { //nolint:gosec // unit files are world-readable by design (systemd requirement)
		return false, fmt.Errorf("systemdunit: apply: write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, unitPath); err != nil {
		return false, fmt.Errorf("systemdunit: apply: rename %s to %s: %w", tmpPath, unitPath, err)
	}
	printfln(out, "updated systemd unit: %s (previous content saved to %s)", unitPath, backupPath)

	if reload {
		if err := daemonReload(ctx, out); err != nil {
			return true, fmt.Errorf("systemdunit: apply: %w", err)
		}
	}

	return true, nil
}

// Reload runs `systemctl daemon-reload` bounded by a 30s timeout,
// writing progress to out if non-nil. Exported so callers that apply
// more than one unit file in a single invocation (e.g. cmd/agent's
// `systemd-unit apply`, which may update both the main agent unit and
// the SPEC-v0.6 §2 remote-update .path/.service units) can call Apply
// with reload=false for each and run a single combined reload
// afterward, rather than reloading once per unit.
func Reload(ctx context.Context, out interface{ Write([]byte) (int, error) }) error {
	return daemonReload(ctx, out)
}

// daemonReload runs `systemctl daemon-reload` bounded by reloadTimeout.
func daemonReload(ctx context.Context, out interface{ Write([]byte) (int, error) }) error {
	reloadCtx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()

	cmd := exec.CommandContext(reloadCtx, "systemctl", "daemon-reload")
	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		printfln(out, "%s", strings.TrimRight(string(output), "\n"))
	}
	if err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	printfln(out, "systemctl daemon-reload: OK")
	return nil
}

// printfln writes a formatted, newline-terminated message to out if
// non-nil, matching selfupdate's own printf helper's shape.
func printfln(out interface{ Write([]byte) (int, error) }, format string, args ...any) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, format+"\n", args...)
}
