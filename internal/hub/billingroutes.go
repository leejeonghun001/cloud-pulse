package hub

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// registerBillingRoutes registers the cloud billing API (SPEC-v0.6 §1):
// GET /api/v1/billing, POST /api/v1/billing/refresh, and the polling
// interval setting.
func (s *Server) registerBillingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/billing", s.requireUser(s.handleGetBilling))
	mux.HandleFunc("POST /api/v1/billing/refresh", s.requireAdmin(s.handlePostBillingRefresh))
	mux.HandleFunc("PUT /api/v1/settings/billing/interval", s.requireAdmin(s.handleSetBillingInterval))
	mux.HandleFunc("PUT /api/v1/billing/accounts/label", s.requireAdmin(s.handleSetBillingAccountLabel))
	mux.HandleFunc("DELETE /api/v1/billing/accounts", s.requireAdmin(s.handleDeleteBillingAccount))
}

// Audit actions for billing accounts.
const (
	AuditActionBillingAccountLabel  models.AuditAction = "billing_account.label"
	AuditActionBillingAccountDelete models.AuditAction = "billing_account.delete"
)

// maxBillingAccountLabel bounds an account display label, in bytes.
const maxBillingAccountLabel = 64

// billingAccountLabelRequest is the body of PUT
// /api/v1/billing/accounts/label.
type billingAccountLabelRequest struct {
	Provider  models.CloudBillingProvider `json:"provider"`
	AccountID string                      `json:"account_id"`
	Label     string                      `json:"label"`
}

// handleSetBillingAccountLabel names a billing account (admin). An
// empty label clears it.
func (s *Server) handleSetBillingAccountLabel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req billingAccountLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body", Code: "invalid_request_body"})
		return
	}
	label := strings.TrimSpace(req.Label)
	if !utf8.ValidString(label) || len(label) > maxBillingAccountLabel {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "label must be valid text of at most 64 bytes", Code: "invalid_label"})
		return
	}
	before, err := s.store.GetCloudCostSnapshot(ctx, req.Provider, req.AccountID)
	if err != nil {
		s.logger.Error("billing: get account for label failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	if err := s.store.SetCloudCostLabel(ctx, req.Provider, req.AccountID, label); err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "billing account not found", Code: "account_not_found"})
			return
		}
		s.logger.Error("billing: set account label failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	key := models.CloudCostKey(req.Provider, req.AccountID)
	s.recordAudit(ctx, r, AuditActionBillingAccountLabel, "billing_account", key,
		map[string]string{"label": before.Label}, map[string]string{"label": label})
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "label": label})
}

