// Command cloud-pulse-agent collects host resource metrics and reports
// them to a cloud-pulse hub. It is wiring only: configuration loading,
// logger setup, signal handling, and delegating to internal/agent.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/agent"
	"github.com/leejeonghun001/cloud-pulse/internal/config"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "update" {
		os.Exit(runUpdate(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "systemd-unit" {
		os.Exit(runSystemdUnit(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "service" {
		os.Exit(runService(os.Args[2:]))
	}
	if len(os.Args) > 1 && (os.Args[1] == "plist" || os.Args[1] == "launchd") {
		os.Exit(runPlist(os.Args[2:]))
	}
	// A Windows service launch invokes this same binary with no
	// recognizable subcommand argument at all (the SCM starts it with
	// just the BinaryPathName's own argv, e.g. "cloud-pulse-agent.exe
	// --env-file C:\...\agent.env") — agent.IsWindowsService() (a
	// windows-only real check; always false elsewhere) distinguishes
	// that from an ordinary interactive/foreground invocation of the
	// same flags, so main.go doesn't need a Windows-only build tag of
	// its own.
	if agent.IsWindowsService() {
		envFileFlag := flag.String("env-file", "", "read configuration from a KEY=VALUE env file instead of the process environment")
		flag.Parse()
		os.Exit(runAsWindowsService(*envFileFlag))
	}
	os.Exit(run())
}

// run contains all logic previously inlined in main, returning a process
// exit code so main can stay a single os.Exit call site.
func run() int {
	versionFlag := flag.Bool("version", false, "print version information and exit")
	onceFlag := flag.Bool("once", false, "collect two samples one second apart, print the second as JSON, and exit")
	printHostFlag := flag.Bool("print-host", false, "print detected host info as JSON and exit")
	envFileFlag := flag.String("env-file", "", "read configuration from a KEY=VALUE env file instead of the process environment (macOS launchd / Windows service; see docs)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent [flags]")
		fmt.Fprintln(os.Stderr, "       cloud-pulse-agent update [--check] [--version vX.Y.Z] [--no-restart]")
		fmt.Fprintln(os.Stderr, "       cloud-pulse-agent systemd-unit <print|apply> [flags]")
		fmt.Fprintln(os.Stderr, "       cloud-pulse-agent service install|uninstall|start|stop|status [--env-file FILE]")
		fmt.Fprintln(os.Stderr, "       cloud-pulse-agent plist <print|apply> [flags]")
		fmt.Fprintln(os.Stderr, "  --env-file FILE  read configuration from FILE (KEY=VALUE) instead of the process environment")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.String())
		return 0
	}

	if *onceFlag {
		return runOnce()
	}

	if *printHostFlag {
		return runPrintHost()
	}

	return runAgent(*envFileFlag)
}

// runOnce collects two samples one second apart (so rate fields are
// populated) and prints the second as indented JSON. It requires no hub
// configuration, only network-interface exclusion defaults.
func runOnce() int {
	src := agent.NewGopsutilSource()
	collector := agent.NewCollector(src, agent.CollectorOptions{NetExclude: defaultNetExcludeGlobs()})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := collector.Collect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: collect: %v\n", err)
		return 1
	}
	time.Sleep(1 * time.Second)

	sample, err := collector.Collect(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: collect: %v\n", err)
		return 1
	}

	return printJSON(sample)
}

// runPrintHost prints the detected HostInfo as indented JSON. It loads
// full agent configuration since HostInfo depends on host ID and provider
// settings.
func runPrintHost() int {
	cfg, err := loadConfig("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: %v\n", err)
		return 1
	}

	logger, err := config.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: %v\n", err)
		return 1
	}

	src := agent.NewGopsutilSource()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	info := agent.BuildHostInfo(ctx, src, cfg, logger)
	return printJSON(info)
}

// runAgent runs the normal collect/report loop until SIGINT or SIGTERM.
// envFile, if non-empty, is a path to a KEY=VALUE file
// (config.ParseEnvFile's format) read instead of the process
// environment — used by the macOS launchd and Windows service agent
// wrappers, neither of which has a systemd-style EnvironmentFile=
// mechanism (SPEC-v0.7 §1).
func runAgent(envFile string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runAgentWithContext(ctx, envFile); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: %v\n", err)
		return 1
	}
	return 0
}

