// channelhealthroutes.go implements SPEC-v0.8 §3-2's notification
// channel health report API: GET /api/v1/alerts/channels/health (the
// full per-channel health report), POST
// /api/v1/alerts/channels/{id}/synthetic-test (run one channel's
// synthetic test immediately, "run now"), and PUT
// /api/v1/alerts/channels/{id}/health-settings (the weekly opt-in
// schedule). All admin-only, following registerAlertRoutes' existing
// pattern.
package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/channelhealth"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

// maxChannelHealthBodyBytes bounds the health-settings PUT request
// body (a tiny JSON payload).
const maxChannelHealthBodyBytes = 4 << 10 // 4 KiB

// channelHealthLookbackDays defines the two lookback windows SPEC-v0.8
// §3-2 asks for: 7-day and 30-day delivery success rate.
const (
	channelHealth7DayLookback  = 7 * 24 * time.Hour
	channelHealth30DayLookback = 30 * 24 * time.Hour
	// channelHealthSyntheticStripLimit is the number of most-recent
	// synthetic results returned per channel for the health tab's
	// "최근 12주" strip (SPEC-v0.8 §3-2).
	channelHealthSyntheticStripLimit = 12
)

// registerChannelHealthRoutes registers the channel health report API
// (SPEC-v0.8 §3-2).
func (s *Server) registerChannelHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/alerts/channels/health", s.requireAdmin(s.handleGetChannelsHealth))
	mux.HandleFunc("POST /api/v1/alerts/channels/{id}/synthetic-test", s.requireAdmin(s.handleRunSyntheticTest))
	mux.HandleFunc("PUT /api/v1/alerts/channels/{id}/health-settings", s.requireAdmin(s.handleSetChannelHealthSettings))
}

