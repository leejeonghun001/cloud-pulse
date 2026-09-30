package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/alerting/chart"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

// maxAlertBodyBytes bounds the size of alert rule/channel request
// bodies (all tiny JSON payloads).
const maxAlertBodyBytes = 16 << 10 // 16 KiB

// maxEventsPageLimit is the largest "limit" GET /api/v1/alerts/events
// accepts; a request for more is silently capped rather than rejected.
const maxEventsPageLimit = 200

// defaultEventsPageLimit is used when "limit" is omitted or invalid.
const defaultEventsPageLimit = 50

// registerAlertRoutes registers the alerting API (SPEC-v0.5 §B):
// channel/rule CRUD, channel/rule test+preview, and event listing.
func (s *Server) registerAlertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/alerts/channels", s.requireAdmin(s.handleListChannels))
	mux.HandleFunc("POST /api/v1/alerts/channels", s.requireAdmin(s.handleCreateChannel))
	mux.HandleFunc("PUT /api/v1/alerts/channels/{id}", s.requireAdmin(s.handleUpdateChannel))
	mux.HandleFunc("DELETE /api/v1/alerts/channels/{id}", s.requireAdmin(s.handleDeleteChannel))
	mux.HandleFunc("POST /api/v1/alerts/channels/{id}/test", s.requireAdmin(s.handleTestExistingChannel))
	mux.HandleFunc("POST /api/v1/alerts/channels/test", s.requireAdmin(s.handleTestDraftChannel))

	mux.HandleFunc("GET /api/v1/alerts/rules", s.requireAdmin(s.handleListRules))
	mux.HandleFunc("POST /api/v1/alerts/rules", s.requireAdmin(s.handleCreateRule))
	mux.HandleFunc("PUT /api/v1/alerts/rules/{id}", s.requireAdmin(s.handleUpdateRule))
	mux.HandleFunc("DELETE /api/v1/alerts/rules/{id}", s.requireAdmin(s.handleDeleteRule))
	mux.HandleFunc("POST /api/v1/alerts/rules/{id}/preview", s.requireAdmin(s.handlePreviewRule))

	mux.HandleFunc("GET /api/v1/alerts/events", s.requireUser(s.handleListAlertEvents))
	mux.HandleFunc("GET /api/v1/alerts/active", s.requireUser(s.handleListActiveAlerts))
}

// --- Channels ---

// handleListChannels responds with every configured notify channel,
// secrets redacted: GET /api/v1/alerts/channels (admin).
func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	channels, err := s.store.ListNotifyChannels(ctx)
	if err != nil {
		s.logger.Error("list notify channels failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	out := make([]models.NotifyChannel, len(channels))
	for i, ch := range channels {
		out[i] = ch.Redacted()
	}
	writeJSON(w, http.StatusOK, out)
}

// channelRequest is the body of POST /api/v1/alerts/channels and PUT
// /api/v1/alerts/channels/{id}.
type channelRequest struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// validChannelTypes lists every accepted NotifyChannel.Type value.
var validChannelTypes = map[models.NotifyChannelType]bool{
	models.NotifyChannelDiscord:  true,
	models.NotifyChannelTelegram: true,
	models.NotifyChannelWhatsApp: true,
	models.NotifyChannelWebhook:  true,
}

// validateChannelRequest validates req's shared fields (name/type),
// returning field-level errors in the shape handlers hand to
// models.APIError.Details. Config-specific validation is deliberately
// minimal here (the notify stage's Sender constructors are the
// authority on a given type's required Config keys; this only rejects
// structurally invalid input the hub layer can check without importing
// internal/notify).
func validateChannelRequest(req channelRequest) (models.NotifyChannelType, map[string]string) {
	details := make(map[string]string)
	if req.Name == "" {
		details["name"] = "must not be empty"
	}
	if len(req.Name) > 200 {
		details["name"] = "must be at most 200 characters"
	}
	typ := models.NotifyChannelType(req.Type)
	if !validChannelTypes[typ] {
		details["type"] = "must be one of discord, telegram, whatsapp, webhook"
	}
	return typ, details
}

// handleCreateChannel creates a new notify channel: POST
// /api/v1/alerts/channels (admin).
func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxAlertBodyBytes)
	var req channelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	typ, details := validateChannelRequest(req)
	if len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "validation failed", Details: details})
		return
	}

	created, err := s.store.CreateNotifyChannel(ctx, models.NotifyChannel{
		Name: req.Name, Type: typ, Enabled: req.Enabled, Config: req.Config,
	})
	if err != nil {
		s.logger.Error("create notify channel failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, created.Redacted())
}

// handleUpdateChannel updates an existing notify channel: PUT
// /api/v1/alerts/channels/{id} (admin). A secret Config field that is
// omitted from the request body or sent as models.RedactedConfigValue
// ("***") preserves the previously stored secret rather than being
// cleared or literally set to "***" — see NotifyChannel.SecretFields.
func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseAlertID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}

	existing, err := s.store.GetNotifyChannel(ctx, id)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "channel not found"})
			return
		}
		s.logger.Error("get notify channel failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAlertBodyBytes)
	var req channelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	typ, details := validateChannelRequest(req)
	if len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "validation failed", Details: details})
		return
	}

	merged := mergePreservedSecrets(existing, typ, req.Config)

	updated, err := s.store.UpdateNotifyChannel(ctx, models.NotifyChannel{
		ID: id, Name: req.Name, Type: typ, Enabled: req.Enabled, Config: merged,
	})
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "channel not found"})
			return
		}
		s.logger.Error("update notify channel failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, updated.Redacted())
}

