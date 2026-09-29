package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/config"
	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

// resetNetworkTimeout bounds the whole reset-network CLI run (DB open,
// delete setting, chown), so a stuck/locked database can't hang the
// process forever.
const resetNetworkTimeout = 30 * time.Second

// runResetNetwork implements `cloud-pulse-hub reset-network
// [--data-dir DIR]`: deletes the persisted "network_config" setting so
// the hub falls back to CP_LISTEN/CP_ALLOWED_CIDRS on next restart — the
// recovery path for an admin who locked themselves out of the dashboard
// via the Network settings page. See SPEC-v0.4 §2.
func runResetNetwork(args []string) int {
	fs := flag.NewFlagSet("reset-network", flag.ContinueOnError)
	dataDirFlag := fs.String("data-dir", "", "override CP_DATA_DIR / default data directory resolution")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-hub reset-network [--data-dir DIR]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dataDir := resolveDataDir(*dataDirFlag, os.LookupEnv, defaultSystemDataDirExists)
	dbPath := filepath.Join(dataDir, "cloud-pulse.db")

	if err := resetNetwork(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub reset-network: %v\n", err)
		return 1
	}

	if err := chownToDataDirOwner(dbPath, dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-hub reset-network: warning: %v\n", err)
		// Non-fatal: the setting was already cleared successfully.
	}

	printResetNetworkSummary(os.Stdout)
	return 0
}

// resetNetwork opens the SQLite database at dbPath (applying
// migrations as needed, working correctly even while the hub is
// running thanks to WAL + busy_timeout) and deletes the persisted
// "network_config" setting.
//
// This is a small, directly unit-testable helper: tests call it against
// a temp-dir database without going through the CLI's flag parsing.
func resetNetwork(dbPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), resetNetworkTimeout)
	defer cancel()

	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }() // best-effort close; the operation above already completed or failed

	if err := db.SetSetting(ctx, hub.SettingNetworkConfig, ""); err != nil {
		return fmt.Errorf("clear network config setting: %w", err)
	}
	return nil
}

// printResetNetworkSummary prints what the hub will use for its listen
// address/allowlist after the next restart, reading CP_LISTEN/
// CP_ALLOWED_CIDRS from the current environment (the same env the
// systemd unit/shell the operator is running this in will use to
// restart the hub).
func printResetNetworkSummary(w *os.File) {
	cfg, err := config.LoadHub(os.LookupEnv)
	if err != nil {
		// Best-effort: this is a CLI summary printed just before a
		// successful exit; a write failure here (e.g. closed pipe) has
		// no recovery action and isn't worth aborting for.
		_, _ = fmt.Fprintln(w, "cloud-pulse-hub: network configuration override cleared.")
		_, _ = fmt.Fprintln(w, "restart the hub to apply; it will fall back to CP_LISTEN/CP_ALLOWED_CIDRS from its environment.")
		return
	}
	_, _ = fmt.Fprintln(w, "cloud-pulse-hub: network configuration override cleared.")
	_, _ = fmt.Fprintf(w, "after restart, the hub will listen on %q with allowlist %s\n", cfg.Listen, formatCIDRs(cfg.AllowedCIDRs))
}
