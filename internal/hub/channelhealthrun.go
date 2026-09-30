// channelhealthrun.go wires SPEC-v0.8 §3-2's weekly synthetic
// connectivity test and channel-failure fallback notification into the
// hub: a per-minute scheduler tick that runs internal/channelhealth.IsDue
// against every channel's stored schedule, and the consecutive-
// delivery-failure / synthetic-test-failure fallback dispatch.
package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/channelhealth"
	"github.com/leejeonghun001/cloud-pulse/internal/i18n"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// channelHealthTickInterval is how often the background scheduler
// checks every channel's schedule for IsDue. One minute keeps the
// worst-case delay between a scheduled minute-of-day and the actual
// run comfortably inside channelhealth's dueWindow (1h) even under
// load, while avoiding a per-second busy loop.
const channelHealthTickInterval = 1 * time.Minute

// ChannelHealthRuntime wires SPEC-v0.8 §3-2's channel health report
// into the hub: the weekly synthetic-test scheduler and the channel-
// failure fallback notifier. Constructed by cmd/hub and passed as
// Options.ChannelHealth; nil disables the background scheduler and
// fallback dispatch entirely (the health report API still works read-
// only against whatever data already exists).
type ChannelHealthRuntime struct {
	// Now returns the current time; nil defaults to time.Now. Tests
	// inject a fixed/advancing clock.
	Now func() time.Time
	// RunOptions configures internal/channelhealth.RunSyntheticTest
	// (sender factory, per-attempt timeout). Zero value uses
	// production defaults.
	RunOptions channelhealth.RunOptions
}

func (c *ChannelHealthRuntime) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// RunChannelHealthLoop runs the weekly synthetic-test scheduler until
// ctx is canceled. A nil Options.ChannelHealth makes this a no-op —
// callers should still be able to invoke it unconditionally, mirroring
// RunBillingLoop/RunStorageLoop's convention.
func (s *Server) RunChannelHealthLoop(ctx context.Context) {
	if s.opts.ChannelHealth == nil {
		return
	}
	ticker := time.NewTicker(channelHealthTickInterval)
	defer ticker.Stop()
	s.runDueChannelHealthChecks(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDueChannelHealthChecks(ctx)
		}
	}
}

// runDueChannelHealthChecks runs a synthetic test for every channel
// whose stored schedule is currently due, then re-evaluates that
// channel's health state and dispatches (or clears) the fallback
// notification accordingly. Errors are logged, never propagated — this
// runs on the background scheduler, no request is waiting on it.
func (s *Server) runDueChannelHealthChecks(ctx context.Context) {
	rt := s.opts.ChannelHealth
	if rt == nil {
		return
	}
	now := rt.now()

	settingsList, err := s.store.ListChannelHealthSettings(ctx)
	if err != nil {
		s.logger.Error("channel health: list settings failed", "error", err)
		return
	}
	for _, cs := range settingsList {
		if !channelhealth.IsDue(cs, now) {
			continue
		}
		s.runOneChannelSyntheticTest(ctx, cs.ChannelID, now)
	}
}

// runOneChannelSyntheticTest runs and persists one channel's synthetic
// test, updates its schedule's LastRunAt, and evaluates the fallback
// dispatch.
func (s *Server) runOneChannelSyntheticTest(ctx context.Context, channelID int64, now time.Time) {
	rt := s.opts.ChannelHealth
	ch, err := s.store.GetNotifyChannel(ctx, channelID)
	if err != nil {
		s.logger.Error("channel health: get channel failed", "channel_id", channelID, "error", err)
		return
	}

	created, err := s.store.CreateSyntheticResult(ctx, models.SyntheticResult{ChannelID: channelID, StartedAt: now.Unix()})
	if err != nil {
		s.logger.Error("channel health: create synthetic result failed", "channel_id", channelID, "error", err)
		return
	}

	result := channelhealth.RunSyntheticTest(ctx, ch, rt.RunOptions)
	result.ID = created.ID
	result.ChannelID = channelID
	result.StartedAt = created.StartedAt
	result.FinishedAt = rt.now().Unix()

	if err := s.store.UpdateSyntheticResult(ctx, result); err != nil {
		s.logger.Error("channel health: update synthetic result failed", "channel_id", channelID, "error", err)
	}
	if err := s.store.UpdateChannelHealthSettingsLastRun(ctx, channelID, now.Unix()); err != nil {
		s.logger.Error("channel health: update last run failed", "channel_id", channelID, "error", err)
	}

	s.evaluateChannelFailureDispatch(ctx, ch, result.Outcome == models.SyntheticFailed, rt.now())
}

