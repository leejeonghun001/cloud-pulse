package models

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
