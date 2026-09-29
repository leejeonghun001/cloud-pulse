package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/systemdunit"
)

// systemdUnitApplyTimeout bounds `systemd-unit apply`, including its
// optional `systemctl daemon-reload` step.
const systemdUnitApplyTimeout = 60 * time.Second

// defaultHubUnitPath is the systemd unit path install-hub.sh installs
// to on a real (non-sandboxed) install.
const defaultHubUnitPath = "/etc/systemd/system/cloud-pulse-hub.service"

// runSystemdUnit implements `cloud-pulse-hub systemd-unit <print|apply>
// [flags]`. It is dispatched from main before flag.Parse/config loading,
// the same pattern as `update` (see cmd/hub/update.go), so it works
// against a completely unconfigured install.
func runSystemdUnit(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-hub systemd-unit <print|apply> [flags]")
		return 1
	}

	switch args[0] {
	case "print":
		return runSystemdUnitPrint(args[1:])
	case "apply":
		return runSystemdUnitApply(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub: systemd-unit: unknown subcommand %q\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-hub systemd-unit <print|apply> [flags]")
		return 1
	}
}

// runSystemdUnitPrint implements `systemd-unit print --bin-path P
// --env-file E [--user U --group G] [--read-write-path D]`, printing
// the rendered unit file to stdout. This is what install-hub.sh's
// render_unit() calls into on a v0.3.1+ downloaded binary, falling back
// to its own heredoc only if this invocation fails.
func runSystemdUnitPrint(args []string) int {
	fs := flag.NewFlagSet("cloud-pulse-hub systemd-unit print", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	binPath := fs.String("bin-path", "", "absolute path to the installed cloud-pulse-hub binary (required)")
	envFile := fs.String("env-file", "", "absolute path to the hub's EnvironmentFile (required)")
	user := fs.String("user", "", "service User= override (default: cloud-pulse)")
	group := fs.String("group", "", "service Group= override (default: cloud-pulse)")
	readWritePath := fs.String("read-write-path", "", "sandbox data dir; empty renders StateDirectory=cloud-pulse for a real install")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-hub systemd-unit print --bin-path P --env-file E [--user U --group G] [--read-write-path D]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	unit, err := systemdunit.Render(systemdunit.Params{
		Binary:        systemdunit.HubBinary,
		BinPath:       *binPath,
		EnvFile:       *envFile,
		User:          *user,
		Group:         *group,
		ReadWritePath: *readWritePath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub: systemd-unit print: %v\n", err)
		return 1
	}
	fmt.Print(unit)
	return 0
}

// runSystemdUnitApply implements `systemd-unit apply [--unit-path P]
// [--no-reload]`, rewriting an already-installed unit file to match the
// current Render output if it has drifted, then (unless --no-reload)
// running `systemctl daemon-reload`.
func runSystemdUnitApply(args []string) int {
	fs := flag.NewFlagSet("cloud-pulse-hub systemd-unit apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	unitPath := fs.String("unit-path", defaultHubUnitPath, "path to the installed systemd unit file")
	noReload := fs.Bool("no-reload", false, "skip systemctl daemon-reload even if the unit changed")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-hub systemd-unit apply [--unit-path /etc/systemd/system/cloud-pulse-hub.service] [--no-reload]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), systemdUnitApplyTimeout)
	defer cancel()

	if _, err := systemdunit.Apply(ctx, *unitPath, !*noReload, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub: systemd-unit apply: %v\n", err)
		return 1
	}
	return 0
}