// mergePreservedSecrets returns a copy of newConfig where every secret
// field (per typ.SecretFields()) that is absent from newConfig, or
// equal to models.RedactedConfigValue, is replaced by existing's stored
// value for that field instead (preserve-on-omit/preserve-on-"***").
func mergePreservedSecrets(existing models.NotifyChannel, typ models.NotifyChannelType, newConfig map[string]string) map[string]string {
	out := make(map[string]string, len(newConfig))
	for k, v := range newConfig {
		out[k] = v
	}
	for _, field := range typ.SecretFields() {
		v, present := out[field]
		if !present || v == models.RedactedConfigValue {
			if existing.Config != nil {
				out[field] = existing.Config[field]
			}
		}
	}
	return out
}

// handleDeleteChannel deletes a notify channel: DELETE
// /api/v1/alerts/channels/{id} (admin). Does not cascade into
// alert_rules.channel_ids; a rule referencing the deleted channel simply
// skips it at delivery time (see internal/alerting.Engine.notifyEvent).
func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseAlertID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}
	if err := s.store.DeleteNotifyChannel(ctx, id); err != nil {
		s.logger.Error("delete notify channel failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// testMessage builds the sample notification payload sent by the
// channel-test endpoints. It carries a deterministic demo chart so every
// platform's image-delivery path can be verified before a channel is saved.
func testMessage() (any, error) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	image, err := chart.Render(chart.Params{
		Title:     "cloud-pulse · test alert",
		Threshold: 90,
		Unit:      "%",
		Points: []chart.Point{
			{Time: base.Add(-15 * time.Minute), Value: 48},
			{Time: base.Add(-10 * time.Minute), Value: 62},
			{Time: base.Add(-5 * time.Minute), Value: 84},
			{Time: base, Value: 93},
		},
		BreachStart: base.Add(-5 * time.Minute),
	})
	if err != nil {
		return nil, fmt.Errorf("hub: render notification test chart: %w", err)
	}
	return alerting.Message{
		Title:     "cloud-pulse: test notification",
		Text:      "This is a test notification from cloud-pulse's alert channel settings.",
		Image:     image,
		ImageName: "chart.png",
	}, nil
}

// handleTestExistingChannel sends a sample notification to a saved
// channel: POST /api/v1/alerts/channels/{id}/test (admin).
func (s *Server) handleTestExistingChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseAlertID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}
	ch, err := s.store.GetNotifyChannel(ctx, id)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "channel not found"})
			return
		}
		s.logger.Error("get notify channel failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	s.sendTestMessage(w, r, ch)
}

// handleTestDraftChannel sends a sample notification using an unsaved
// channel configuration from the request body, for the "Add channel"
// dialog's "Send test" button: POST /api/v1/alerts/channels/test
// (admin).
func (s *Server) handleTestDraftChannel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAlertBodyBytes)
	var req channelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}
	typ, details := validateChannelRequest(req)
	if len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "validation failed", Details: details})
		return
	}
	s.sendTestMessage(w, r, models.NotifyChannel{Name: req.Name, Type: typ, Enabled: true, Config: req.Config})
}

