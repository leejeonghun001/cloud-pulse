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

// CloudCostCollectorOptions configures NewCloudCostCollector.
type CloudCostCollectorOptions struct {
	// Provider is the cloud whose account this agent's own role queries:
	// models.CloudBillingOCI (instance principal, CP_OCI_BILLING) or
	// models.CloudBillingAWS (EC2 instance role, CP_AWS_BILLING).
	Provider models.CloudBillingProvider
	// TenancyID is the tenancy OCID to query (CP_OCI_TENANCY_ID); OCI
	// only, and also the account ID reported for it.
	TenancyID string
	// Auth is the `oci --auth` mode, e.g. "instance_principal"; OCI only.
	Auth string
	// CLI is the absolute `oci`/`aws` path; empty looks it up on
	// ociCLIPath.
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

// CloudCostCollector queries its own cloud account's cost (OCI Usage
// API, or AWS Cost Explorer) from the agent's host with the instance's
// role, on a timer, and holds the newest snapshot until a report
// carries it to the hub (see Reporter's CloudCost option). Collection runs on its own
// goroutine (Run) because one CLI call may take up to
// billing.CallTimeout, far longer than a sample interval.
type CloudCostCollector struct {
	opts CloudCostCollectorOptions

	mu         sync.Mutex
	pending    *models.CloudCostSnapshot
	pendingNet *models.CloudNetworkUsage
	target     OCINetworkTarget
	awsAccount string
	refresh    chan struct{}
}

// agentRefreshMinGap is the shortest time between two collections when
// the hub asks for an early refresh. The OCI APIs are free, so this only
// stops a burst of requests becoming a burst of CLI runs; AWS Cost
// Explorer bills each call, so its gap is longer.
const (
	agentRefreshMinGap    = time.Minute
	awsAgentRefreshMinGap = 10 * time.Minute
)

// NewCloudCostCollector returns a collector for opts, filling defaults.
func NewCloudCostCollector(opts CloudCostCollectorOptions) *CloudCostCollector {
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
	return &CloudCostCollector{opts: opts, refresh: make(chan struct{}, 1)}
}

// Run collects once immediately, then every Interval, until ctx is
// canceled.
func (c *CloudCostCollector) Run(ctx context.Context) {
	for {
		started := time.Now()
		c.CollectOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.opts.Interval):
		case <-c.refresh:
			gap := agentRefreshMinGap
			if c.opts.Provider == models.CloudBillingAWS {
				gap = awsAgentRefreshMinGap
			}
			if wait := gap - time.Since(started); wait > 0 {
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
func (c *CloudCostCollector) RequestRefresh() {
	if c == nil {
		return
	}
	select {
	case c.refresh <- struct{}{}:
	default:
	}
}

// CollectOnce runs one cost query and stores the result as the pending
// snapshot, replacing any older one the hub hasn't accepted yet.
func (c *CloudCostCollector) CollectOnce(ctx context.Context) {
	home, err := ociHomeDir()
	if err != nil {
		c.opts.Logger.WarnContext(ctx, "cloud billing: create CLI home dir failed", "error", err)
		return
	}
	if c.opts.Provider == models.CloudBillingAWS {
		c.collectAWS(ctx, home)
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
	snap.AccountID = c.opts.TenancyID
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

// collectAWS queries AWS Cost Explorer with the EC2 instance's IAM role
// (the CLI finds its credentials through instance metadata). The
// account ID is looked up once and cached.
func (c *CloudCostCollector) collectAWS(ctx context.Context, home string) {
	cfg := billing.AWSConfig{
		// Cost Explorer is served from us-east-1 whatever the instance's
		// region; the CLI needs a region to sign with.
		Env:     append(billing.BuildAWSEnv(home, ociCLIPath, "", "", "", "", "", ""), "AWS_DEFAULT_REGION=us-east-1"),
		HomeDir: home,
		CLI:     c.opts.CLI,
	}
	var snap models.CloudCostSnapshot
	if c.awsAccount == "" {
		account, failed := billing.AWSAccountID(ctx, c.opts.Runner, cfg)
		if failed != nil {
			snap = *failed
		} else {
			c.awsAccount = account
		}
	}
	if c.awsAccount != "" {
		snap = billing.CollectAWS(ctx, c.opts.Runner, cfg, c.opts.Now())
		snap.AccountID = c.awsAccount
	}
	if ctx.Err() != nil {
		return
	}
	if snap.Status == models.CloudBillingOK {
		c.opts.Logger.InfoContext(ctx, "aws billing: collected", "account", snap.AccountID, "currency", snap.Currency, "mtd_cost", snap.MTDCost)
	} else {
		c.opts.Logger.WarnContext(ctx, "aws billing: collection failed", "status", snap.Status, "detail", snap.StatusDetail)
	}
	// A failure before the account is known is still sent: the hub files
	// it under this host until the account ID arrives, so the dashboard
	// can say what is wrong.
	c.mu.Lock()
	c.pending = &snap
	c.mu.Unlock()
}

// collectNetwork queries the instance's VNIC traffic for the month and
// stores it as the pending network usage. The compartment/VNIC lookup
// is cached after its first success.
func (c *CloudCostCollector) collectNetwork(ctx context.Context, home string) {
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
func (c *CloudCostCollector) PendingNetwork() *models.CloudNetworkUsage {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingNet
}

// MarkNetworkSent clears the pending network usage if it is still u.
func (c *CloudCostCollector) MarkNetworkSent(u *models.CloudNetworkUsage) {
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
func (c *CloudCostCollector) Pending() *models.CloudCostSnapshot {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending
}

// MarkSent clears the pending snapshot if it is still snap, i.e. no
// newer collection replaced it while the report was in flight.
func (c *CloudCostCollector) MarkSent(snap *models.CloudCostSnapshot) {
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
