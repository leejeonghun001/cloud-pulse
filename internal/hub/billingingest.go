package hub

import (
	"context"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Bounds for an agent-reported cost snapshot. The agent's own collector
// never comes near them; they only stop a misbehaving agent from
// writing an oversized or nonsensical row.
const (
	maxAgentCostResources  = 10000
	maxAgentCostResourceID = 255
	maxAgentCostDetail     = 200
	maxAgentCostCurrency   = 8
	maxAgentCostAmount     = 1e12
)

// ingestAgentCloudCost persists a provider cost snapshot an agent
// collected itself (CP_OCI_BILLING=instance_principal), merged the same
// way as the hub's own collector output. Like inventory, it is
// supplementary to the report: an invalid snapshot is dropped and a
// storage failure is logged, never failing metrics ingestion. Ignored
// when billing is disabled on the hub (CP_BILLING=off).
func (s *Server) ingestAgentCloudCost(ctx context.Context, hostID string, snap models.CloudCostSnapshot, now time.Time) {
	if s.opts.Billing == nil {
		return
	}
	clean, ok := sanitizeAgentCloudCost(snap)
	if !ok {
		s.logger.Warn("ingest: dropping invalid agent cloud cost snapshot", "host_id", hostID, "provider", snap.Provider)
		return
	}
	clean.SourceHost = hostID
	if clean.AccountID == "" {
		// The agent doesn't know (or, if older, doesn't say) its account;
		// keep it apart from every other account until it does.
		clean.AccountID = "host:" + hostID
	}
	// Not serialized with Billing.runMu: a hub CLI run can hold it for
	// minutes, and this runs inside the agent's report request. The
	// upsert is atomic; with the hub's own OCI collection configured too
	// (unsupported alongside agent-side collection) the last write wins.
	interval := s.resolveBillingInterval(ctx)
	if _, err := billing.PersistSnapshot(ctx, s.store, clean, now, interval); err != nil {
		s.logger.Error("ingest: persist agent cloud cost failed", "host_id", hostID, "error", err)
		return
	}
	s.dropOtherAgentAccounts(ctx, hostID, clean)
	s.evaluateCostAlerts(ctx, now)
}

// dropOtherAgentAccounts removes this host's earlier rows for the same
// provider under another account — its pre-upgrade "host:" placeholder,
// or an account it no longer reports after its config changed. An
// account other agents also report is left to them.
func (s *Server) dropOtherAgentAccounts(ctx context.Context, hostID string, current models.CloudCostSnapshot) {
	if strings.HasPrefix(current.AccountID, "host:") {
		// A placeholder (account not known this time, e.g. a failed
		// lookup after a restart) must never displace a real account.
		return
	}
	all, err := s.store.ListCloudCostSnapshots(ctx)
	if err != nil {
		s.logger.Error("ingest: list cost accounts failed", "error", err)
		return
	}
	for _, snap := range all {
		if snap.SourceHost != hostID || snap.Provider != current.Provider || snap.AccountID == current.AccountID || snap.AccountID == "" {
			continue
		}
		if err := s.store.DeleteCloudCostSnapshot(ctx, snap.Provider, snap.AccountID); err != nil {
			s.logger.Error("ingest: drop stale cost account failed", "account", snap.Key(), "error", err)
		}
	}
}

// sanitizeAgentCloudCost validates an agent-reported snapshot and
// returns only the fields an agent may set; timestamps, staleness,
// source and label are the hub's to assign. OCI and AWS — the providers
// with agent-side collection — are accepted.
func sanitizeAgentCloudCost(snap models.CloudCostSnapshot) (models.CloudCostSnapshot, bool) {
	if !validAgentAccount(snap.Provider, snap.AccountID) {
		return models.CloudCostSnapshot{}, false
	}
	if !validCloudStatus(snap.Status) {
		return models.CloudCostSnapshot{}, false
	}
	switch snap.ForecastMethod {
	case "", "api", "linear":
	default:
		return models.CloudCostSnapshot{}, false
	}
	if !validAmount(snap.MTDCost) || !validAmount(snap.ForecastCost) {
		return models.CloudCostSnapshot{}, false
	}
	if len(snap.Currency) > maxAgentCostCurrency || len(snap.PerResource) > maxAgentCostResources {
		return models.CloudCostSnapshot{}, false
	}
	for id, cost := range snap.PerResource {
		if id == "" || len(id) > maxAgentCostResourceID || !utf8.ValidString(id) || !validAmount(cost) {
			return models.CloudCostSnapshot{}, false
		}
	}
	detail := truncateUTF8(snap.StatusDetail, maxAgentCostDetail)
	return models.CloudCostSnapshot{
		Provider:       snap.Provider,
		AccountID:      snap.AccountID,
		Status:         snap.Status,
		StatusDetail:   detail,
		Currency:       snap.Currency,
		MTDCost:        snap.MTDCost,
		ForecastCost:   snap.ForecastCost,
		ForecastMethod: snap.ForecastMethod,
		AccountLevel:   snap.AccountLevel,
		PerResource:    snap.PerResource,
	}, true
}

// validAmount reports whether v is a finite cost within a sane range.
// Negative values are allowed: credits and refunds can make a
// month-to-date total negative.
func validAmount(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= maxAgentCostAmount
}

// maxAgentNetworkBytes bounds a reported monthly byte count (~1 EiB).
const maxAgentNetworkBytes = 1 << 60

var monthPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// ingestAgentCloudNetwork stores a host's provider-measured monthly
// traffic (OCI VNIC metrics) reported by its own agent. A failed attempt
// keeps the last successful month and byte counts, like cost snapshots.
// Supplementary to the report: invalid input is dropped, storage
// errors are logged.
func (s *Server) ingestAgentCloudNetwork(ctx context.Context, hostID string, u models.CloudNetworkUsage, now time.Time) {
	if !validAgentCloudNetwork(u) {
		s.logger.Warn("ingest: dropping invalid agent cloud network usage", "host_id", hostID)
		return
	}
	clean := models.CloudNetworkUsage{
		Provider:      u.Provider,
		Month:         u.Month,
		InBytes:       u.InBytes,
		OutBytes:      u.OutBytes,
		Status:        u.Status,
		StatusDetail:  truncateUTF8(u.StatusDetail, maxAgentCostDetail),
		LastAttemptAt: now.Unix(),
	}
	if clean.Status == models.CloudBillingOK {
		clean.LastSuccessAt = now.Unix()
	} else if prev, err := s.store.GetHostCloudNetwork(ctx, hostID); err == nil {
		clean.Month, clean.InBytes, clean.OutBytes, clean.LastSuccessAt = prev.Month, prev.InBytes, prev.OutBytes, prev.LastSuccessAt
	} else {
		clean.Month, clean.InBytes, clean.OutBytes = "", 0, 0
	}
	if err := s.store.SetHostCloudNetwork(ctx, hostID, clean); err != nil {
		s.logger.Error("ingest: persist agent cloud network failed", "host_id", hostID, "error", err)
	}
}

func validAgentCloudNetwork(u models.CloudNetworkUsage) bool {
	if u.Provider != models.CloudBillingOCI || !validCloudStatus(u.Status) {
		return false
	}
	if u.Status == models.CloudBillingOK && !monthPattern.MatchString(u.Month) {
		return false
	}
	return u.InBytes <= maxAgentNetworkBytes && u.OutBytes <= maxAgentNetworkBytes
}

func validCloudStatus(status models.CloudBillingStatus) bool {
	switch status {
	case models.CloudBillingOK, models.CloudBillingNotInstalled, models.CloudBillingNotConfigured,
		models.CloudBillingAuthFailed, models.CloudBillingPermissionDenied, models.CloudBillingError:
		return true
	}
	return false
}

// truncateUTF8 drops invalid UTF-8 entirely and cuts s to at most max
// bytes on a rune boundary.
func truncateUTF8(s string, max int) string {
	if !utf8.ValidString(s) {
		return ""
	}
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

var (
	awsAccountPattern = regexp.MustCompile(`^\d{12}$`)
	ociTenancyPattern = regexp.MustCompile(`^ocid1\.tenancy\.[a-z0-9]+\.[a-z0-9-]*\.[a-z0-9]{1,128}$`)
)

// validAgentAccount checks an agent-reported (provider, account): an
// OCI tenancy OCID or a 12-digit AWS account ID — or "" when the agent
// doesn't know it (an OCI agent older than account IDs, or an AWS agent
// whose account lookup itself failed), filed under the host instead.
func validAgentAccount(provider models.CloudBillingProvider, accountID string) bool {
	switch provider {
	case models.CloudBillingOCI:
		return accountID == "" || ociTenancyPattern.MatchString(accountID)
	case models.CloudBillingAWS:
		return accountID == "" || awsAccountPattern.MatchString(accountID)
	}
	return false
}
