package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/systemdunit"
)

// systemdUnitApplyTimeout bounds `systemd-unit apply`, including its
// optional `systemctl daemon-reload` step.
const systemdUnitApplyTimeout = 60 * time.Second

// defaultAgentUnitPath is the systemd unit path install-agent.sh
// installs to on a real (non-sandboxed) install.
const defaultAgentUnitPath = "/etc/systemd/system/cloud-pulse-agent.service"

// defaultRequestPath and defaultResultDir are the fixed SPEC-v0.6 §2
// paths the remote-update .path/.service units are always rendered
// against on a real (non-sandboxed) install.
const (
	defaultRequestPath = "/var/lib/cloud-pulse-agent/update-request.json"
	defaultResultDir   = "/var/lib/cloud-pulse-agent-update"
)

// defaultUpdatePathUnitPath and defaultUpdateServiceUnitPath are the
// systemd unit paths for the two additional remote-update units
// (SPEC-v0.6 §2), alongside defaultAgentUnitPath.
const (
	defaultUpdatePathUnitPath    = "/etc/systemd/system/" + systemdunit.UpdatePathUnitName
	defaultUpdateServiceUnitPath = "/etc/systemd/system/" + systemdunit.UpdateServiceUnitName
)

// runSystemdUnit implements `cloud-pulse-agent systemd-unit
// <print|apply> [flags]`. It is dispatched from main before
// flag.Parse/config loading, the same pattern as `update` (see
// cmd/agent/update.go), so it works against a completely unconfigured
// install.
func runSystemdUnit(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent systemd-unit <print|apply> [flags]")
		return 1
	}

	switch args[0] {
	case "print":
		return runSystemdUnitPrint(args[1:])
	case "apply":
		return runSystemdUnitApply(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: systemd-unit: unknown subcommand %q\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent systemd-unit <print|apply> [flags]")
		return 1
	}
}

// runSystemdUnitPrint implements `systemd-unit print --bin-path P
// --env-file E [--user U --group G] [--remote-update]`, printing the
// rendered unit file to stdout. This is what install-agent.sh's
// render_unit() calls into on a v0.3.1+ downloaded binary, falling
// back to its own heredoc only if this invocation fails.
// --read-write-path is accepted (for symmetry with the hub's flag set)
// but ignored: the agent unit's only StateDirectory concept is the
// fixed "cloud-pulse-agent" one --remote-update enables (SPEC-v0.6 §2),
// which is not itself configurable to a different path.
func runSystemdUnitPrint(args []string) int {
	fs := flag.NewFlagSet("cloud-pulse-agent systemd-unit print", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	binPath := fs.String("bin-path", "", "absolute path to the installed cloud-pulse-agent binary (required)")
	envFile := fs.String("env-file", "", "absolute path to the agent's EnvironmentFile (required)")
	user := fs.String("user", "", "service User= override (default: cloud-pulse)")
	group := fs.String("group", "", "service Group= override (default: cloud-pulse)")
	supplementaryGroups := fs.String("supplementary-groups", "", "comma-separated SupplementaryGroups= list, e.g. \"docker\" (SPEC-v0.5 §C --docker)")
	remoteUpdate := fs.Bool("remote-update", false, "render StateDirectory=cloud-pulse-agent for the remote batch-update feature (SPEC-v0.6 §2, only when CP_REMOTE_UPDATE=on)")
	_ = fs.String("read-write-path", "", "ignored for the agent (no configurable StateDirectory/ReadWritePaths in its unit)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent systemd-unit print --bin-path P --env-file E [--user U --group G] [--supplementary-groups G1,G2] [--remote-update]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	unit, err := systemdunit.Render(systemdunit.Params{
		Binary:              systemdunit.AgentBinary,
		BinPath:             *binPath,
		EnvFile:             *envFile,
		User:                *user,
		Group:               *group,
		SupplementaryGroups: splitNonEmpty(*supplementaryGroups),
		RemoteUpdate:        *remoteUpdate,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: systemd-unit print: %v\n", err)
		return 1
	}
	fmt.Print(unit)
	return 0
}

// runSystemdUnitApply implements `systemd-unit apply [--unit-path P]
// [--no-reload]`, rewriting an already-installed unit file to match the
// current Render output if it has drifted, then (unless --no-reload)
// running `systemctl daemon-reload`. When the agent's own env file (the
// EnvironmentFile= path recorded in the existing unit) has
// CP_REMOTE_UPDATE=on, this also creates/updates
// cloud-pulse-agent-update.path/.service alongside the main unit
// (SPEC-v0.6 §2) — enabling the .path unit for boot is left to the
// caller (install-agent.sh / a future explicit enable step), matching
// ApplyUpdateUnits' own division of responsibility.
func runSystemdUnitApply(args []string) int {
	fs := flag.NewFlagSet("cloud-pulse-agent systemd-unit apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	unitPath := fs.String("unit-path", defaultAgentUnitPath, "path to the installed systemd unit file")
	updatePathUnitPath := fs.String("update-path-unit-path", defaultUpdatePathUnitPath, "path to the remote-update .path unit file")
	updateServiceUnitPath := fs.String("update-service-unit-path", defaultUpdateServiceUnitPath, "path to the remote-update .service unit file")
	requestPath := fs.String("request-path", defaultRequestPath, "request file path the remote-update .path unit watches")
	resultDir := fs.String("result-dir", defaultResultDir, "root-owned result directory the remote-update .service unit writes into")
	noReload := fs.Bool("no-reload", false, "skip systemctl daemon-reload even if a unit changed")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent systemd-unit apply [--unit-path /etc/systemd/system/cloud-pulse-agent.service] [--no-reload]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), systemdUnitApplyTimeout)
	defer cancel()

	mainChanged, err := systemdunit.Apply(ctx, *unitPath, false, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: systemd-unit apply: %v\n", err)
		return 1
	}

	updateChanged, err := applyUpdateUnitsIfEnabled(*unitPath, *updatePathUnitPath, *updateServiceUnitPath, *requestPath, *resultDir)
	if err != nil {
		// A failure here is a warning, not a hard error: the primary
		// unit (mainChanged above) already applied successfully, and
		// the remote-update feature is opt-in — a problem determining
		// or applying its units should never block the main agent
		// unit's own update-apply pass.
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: systemd-unit apply: warning: remote-update units: %v\n", err)
	}

	if !*noReload && (mainChanged || updateChanged) {
		if err := systemdunit.Reload(ctx, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "cloud-pulse-agent: systemd-unit apply: %v\n", err)
			return 1
		}
	}
	return 0
}