// handleDeleteBillingAccount removes a billing account's stored figures
// (admin): DELETE /api/v1/billing/accounts?provider=&account_id=. An
// agent still reporting the account recreates it on its next report.
func (s *Server) handleDeleteBillingAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	provider := models.CloudBillingProvider(r.URL.Query().Get("provider"))
	accountID := r.URL.Query().Get("account_id")
	if provider != models.CloudBillingAWS && provider != models.CloudBillingOCI {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "provider must be aws or oci", Code: "invalid_provider"})
		return
	}
	if err := s.store.DeleteCloudCostSnapshot(ctx, provider, accountID); err != nil {
		s.logger.Error("billing: delete account failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	key := models.CloudCostKey(provider, accountID)
	s.recordAudit(ctx, r, AuditActionBillingAccountDelete, "billing_account", key, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleGetBilling serves GET /api/v1/billing: every provider's
// persisted snapshot, combined with the current host list (matching
// each host's CloudInstanceID against its provider's snapshot) and the
// network-cost stage's per-host estimate hook.
func (s *Server) handleGetBilling(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var snapshots []models.CloudCostSnapshot
	var intervalSeconds int64
	var estimator billing.NetworkEstimator

	if s.opts.Billing != nil {
		var err error
		snapshots, err = s.store.ListCloudCostSnapshots(ctx)
		if err != nil {
			s.logger.Error("billing: list cloud cost snapshots failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
			return
		}
		interval := s.resolveBillingInterval(ctx)
		intervalSeconds = int64(billingIntervalDuration(interval).Seconds())
		estimator = s.opts.Billing.networkEstimator()
	}

	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		s.logger.Error("billing: list hosts failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}

	display, err := s.loadDisplayCurrencySettings(ctx)
	if err != nil {
		s.logger.Error("billing: load display currency settings failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}

	view := billing.BuildBillingView(snapshots, hosts, estimator, intervalSeconds, display)
	writeJSON(w, http.StatusOK, view)
}

// handlePostBillingRefresh serves POST /api/v1/billing/refresh (admin).
// Providers collected by an agent (OCI via instance principal) are
// refreshed by asking that agent to query again on its next report
// (throttled per host by agentRefreshMinInterval); the hub's own CLI
// collection runs when its 10-minute throttle allows (SPEC-v0.6 §1:
// AWS Cost Explorer bills per call). 429 only when neither could run.
func (s *Server) handlePostBillingRefresh(w http.ResponseWriter, r *http.Request) {
	if s.opts.Billing == nil {
		writeJSON(w, http.StatusNotImplemented, models.APIError{Error: "billing is not enabled on this hub", Code: "billing_not_enabled"})
		return
	}
	ctx := r.Context()
	now := s.opts.now()

	stored, err := s.store.ListCloudCostSnapshots(ctx)
	if err != nil {
		s.logger.Error("billing: list snapshots for refresh failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	var agentHosts []string
	for _, snap := range stored {
		if snap.SourceHost != "" && !slices.Contains(agentHosts, snap.SourceHost) {
			agentHosts = append(agentHosts, snap.SourceHost)
		}
	}
	requested, agentRetry := s.opts.Billing.requestAgentRefresh(agentHosts, now)

	allowed, hubRetry := s.opts.Billing.refreshAllowed()
	if !allowed && len(requested) == 0 {
		retry := hubRetry
		if agentRetry > 0 && agentRetry < retry {
			retry = agentRetry
		}
		rateLimited(w, int(retry.Seconds()))
		return
	}

	snapshots := stored
	if allowed {
		interval := s.resolveBillingInterval(ctx)
		snapshots, err = s.opts.Billing.run(ctx, interval)
		s.opts.Billing.refreshGate().Record()
		if err != nil {
			s.logger.Error("billing: manual refresh failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
			return
		}
		s.evaluateCostAlerts(ctx, now)
	}
	if requested == nil {
		requested = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshots":               snapshots,
		"hub_polled":              allowed,
		"agent_refresh_requested": requested,
	})
}

// AuditActionBillingIntervalChange records a change to the hub-side
// cloud billing polling interval (SPEC-v0.6 §1 / §3 개선 c).
const AuditActionBillingIntervalChange models.AuditAction = "billing_settings.interval_change"

// billingIntervalRequest is the body of PUT
// /api/v1/settings/billing/interval.
type billingIntervalRequest struct {
	Interval models.BillingInterval `json:"interval"`
}

// handleSetBillingInterval sets the hub-side cloud billing polling
// interval override: PUT /api/v1/settings/billing/interval (admin).
// Only "6h", "12h", or "24h" are accepted (SPEC-v0.6 §1); any other
// value is rejected with 400. The change takes effect on the
// scheduler's very next tick (RunBillingLoop re-reads the effective
// interval before each wait) — no restart required.
func (s *Server) handleSetBillingInterval(w http.ResponseWriter, r *http.Request) {
	if s.opts.Billing == nil {
		writeJSON(w, http.StatusNotImplemented, models.APIError{Error: "billing is not enabled on this hub", Code: "billing_not_enabled"})
		return
	}

	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxPricingBodyBytes)
	var req billingIntervalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body", Code: "invalid_request_body"})
		return
	}
	if !models.ValidBillingInterval(req.Interval) {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "interval must be one of 6h, 12h, 24h", Code: "invalid_interval"})
		return
	}

	before := s.resolveBillingInterval(ctx)
	if err := s.store.SetSetting(ctx, SettingBillingInterval, string(req.Interval)); err != nil {
		s.logger.Error("set billing interval setting failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}

	s.recordAudit(ctx, r, AuditActionBillingIntervalChange, "billing_settings", "",
		map[string]string{"interval": string(before)},
		map[string]string{"interval": string(req.Interval)},
	)
	writeJSON(w, http.StatusOK, map[string]string{"interval": string(req.Interval)})
}
