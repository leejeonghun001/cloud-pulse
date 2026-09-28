// Command cloud-pulse-hub runs the cloud-pulse hub: an HTTP API that
// ingests agent reports, serves host/egress/bucket read models and the
// embedded dashboard, and runs background maintenance. It is wiring
// only: configuration loading, adapter construction, and signal
// handling live here; business logic lives in internal/hub,
// internal/storage, and internal/cloud.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/cloud"
	"github.com/leejeonghun001/cloud-pulse/internal/config"
	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
	"github.com/leejeonghun001/cloud-pulse/web"
)

// Compile-time assertions that the concrete adapters satisfy the
// interfaces internal/hub depends on. These catch method-set drift
// between packages at build time rather than at wiring runtime.
var (
	_ hub.Store           = (*storage.DB)(nil)
	_ hub.BucketCollector = (*cloud.S3Collector)(nil)
	_ hub.BucketCollector = (*cloud.R2Collector)(nil)
	_ hub.Notifier        = hub.WebhookNotifier{}
)

// httpClientTimeout bounds every HTTP call made by cloud collectors.
const httpClientTimeout = 30 * time.Second

// shutdownTimeout bounds how long graceful shutdown waits for in-flight
// requests to finish.
const shutdownTimeout = 10 * time.Second

func main() {
	os.Exit(run())
}

// run contains all logic previously inlined in main, returning a process
// exit code so main can stay a single os.Exit call site.
func run() int {
	versionFlag := flag.Bool("version", false, "print version information and exit")
	genTokenFlag := flag.Bool("gen-token", false, "print a random 32-byte hex token and exit")
	checkConfigFlag := flag.Bool("check-config", false, "load and validate configuration, print a redacted summary, and exit")
	listenFlag := flag.String("listen", "", "override CP_LISTEN (address to listen on)")
	dataDirFlag := flag.String("data-dir", "", "override CP_DATA_DIR (directory for the SQLite database)")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.String())
		return 0
	}

	if *genTokenFlag {
		return runGenToken()
	}

	cfg, err := loadConfig(*listenFlag, *dataDirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub: %v\n", err)
		return 1
	}

	if *checkConfigFlag {
		printConfigSummary(os.Stdout, cfg)
		return 0
	}

	return runHub(cfg)
}

// runGenToken prints 32 random bytes hex-encoded (64 hex characters),
// suitable for use as CP_AGENT_TOKEN or CP_UI_TOKEN.
func runGenToken() int {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub: generate token: %v\n", err)
		return 1
	}
	fmt.Println(hex.EncodeToString(buf))
	return 0
}

// loadConfig loads hub configuration from the environment, applying
// -listen/-data-dir flag overrides after env resolution.
func loadConfig(listen, dataDir string) (config.Hub, error) {
	cfg, err := config.LoadHub(os.LookupEnv)
	if err != nil {
		return config.Hub{}, err
	}
	if listen != "" {
		cfg.Listen = listen
	}
	if dataDir != "" {
		cfg.DataDir = dataDir
	}
	return cfg, nil
}

// printConfigSummary prints a human-readable, secret-redacted summary of
// cfg to w.
func printConfigSummary(w *os.File, cfg config.Hub) {
	lines := []string{
		"cloud-pulse-hub configuration (secrets redacted):",
		fmt.Sprintf("  listen:            %s", cfg.Listen),
		fmt.Sprintf("  data_dir:          %s", cfg.DataDir),
		fmt.Sprintf("  db_path:           %s", cfg.DBPath()),
		fmt.Sprintf("  agent_token:       %s", redactPresence(cfg.AgentToken)),
		fmt.Sprintf("  ui_token:          %s", redactPresence(cfg.UIToken)),
		fmt.Sprintf("  allowed_cidrs:     %s", formatCIDRs(cfg.AllowedCIDRs)),
		fmt.Sprintf("  offline_after:     %s", cfg.OfflineAfter),
		fmt.Sprintf("  cloud_interval:    %s", cfg.CloudInterval),
		fmt.Sprintf("  alert_webhook_url: %s", redactPresence(cfg.AlertWebhookURL)),
		fmt.Sprintf("  log_level:         %s", cfg.LogLevel),
		fmt.Sprintf("  log_format:        %s", cfg.LogFormat),
		fmt.Sprintf("  s3_enabled:        %t (%d bucket(s))", cfg.S3Enabled(), len(cfg.S3Buckets)),
		fmt.Sprintf("  r2_enabled:        %t (%d bucket(s) configured, empty = all)", cfg.R2Enabled(), len(cfg.R2Buckets)),
	}
	for _, line := range lines {
		// Best-effort: -check-config writes to stdout for human
		// inspection; a write failure here (e.g. closed pipe) has no
		// recovery action and isn't worth aborting the process for.
		_, _ = fmt.Fprintln(w, line)
	}
}