// applyUpdateUnitsIfEnabled reads CP_REMOTE_UPDATE from the agent's own
// EnvironmentFile (recorded in the already-installed unit at
// unitPath), and — only when it is "on" — creates/updates the two
// remote-update units via systemdunit.ApplyUpdateUnits. It is a no-op
// (changed=false, nil error) when the main unit does not exist yet,
// CP_REMOTE_UPDATE is off/unset, or the env file cannot be read (e.g.
// permission denied when this is invoked without root — the caller
// treats this as a warning, never fatal).
func applyUpdateUnitsIfEnabled(unitPath, pathUnitPath, serviceUnitPath, requestPath, resultDir string) (bool, error) {
	existing, err := os.ReadFile(unitPath) //nolint:gosec // unitPath is an operator-supplied systemd unit path, not user request input
	if err != nil {
		return false, nil //nolint:nilerr // missing/unreadable unit just means "nothing to do yet"
	}
	params, err := systemdunit.ParseExisting(string(existing))
	if err != nil {
		return false, fmt.Errorf("parse existing unit %s: %w", unitPath, err)
	}

	on, err := envFileHasRemoteUpdateOn(params.EnvFile)
	if err != nil {
		return false, err
	}
	if !on {
		return false, nil
	}

	return systemdunit.ApplyUpdateUnits(pathUnitPath, serviceUnitPath, systemdunit.UpdateUnitParams{
		BinPath:     params.BinPath,
		RequestPath: requestPath,
		ResultDir:   resultDir,
	}, os.Stdout)
}

// envFileHasRemoteUpdateOn reads envPath (an EnvironmentFile= target,
// simple KEY=VALUE lines, never shell-interpreted — matching how
// systemd itself parses EnvironmentFile=) and reports whether the last
// active (non-comment) CP_REMOTE_UPDATE assignment equals "on"
// (case-insensitive), mirroring internal/config's own
// last-line-wins/case-insensitive parsing rule for this variable.
func envFileHasRemoteUpdateOn(envPath string) (bool, error) {
	f, err := os.Open(envPath) //nolint:gosec // envPath is derived from the already-installed unit's own EnvironmentFile=, not user request input
	if err != nil {
		return false, fmt.Errorf("open env file %s: %w", envPath, err)
	}
	defer func() { _ = f.Close() }()

	on := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		value, ok := strings.CutPrefix(line, "CP_REMOTE_UPDATE=")
		if !ok {
			continue
		}
		on = strings.EqualFold(strings.TrimSpace(value), "on")
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("read env file %s: %w", envPath, err)
	}
	return on, nil
}

// splitNonEmpty splits a comma-separated list into its non-empty,
// whitespace-trimmed elements, returning nil (not an empty slice) when
// csv is empty or contains only commas/whitespace — so an unset
// --supplementary-groups flag renders no SupplementaryGroups= line at
// all rather than an empty one.
func splitNonEmpty(csv string) []string {
	var out []string
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
