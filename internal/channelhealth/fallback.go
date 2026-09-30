// Package channelhealth implements SPEC-v0.8 §3-2's notification
// channel health report: weekly opt-in synthetic connectivity tests,
// delivery-statistics-derived health states, and the channel-failure
// fallback notification. It depends on internal/models and
// internal/notify's already-exported verify interfaces
// (RefSender/MessageReader/MessageDeleter) but not on internal/hub or
// internal/alerting, so it can be unit-tested with fakes and wired
// into the hub the same way internal/alerting's engine is: a small
// adapter in internal/hub owns the Store/NotifySenderFactory
// dependency injection.
package channelhealth

import "github.com/leejeonghun001/cloud-pulse/internal/models"

// SelectFallbackChannel deterministically picks the channel that
// should carry a "channel X is failing" notification for
// failingChannelID (SPEC-v0.8 §3-2): the lowest-ID channel among
// candidates that is (a) not failingChannelID itself, (b) Enabled,
// and (c) not itself currently failing per healthByChannelID (a
// channel with no entry in healthByChannelID is treated as healthy —
// e.g. one with no delivery/synthetic history yet, models.
// ChannelUnknown, is still eligible: an untested channel is not known
// to be broken). Returns (0, false) if no eligible candidate exists,
// signaling callers to fall back to the dashboard banner instead
// (SPEC-v0.8 §3-2: "정상 채널이 없으면 대시보드 배너로 표시").
//
// Deterministic by design (SPEC-v0.8 §3-2 + prep contract: "deterministic
// choice") so repeated calls during the same incident and across hub
// restarts always agree on the same fallback channel, and so tests can
// assert an exact expected pick rather than "any of these."
func SelectFallbackChannel(candidates []models.NotifyChannel, failingChannelID int64, healthByChannelID map[int64]models.ChannelHealthState) (int64, bool) {
	var best int64
	found := false
	for _, ch := range candidates {
		if ch.ID == failingChannelID {
			continue
		}
		if !ch.Enabled {
			continue
		}
		if healthByChannelID[ch.ID] == models.ChannelFailing {
			continue
		}
		if !found || ch.ID < best {
			best = ch.ID
			found = true
		}
	}
	return best, found
}
