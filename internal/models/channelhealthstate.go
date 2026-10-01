package models

// ComputeChannelHealthState is the pure function deriving a channel's
// overall ChannelHealthState from its most recent synthetic test
// result (if any) and its 7-day delivery statistics (SPEC-v0.8 §3-2).
// Thresholds (documented here, the single source of truth — do not
// duplicate these numbers elsewhere without a comment pointing back to
// this function):
//
//  1. lastSynthetic != nil && lastSynthetic.Outcome == SyntheticFailed
//     => ChannelFailing, unconditionally. A failed connectivity check
//     is the strongest signal available and overrides delivery stats
//     (a channel can have a healthy delivery rate yet already be
//     broken if no alert has fired recently to exercise it).
//  2. stats7d.Total == 0 => ChannelUnknown, unless step 1 already
//     classified the channel as failing. No deliveries and no failed
//     synthetic test means there's nothing to compute a rate from.
//  3. Otherwise, using stats7d.SuccessRate() (in [0, 1]):
//     - rate < failingThreshold (0.5)  => ChannelFailing
//     - rate < degradedThreshold (0.9) => ChannelDegraded
//     - otherwise                       => ChannelHealthy
//
// A channel with synthetic testing disabled (settings == nil or
// settings.Enabled == false) is still classified from its delivery
// stats alone — synthetic testing is an opt-in enhancement, not a
// prerequisite for a health state to exist.
func ComputeChannelHealthState(lastSynthetic *SyntheticResult, stats7d ChannelDeliveryStats) ChannelHealthState {
	if lastSynthetic != nil && lastSynthetic.Outcome == SyntheticFailed {
		return ChannelFailing
	}
	if stats7d.Total == 0 {
		return ChannelUnknown
	}
	rate := stats7d.SuccessRate()
	switch {
	case rate < failingSuccessRateThreshold:
		return ChannelFailing
	case rate < degradedSuccessRateThreshold:
		return ChannelDegraded
	default:
		return ChannelHealthy
	}
}

// failingSuccessRateThreshold is the 7-day delivery success rate
// (exclusive upper bound) below which a channel is classified
// ChannelFailing per ComputeChannelHealthState's step 3 — anything
// under 50% success means more deliveries are failing than succeeding.
const failingSuccessRateThreshold = 0.5

// degradedSuccessRateThreshold is the 7-day delivery success rate
// (exclusive upper bound) below which a channel is classified
// ChannelDegraded (and at/above failingSuccessRateThreshold) per
// ComputeChannelHealthState's step 3 — anything under 90% success
// still delivers most alerts but has a real, non-noise failure rate
// worth surfacing.
const degradedSuccessRateThreshold = 0.9

// ConsecutiveFailureThreshold is the default number of consecutive
// delivery failures (independent of the 7-day rate) that triggers the
// channel-failure fallback notification (SPEC-v0.8 §3-2: "연속 N회
// (기본 3) 전송이 실패하면"). Exported so the alerting stage's
// consecutive-failure counter and its tests share one constant rather
// than each hard-coding 3.
const ConsecutiveFailureThreshold = 3
