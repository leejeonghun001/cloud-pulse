// Command cloud-pulse-agent collects host resource metrics and reports
// them to a cloud-pulse hub. It is wiring only: configuration loading,
// logger setup, signal handling, and delegating to internal/agent.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
	os.Exit(run())
}

// run contains all logic previously inlined in main, returning a process
// exit code so main can stay a single os.Exit call site.
func run() int {
	versionFlag := flag.Bool("version", false, "print version information and exit")
	onceFlag := flag.Bool("once", false, "collect two samples one second apart, print the second as JSON, and exit")
	printHostFlag := flag.Bool("print-host", false, "print detected host info as JSON and exit")
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

	return runAgent()
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
	cfg, err := loadConfig()
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
func runAgent() int {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: %v\n", err)
		return 1
	}

	logger, err := config.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	src := agent.NewGopsutilSource()
	collector := agent.NewCollector(src, agent.CollectorOptions{
		NetExclude: cfg.NetExclude,
		Logger:     logger,
	})
	reporter := agent.NewReporter(agent.ReporterOptions{
		HubURL: cfg.HubURL,
		Token:  cfg.Token,
		Logger: logger,
	})

	hostInfoFunc := func(ctx context.Context) models.HostInfo {
		return agent.BuildHostInfo(ctx, src, cfg, logger)
	}

	logger.Info("cloud-pulse-agent starting",
		"host_id", cfg.HostID,
		"hub_url", cfg.HubURL,
		"interval", cfg.Interval,
		"version", version.Version,
	)

	if err := agent.Run(ctx, collector, reporter, hostInfoFunc, cfg.Interval, logger); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: run: %v\n", err)
		return 1
	}
	return 0
}

// loadConfig loads agent configuration from the environment.
func loadConfig() (config.Agent, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "host"
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
