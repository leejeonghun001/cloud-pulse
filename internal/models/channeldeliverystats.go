package models

import "sort"

// AggregateChannelDeliveryStats computes a ChannelDeliveryStats for
// one channel from a list of AlertEvent deliveries observed within a
// lookback window, pure and side-effect free (SPEC-v0.8 §3-2's "7일·
// 30일 성공률" figures) — callers (the hub's channel-health handler)
// are responsible for fetching the right window of AlertEvents from
// the store and filtering to windowStart..now before calling this.
//
// deliveries should contain, for the target channelID, every
// models.Delivery observed across every AlertEvent in the window
// (flattened by the caller — an AlertEvent's Deliveries slice may
// contain entries for other channels too, which callers must filter
// out before calling this, since this function trusts every passed-in
// Delivery belongs to the channel being aggregated).
func AggregateChannelDeliveryStats(deliveries []Delivery) ChannelDeliveryStats {
	var stats ChannelDeliveryStats
	var latencySum int64
	var latencyCount int

	for _, d := range deliveries {
		stats.Total++
		if d.OK {
			stats.Success++
			if d.At > stats.LastSuccessAt {
				stats.LastSuccessAt = d.At
			}
		} else {
			if d.At > stats.LastFailureAt {
				stats.LastFailureAt = d.At
				if d.Diagnosis != nil {
					stats.LastFailureDiagnosis = *d.Diagnosis
				} else {
					stats.LastFailureDiagnosis = ""
				}
			}
		}
		if d.DurationMS > 0 {
			latencySum += d.DurationMS
			latencyCount++
			if d.DurationMS > stats.MaxLatencyMS {
				stats.MaxLatencyMS = d.DurationMS
			}
		}
	}

	if latencyCount > 0 {
		stats.AvgLatencyMS = latencySum / int64(latencyCount)
	}
	return stats
}

// DeliveriesForChannel filters events' deliveries down to only those
// addressed to channelID, flattened across every event, for use as
// AggregateChannelDeliveryStats's input.
func DeliveriesForChannel(events []AlertEvent, channelID int64) []Delivery {
	out := make([]Delivery, 0)
	for _, ev := range events {
		for _, d := range ev.Deliveries {
			if d.ChannelID == channelID {
				out = append(out, d)
			}
		}
	}
	return out
}

// HasConsecutiveDeliveryFailures reports whether the n most recent
// deliveries (chronologically by At) all failed (OK == false). This is
// the delivery-side trigger of the channel-failure fallback
// notification (SPEC-v0.8 §3-2: "연속 N회(기본 3) 전송이 실패하면"), with n
// = ConsecutiveFailureThreshold.
//
// deliveries must all belong to one channel (DeliveriesForChannel's
// output); input order does not matter — ListAlertEvents returns
// events newest-first, and a single event can carry several deliveries
// for the same channel, so the helper sorts by At itself.
// Returns false when fewer than n deliveries exist: an insufficient
// history is never a failure signal.
func HasConsecutiveDeliveryFailures(deliveries []Delivery, n int) bool {
	if n <= 0 {
		return false
	}
	sorted := make([]Delivery, len(deliveries))
	copy(sorted, deliveries)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At < sorted[j].At })
	if len(sorted) < n {
		return false
	}
	for _, d := range sorted[len(sorted)-n:] {
		if d.OK {
			return false
		}
	}
	return true
}
