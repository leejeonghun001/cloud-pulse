package billing

import "github.com/leejeonghun001/cloud-pulse/internal/models"

// BytesPerGB is the number of bytes in one GB for billing purposes:
// cloud providers bill egress in decimal (SI) gigabytes, 10^9 bytes —
// NOT the binary GiB (2^30 bytes) models.GiB/models.HumanBytes use for
// display. SPEC-v0.6 §3 is explicit about this: "GB = 10^9 bytes,
// 클라우드 청구 단위" ("the unit cloud providers bill in").
const BytesPerGB = 1_000_000_000

// bytesToGB converts b to GB (10^9 bytes) as a float64. A uint64 up to
// 2^64-1 divided by 1e9 fits well within float64's 53-bit mantissa
// precision for any realistic monthly egress volume (petabytes would
// need ~2^60 bytes to lose integer precision here), and the result only
// ever feeds into further floating-point tier arithmetic, so no
// precision is lost that ValidatePlan/EstimateCost's rounding didn't
// already assume.
func bytesToGB(b uint64) float64 {
	return float64(b) / BytesPerGB
}

// tierCost applies plan's free allowance and tiers to usageGB (already
// net of pooling, if any), returning the cost in USD. usageGB and
// freeGB are both non-negative GB figures; tiers must be in ascending
// order with a trailing unbounded tier (SortedTiers/ValidatePlan
// enforce this shape; tierCost itself does not re-validate, so callers
// must pass a valid plan).
func tierCost(tiers []models.PricingTier, freeGB, usageGB float64) float64 {
	billable := usageGB - freeGB
	if billable <= 0 {
		return 0
	}

	var cost float64
	lowerBound := 0.0
	for _, t := range tiers {
		if billable <= lowerBound {
			break
		}
		upperBound := t.UpToGB
		if upperBound == 0 {
			// Unbounded tier: everything remaining is billed at this rate.
			cost += (billable - lowerBound) * t.PricePerGB
			break
		}
		tierGB := upperBound - lowerBound
		amountInTier := billable - lowerBound
		if amountInTier > tierGB {
			amountInTier = tierGB
		}
		cost += amountInTier * t.PricePerGB
		lowerBound = upperBound
	}
	return cost
}

// roundCents rounds a USD amount to the nearest cent (2 decimal
// places), matching SPEC-v0.6 §3's "cent rounding" requirement. Uses
// the standard round-half-away-from-zero rule; billing amounts are
// always non-negative in this package, so the sign branch never
// triggers in practice but is kept for defensive correctness (a
// negative amount would otherwise round toward zero incorrectly). A
// small epsilon is added before truncating to counter float64
// representation error (e.g. 1.005 is actually stored as
// 1.00499999999999989...), so an exact-looking decimal input rounds the
// way a human expects rather than down due to binary floating-point
// noise.
func roundCents(usd float64) float64 {
	if usd < 0 {
		return -roundCents(-usd)
	}
	const epsilon = 1e-9
	return float64(int64(usd*100+0.5+epsilon)) / 100
}

// HostUsage is one host's current-month egress usage, as fed into
// EstimateNetworkCost.
type HostUsage struct {
	HostID string
	// TxBytes is month-to-date outbound (egress) bytes.
	TxBytes uint64
	// ProjectedTxBytes is the linear month-end projection of TxBytes
	// (see models.ComputeEgress's ProjectedTxBytes), used for the
	// Projected half of the resulting EstimatedCost.
	ProjectedTxBytes uint64
	// RxBytes/ProjectedRxBytes mirror TxBytes/ProjectedTxBytes for
	// inbound traffic, billed via plan.IngressPricePerGB (0 by
	// default, so most plans produce 0 ingress cost).
	RxBytes          uint64
	ProjectedRxBytes uint64
}

