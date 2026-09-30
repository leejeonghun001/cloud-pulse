package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/config"
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

// defaultRemoteUpdateResultDir is the root-owned result directory
// `update --from-request` writes to when --result-dir is not given
// (SPEC-v0.6 §2).
const defaultRemoteUpdateResultDir = "/var/lib/cloud-pulse-agent-update"

// exitUpdateAvailable is returned by `update --check` when a newer
// release exists, distinguishing "checked, found an update" from a
// plain success (0) or an error (1) for scripting purposes.
const exitUpdateAvailable = 10

// runUpdate implements `cloud-pulse-agent update [--check] [--version
// vX.Y.Z] [--no-restart] [--from-request FILE]`. It is dispatched from
// main before flag.Parse / config loading, so it works even against an
// unconfigured installation (no CP_HUB_URL etc. required).
func runUpdate(args []string) int {
	fs := flag.NewFlagSet("cloud-pulse-agent update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	checkFlag := fs.Bool("check", false, "check for an available update without installing it")
	versionFlag := fs.String("version", "", "install this exact release tag instead of the latest (allows downgrade)")
	noRestartFlag := fs.Bool("no-restart", false, "install the update but do not attempt to restart the service")
	fromRequestFlag := fs.String("from-request", "", "read a hub-delivered update request file (SPEC-v0.6 §2) and apply it; incompatible with --check/--version/--no-restart")
	resultDirFlag := fs.String("result-dir", defaultRemoteUpdateResultDir, "root-owned directory --from-request writes its result.json into")
	envFileFlag := fs.String("env-file", "", "read CP_UPDATE_LATEST_URL/CP_RELEASE_BASE_URL from this KEY=VALUE file")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cloud-pulse-agent update [--check] [--version vX.Y.Z] [--no-restart]")
		fmt.Fprintln(os.Stderr, "       cloud-pulse-agent update --from-request FILE [--result-dir DIR]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *fromRequestFlag != "" {
		if *checkFlag || *versionFlag != "" || *noRestartFlag {
			fmt.Fprintln(os.Stderr, "cloud-pulse-agent: update: --from-request cannot be combined with --check/--version/--no-restart")
			return 1
		}
		return runUpdateFromRequest(*fromRequestFlag, *resultDirFlag, *envFileFlag)
	}

	source, err := updateSourceFromEnvFile(*envFileFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: update: %v\n", err)
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
		Source:    source,
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

// runUpdateFromRequest implements `update --from-request FILE
// [--result-dir DIR]` (SPEC-v0.6 §2 step 4): reads and validates
// requestPath, runs the normal selfupdate pipeline against
// this binary's own environment-sourced release feed, and always
// writes an outcome to resultDir/result.json — a failure of the update
// itself is reported through that file, not this process's exit code
// (see selfupdate.RunFromRequest's doc comment), so the systemd oneshot
// service that invokes this is expected to treat any exit code as
// "ran," not "succeeded."
func runUpdateFromRequest(requestPath, resultDir, envFile string) int {
	source, err := updateSourceFromEnvFile(envFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: update --from-request: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	result, err := selfupdate.RunFromRequest(ctx, selfupdate.FromRequestOptions{
		RequestPath: requestPath,
		ResultDir:   resultDir,
		Binary:      updateBinaryName,
		Current:     version.Version,
		Source:      source,
		Stdout:      os.Stdout,
	})
	if err != nil {
		// Only reachable for a malformed/unreadable request file or a
		// failure to write the result file at all — the update
		// attempt's own success/failure is otherwise always captured
		// in the result file per RunFromRequest's contract.
		fmt.Fprintf(os.Stderr, "cloud-pulse-agent: update --from-request: %v\n", err)
		return 1
	}

	fmt.Println(result.Message)
	return 0
}

// updateSourceFromEnvFile returns the self-update feed selected by the
// process environment or, for launchd/Windows helper invocations, the
// root-owned agent.env file. It deliberately reads only source variables;
// update requests never carry a URL or checksum source.
func updateSourceFromEnvFile(envFile string) (selfupdate.Source, error) {
	if envFile == "" {
		return selfupdate.SourceFromEnv(os.LookupEnv), nil
	}
	content, err := os.ReadFile(envFile)
	if err != nil {
		return selfupdate.Source{}, fmt.Errorf("read env file: %w", err)
	}
	lookup, err := config.LookupFuncFromEnvFile(string(content))
	if err != nil {
		return selfupdate.Source{}, fmt.Errorf("parse env file: %w", err)
	}
	return selfupdate.SourceFromEnv(lookup), nil
}
