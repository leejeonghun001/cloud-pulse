package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// maxAdminBodyBytes bounds the size of admin request bodies (limits and
// webhook settings are tiny JSON payloads).
const maxAdminBodyBytes = 16 << 10 // 16 KiB

// maxLimitBytes is the largest value accepted for a host's
// egress/ingress limit override: uint64 values above this cannot be
// stored in SQLite's signed 64-bit INTEGER column.
const maxLimitBytes = uint64(1) << 62

// maxWebhookURLLen is the largest accepted length for a webhook URL.
const maxWebhookURLLen = 2048

// installCommandTemplate is the install-agent.sh one-liner returned by
// the agent-token reveal endpoint. hubURL and token are substituted.
const installCommandTemplate = "curl -fsSL https://raw.githubusercontent.com/leejeonghun001/cloud-pulse/main/scripts/install-agent.sh | sudo bash -s -- --hub-url %s --token %s"

// validHostHeader matches an http.Request.Host value safe to embed in a
// generated shell command: hostnames/IPv6 literals plus an optional
// ":port", nothing else.
var validHostHeader = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]+$`)

// handleGetSettings responds with the hub's current configuration and
// per-host limits: GET /api/v1/settings (admin).
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	view, err := s.buildSettingsView(ctx)
	if err != nil {
		s.logger.Error("get settings failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// buildSettingsView assembles a models.SettingsView from current
// configuration, stored settings, hosts, and limits. It makes exactly
// one ListHosts and one ListHostLimits call (no N+1).
func (s *Server) buildSettingsView(ctx context.Context) (models.SettingsView, error) {
	webhookURL, webhookSource, err := s.effectiveWebhookURL(ctx)
	if err != nil {
		return models.SettingsView{}, err
	}

	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		return models.SettingsView{}, fmt.Errorf("hub: list hosts for settings: %w", err)
	}
	limits, err := s.store.ListHostLimits(ctx)
	if err != nil {
		return models.SettingsView{}, fmt.Errorf("hub: list host limits for settings: %w", err)
	}
	limitsByHost := indexLimitsByHost(limits)

	hostViews := make([]models.HostLimitsView, 0, len(hosts))
	for _, h := range hosts {
		l := limitsByHost[h.Info.ID]
		txLimit, rxLimit, _, _ := models.EffectiveLimits(h.Info.EgressLimitBytes, l)
		hostViews = append(hostViews, models.HostLimitsView{
			HostID:                     h.Info.ID,
			Hostname:                   h.Info.Hostname,
			Provider:                   h.Info.Provider,
			AgentEgressLimitBytes:      h.Info.EgressLimitBytes,
			EgressLimitBytes:           l.EgressLimitBytes,
			IngressLimitBytes:          l.IngressLimitBytes,
			EffectiveEgressLimitBytes:  txLimit,
			EffectiveIngressLimitBytes: rxLimit,
		})
	}
	sort.Slice(hostViews, func(i, j int) bool { return hostViews[i].Hostname < hostViews[j].Hostname })

	return models.SettingsView{
		Version:              version.Version,
		AllowedCIDRs:         formatAllowedCIDRsForView(s.opts.AllowedCIDRs),
		OfflineAfterSeconds:  int64(s.opts.OfflineAfter.Seconds()),
		CloudIntervalSeconds: int64(s.opts.CloudInterval.Seconds()),
		UIAuthEnabled:        s.opts.UIToken != "",
		AgentTokenHint:       tokenHint(s.opts.AgentToken),
		AlertWebhookURL:      webhookURL,
		AlertWebhookSource:   webhookSource,
		Hosts:                hostViews,
	}, nil
}

// formatAllowedCIDRsForView renders cidrs for models.SettingsView: ["*"]
// when the allowlist is disabled (nil, allow-all), otherwise each
// prefix's string form.
func formatAllowedCIDRsForView(cidrs []netip.Prefix) []string {
	if cidrs == nil {
		return []string{"*"}
	}
	out := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, c.String())
	}
	return out
}

// tokenHint returns the first 4 + "…" + last 4 characters of token. For
// tokens shorter than 9 characters (shouldn't occur given the >=16-char
// minimum enforced by config.LoadHub, but handled defensively) it
// returns a fully-masked placeholder rather than the token itself.
func tokenHint(token string) string {
	if len(token) < 9 {
		return "••••"
	}
	return token[:4] + "…" + token[len(token)-4:]
}

// handleGetAgentToken responds with the full agent token and a
// ready-to-run install command: GET /api/v1/settings/agent-token
// (admin). The token is never logged; only its reveal is logged, at
// info level, with the requester's remote address.
func (s *Server) handleGetAgentToken(w http.ResponseWriter, r *http.Request) {
	s.logger.Info("agent token revealed", "remote", r.RemoteAddr)

	writeJSON(w, http.StatusOK, models.AgentTokenView{
		AgentToken:     s.opts.AgentToken,
		InstallCommand: installCommand(r, s.opts.AgentToken),
	})
}

// installCommand builds the install-agent.sh one-liner using the
// request's scheme (from r.TLS, never X-Forwarded-*) and Host header.
// An invalid/unsafe Host value (fails validHostHeader or exceeds 255
// characters) is replaced with the placeholder "<HUB_URL>" rather than
// ever interpolating untrusted, unvalidated request data into a
// generated shell command.
func installCommand(r *http.Request, token string) string {
	hubURL := "<HUB_URL>"
	if isValidHostHeader(r.Host) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		hubURL = scheme + "://" + r.Host
	}
	return fmt.Sprintf(installCommandTemplate, hubURL, token)
}

// isValidHostHeader reports whether host is safe to embed unescaped in
// a generated shell command: at most 255 characters, drawn only from
// [A-Za-z0-9.:\[\]-].
func isValidHostHeader(host string) bool {
	return host != "" && len(host) <= 255 && validHostHeader.MatchString(host)
}

// hostLimitsRequest is the body of PUT /api/v1/hosts/{id}/limits. An
// absent field decodes as a nil pointer, meaning "clear this override".
type hostLimitsRequest struct {
	EgressLimitBytes  *uint64 `json:"egress_limit_bytes"`
	IngressLimitBytes *uint64 `json:"ingress_limit_bytes"`
}

// handleSetHostLimits sets or clears a host's outbound/inbound limit
// overrides: PUT /api/v1/hosts/{id}/limits (admin).
func (s *Server) handleSetHostLimits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	if _, err := s.store.GetHost(ctx, id); err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "host not found"})
			return
		}
		s.logger.Error("get host failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	var req hostLimitsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	if req.EgressLimitBytes != nil && *req.EgressLimitBytes > maxLimitBytes {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "egress_limit_bytes exceeds maximum storable value"})
		return
	}
	if req.IngressLimitBytes != nil && *req.IngressLimitBytes > maxLimitBytes {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "ingress_limit_bytes exceeds maximum storable value"})
		return
	}

	limits := models.HostLimits{
		HostID:            id,
		EgressLimitBytes:  req.EgressLimitBytes,
		IngressLimitBytes: req.IngressLimitBytes,
	}
	if err := s.store.SetHostLimits(ctx, limits); err != nil {
		s.logger.Error("set host limits failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	stored, err := s.store.GetHostLimits(ctx, id)
	if err != nil {
		s.logger.Error("get host limits failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, stored)
}

// alertWebhookRequest is the body of PUT /api/v1/settings/alerts.
type alertWebhookRequest struct {
	WebhookURL string `json:"webhook_url"`
}

// handleSetAlertWebhook sets or clears the hub-side webhook URL
// override: PUT /api/v1/settings/alerts (admin). An empty webhook_url
// clears the override, falling back to CP_ALERT_WEBHOOK_URL (or "none").
func (s *Server) handleSetAlertWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	var req alertWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	if req.WebhookURL != "" {
		if err := validateAdminWebhookURL(req.WebhookURL); err != nil {
			writeJSON(w, http.StatusBadRequest, models.APIError{Error: err.Error()})
			return
		}
	}

	if err := s.store.SetSetting(ctx, SettingWebhookURL, req.WebhookURL); err != nil {
		s.logger.Error("set alert webhook failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	view, err := s.buildSettingsView(ctx)
	if err != nil {
		s.logger.Error("get settings failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// validateAdminWebhookURL validates a non-empty webhook URL: http(s)
// scheme, non-empty host, at most maxWebhookURLLen characters, and no
// whitespace or control characters.
func validateAdminWebhookURL(raw string) error {
	if len(raw) > maxWebhookURLLen {
		return fmt.Errorf("webhook_url exceeds maximum length of %d characters", maxWebhookURLLen)
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f {
			return fmt.Errorf("webhook_url must not contain whitespace or control characters")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("webhook_url: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook_url must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("webhook_url missing host")
	}
	return nil
}

// handleTestAlertWebhook sends a test notification to the effective
// webhook URL: POST /api/v1/settings/alerts/test (admin).
func (s *Server) handleTestAlertWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	url, _, err := s.effectiveWebhookURL(ctx)
	if err != nil {
		s.logger.Error("resolve webhook url failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	if url == "" {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "no webhook url configured"})
		return
	}

	notifier := s.resolveNotifier(url)
	notifyCtx, cancel := context.WithTimeout(ctx, alertNotifyTimeout)
	defer cancel()
	if err := notifier.Notify(notifyCtx, "cloud-pulse: test notification",
		"This is a test notification from cloud-pulse's hub settings page."); err != nil {
		s.logger.Error("test webhook notify failed", "error", err)
		writeJSON(w, http.StatusBadGateway, models.APIError{Error: "failed to send test notification: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}
