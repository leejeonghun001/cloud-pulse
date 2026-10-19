package agent

import (
	"context"
	"log/slog"
	"net/http"
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
	// Network, when non-nil, overrides how the instance's compartment
	// and VNICs are discovered (OCI instance metadata by default).
	// Tests inject a fixed target.
	Network func(ctx context.Context) OCINetworkTarget
}

// OCICostCollector queries the OCI Usage API from the agent's own host
// on a timer and holds the newest snapshot until a report carries it to
// the hub (see Reporter's CloudCost option). Collection runs on its own
// goroutine (Run) because one CLI call may take up to
// billing.CallTimeout, far longer than a sample interval.
type OCICostCollector struct {
	opts OCICostCollectorOptions

	mu         sync.Mutex
	pending    *models.CloudCostSnapshot
	pendingNet *models.CloudNetworkUsage
	target     OCINetworkTarget
	refresh    chan struct{}
}

// agentRefreshMinGap is the shortest time between two collections when
// the hub asks for an early refresh; the OCI APIs are free, this only
// stops a burst of requests from becoming a burst of CLI runs.
const agentRefreshMinGap = time.Minute

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
	if opts.Network == nil {
		opts.Network = func(ctx context.Context) OCINetworkTarget {
			return ociNetworkTarget(ctx, http.DefaultClient, ociBaseURL(currentCloudMetaOverrides()))
		}
	}
	return &OCICostCollector{opts: opts, refresh: make(chan struct{}, 1)}
}

// Run collects once immediately, then every Interval, until ctx is
// canceled.
func (c *OCICostCollector) Run(ctx context.Context) {
	for {
		started := time.Now()
		c.CollectOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.opts.Interval):
		case <-c.refresh:
			if wait := agentRefreshMinGap - time.Since(started); wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
		}
	}
}

// RequestRefresh asks Run to collect now rather than at the next
// interval (the hub relays a dashboard "Refresh now"). Non-blocking; a
// request while one is already queued is a no-op.
func (c *OCICostCollector) RequestRefresh() {
	if c == nil {
		return
	}
	select {
	case c.refresh <- struct{}{}:
	default:
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

	c.collectNetwork(ctx, home)
}

// collectNetwork queries the instance's VNIC traffic for the month and
// stores it as the pending network usage. The compartment/VNIC lookup
// is cached after its first success.
func (c *OCICostCollector) collectNetwork(ctx context.Context, home string) {
	if len(c.target.VNICIDs) == 0 {
		c.target = c.opts.Network(ctx)
	}
	usage := billing.CollectOCINetwork(ctx, c.opts.Runner, billing.OCINetworkConfig{
		CompartmentID: c.target.CompartmentID,
		VNICIDs:       c.target.VNICIDs,
		Env:           billing.BuildOCIEnv(home, ociCLIPath, "", ""),
		HomeDir:       home,
		Auth:          c.opts.Auth,
		CLI:           c.opts.CLI,
	}, c.opts.Now())
	if ctx.Err() != nil {
		return
	}
	if usage.Status == models.CloudBillingOK {
		c.opts.Logger.InfoContext(ctx, "oci network: collected", "month", usage.Month, "in_bytes", usage.InBytes, "out_bytes", usage.OutBytes)
	} else {
		c.opts.Logger.WarnContext(ctx, "oci network: collection failed", "status", usage.Status, "detail", usage.StatusDetail)
	}
	c.mu.Lock()
	c.pendingNet = &usage
	c.mu.Unlock()
}

// PendingNetwork returns the network usage waiting to be sent, or nil.
func (c *OCICostCollector) PendingNetwork() *models.CloudNetworkUsage {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingNet
}

// MarkNetworkSent clears the pending network usage if it is still u.
func (c *OCICostCollector) MarkNetworkSent(u *models.CloudNetworkUsage) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingNet == u {
		c.pendingNet = nil
	}
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