// evaluateChannelFailureDispatch opens/dispatches or recovers a
// channel-failure incident based on the channel's freshly computed
// health state (SPEC-v0.8 §3-2): a synthetic-test failure or N (default
// models.ConsecutiveFailureThreshold) consecutive delivery failures
// opens/keeps-open an incident, sending the fallback notification once
// per incident through a different healthy enabled channel (deterministic
// selection, self/disabled/failing excluded — see
// channelhealth.SelectFallbackChannel); a healthy/degraded state closes
// any open incident. isFailingNow is the synthetic-test outcome for the
// channel just tested; consecutive delivery failures are checked
// independently via the channel's stats (both paths can independently
// classify a channel as ChannelFailing per models.ComputeChannelHealthState,
// but this function only receives the synthetic signal directly —
// callers evaluating a plain delivery failure use
// evaluateChannelFailureDispatchFromDeliveryFailures instead).
func (s *Server) evaluateChannelFailureDispatch(ctx context.Context, ch models.NotifyChannel, isFailingNow bool, now time.Time) {
	if !isFailingNow {
		s.recoverChannelFailureIncidentIfOpen(ctx, ch, now)
		return
	}
	s.openOrContinueChannelFailureIncident(ctx, ch, now)
}

// recoverChannelFailureIncidentIfOpen closes ch's open incident (if
// any), optionally sending a recovery message through the same
// fallback channel that carried the original failure notification
// (SPEC-v0.8 §3-2: "복구 메시지는 선택 사항").
func (s *Server) recoverChannelFailureIncidentIfOpen(ctx context.Context, ch models.NotifyChannel, now time.Time) {
	inc, ok, err := s.store.GetOpenChannelFailureIncident(ctx, ch.ID)
	if err != nil {
		s.logger.Error("channel health: get open incident failed", "channel_id", ch.ID, "error", err)
		return
	}
	if !ok {
		return
	}
	if err := s.store.RecoverChannelFailureIncident(ctx, ch.ID, now.Unix()); err != nil {
		s.logger.Error("channel health: recover incident failed", "channel_id", ch.ID, "error", err)
		return
	}
	if inc.NotifiedAt == 0 || inc.FallbackChannelID == 0 {
		return
	}
	fallback, err := s.store.GetNotifyChannel(ctx, inc.FallbackChannelID)
	if err != nil {
		return // fallback channel since deleted; nothing to notify
	}
	lang := s.effectiveNotifyLanguage(ctx)
	msg := i18n.T(lang, "channel_health.channel_recovered", map[string]string{"channel_name": ch.Name})
	s.sendFallbackNotification(ctx, fallback, msg)
}

// openOrContinueChannelFailureIncident opens a new incident for ch (or
// leaves an already-open one untouched if already notified), then
// attempts the fallback dispatch if not yet sent.
func (s *Server) openOrContinueChannelFailureIncident(ctx context.Context, ch models.NotifyChannel, now time.Time) {
	inc, ok, err := s.store.GetOpenChannelFailureIncident(ctx, ch.ID)
	if err != nil {
		s.logger.Error("channel health: get open incident failed", "channel_id", ch.ID, "error", err)
		return
	}
	if !ok {
		if err := s.store.OpenChannelFailureIncident(ctx, ch.ID, now.Unix()); err != nil {
			s.logger.Error("channel health: open incident failed", "channel_id", ch.ID, "error", err)
			return
		}
	} else if inc.NotifiedAt != 0 {
		// Already notified for this open incident — SPEC-v0.8 §3-2's
		// "once per incident" de-duplication.
		return
	}
	s.dispatchChannelFailureFallback(ctx, ch, now)
}

// dispatchChannelFailureFallback selects a fallback channel (excluding
// ch itself, disabled channels, and channels currently classified
// ChannelFailing) and sends the "channel X is failing" message through
// it, recording the dispatch. If no eligible fallback exists, the
// incident stays un-notified (NotifiedAt remains 0) so the dashboard
// banner (GET /api/v1/alerts/channels/health's BannerNeeded) surfaces
// it instead — retried on the next tick in case a channel's health
// improves or a new one is added.
func (s *Server) dispatchChannelFailureFallback(ctx context.Context, ch models.NotifyChannel, now time.Time) {
	channels, err := s.store.ListNotifyChannels(ctx)
	if err != nil {
		s.logger.Error("channel health: list channels for fallback failed", "error", err)
		return
	}
	healthByChannel, err := s.computeAllChannelHealthStates(ctx, channels, now)
	if err != nil {
		s.logger.Error("channel health: compute health states for fallback failed", "error", err)
		return
	}

	fallbackID, found := channelhealth.SelectFallbackChannel(channels, ch.ID, healthByChannel)
	if !found {
		return // no eligible fallback; dashboard banner covers this (BannerNeeded)
	}
	fallback, err := s.store.GetNotifyChannel(ctx, fallbackID)
	if err != nil {
		s.logger.Error("channel health: get fallback channel failed", "fallback_id", fallbackID, "error", err)
		return
	}

	lang := s.effectiveNotifyLanguage(ctx)
	msg := i18n.T(lang, "channel_health.channel_down", map[string]string{"channel_name": ch.Name})
	if !s.sendFallbackNotification(ctx, fallback, msg) {
		return
	}
	if err := s.store.MarkChannelFailureIncidentNotified(ctx, ch.ID, now.Unix(), fallbackID); err != nil {
		s.logger.Error("channel health: mark incident notified failed", "channel_id", ch.ID, "error", err)
	}
}

