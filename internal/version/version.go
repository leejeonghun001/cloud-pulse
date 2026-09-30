// Package version holds build-time metadata about the cloud-pulse binaries.
//
// Version, Commit, and Date are intended to be set at build time via
// -ldflags, e.g.:
//
//	go build -ldflags "-X github.com/leejeonghun001/cloud-pulse/internal/version.Version=v1.2.3 \
//	  -X github.com/leejeonghun001/cloud-pulse/internal/version.Commit=$(git rev-parse HEAD) \
//	  -X github.com/leejeonghun001/cloud-pulse/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// When unset, they default to development placeholder values.
package version

// Version is the semantic version of the build (e.g. "v1.2.3").
// Defaults to "dev" when not set via -ldflags.
var Version = "dev"

// Commit is the git commit SHA the binary was built from.
// Defaults to "none" when not set via -ldflags.
var Commit = "none"

// Date is the UTC build timestamp, RFC3339 formatted.
// Defaults to "unknown" when not set via -ldflags.
var Date = "unknown"

// String returns a human-readable representation of the build metadata,
// e.g. "dev (none, unknown)" or "v1.2.3 (abc1234, 2026-09-29T00:00:00Z)".
func String() string {
	return Format(Version, Commit, Date)
}

// Format returns a human-readable representation of explicit build metadata.
func Format(version, commit, date string) string {
	return version + " (" + commit + ", " + date + ")"
}