// runAgentWithContext is runAgent's actual body, taking an
// already-constructed ctx instead of deriving one from OS signals
// itself — this is the entry point service.go's Windows service
// handler calls directly, passing a ctx that's canceled by a
// Stop/Shutdown SCM control request (via agent.ServiceHandler) rather
// than a Unix signal. It returns an error (never calling os.Exit or
// printing to stderr itself) so both runAgent and the Windows service
// wrapper can decide how to report/log a failure in a way appropriate
// to their own context (a bare CLI process vs. a service whose stderr
// nobody is watching).
func runAgentWithContext(ctx context.Context, envFile string) error {
	cfg, err := loadConfig(envFile)
	if err != nil {
		return err
	}

	logger, err := config.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}

	src := agent.NewGopsutilSource()
	collector := agent.NewCollector(src, agent.CollectorOptions{
		NetExclude: cfg.NetExclude,
		Logger:     logger,
	})

	var clock *agent.HubClock
	if cfg.TimeSync == "hub" {
		clock = &agent.HubClock{}
		syncCtx, syncCancel := context.WithTimeout(ctx, 20*time.Second)
		clock.SyncInitial(syncCtx, nil, cfg.HubURL, cfg.Token, logger, logClockOffsetWarning(logger))
		syncCancel()
	}

	reporter := agent.NewReporter(agent.ReporterOptions{
		HubURL:       cfg.HubURL,
		Token:        cfg.Token,
		Logger:       logger,
		Clock:        clock,
		LogClockWarn: logClockOffsetWarning(logger),
		Inventory:    agent.NewInventoryCollector(agent.InventoryCollectorOptions{Docker: cfg.Docker, Logger: logger}),
	})

	hostInfoFunc := func(ctx context.Context) models.HostInfo {
		return agent.BuildHostInfo(ctx, src, cfg, logger)
	}

	logger.Info("cloud-pulse-agent starting",
		"host_id", cfg.HostID,
		"hub_url", cfg.HubURL,
		"interval", cfg.Interval,
		"time_sync", cfg.TimeSync,
		"remote_update", cfg.RemoteUpdate,
		"cloud_metadata", cfg.CloudMetadata,
		"version", version.Version,
	)

	runOpts := agent.RunOptions{
		Logger: logger,
		Clock:  clock,
		Jitter: cfg.SendJitter,
	}
	if err := agent.RunWithOptions(ctx, collector, reporter, hostInfoFunc, cfg.Interval, runOpts); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

// logClockOffsetWarning returns a HubClock warning callback that logs at
// warn level via logger. Extracted so both the initial sync and every
// subsequent Reporter.Flush observation log identically.
func logClockOffsetWarning(logger *slog.Logger) func(offset time.Duration) {
	return func(offset time.Duration) {
		logger.Warn("agent clock differs from hub; timestamps are corrected to hub time", "offset", offset)
	}
}

// loadConfig loads agent configuration from the environment, or from
// envFile (a KEY=VALUE file per config.ParseEnvFile) when non-empty.
func loadConfig(envFile string) (config.Agent, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "host"
	}
	if envFile != "" {
		content, err := os.ReadFile(envFile)
		if err != nil {
			return config.Agent{}, fmt.Errorf("cloud-pulse-agent: read env file: %w", err)
		}
		lookup, err := config.LookupFuncFromEnvFile(string(content))
		if err != nil {
			return config.Agent{}, fmt.Errorf("cloud-pulse-agent: parse env file: %w", err)
		}
		return config.LoadAgent(lookup, hostname)
	}
	return config.LoadAgent(os.LookupEnv, hostname)
}

// defaultNetExcludeGlobs returns the network interface exclusion globs
// used by -once, which runs without hub configuration. It mirrors
// config.LoadAgent's default so -once output matches normal-mode
// filtering.
func defaultNetExcludeGlobs() []string {
	return config.DefaultAgentNetExclude()
}

// printJSON writes v to stdout as indented JSON and returns the process
// exit code.
func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: encode json: %v\n", err)
		return 1
	}
	return 0
}