// sendFallbackNotification sends msg as a plain text notification
// through fallback via s.opts.NotifyFactory, returning whether the send
// succeeded. A nil NotifyFactory (no notify stage wired) or a build/
// send error is logged and reported as failure — never retried within
// this call; the next scheduler tick (if the incident is still open and
// unnotified) will try again.
func (s *Server) sendFallbackNotification(ctx context.Context, fallback models.NotifyChannel, msg string) bool {
	if s.opts.NotifyFactory == nil {
		return false
	}
	sender, err := s.opts.NotifyFactory(fallback)
	if err != nil {
		s.logger.Error("channel health: build fallback sender failed", "fallback_id", fallback.ID, "error", err)
		return false
	}
	sendCtx, cancel := context.WithTimeout(ctx, alertNotifyTimeout)
	defer cancel()
	if err := sender.Send(sendCtx, fallbackAlertingMessage(msg)); err != nil {
		s.logger.Error("channel health: send fallback notification failed", "fallback_id", fallback.ID, "error", err)
		return false
	}
	return true
}

// fallbackAlertingMessage builds the alerting.Message a channel-
// failure fallback notification sends: a plain warning-severity text
// message, no chart image (there is no time series to attach — the
// message is about the channel itself, not a metric breach).
func fallbackAlertingMessage(text string) alerting.Message {
	return alerting.Message{
		Title:    "cloud-pulse channel health",
		Text:     text,
		Severity: alerting.SeverityWarning,
	}
}

// effectiveNotifyLanguage reads the hub-wide notify_language setting
// (SPEC-v0.8 §3-3), defaulting to i18n.DefaultLang if unset/invalid —
// read fresh on every call per SettingNotifyLanguage's doc comment (no
// caching), so a language change takes effect on the very next
// notification.
func (s *Server) effectiveNotifyLanguage(ctx context.Context) i18n.Lang {
	raw, ok, err := s.store.GetSetting(ctx, SettingNotifyLanguage)
	if err != nil || !ok {
		return i18n.DefaultLang
	}
	lang, parsedOK := i18n.Parse(raw)
	if !parsedOK {
		return i18n.DefaultLang
	}
	return lang
}

// computeAllChannelHealthStates computes each channel's current
// models.ChannelHealthState from its most recent synthetic result and
// 7-day delivery stats, for SelectFallbackChannel's candidate
// evaluation.
func (s *Server) computeAllChannelHealthStates(ctx context.Context, channels []models.NotifyChannel, now time.Time) (map[int64]models.ChannelHealthState, error) {
	events, err := s.store.ListAlertEvents(ctx, "", "", 0, 0)
	if err != nil {
		return nil, fmt.Errorf("hub: list alert events for channel health: %w", err)
	}
	weekAgo := now.Add(-7 * 24 * time.Hour).Unix()
	recentDeliveries := filterDeliveriesSince(events, weekAgo)

	out := make(map[int64]models.ChannelHealthState, len(channels))
	for _, ch := range channels {
		results, err := s.store.ListSyntheticResults(ctx, ch.ID, 1)
		if err != nil {
			return nil, fmt.Errorf("hub: list synthetic results for channel %d: %w", ch.ID, err)
		}
		var last *models.SyntheticResult
		if len(results) > 0 {
			last = &results[0]
		}
		stats := models.AggregateChannelDeliveryStats(models.DeliveriesForChannel(recentDeliveries, ch.ID))
		out[ch.ID] = models.ComputeChannelHealthState(last, stats)
	}
	return out, nil
}

// filterDeliveriesSince returns a copy of events whose StartedAt is at
// or after since, for scoping AggregateChannelDeliveryStats's input to
// a lookback window without mutating the caller's slice.
func filterDeliveriesSince(events []models.AlertEvent, since int64) []models.AlertEvent {
	out := make([]models.AlertEvent, 0, len(events))
	for _, ev := range events {
		if ev.StartedAt >= since {
			out = append(out, ev)
		}
	}
	return out
}