// handleGetChannelsHealth serves GET /api/v1/alerts/channels/health
// (admin): every configured channel's health state, delivery stats,
// synthetic-test schedule/history, and whether a dashboard banner is
// needed because some failing channel had no healthy fallback.
func (s *Server) handleGetChannelsHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.opts.now()

	channels, err := s.store.ListNotifyChannels(ctx)
	if err != nil {
		s.logger.Error("channel health: list channels failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	events, err := s.store.ListAlertEvents(ctx, "", "", 0, 0)
	if err != nil {
		s.logger.Error("channel health: list alert events failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	deliveries7d := filterDeliveriesSince(events, now.Add(-channelHealth7DayLookback).Unix())
	deliveries30d := filterDeliveriesSince(events, now.Add(-channelHealth30DayLookback).Unix())

	report := models.ChannelHealthReport{Channels: make([]models.ChannelHealth, 0, len(channels))}
	healthByChannel := make(map[int64]models.ChannelHealthState, len(channels))

	for _, ch := range channels {
		health, err := s.buildChannelHealth(ctx, ch, deliveries7d, deliveries30d)
		if err != nil {
			s.logger.Error("channel health: build channel health failed", "channel_id", ch.ID, "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
		report.Channels = append(report.Channels, health)
		healthByChannel[ch.ID] = health.State
	}

	report.BannerNeeded, report.BannerChannelNames = s.channelHealthBannerState(ctx, channels, healthByChannel)
	writeJSON(w, http.StatusOK, report)
}

// buildChannelHealth assembles one channel's full models.ChannelHealth
// row from its stored settings, synthetic-test history, and the
// already-fetched 7-day/30-day delivery windows.
func (s *Server) buildChannelHealth(ctx context.Context, ch models.NotifyChannel, deliveries7d, deliveries30d []models.AlertEvent) (models.ChannelHealth, error) {
	stats7d := models.AggregateChannelDeliveryStats(models.DeliveriesForChannel(deliveries7d, ch.ID))
	stats30d := models.AggregateChannelDeliveryStats(models.DeliveriesForChannel(deliveries30d, ch.ID))

	recent, err := s.store.ListSyntheticResults(ctx, ch.ID, channelHealthSyntheticStripLimit)
	if err != nil {
		return models.ChannelHealth{}, err
	}
	// Defense in depth for historical entries written before channel-aware
	// error sanitization. Copy so a fake/store-owned slice is not mutated.
	recent = append([]models.SyntheticResult(nil), recent...)
	for i := range recent {
		recent[i].Detail = notify.SafeText(ch, recent[i].Detail)
	}
	var last *models.SyntheticResult
	if len(recent) > 0 {
		last = &recent[0]
	}

	settings, err := s.store.GetChannelHealthSettings(ctx, ch.ID)
	if err != nil {
		return models.ChannelHealth{}, err
	}
	var settingsPtr *models.ChannelHealthSettings
	if settings.UpdatedAt != 0 {
		settingsPtr = &settings
	}

	return models.ChannelHealth{
		ChannelID:       ch.ID,
		ChannelName:     ch.Name,
		ChannelType:     ch.Type,
		Enabled:         ch.Enabled,
		State:           models.ComputeChannelHealthState(last, stats7d),
		Stats7d:         stats7d,
		Stats30d:        stats30d,
		Settings:        settingsPtr,
		LastSynthetic:   last,
		RecentSynthetic: recent,
	}, nil
}

// channelHealthBannerState reports whether the dashboard banner is
// needed (SPEC-v0.8 §3-2: some channel is failing and has an open,
// un-notified incident because no eligible fallback channel exists)
// and which channel name(s) to mention.
func (s *Server) channelHealthBannerState(ctx context.Context, channels []models.NotifyChannel, healthByChannel map[int64]models.ChannelHealthState) (bool, []string) {
	var names []string
	for _, ch := range channels {
		if healthByChannel[ch.ID] != models.ChannelFailing {
			continue
		}
		inc, ok, err := s.store.GetOpenChannelFailureIncident(ctx, ch.ID)
		if err != nil || !ok {
			continue
		}
		if inc.NotifiedAt == 0 {
			names = append(names, ch.Name)
		}
	}
	return len(names) > 0, names
}

// handleRunSyntheticTest runs a channel's synthetic test immediately:
// POST /api/v1/alerts/channels/{id}/synthetic-test (admin, "run now").
// Runs synchronously with a 25s whole-route deadline (including storage
// and fallback dispatch), returning the resulting models.SyntheticResult.
func (s *Server) handleRunSyntheticTest(w http.ResponseWriter, r *http.Request) {
	// Includes store work, fallback dispatch and final persistence, below
	// the hub's 30s WriteTimeout. A shorter parent deadline always wins.
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	id, err := parseAlertID(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}
	ch, err := s.store.GetNotifyChannel(ctx, id)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "channel not found"})
			return
		}
		s.logger.Error("channel health: get channel failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	now := s.opts.now()
	created, err := s.store.CreateSyntheticResult(ctx, models.SyntheticResult{ChannelID: id, StartedAt: now.Unix()})
	if err != nil {
		s.logger.Error("channel health: create synthetic result failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	runOpts := channelhealth.RunOptions{}
	if s.opts.ChannelHealth != nil {
		runOpts = s.opts.ChannelHealth.RunOptions
	}
	result := channelhealth.RunSyntheticTest(ctx, ch, runOpts)
	result.ID = created.ID
	result.ChannelID = id
	result.StartedAt = created.StartedAt
	result.FinishedAt = s.opts.now().Unix()

	if err := s.store.UpdateSyntheticResult(ctx, result); err != nil {
		s.logger.Error("channel health: update synthetic result failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	if s.opts.ChannelHealth != nil {
		s.evaluateChannelFailureDispatch(ctx, ch, result.Outcome == models.SyntheticFailed, s.opts.now())
	}

	writeJSON(w, http.StatusOK, result)
}

// channelHealthSettingsRequest is the body of PUT
// /api/v1/alerts/channels/{id}/health-settings.
type channelHealthSettingsRequest struct {
	Enabled     bool `json:"enabled"`
	Weekday     int  `json:"weekday"`
	MinuteOfDay int  `json:"minute_of_day"`
}

// handleSetChannelHealthSettings sets a channel's weekly synthetic-
// test schedule: PUT /api/v1/alerts/channels/{id}/health-settings
// (admin). Weekday/MinuteOfDay are UTC (SPEC-v0.8 §3-2's DST-
// independent storage contract) — the caller (the dashboard) is
// responsible for converting from the browser's local time before
// sending this request.
func (s *Server) handleSetChannelHealthSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseAlertID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}
	if _, err := s.store.GetNotifyChannel(ctx, id); err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "channel not found"})
			return
		}
		s.logger.Error("channel health: get channel failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxChannelHealthBodyBytes)
	var req channelHealthSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}
	details := map[string]string{}
	if !models.ValidWeekday(req.Weekday) {
		details["weekday"] = "must be between 0 (Sunday) and 6 (Saturday)"
	}
	if !models.ValidMinuteOfDay(req.MinuteOfDay) {
		details["minute_of_day"] = "must be between 0 and 1439"
	}
	if len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "validation failed", Details: details})
		return
	}

	settings := models.ChannelHealthSettings{
		ChannelID:   id,
		Enabled:     req.Enabled,
		Weekday:     req.Weekday,
		MinuteOfDay: req.MinuteOfDay,
		UpdatedAt:   s.opts.now().Unix(),
	}
	if existing, err := s.store.GetChannelHealthSettings(ctx, id); err == nil {
		settings.LastRunAt = existing.LastRunAt
	}

	if err := s.store.SetChannelHealthSettings(ctx, settings); err != nil {
		s.logger.Error("channel health: set settings failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, settings)
}