// EstimateNetworkCost computes each host's EstimatedCost from usages
// under plan. When plan.PoolFreeTier is true, the free allowance is
// shared across every host in usages (representing a single
// account/tenancy-wide free tier) and the resulting *cost* (not the
// free allowance itself) is allocated back to each host in proportion
// to its own share of the pool's total usage — see the package doc and
// SPEC-v0.6 §3: "차감 결과는 사용량 비율로 각 호스트에 배분한다" ("the
// result of the deduction is allocated to each host in proportion to
// usage").
//
// A plan with PoolFreeTier false (or a single-host usages slice, where
// pooling and per-host calculation are equivalent) computes each host's
// cost independently against its own usage and plan.EgressFreeGB.
//
// The returned map is keyed by HostID; every entry in usages produces
// exactly one entry in the result, even a host with zero usage (cost
// 0). Callers pass an empty/nil plan.EgressTiers or an unassigned plan
// (nil) as "no plan" by not calling this function at all — there is no
// sentinel "zero plan" here, matching HostPricing's own "PlanID == 0
// means unassigned" convention.
func EstimateNetworkCost(plan models.PricingPlan, usages []HostUsage) map[string]models.EstimatedCost {
	out := make(map[string]models.EstimatedCost, len(usages))
	if len(usages) == 0 {
		return out
	}

	tiers := SortedTiers(plan.EgressTiers)

	if !plan.PoolFreeTier || len(usages) == 1 {
		for _, u := range usages {
			out[u.HostID] = estimateSingleHost(plan, tiers, u)
		}
		return out
	}

	// Pooled: compute the pool's total egress cost once (free tier
	// shared across every host on this plan), then allocate that total
	// back to each host proportional to its own share of total usage.
	var totalTxGB, totalProjectedTxGB float64
	for _, u := range usages {
		totalTxGB += bytesToGB(u.TxBytes)
		totalProjectedTxGB += bytesToGB(u.ProjectedTxBytes)
	}

	poolMTDCost := tierCost(tiers, plan.EgressFreeGB, totalTxGB)
	poolProjectedCost := tierCost(tiers, plan.EgressFreeGB, totalProjectedTxGB)

	for _, u := range usages {
		var mtdShare, projectedShare float64
		if totalTxGB > 0 {
			mtdShare = poolMTDCost * (bytesToGB(u.TxBytes) / totalTxGB)
		}
		if totalProjectedTxGB > 0 {
			projectedShare = poolProjectedCost * (bytesToGB(u.ProjectedTxBytes) / totalProjectedTxGB)
		}

		ingressMTD := bytesToGB(u.RxBytes) * plan.IngressPricePerGB
		ingressProjected := bytesToGB(u.ProjectedRxBytes) * plan.IngressPricePerGB

		out[u.HostID] = models.EstimatedCost{
			MTD:       roundCents(mtdShare + ingressMTD),
			Projected: roundCents(projectedShare + ingressProjected),
			PlanID:    plan.ID,
			PlanName:  plan.Name,
		}
	}
	return out
}

// estimateSingleHost computes one host's cost independently: its own
// egress against the plan's full (unshared) free allowance, plus
// ingress at plan.IngressPricePerGB.
func estimateSingleHost(plan models.PricingPlan, tiers []models.PricingTier, u HostUsage) models.EstimatedCost {
	mtdEgress := tierCost(tiers, plan.EgressFreeGB, bytesToGB(u.TxBytes))
	projectedEgress := tierCost(tiers, plan.EgressFreeGB, bytesToGB(u.ProjectedTxBytes))
	mtdIngress := bytesToGB(u.RxBytes) * plan.IngressPricePerGB
	projectedIngress := bytesToGB(u.ProjectedRxBytes) * plan.IngressPricePerGB

	return models.EstimatedCost{
		MTD:       roundCents(mtdEgress + mtdIngress),
		Projected: roundCents(projectedEgress + projectedIngress),
		PlanID:    plan.ID,
		PlanName:  plan.Name,
	}
}
