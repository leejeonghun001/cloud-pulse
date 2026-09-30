package billing

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Store is the persistence surface Collector needs — a narrow subset of
// hub.Store (kept as its own interface here so internal/billing has no
// dependency on internal/hub, matching this repo's layering convention).
type Store interface {
	GetCloudCostSnapshot(ctx context.Context, provider models.CloudBillingProvider) (models.CloudCostSnapshot, error)
	SetCloudCostSnapshot(ctx context.Context, snap models.CloudCostSnapshot) error
}

// Options configures a Collector.
type Options struct {
	// Runner executes aws/oci CLI calls; ExecRunner{} in production.
	Runner CommandRunner
	// DataDir is the hub's data directory (config.Hub.DataDir); the
	// sandboxed HOME for CLI child processes is created at
	// <DataDir>/cloud-cli.
	DataDir string
	// PATH is the minimal PATH passed to CLI child processes. Defaults
	// to "/usr/bin:/bin:/usr/local/bin" when empty.
	PATH string

	// AWSAccessKeyID/AWSSecretAccessKey/AWSSessionToken/AWSProfile/
	// AWSConfigFile/AWSCredentialsFile are passed through to `aws` (see
	// BuildAWSEnv); all optional.
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSSessionToken    string
	AWSProfile         string
	AWSConfigFile      string
	AWSCredentialsFile string
	// AWSResourcesEnabled enables the per-resource
	// get-cost-and-usage-with-resources call (CP_BILLING_AWS_RESOURCES).
	AWSResourcesEnabled bool

	// OCITenancyID/OCIProfile/OCIConfigFile configure `oci` (see
	// BuildOCIEnv); all optional (an empty TenancyID makes the OCI
	// collector report not_configured without ever invoking the CLI).
	OCITenancyID  string
	OCIProfile    string
	OCIConfigFile string

	// Now returns the current time; defaults to time.Now when nil,
	// overridden by tests for determinism.
	Now func() time.Time
}

// Collector runs the AWS/OCI billing collectors (SPEC-v0.6 §1),
// merging each result against the previously persisted snapshot to
// preserve LastSuccessAt/LastSuccessAt-derived fields on a quiet-skip
// and compute the Stale flag.
type Collector struct {
	opts  Options
	store Store
}

// New constructs a Collector. It does not create any directories or
// touch the filesystem until Run is first called.
func New(store Store, opts Options) *Collector {
	if opts.PATH == "" {
		opts.PATH = "/usr/bin:/bin:/usr/local/bin"
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Collector{opts: opts, store: store}
}

// homeDir returns <DataDir>/cloud-cli, creating it (mode 0700) if it
// doesn't already exist.
func (c *Collector) homeDir() (string, error) {
	dir := filepath.Join(c.opts.DataDir, "cloud-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("billing: create cloud-cli home dir: %w", err)
	}
	return dir, nil
}

// Run collects both providers (AWS, then OCI) and persists the merged
// snapshots via Store, returning the two results. A provider whose
// collection attempt errors at the Store level (rather than the CLI
// level — CLI-level failures are captured as a quiet-skip status, not a
// Go error) is still returned in the slice with whatever snapshot was
// computed; the store error is returned separately so the caller can
// log it without losing the other provider's successfully persisted
// result.
func (c *Collector) Run(ctx context.Context, interval models.BillingInterval) ([]models.CloudCostSnapshot, error) {
	home, err := c.homeDir()
	if err != nil {
		return nil, err
	}

	now := c.opts.Now()
	results := make([]models.CloudCostSnapshot, 0, 2)
	var storeErrs []error

	awsSnap := CollectAWS(ctx, c.opts.Runner, AWSConfig{
		Resources: c.opts.AWSResourcesEnabled,
		Env: BuildAWSEnv(home, c.opts.PATH, c.opts.AWSAccessKeyID, c.opts.AWSSecretAccessKey,
			c.opts.AWSSessionToken, c.opts.AWSProfile, c.opts.AWSConfigFile, c.opts.AWSCredentialsFile),
		HomeDir: home,
	}, now)
	awsSnap, err = c.mergeAndPersist(ctx, awsSnap, now, interval)
	if err != nil {
		storeErrs = append(storeErrs, err)
	}
	results = append(results, awsSnap)

	ociSnap := CollectOCI(ctx, c.opts.Runner, OCIConfig{
		TenancyID: c.opts.OCITenancyID,
		Env:       BuildOCIEnv(home, c.opts.PATH, c.opts.OCIConfigFile, c.opts.OCIProfile),
		HomeDir:   home,
	}, now)
	ociSnap, err = c.mergeAndPersist(ctx, ociSnap, now, interval)
	if err != nil {
		storeErrs = append(storeErrs, err)
	}
	results = append(results, ociSnap)

	if len(storeErrs) > 0 {
		return results, fmt.Errorf("billing: persist snapshot(s): %w", storeErrs[0])
	}
	return results, nil
}

// mergeAndPersist merges fresh's quiet-skip-vs-success outcome against
// the previously stored snapshot for fresh.Provider (preserving
// LastSuccessAt and the last successful MTD/forecast/currency/
// PerResource values on a non-OK status per SPEC-v0.6 §1 개선 a),
// recomputes Stale, persists the result, and returns it.
func (c *Collector) mergeAndPersist(ctx context.Context, fresh models.CloudCostSnapshot, now time.Time, interval models.BillingInterval) (models.CloudCostSnapshot, error) {
	prev, err := c.store.GetCloudCostSnapshot(ctx, fresh.Provider)
	if err != nil {
		return fresh, fmt.Errorf("billing: load previous snapshot: %w", err)
	}

	merged := mergeSnapshot(prev, fresh, now.Unix())
	merged.Stale = models.CloudCostStale(merged.LastSuccessAt, now.Unix(), interval)

	if err := c.store.SetCloudCostSnapshot(ctx, merged); err != nil {
		return merged, fmt.Errorf("billing: save snapshot: %w", err)
	}
	return merged, nil
}

// mergeSnapshot implements SPEC-v0.6 §1 개선 a's freshness-preservation
// rule as a pure function (exported indirectly via mergeAndPersist, kept
// separate for direct unit testing): fresh's Status/StatusDetail always
// win (they describe *this* attempt), LastAttemptAt is always now, but
// on anything other than CloudBillingOK the previous MTD/Forecast/
// Currency/PerResource/CollectedAt/LastSuccessAt values are carried
// forward untouched rather than being zeroed out.
func mergeSnapshot(prev, fresh models.CloudCostSnapshot, nowUnix int64) models.CloudCostSnapshot {
	out := fresh
	out.LastAttemptAt = nowUnix

	if fresh.Status == models.CloudBillingOK {
		out.LastSuccessAt = nowUnix
		out.CollectedAt = nowUnix
		return out
	}

	// Quiet skip: preserve the last successful figures, if any.
	out.MTDCost = prev.MTDCost
	out.ForecastCost = prev.ForecastCost
	out.ForecastMethod = prev.ForecastMethod
	out.Currency = prev.Currency
	out.AccountLevel = prev.AccountLevel
	out.PerResource = prev.PerResource
	out.CollectedAt = prev.CollectedAt
	out.LastSuccessAt = prev.LastSuccessAt
	return out
}