// sendTestMessage constructs a Sender for ch via s.opts.NotifyFactory
// and sends a fixed sample message, responding with the delivery
// outcome. A nil NotifyFactory (no notify stage wired yet) responds 501.
func (s *Server) sendTestMessage(w http.ResponseWriter, r *http.Request, ch models.NotifyChannel) {
	if s.opts.NotifyFactory == nil {
		writeJSON(w, http.StatusNotImplemented, models.APIError{Error: "notification sending is not configured on this hub"})
		return
	}
	sender, err := s.opts.NotifyFactory(ch)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid channel configuration: " + err.Error()})
		return
	}

	message, err := testMessage()
	if err != nil {
		s.logger.Error("render notification test chart failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "could not render test chart"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), alertNotifyTimeout)
	defer cancel()
	sendErr := sender.Send(ctx, message)

	if sendErr != nil {
		diagnosis := notify.DiagnoseError(sendErr)
		writeJSON(w, http.StatusOK, models.TestResult{OK: false, Diagnosis: &diagnosis})
		return
	}
	writeJSON(w, http.StatusOK, models.TestResult{OK: true, Delivery: fmt.Sprintf("sent a test %s notification to %q", ch.Type, ch.Name)})
}

// --- Rules ---

// handleListRules responds with every configured alert rule: GET
// /api/v1/alerts/rules (admin).
func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListAlertRules(r.Context())
	if err != nil {
		s.logger.Error("list alert rules failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// ruleRequest is the body of POST /api/v1/alerts/rules and PUT
// /api/v1/alerts/rules/{id}.
type ruleRequest struct {
	Name           string  `json:"name"`
	Enabled        bool    `json:"enabled"`
	Metric         string  `json:"metric"`
	HostID         string  `json:"host_id"`
	Operator       string  `json:"operator"`
	Threshold      float64 `json:"threshold"`
	DurationSec    int     `json:"duration_sec"`
	CooldownSec    *int    `json:"cooldown_sec"`
	NotifyResolved bool    `json:"notify_resolved"`
	ChannelIDs     []int64 `json:"channel_ids"`
}

var validAlertMetrics = map[models.AlertMetric]bool{
	models.AlertMetricCPU:          true,
	models.AlertMetricMemory:       true,
	models.AlertMetricDisk:         true,
	models.AlertMetricLoad1:        true,
	models.AlertMetricEgressOutPct: true,
	models.AlertMetricEgressInPct:  true,
	models.AlertMetricHostDown:     true,
}

var validAlertOperators = map[models.AlertOperator]bool{
	models.AlertOperatorGT:  true,
	models.AlertOperatorGTE: true,
}

// alertingDefaultCooldownSec mirrors internal/alerting.DefaultCooldownSec
// (3600), applied when a create/update request omits cooldown_sec
// entirely (distinguished from an explicit 0, which means "never
// re-notify" per AlertRule.CooldownSec's doc comment).
const alertingDefaultCooldownSec = 3600

// validateRuleRequest validates req, returning the constructed
// models.AlertRule (with CreatedAt/UpdatedAt/ID left zero for the
// caller to fill in) and any field-level validation errors.
func validateRuleRequest(req ruleRequest) (models.AlertRule, map[string]string) {
	details := make(map[string]string)
	if req.Name == "" {
		details["name"] = "must not be empty"
	}
	metric := models.AlertMetric(req.Metric)
	if !validAlertMetrics[metric] {
		details["metric"] = "must be one of cpu, memory, disk, load1, egress_out_pct, egress_in_pct, host_down"
	}
	op := models.AlertOperator(req.Operator)
	if !validAlertOperators[op] {
		details["operator"] = `must be ">" or ">="`
	}
	if req.DurationSec < 0 {
		details["duration_sec"] = "must not be negative"
	}
	if req.CooldownSec != nil && *req.CooldownSec < 0 {
		details["cooldown_sec"] = "must not be negative"
	}
	if req.HostID != "" && !models.ValidHostID(req.HostID) {
		details["host_id"] = "invalid host id"
	}

	cooldown := alertingDefaultCooldownSec
	if req.CooldownSec != nil {
		cooldown = *req.CooldownSec
	}
	channelIDs := req.ChannelIDs
	if channelIDs == nil {
		channelIDs = []int64{}
	}

	return models.AlertRule{
		Name:           req.Name,
		Enabled:        req.Enabled,
		Metric:         metric,
		HostID:         req.HostID,
		Operator:       op,
		Threshold:      req.Threshold,
		DurationSec:    req.DurationSec,
		CooldownSec:    cooldown,
		NotifyResolved: req.NotifyResolved,
		ChannelIDs:     channelIDs,
	}, details
}

// handleCreateRule creates a new alert rule: POST /api/v1/alerts/rules
// (admin).
func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxAlertBodyBytes)
	var req ruleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}
	rule, details := validateRuleRequest(req)
	if len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "validation failed", Details: details})
		return
	}

	created, err := s.store.CreateAlertRule(ctx, rule)
	if err != nil {
		s.logger.Error("create alert rule failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, created)
}

// handleUpdateRule updates an existing alert rule: PUT
// /api/v1/alerts/rules/{id} (admin).
func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseAlertID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAlertBodyBytes)
	var req ruleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}
	rule, details := validateRuleRequest(req)
	if len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "validation failed", Details: details})
		return
	}
	rule.ID = id

	updated, err := s.store.UpdateAlertRule(ctx, rule)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "rule not found"})
			return
		}
		s.logger.Error("update alert rule failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleDeleteRule deletes an alert rule and its persisted state-machine
// rows: DELETE /api/v1/alerts/rules/{id} (admin).
func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseAlertID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}
	if err := s.store.DeleteAlertRule(ctx, id); err != nil {
		s.logger.Error("delete alert rule failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	if err := s.store.DeleteAlertStatesForRule(ctx, id); err != nil {
		s.logger.Error("delete alert states for rule failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePreviewRule reports whether an existing rule is currently
// satisfied for each host: POST /api/v1/alerts/rules/{id}/preview
// (admin). A nil Options.Alerting (no engine wired) responds 501.
func (s *Server) handlePreviewRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseAlertID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}

	rule, err := s.store.GetAlertRule(ctx, id)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "rule not found"})
			return
		}
		s.logger.Error("get alert rule failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.previewRule(w, r, rule)
}

