// plist.go dispatches `cloud-pulse-agent plist <print|apply>` (also
// reachable as `launchd <print|apply>`, an alias) — SPEC-v0.7 §1's
// macOS launchd unit renderer, mirroring this project's existing
// `systemd-unit print|apply` subcommand for Linux.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/launchd"
)

// plistApplyTimeout bounds `plist apply`, including its optional
// `launchctl kickstart` step.
const plistApplyTimeout = 60 * time.Second

// runPlist is the entry point for `cloud-pulse-agent plist ...` /
// `cloud-pulse-agent launchd ...`, dispatched from main before any
// normal flag parsing or agent config loading (the same pattern as
// runUpdate/runSystemdUnit). Returns a process exit code.
func runPlist(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent plist <print|apply> [flags]")
		return 1
	}

	switch args[0] {
	case "print":
		return runPlistPrint(args[1:])
	case "apply":
		return runPlistApply(args[1:])
	case "update-print":
		return runPlistUpdatePrint(args[1:])
	case "-h", "--help":
		printPlistUsage(true)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: plist: unknown subcommand %q\n", args[0])
		printPlistUsage(false)
		return 1
	}
}

// runPlistPrint implements `plist print --bin-path P --env-file E
// [--user U] [--log-path P]`, printing the rendered plist to stdout.
// This is what install-agent.sh's render_plist() calls into on a
// v0.7.0+ downloaded binary, falling back to its own heredoc only if
// this invocation fails.
func runPlistPrint(args []string) int {
	fs := flag.NewFlagSet("cloud-pulse-agent plist print", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	binPath := fs.String("bin-path", "", "absolute path to the installed cloud-pulse-agent binary (required)")
	envFile := fs.String("env-file", "", "absolute path to the agent's KEY=VALUE env file (required)")
	user := fs.String("user", "", "LaunchDaemon UserName override (default: _cloudpulse)")
	logPath := fs.String("log-path", "", "StandardOutPath/StandardErrorPath override (default: /Library/Logs/cloud-pulse-agent.log)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent plist print --bin-path P --env-file E [--user U] [--log-path P]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	plist, err := launchd.Render(launchd.Params{
		BinPath:  *binPath,
		EnvFile:  *envFile,
		UserName: *user,
		LogPath:  *logPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: plist print: %v\n", err)
		return 1
	}
	fmt.Print(plist)
	return 0
}

// runPlistUpdatePrint prints the privileged macOS remote-update helper plist.
// install-agent.sh invokes it after downloading a current agent binary; its
// fallback heredoc remains for legacy pinned installs.
func runPlistUpdatePrint(args []string) int {
	fs := flag.NewFlagSet("cloud-pulse-agent plist update-print", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	binPath := fs.String("bin-path", "", "absolute path to the installed cloud-pulse-agent binary (required)")
	envFile := fs.String("env-file", "", "absolute path to the agent's KEY=VALUE env file (required)")
	requestPath := fs.String("request-path", "", "absolute request file path (required)")
	resultDir := fs.String("result-dir", "", "absolute privileged result directory (required)")
	logPath := fs.String("log-path", "", "helper StandardOutPath/StandardErrorPath (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	plist, err := launchd.RenderUpdate(launchd.UpdateParams{
		BinPath: *binPath, EnvFile: *envFile, RequestPath: *requestPath,
		ResultDir: *resultDir, LogPath: *logPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: plist update-print: %v\n", err)
		return 1
	}
	fmt.Print(plist)
	return 0
}

// runPlistApply implements `plist apply [--plist-path
// /Library/LaunchDaemons/com.cloudpulse.agent.plist] [--no-reload]`,
// rewriting an already-installed plist to match the current Render
// output if it has drifted, then (unless --no-reload) running
// `launchctl kickstart -k system/com.cloudpulse.agent`.
func runPlistApply(args []string) int {
	fs := flag.NewFlagSet("cloud-pulse-agent plist apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	plistPath := fs.String("plist-path", launchd.PlistPath, "path to the installed LaunchDaemon plist")
	noReload := fs.Bool("no-reload", false, "skip launchctl kickstart even if the plist changed")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent plist apply [--plist-path /Library/LaunchDaemons/com.cloudpulse.agent.plist] [--no-reload]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), plistApplyTimeout)
	defer cancel()

	if _, err := launchd.Apply(ctx, *plistPath, !*noReload, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: plist apply: %v\n", err)
		return 1
	}
	return 0
}

// printPlistUsage documents the plist subcommand's flag surface.
// toStdout selects os.Stdout (a `--help` request) vs os.Stderr (usage
// shown alongside an error).
func printPlistUsage(toStdout bool) {
	lines := []string{
		"usage: cloud-pulse-agent plist print --bin-path P --env-file E [--user U] [--log-path P]",
		"       cloud-pulse-agent plist apply [--plist-path /Library/LaunchDaemons/com.cloudpulse.agent.plist] [--no-reload]",
		"",
		"macOS-only: renders/re-renders the com.cloudpulse.agent LaunchDaemon plist,",
		"mirroring systemd-unit's print/apply behavior for Linux. `launchd` is accepted",
		"as an alias for `plist`.",
	}
	if toStdout {
		for _, l := range lines {
			fmt.Println(l)
		}
		return
	}
	for _, l := range lines {
		fmt.Fprintln(os.Stderr, l)
	}
}