// redactPresence reports only whether a secret-shaped value is set,
// never its content.
func redactPresence(v string) string {
	if v == "" {
		return "(not set)"
	}
	return "(set)"
}

// formatCIDRs renders the allowlist for display; a nil slice means allow
// all.
func formatCIDRs(cidrs []netip.Prefix) string {
	if cidrs == nil {
		return "* (allow all)"
	}
	parts := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		parts = append(parts, c.String())
	}
	return strings.Join(parts, ",")
}

// runHub wires and runs the hub server until it receives SIGINT/SIGTERM,
// then shuts down gracefully.
func runHub(cfg config.Hub) int {
	logger, err := config.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.DBPath())
	if err != nil {
		logger.Error("open storage failed", "error", err)
		return 1
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("close storage failed", "error", err)
		}
	}()

	collectors := buildCollectors(cfg)

	var notifier hub.Notifier
	if cfg.AlertWebhookURL != "" {
		notifier = hub.WebhookNotifier{
			URL:    cfg.AlertWebhookURL,
			Client: &http.Client{Timeout: httpClientTimeout},
		}
	}

	srv := hub.New(hub.Options{
		AgentToken:    cfg.AgentToken,
		UIToken:       cfg.UIToken,
		AllowedCIDRs:  cfg.AllowedCIDRs,
		OfflineAfter:  cfg.OfflineAfter,
		CloudInterval: cfg.CloudInterval,
	}, store, collectors, notifier, web.Assets(), logger)

	logStartupSummary(logger, cfg, collectors)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	bgCtx, stopBg := context.WithCancel(context.Background())
	bgDone := make(chan struct{})
	go func() {
		defer close(bgDone)
		srv.RunBackground(bgCtx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			logger.Error("listen failed", "error", err)
			stopBg()
			<-bgDone
			return 1
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}

	stopBg()
	<-bgDone
	srv.Wait()

	logger.Info("shutdown complete")
	return 0
}

// buildCollectors constructs the enabled cloud bucket collectors from
// cfg. A collector is included only when its required credentials and
// bucket list are configured.
func buildCollectors(cfg config.Hub) []hub.BucketCollector {
	client := &http.Client{Timeout: httpClientTimeout}

	var collectors []hub.BucketCollector

	if cfg.S3Enabled() {
		buckets := make([]cloud.S3Bucket, 0, len(cfg.S3Buckets))
		for _, b := range cfg.S3Buckets {
			buckets = append(buckets, cloud.S3Bucket{Name: b.Name, Region: b.Region})
		}
		collectors = append(collectors, &cloud.S3Collector{
			Buckets:  buckets,
			FilterID: cfg.S3FilterID,
			Creds: cloud.Credentials{
				AccessKeyID:     cfg.AWSAccessKeyID,
				SecretAccessKey: cfg.AWSSecretAccessKey,
				SessionToken:    cfg.AWSSessionToken,
			},
			Client: client,
			Window: cfg.CloudInterval,
		})
	}

	if cfg.R2Enabled() {
		collectors = append(collectors, &cloud.R2Collector{
			AccountID: cfg.R2AccountID,
			APIToken:  cfg.R2APIToken,
			Buckets:   cfg.R2Buckets,
			Client:    client,
			Window:    cfg.CloudInterval,
		})
	}

	return collectors
}

// logStartupSummary logs a one-time startup summary of the hub's
// configuration. It never logs token values, and warns loudly if the
// hub is reachable from any address (nil AllowedCIDRs) with no UI
// authentication configured.
func logStartupSummary(logger *slog.Logger, cfg config.Hub, collectors []hub.BucketCollector) {
	names := make([]string, 0, len(collectors))
	for _, c := range collectors {
		names = append(names, c.Name())
	}

	logger.Info("cloud-pulse-hub starting",
		"version", version.Version,
		"listen", cfg.Listen,
		"db_path", cfg.DBPath(),
		"allowlist_entries", len(cfg.AllowedCIDRs),
		"allow_all", cfg.AllowedCIDRs == nil,
		"ui_auth", cfg.UIToken != "",
		"collectors", names,
	)

	if cfg.AllowedCIDRs == nil && cfg.UIToken == "" {
		logger.Warn("SECURITY: CP_ALLOWED_CIDRS allows all addresses (unset or '*') AND CP_UI_TOKEN is empty; " +
			"the hub's read API is unauthenticated and reachable from anywhere it is exposed on the network")
	}
}
