// service.go dispatches `cloud-pulse-agent service <install|uninstall|
// start|stop|status|run-updater>` (SPEC-v0.7 §1's Windows service
// subcommand). This subcommand is only meaningful on Windows (see
// runService's platform check below); on every other OS it prints a
// short redirect message and exits 1 without attempting anything,
// mirroring how `plist`/`launchd` behaves symmetrically for non-macOS.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/leejeonghun001/cloud-pulse/internal/agent"
	"github.com/leejeonghun001/cloud-pulse/internal/config"
	"github.com/leejeonghun001/cloud-pulse/internal/selfupdate"
	"github.com/leejeonghun001/cloud-pulse/internal/updatepaths"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// defaultWindowsRequestDir/defaultWindowsResultDir read the canonical
// Windows remote-update paths from internal/updatepaths.
func defaultWindowsRequestDir() string {
	paths, _ := updatepaths.For("windows")
	return paths.RequestDir
}

func defaultWindowsResultDir() string {
	paths, _ := updatepaths.For("windows")
	return paths.ResultDir
}

// runService is the entry point for `cloud-pulse-agent service ...`,
// dispatched from main before any normal flag parsing or agent config
// loading (the same pattern as runUpdate/runSystemdUnit). Returns a
// process exit code.
//
// When this process is itself already running under the Windows
// Service Control Manager (agent.IsWindowsService), main dispatches
// straight to runAsWindowsService instead of reaching this function at
// all — see main.go. This function only handles the *management*
// subcommands (install/uninstall/start/stop/status) plus run-updater,
// issued from an interactive administrator PowerShell/cmd session (the
// management subcommands) or by the SCM itself (run-updater, when
// invoked as the cloud-pulse-agent-updater service's own entry point —
// see install-agent.ps1's Install-CloudPulseService call for that
// service).
func runService(args []string) int {
	if len(args) == 0 {
		printServiceUsage(false)
		return 1
	}
	for _, a := range args {
		if a == "-h" || a == "--help" {
			printServiceUsage(true)
			return 0
		}
	}

	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "cloud-pulse-agent service: Windows-only; on this OS use systemd-unit (Linux) or plist (macOS) instead")
		return 1
	}

	fs := flag.NewFlagSet("cloud-pulse-agent service", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	envFile := fs.String("env-file", "", "path to the KEY=VALUE env file the installed service should read (install, run-updater)")
	binPath := fs.String("bin-path", "", "absolute path to the installed cloud-pulse-agent.exe (install only; default: this process's own path)")
	requestDir := fs.String("request-dir", defaultWindowsRequestDir(), "directory the run-updater service polls for update-request.json (run-updater only)")
	resultDir := fs.String("result-dir", defaultWindowsResultDir(), "root-owned-equivalent directory run-updater writes result.json into (run-updater only)")
	logFile := fs.String("log-file", "", "append service diagnostics to this rotating log file (run-updater only)")
	fs.Usage = func() { printServiceUsage(false) }
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}

	switch args[0] {
	case "install":
		return runServiceInstall(*binPath, *envFile)
	case "uninstall":
		return runServiceControlAction("uninstall", agent.UninstallWindowsService)
	case "start":
		return runServiceControlAction("start", agent.StartWindowsService)
	case "stop":
		return runServiceControlAction("stop", agent.StopWindowsService)
	case "status":
		return runServiceStatus()
	case "run-updater":
		return runServiceRunUpdater(*requestDir, *resultDir, *envFile, *logFile)
	default:
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: service: unknown subcommand %q\n", args[0])
		printServiceUsage(false)
		return 1
	}
}

// runServiceInstall resolves binPath (defaulting to this process's own
// executable path) and envFile, validates the env file is at least
// readable and parseable (config.ParseEnvFile), and registers the
// Windows service with a fixed "--env-file <envFile>" argument so the
// installed service always runs against that specific config file
// regardless of what CP_* variables happen to be set in whatever
// context the SCM itself runs in (which has none relevant to this
// service — LocalSystem's environment is not the operator's).
func runServiceInstall(binPath, envFile string) int {
	if envFile == "" {
		fmt.Fprintln(os.Stderr, "cloud-pulse-agent: service install: --env-file is required")
		return 1
	}
	content, err := os.ReadFile(envFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: service install: read env file: %v\n", err)
		return 1
	}
	if _, err := config.ParseEnvFile(string(content)); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: service install: parse env file: %v\n", err)
		return 1
	}

	if binPath == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cloud-pulse-agent: service install: resolve own executable path: %v\n", err)
			return 1
		}
		binPath = exe
	}

	commandLine := quoteWindowsArg(binPath) + " --env-file " + quoteWindowsArg(envFile)
	if err := agent.InstallWindowsService(commandLine); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: service install: %v\n", err)
		return 1
	}
	fmt.Println("cloud-pulse-agent service installed")
	return 0
}

// runServiceControlAction runs fn (Uninstall/Start/Stop) and reports
// its outcome uniformly, past-tensing verb correctly for each of the
// three possible values ("uninstalled"/"started"/"stopped" — a naive
// verb+"ed" suffix would incorrectly yield "uninstallped" for
// "uninstall").
func runServiceControlAction(verb string, fn func() error) int {
	if err := fn(); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: service %s: %v\n", verb, err)
		return 1
	}
	fmt.Printf("cloud-pulse-agent service %s\n", pastTense(verb))
	return 0
}