// previewRule runs rule's condition against every current host snapshot
// via s.opts.Alerting.Preview.
func (s *Server) previewRule(w http.ResponseWriter, r *http.Request, rule models.AlertRule) {
	if s.opts.Alerting == nil {
		writeJSON(w, http.StatusNotImplemented, models.APIError{Error: "alert evaluation is not configured on this hub"})
		return
	}

	ctx := r.Context()
	now := s.opts.now()
	hosts, err := s.hostSnapshots(ctx, now)
	if err != nil {
		s.logger.Error("build host snapshots for preview failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	result, err := s.opts.Alerting.Preview(ctx, now, rule, hosts)
	if err != nil {
		s.logger.Error("preview rule failed", "rule_id", rule.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// --- Events ---

// handleListAlertEvents responds with paginated alert events: GET
// /api/v1/alerts/events?state=&host=&limit=&before= (SPEC-v0.5 §B).
func (s *Server) handleListAlertEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	state := models.AlertEventState(q.Get("state"))
	if state != "" && state != models.AlertEventFiring && state != models.AlertEventResolved {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid state"})
		return
	}
	hostID := q.Get("host")

	limit := defaultEventsPageLimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid limit"})
			return
		}
		limit = parsed
	}
	if limit > maxEventsPageLimit {
		limit = maxEventsPageLimit
	}

	var before int64
	if raw := q.Get("before"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid before"})
			return
		}
		before = parsed
	}

	events, err := s.store.ListAlertEvents(r.Context(), state, hostID, before, limit)
	if err != nil {
		s.logger.Error("list alert events failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleListActiveAlerts responds with every currently-firing event:
// GET /api/v1/alerts/active — used by the dashboard's navbar bell.
func (s *Server) handleListActiveAlerts(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListActiveAlertEvents(r.Context())
	if err != nil {
		s.logger.Error("list active alert events failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	sort.Slice(events, func(i, j int) bool { return events[i].StartedAt > events[j].StartedAt })
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// parseAlertID parses a path value into an alert rule/channel/event ID.
func parseAlertID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id %q: %w", raw, err)
	}
	return id, nil
}
