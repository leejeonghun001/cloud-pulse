package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// ociCLIPath is the PATH every agent-side `oci` invocation runs with,
// matching the hub collector's minimal default.
const ociCLIPath = "/usr/local/bin:/usr/bin:/bin"

// OCICostCollectorOptions configures NewOCICostCollector.
type OCICostCollectorOptions struct {
	// TenancyID is the tenancy OCID to query (CP_OCI_TENANCY_ID).
	TenancyID string
	// Auth is the `oci --auth` mode, e.g. "instance_principal".
	Auth string
	// CLI is the absolute `oci` path; empty looks it up on ociCLIPath.
	CLI string
	// Interval is the time between two collections.
	Interval time.Duration
	// Logger receives one line per collection outcome. nil discards.
	Logger *slog.Logger
	// Runner runs the CLI; nil uses billing.ExecRunner. Tests inject a
	// fake.
	Runner billing.CommandRunner
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
}

// OCICostCollector queries the OCI Usage API from the agent's own host
// on a timer and holds the newest snapshot until a report carries it to
// the hub (see Reporter's CloudCost option). Collection runs on its own
// goroutine (Run) because one CLI call may take up to
// billing.CallTimeout, far longer than a sample interval.
type OCICostCollector struct {
	opts OCICostCollectorOptions

	mu      sync.Mutex
	pending *models.CloudCostSnapshot
}

// NewOCICostCollector returns a collector for opts, filling defaults.
func NewOCICostCollector(opts OCICostCollectorOptions) *OCICostCollector {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Runner == nil {
		opts.Runner = billing.ExecRunner{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &OCICostCollector{opts: opts}
}

// Run collects once immediately, then every Interval, until ctx is
// canceled.
func (c *OCICostCollector) Run(ctx context.Context) {
	for {
		c.CollectOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.opts.Interval):
		}
	}
}

// CollectOnce runs one Usage API query and stores the result as the
// pending snapshot, replacing any older one the hub hasn't accepted yet.
func (c *OCICostCollector) CollectOnce(ctx context.Context) {
	home, err := ociHomeDir()
	if err != nil {
		c.opts.Logger.WarnContext(ctx, "oci billing: create CLI home dir failed", "error", err)
		return
	}
	snap := billing.CollectOCI(ctx, c.opts.Runner, billing.OCIConfig{
		TenancyID: c.opts.TenancyID,
		Env:       billing.BuildOCIEnv(home, ociCLIPath, "", ""),
		HomeDir:   home,
		Auth:      c.opts.Auth,
		CLI:       c.opts.CLI,
	}, c.opts.Now())
	if ctx.Err() != nil {
		return
	}
	if snap.Status == models.CloudBillingOK {
		c.opts.Logger.InfoContext(ctx, "oci billing: collected", "currency", snap.Currency, "mtd_cost", snap.MTDCost)
	} else {
		c.opts.Logger.WarnContext(ctx, "oci billing: collection failed", "status", snap.Status, "detail", snap.StatusDetail)
	}

	c.mu.Lock()
	c.pending = &snap
	c.mu.Unlock()
}

// Pending returns the snapshot waiting to be sent, or nil.
func (c *OCICostCollector) Pending() *models.CloudCostSnapshot {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending
}

// MarkSent clears the pending snapshot if it is still snap, i.e. no
// newer collection replaced it while the report was in flight.
func (c *OCICostCollector) MarkSent(snap *models.CloudCostSnapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == snap {
		c.pending = nil
	}
}

// ociHomeDir returns the HOME the CLI runs with: a private directory
// under the temp dir, the one place the hardened systemd unit
// (ProtectSystem=strict, ProtectHome=read-only, PrivateTmp=yes) lets the
// agent write.
func ociHomeDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "cloud-pulse-oci")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
