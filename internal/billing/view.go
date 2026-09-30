package billing

import (
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// NetworkEstimator computes a host's estimated network egress cost for
// the current month — the "network-cost" v0.6.0 stage's pure-function
// interface (SPEC-v0.6 §3). BuildBillingView calls exactly one
// NetworkEstimator per host; when the network-cost stage hasn't landed
// yet (or a hub has no pricing plan assigned to a host), an
// implementation should return a zero models.EstimatedCost rather than
// an error — network estimation is always best-effort, never fatal to
// the Costs page.
//
// This is the "clearly typed hook" SPEC-v0.6 §1 asks for: billingroutes.go
// wires a package-level NetworkEstimator into BuildBillingView's caller
// (see Server.networkEstimator in internal/hub/billingroutes.go); until
// the network-cost stage replaces it, ZeroNetworkEstimator is used.
type NetworkEstimator func(hostID string) models.EstimatedCost

// ZeroNetworkEstimator is the default NetworkEstimator: every host's
// network cost estimate is zero. Used until the network-cost v0.6.0
// stage supplies its real implementation.
func ZeroNetworkEstimator(string) models.EstimatedCost {
	return models.EstimatedCost{}
}

// BuildHostCost computes a single host's models.HostCost — the
// exported single-host counterpart of BuildBillingView, used by
// handleGetHost/handleListHosts (internal/hub/handlers.go) to populate
// HostSummary.Cost without needing the full host list BuildBillingView
// expects. byProvider should be built the same way BuildBillingView
// builds it internally (keyed by models.CloudCostSnapshot.Provider);
// estimateNetwork defaults to ZeroNetworkEstimator when nil.
func BuildHostCost(info models.HostInfo, byProvider map[models.CloudBillingProvider]models.CloudCostSnapshot, estimateNetwork NetworkEstimator) models.HostCost {
	if estimateNetwork == nil {
		estimateNetwork = ZeroNetworkEstimator
	}
	return buildHostCost(info, byProvider, estimateNetwork)
}

// SnapshotsByProvider indexes snapshots by their Provider field, the
// same lookup BuildBillingView builds internally — exported so
// handleGetHost/handleListHosts can build it once per request and
// reuse it across BuildHostCost calls for every host in that request,
// rather than each call rebuilding the same small map.
func SnapshotsByProvider(snapshots []models.CloudCostSnapshot) map[models.CloudBillingProvider]models.CloudCostSnapshot {
	byProvider := make(map[models.CloudBillingProvider]models.CloudCostSnapshot, len(snapshots))
	for _, s := range snapshots {
		byProvider[s.Provider] = s
	}
	return byProvider
}

// BuildBillingView combines persisted provider snapshots with the
// current host list into the GET /api/v1/billing response (SPEC-v0.6
// §1), matching each host's HostInfo.CloudInstanceID against its
// provider's snapshot PerResource map, and adding network's per-host
// estimate via estimateNetwork. intervalSeconds/display are echoed
// through from the caller's resolved settings.
func BuildBillingView(snapshots []models.CloudCostSnapshot, hosts []models.HostRecord, estimateNetwork NetworkEstimator, intervalSeconds int64, display models.DisplayCurrencySettings) models.BillingView {
	if estimateNetwork == nil {
		estimateNetwork = ZeroNetworkEstimator
	}

	byProvider := SnapshotsByProvider(snapshots)

	hostCosts := make([]models.HostCost, 0, len(hosts))
	for _, h := range hosts {
		hostCosts = append(hostCosts, buildHostCost(h.Info, byProvider, estimateNetwork))
	}

	return models.BillingView{
		Snapshots:       snapshots,
		Hosts:           hostCosts,
		IntervalSeconds: intervalSeconds,
		DisplayCurrency: display,
	}
}

// buildHostCost computes one host's models.HostCost: cloud-billing
// figures matched via CloudInstanceID against byProvider's snapshot for
// the host's own Provider (see providerToBilling), plus the network
// estimate from estimateNetwork.
func buildHostCost(info models.HostInfo, byProvider map[models.CloudBillingProvider]models.CloudCostSnapshot, estimateNetwork NetworkEstimator) models.HostCost {
	hc := models.HostCost{
		HostID:          info.ID,
		Hostname:        info.Hostname,
		NetworkEstimate: estimateNetwork(info.ID),
	}

	billingProvider, ok := providerToBilling(info.Provider)
	if ok {
		hc.Provider = billingProvider
		if snap, found := byProvider[billingProvider]; found {
			hc.InstanceID = info.CloudInstanceID
			if info.CloudInstanceID != "" && snap.PerResource != nil {
				if cost, matched := snap.PerResource[info.CloudInstanceID]; matched {
					hc.Matched = true
					hc.CloudMTD = cost
					// Forecast isn't broken out per-resource by either
					// provider's CLI response; approximate a matched
					// host's own forecast by the same MTD/forecast ratio
					// the account-level snapshot observed (0 when the
					// account MTD is 0, avoiding a divide-by-zero).
					if snap.MTDCost != 0 {
						hc.CloudForecast = cost / snap.MTDCost * snap.ForecastCost
					}
				}
			}
			if !hc.Matched && snap.AccountLevel {
				// Unmatched but the snapshot is account-level: surface
				// the account total per SPEC-v0.6 §1's HostCost doc
				// comment ("Matched is false and these represent
				// 'account total'").
				hc.CloudMTD = snap.MTDCost
				hc.CloudForecast = snap.ForecastCost
			}
		}
	}

	hc.TotalMTD = hc.NetworkEstimate.MTD
	hc.TotalForecast = hc.NetworkEstimate.Projected
	if hc.Matched || (ok && hc.CloudMTD != 0) {
		hc.TotalMTD += hc.CloudMTD
		hc.TotalForecast += hc.CloudForecast
	}
	return hc
}

// providerToBilling maps a models.Provider (agent-reported) to the
// models.CloudBillingProvider a hub billing snapshot is keyed by. OCI's
// two region-based default pricing plans (see
// notes/v06-prep.md's "provider strings" flag) don't affect this
// mapping — billing snapshots are always keyed by the coarse aws/oci
// provider, regardless of region.
func providerToBilling(p models.Provider) (models.CloudBillingProvider, bool) {
	switch p {
	case models.ProviderAWS:
		return models.CloudBillingAWS, true
	case models.ProviderOCI:
		return models.CloudBillingOCI, true
	default:
		return "", false
	}
}
