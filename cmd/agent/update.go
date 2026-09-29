package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/selfupdate"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// updateBinaryName is the release asset base name for this binary, used
// to resolve the correct platform asset and, on Linux, the systemd unit
// this update restarts.
const updateBinaryName = "cloud-pulse-agent"

// updateTimeout bounds the whole `update` subcommand: latest-release
// resolution, download, checksum verification, replace, and restart.
const updateTimeout = 10 * time.Minute

// exitUpdateAvailable is returned by `update --check` when a newer
// release exists, distinguishing "checked, found an update" from a
// plain success (0) or an error (1) for scripting purposes.
const exitUpdateAvailable = 10

// runUpdate implements `cloud-pulse-agent update [--check] [--version
// vX.Y.Z] [--no-restart]`. It is dispatched from main before flag.Parse
// / config loading, so it works even against an unconfigured
// installation (no CP_HUB_URL etc. required).
func runUpdate(args []string) int {
	fs := flag.NewFlagSet("cloud-pulse-agent update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	checkFlag := fs.Bool("check", false, "check for an available update without installing it")
	versionFlag := fs.String("version", "", "install this exact release tag instead of the latest (allows downgrade)")
	noRestartFlag := fs.Bool("no-restart", false, "install the update but do not attempt to restart the service")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent update [--check] [--version vX.Y.Z] [--no-restart]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	result, err := selfupdate.Run(ctx, selfupdate.Options{
		Binary:    updateBinaryName,
		Current:   version.Version,
		Target:    *versionFlag,
		CheckOnly: *checkFlag,
		NoRestart: *noRestartFlag,
		Source:    selfupdate.SourceFromEnv(os.LookupEnv),
		Stdout:    os.Stdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: update: %v\n", err)
		return 1
	}

	fmt.Println(result.Message)

	if *checkFlag && result.UpdateAvailable {
		return exitUpdateAvailable
	}
	return 0
}