// pastTense returns the past-tense form of a service control verb
// ("install"/"uninstall"/"start"/"stop"), used only for this
// subcommand's own small, fixed vocabulary — not a general English
// conjugation helper.
func pastTense(verb string) string {
	switch verb {
	case "uninstall":
		return "uninstalled"
	case "start":
		return "started"
	case "stop":
		return "stopped"
	default:
		return verb + "ed"
	}
}

// runServiceStatus prints the service's current state.
func runServiceStatus() int {
	status, err := agent.WindowsServiceStatus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: service status: %v\n", err)
		return 1
	}
	fmt.Printf("state=%d pid=%d\n", status.State, status.ProcessID)
	return 0
}

// runServiceRunUpdater is the entry point for the
// cloud-pulse-agent-updater Windows service's own BinaryPathName
// (install-agent.ps1 registers this service with `service run-updater
// --env-file <envFile>` as its command line) — SPEC-v0.7 §1: "Windows
// 서비스 방식... 30초 간격으로 확인한다." It runs agent.RunAsService
// under the dedicated updater service name so the SCM sees a normal
// long-running service (Stop/Shutdown cancels the polling loop's
// context exactly like the main agent service), polling requestDir for
// update-request.json every 30s and, when found, running the same
// selfupdate.RunFromRequest pipeline the Linux/macOS oneshot paths use
// — the request file's own JobID/Target is all that's ever trusted
// from the hub; Source is always built from this process's own
// environment (envFile, if given, else the real process environment),
// matching every other platform's security model documented in
// internal/selfupdate/fromrequest.go's package doc comment.
func runServiceRunUpdater(requestDir, resultDir, envFile, logFile string) int {
	loggerOutput, err := newLogWriter(logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: service run-updater: %v\n", err)
		return 1
	}
	defer func() { _ = loggerOutput.Close() }()
	logger, err := config.NewLogger(loggerOutput, "info", "text")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: service run-updater: %v\n", err)
		return 1
	}

	requestPath := filepath.Join(requestDir, updatepaths.RequestFileName)
	source, err := updateSourceFromEnvFile(envFile)
	if err != nil {
		logger.Error("load update source", "error", err)
		return 1
	}

	err = agent.RunAsService("cloud-pulse-agent-updater", func(ctx context.Context) error {
		return agent.RunUpdaterLoop(ctx, agent.UpdaterOptions{
			RequestExists: func() bool {
				_, statErr := os.Stat(requestPath)
				return statErr == nil
			},
			ProcessRequest: func(ctx context.Context) error {
				_, runErr := selfupdate.RunFromRequest(ctx, selfupdate.FromRequestOptions{
					RequestPath: requestPath,
					ResultDir:   resultDir,
					Binary:      updateBinaryName,
					Current:     version.Version,
					Source:      source,
				})
				return runErr
			},
			Logger: logger,
		})
	}, logger)
	if err != nil {
		logger.Error("updater service exited", "error", err)
		return 1
	}
	return 0
}

// runAsWindowsService is called from main when this process is
// detected to be running under the SCM (agent.IsWindowsService()) —
// it never returns until the service is asked to stop.
func runAsWindowsService(envFile string) int {
	if err := agent.RunAsService(agent.WindowsServiceName, func(ctx context.Context) error {
		return runAgentWithContext(ctx, envFile)
	}, nil); err != nil {
		return 1
	}
	return 0
}

// quoteWindowsArg wraps s in double quotes for inclusion in a Windows
// service BinaryPathName command line, escaping any embedded double
// quote by doubling it (the convention CreateProcess's own argument
// parser expects). Both binPath and envFile here are operator-supplied
// absolute paths from an interactive `service install` invocation, not
// externally supplied request input — this quoting exists so a path
// containing a space (e.g. "C:\Program Files\...") is parsed correctly
// by the SCM's own command-line splitting, not as a security boundary.
func quoteWindowsArg(s string) string {
	escaped := ""
	for _, r := range s {
		if r == '"' {
			escaped += `""`
		} else {
			escaped += string(r)
		}
	}
	return `"` + escaped + `"`
}

// printServiceUsage documents the service subcommand's flag surface.
// toStdout selects os.Stdout (a `--help` request) vs os.Stderr (usage
// shown alongside an error).
func printServiceUsage(toStdout bool) {
	lines := []string{
		"usage: cloud-pulse-agent service install --env-file FILE [--bin-path PATH]",
		"       cloud-pulse-agent service uninstall|start|stop|status",
		"       cloud-pulse-agent service run-updater [--request-dir DIR] [--result-dir DIR]",
		"",
		"Windows-only: registers/controls the cloud-pulse-agent Windows service via the",
		"golang.org/x/sys/windows/svc/mgr package. On any other OS this subcommand is",
		"not applicable — use systemd-unit (Linux) or plist (macOS) instead.",
		"run-updater is the cloud-pulse-agent-updater service's own entry point (30s poll",
		"loop for hub-triggered remote updates); it is not meant to be run interactively.",
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
