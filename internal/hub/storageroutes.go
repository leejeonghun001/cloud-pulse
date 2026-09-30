// storageroutes.go registers the storage-usage account API (SPEC-v0.7
// §3: Google Drive / Dropbox connected-account usage). GET
// /api/v1/storage/accounts (list, secrets redacted, combined with each
// account's latest snapshot) has no external-provider dependency and
// works even with Options.Storage == nil. The account CRUD, OAuth
// start/complete, refresh, and interval-setting endpoints are the
// "storage" v0.7.0 stage's implementation, replacing the prep stage's
// 501 stubs; see storagerun.go for the background poller/OAuth
// flow-state runtime these handlers drive.
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage/dropbox"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage/googledrive"
)

// maxStorageBodyBytes bounds the size of storage-account request
// bodies, mirroring maxPricingBodyBytes/maxAlertBodyBytes.
const maxStorageBodyBytes = 16 << 10 // 16 KiB

// Audit actions for storage-account changes (SPEC-v0.7 §3: "감사 로그:
// 계정 추가/삭제/연결, 주기 변경").
const (
	AuditActionStorageAccountCreate  models.AuditAction = "storage_account.create"
	AuditActionStorageAccountDelete  models.AuditAction = "storage_account.delete"
	AuditActionStorageAccountLink    models.AuditAction = "storage_account.link"
	AuditActionStorageIntervalChange models.AuditAction = "storage_settings.interval_change"
)

// registerStorageRoutes registers the storage-usage account API
// (SPEC-v0.7 §3).
func (s *Server) registerStorageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/storage/accounts", s.requireUser(s.handleListStorageAccounts))
	mux.HandleFunc("POST /api/v1/storage/accounts", s.requireAdmin(s.handleCreateStorageAccount))
	mux.HandleFunc("DELETE /api/v1/storage/accounts/{id}", s.requireAdmin(s.handleDeleteStorageAccount))
	mux.HandleFunc("POST /api/v1/storage/accounts/{id}/oauth/start", s.requireAdmin(s.handleStorageOAuthStart))
	mux.HandleFunc("POST /api/v1/storage/accounts/{id}/oauth/complete", s.requireAdmin(s.handleStorageOAuthComplete))
	mux.HandleFunc("POST /api/v1/storage/refresh", s.requireAdmin(s.handleStorageRefresh))
	mux.HandleFunc("PUT /api/v1/settings/storage/interval", s.requireAdmin(s.handleSetStorageInterval))
}

// handleListStorageAccounts responds with every configured storage
// account (secrets redacted) plus its latest snapshot, if any: GET
// /api/v1/storage/accounts.
func (s *Server) handleListStorageAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	accounts, err := s.store.ListStorageAccounts(ctx)
	if err != nil {
		s.logger.Error("storage: list accounts failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	snapshots, err := s.store.ListStorageAccountSnapshots(ctx)
	if err != nil {
		s.logger.Error("storage: list snapshots failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	snapByAccount := make(map[int64]models.StorageAccountSnapshot, len(snapshots))
	for _, snap := range snapshots {
		snapByAccount[snap.AccountID] = snap
	}

	views := make([]models.StorageAccountView, 0, len(accounts))
	for _, a := range accounts {
		view := models.StorageAccountView{Account: a.Redacted()}
		if snap, ok := snapByAccount[a.ID]; ok {
			view.Snapshot = &snap
		}
		views = append(views, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{"accounts": views})
}

// parseStorageAccountID parses r's {id} path value.
func parseStorageAccountID(raw string) (int64, error) {
	return strconv.ParseInt(raw, 10, 64)
}

// validStorageProviders lists the providers a create request may name.
var validStorageProviders = map[models.StorageAccountProvider]bool{
	models.StorageAccountGoogleDrive: true,
	models.StorageAccountDropbox:     true,
}

// handleCreateStorageAccount creates a new account shell (before OAuth
// completes): POST /api/v1/storage/accounts (admin). Config carries
// provider-specific non-secret fields: Google needs "client_id" and
// "client_secret" (a "TVs and Limited Input devices" OAuth client
// still has a client_secret even though the device flow's user-facing
// steps don't expose it — see notes/v07-storage.md); Dropbox needs only
// "app_key" (PKCE has no client secret).
func (s *Server) handleCreateStorageAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxStorageBodyBytes)
	var req models.StorageAccountCreate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	details := validateStorageAccountCreate(req)
	if len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "validation failed", Details: details})
		return
	}

	// client_secret (Google) travels in the create request's Secret
	// map already, per StorageAccountCreate's doc comment — split
	// config/secret at the model boundary, not re-derived here.
	config := req.Config
	if config == nil {
		config = map[string]string{}
	}
	secret := req.Secret
	if secret == nil {
		secret = map[string]string{}
	}

	created, err := s.store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: req.Provider,
		Name:     req.Name,
		Config:   config,
		Secret:   secret,
	})
	if err != nil {
		s.logger.Error("storage: create account failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.recordAudit(ctx, r, AuditActionStorageAccountCreate, "storage_account", strconv.FormatInt(created.ID, 10),
		nil, map[string]string{"provider": string(created.Provider), "name": created.Name})

	writeJSON(w, http.StatusOK, created.Redacted())
}

func validateStorageAccountCreate(req models.StorageAccountCreate) map[string]string {
	details := make(map[string]string)
	if !validStorageProviders[req.Provider] {
		details["provider"] = "must be one of googledrive, dropbox"
	}
	if req.Name == "" {
		details["name"] = "must not be empty"
	}
	switch req.Provider {
	case models.StorageAccountGoogleDrive:
		if req.Config["client_id"] == "" {
			details["config.client_id"] = "required for googledrive"
		}
		if req.Secret["client_secret"] == "" {
			details["secret.client_secret"] = "required for googledrive"
		}
	case models.StorageAccountDropbox:
		if req.Config["app_key"] == "" {
			details["config.app_key"] = "required for dropbox"
		}
	}
	return details
}

// handleDeleteStorageAccount revokes the account's OAuth token
// best-effort, then deletes the row and its snapshot: DELETE
// /api/v1/storage/accounts/{id} (admin).
func (s *Server) handleDeleteStorageAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parseStorageAccountID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}

	existing, err := s.store.GetStorageAccount(ctx, id)
	if err != nil {
		if err == models.ErrNotFound {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "storage account not found"})
			return
		}
		s.logger.Error("storage: get account for delete failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	if s.opts.Storage != nil {
		s.opts.Storage.clearFlow(id)
		if provider, ok := s.opts.Storage.storageRegistry().Get(string(existing.Provider)); ok && len(existing.Secret) > 0 {
			if revokeErr := provider.RevokeToken(ctx, existing.Config, existing.Secret); revokeErr != nil {
				s.logger.Warn("storage: revoke token on delete failed (deleting the account anyway)",
					"account_id", id, "provider", existing.Provider, "error", revokeErr)
			}
		}
	}

	if err := s.store.DeleteStorageAccount(ctx, id); err != nil {
		s.logger.Error("storage: delete account failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.recordAudit(ctx, r, AuditActionStorageAccountDelete, "storage_account", strconv.FormatInt(id, 10),
		map[string]string{"provider": string(existing.Provider), "name": existing.Name}, nil)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleStorageOAuthStart begins the OAuth flow for accountID: POST
// /api/v1/storage/accounts/{id}/oauth/start (admin). For Google, this
// requests a device code and starts a background polling goroutine
// (the hub itself polls Google's token endpoint, per SPEC-v0.7 §3 —
// the client only ever calls oauth/complete to read the current
// status). For Dropbox, this returns a PKCE authorize URL for the user
// to open in a browser; the resulting pasted code is submitted via
// oauth/complete.
func (s *Server) handleStorageOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.opts.Storage == nil {
		writeJSON(w, http.StatusNotImplemented, models.APIError{Error: "storage accounts are not enabled on this hub"})
		return
	}
	ctx := r.Context()
	id, err := parseStorageAccountID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}
	acct, err := s.store.GetStorageAccount(ctx, id)
	if err != nil {
		if err == models.ErrNotFound {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "storage account not found"})
			return
		}
		s.logger.Error("storage: get account for oauth start failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	switch acct.Provider {
	case models.StorageAccountGoogleDrive:
		s.startGoogleDeviceFlow(w, r, acct)
	case models.StorageAccountDropbox:
		s.startDropboxPKCEFlow(w, acct)
	default:
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "unsupported provider"})
	}
}

// storageOAuthHTTPClient returns the runtime's configured HTTP client,
// defaulting to http.DefaultClient for a runtime constructed without
// one (tests).
func (s *Server) storageOAuthHTTPClient() storageusage.HTTPDoer {
	if s.opts.Storage != nil && s.opts.Storage.HTTPClient != nil {
		return s.opts.Storage.HTTPClient
	}
	return http.DefaultClient
}

// startGoogleDeviceFlow implements the Google side of oauth/start: get
// a device code, then launch a background goroutine that polls Google
// until the user approves/denies, the code expires, or the account is
// deleted (ctx canceled via clearFlow).
func (s *Server) startGoogleDeviceFlow(w http.ResponseWriter, r *http.Request, acct models.StorageAccount) {
	clientID := acct.Config["client_id"]
	clientSecret := acct.Secret["client_secret"]
	if clientID == "" || clientSecret == "" {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "account is missing config.client_id/secret.client_secret"})
		return
	}

	client := googledrive.New(s.storageOAuthHTTPClient(), s.googleSSRFChecker)
	resp, err := client.RequestDeviceCode(r.Context(), clientID)
	if err != nil {
		s.logger.Error("storage: request google device code failed", "account_id", acct.ID, "error", err)
		writeJSON(w, http.StatusBadGateway, models.APIError{Error: "failed to start google device flow"})
		return
	}

	flow := s.opts.Storage.flowState(acct.ID, acct.Provider)
	flow.pollMu.Lock()
	flow.deviceCode = resp.DeviceCode
	flow.pollIntervalSec = resp.Interval
	flow.expiresAt = googledrive.DeviceFlowExpiry(s.opts.Storage.now(), resp)
	flow.pollResult = nil
	pollCtx, cancel := context.WithTimeout(context.Background(), googleDevicePollTimeout)
	flow.cancelPoll = cancel
	flow.pollDone = make(chan struct{})
	flow.pollMu.Unlock()

	go s.runGoogleDevicePoll(pollCtx, flow, client, acct, clientID, clientSecret, resp.Interval)

	writeJSON(w, http.StatusOK, models.StorageOAuthStartResponse{
		Flow:            "device",
		VerificationURL: resp.VerificationURL,
		UserCode:        resp.UserCode,
		ExpiresInSec:    resp.ExpiresIn,
		PollIntervalSec: resp.Interval,
	})
}

// runGoogleDevicePoll polls Google's token endpoint on intervalSec
// (growing on slow_down per RFC 8628) until a terminal outcome, storing
// the result on flow for handleStorageOAuthComplete to read, and — on
// success — persisting the refresh token into the account's Secret and
// triggering an immediate quota collection.
func (s *Server) runGoogleDevicePoll(ctx context.Context, flow *oauthFlowState, client *googledrive.Client,
	acct models.StorageAccount, clientID, clientSecret string, intervalSec int) {
	defer close(flow.pollDone)

	if intervalSec <= 0 {
		intervalSec = 5
	}
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		flow.pollMu.Lock()
		deviceCode := flow.deviceCode
		flow.pollMu.Unlock()

		res := client.Poll(ctx, clientID, clientSecret, deviceCode)
		switch res.Outcome {
		case googledrive.PollGranted:
			s.completeGoogleDeviceFlow(ctx, acct, res)
			flow.pollMu.Lock()
			flow.pollResult = &deviceFlowResult{status: "connected"}
			flow.pollMu.Unlock()
			return
		case googledrive.PollAuthorizationPending:
			continue
		case googledrive.PollSlowDown:
			intervalSec += 5
			ticker.Reset(time.Duration(intervalSec) * time.Second)
			continue
		case googledrive.PollAccessDenied:
			flow.pollMu.Lock()
			flow.pollResult = &deviceFlowResult{status: "denied"}
			flow.pollMu.Unlock()
			return
		case googledrive.PollExpired:
			flow.pollMu.Lock()
			flow.pollResult = &deviceFlowResult{status: "expired"}
			flow.pollMu.Unlock()
			return
		default:
			flow.pollMu.Lock()
			flow.pollResult = &deviceFlowResult{status: "error", detail: res.ErrorDetail}
			flow.pollMu.Unlock()
			return
		}
	}
}

// completeGoogleDeviceFlow persists the granted refresh token into the
// account's Secret and immediately collects a quota snapshot.
func (s *Server) completeGoogleDeviceFlow(ctx context.Context, acct models.StorageAccount, res googledrive.PollResult) {
	acct.Secret["refresh_token"] = res.RefreshToken
	updated, err := s.store.UpdateStorageAccount(ctx, acct)
	if err != nil {
		s.logger.Error("storage: persist google refresh token failed", "account_id", acct.ID, "error", err)
		return
	}
	if s.opts.Storage != nil {
		s.collectOneStorageAccount(ctx, updated, s.opts.Storage.now())
	}
}

// googleSSRFChecker validates a URL against storageusage's official
// Google hosts, unless s.opts.Storage.AllowCustomEndpoints is set (test/
// smoke harness only — see config.Hub.StorageAllowCustomEndpoints),
// in which case a configured RedirectBase takes over routing entirely.
func (s *Server) googleSSRFChecker(rawURL string) (*url.URL, error) {
	if s.opts.Storage == nil {
		return storageusage.ValidateGoogleURL(rawURL)
	}
	return storageusage.ValidateGoogleURLWithOptions(rawURL, s.opts.Storage.AllowCustomEndpoints, s.opts.Storage.FakeBaseURL)
}

// startDropboxPKCEFlow implements the Dropbox side of oauth/start:
// generate a fresh PKCE verifier, store it in transient flow state, and
// return the authorize URL for the user to open.
func (s *Server) startDropboxPKCEFlow(w http.ResponseWriter, acct models.StorageAccount) {
	appKey := acct.Config["app_key"]
	if appKey == "" {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "account is missing config.app_key"})
		return
	}

	state, err := dropbox.NewPKCEState()
	if err != nil {
		s.logger.Error("storage: generate dropbox pkce state failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	flow := s.opts.Storage.flowState(acct.ID, acct.Provider)
	flow.pollMu.Lock()
	flow.pkceVerifier = state.CodeVerifier
	flow.pollMu.Unlock()

	writeJSON(w, http.StatusOK, models.StorageOAuthStartResponse{
		Flow:         "pkce",
		AuthorizeURL: dropbox.AuthorizeURL(appKey, state),
	})
}

// handleStorageOAuthComplete completes or reports progress on
// accountID's OAuth flow: POST
// /api/v1/storage/accounts/{id}/oauth/complete (admin). For Dropbox,
// req.Code is the user-pasted authorization code, exchanged
// synchronously. For Google, this endpoint takes no meaningful body —
// it reports whatever the background device-flow poller
// (started by oauth/start) has concluded so far.
func (s *Server) handleStorageOAuthComplete(w http.ResponseWriter, r *http.Request) {
	if s.opts.Storage == nil {
		writeJSON(w, http.StatusNotImplemented, models.APIError{Error: "storage accounts are not enabled on this hub"})
		return
	}
	ctx := r.Context()
	id, err := parseStorageAccountID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}
	acct, err := s.store.GetStorageAccount(ctx, id)
	if err != nil {
		if err == models.ErrNotFound {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "storage account not found"})
			return
		}
		s.logger.Error("storage: get account for oauth complete failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxStorageBodyBytes)
	var req models.StorageOAuthCompleteRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // Google's poll-status path has no meaningful body; ignore decode errors on an empty body

	switch acct.Provider {
	case models.StorageAccountDropbox:
		s.completeDropboxPKCEFlow(w, r, acct, req.Code)
	case models.StorageAccountGoogleDrive:
		s.reportGoogleDeviceFlowStatus(w, acct)
	default:
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "unsupported provider"})
	}
}

// completeDropboxPKCEFlow exchanges the user-pasted code for tokens
// using the stored PKCE verifier, persists the refresh token, and
// triggers an immediate quota collection.
func (s *Server) completeDropboxPKCEFlow(w http.ResponseWriter, r *http.Request, acct models.StorageAccount, code string) {
	if code == "" {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "code is required"})
		return
	}

	flow := s.opts.Storage.flowState(acct.ID, acct.Provider)
	flow.pollMu.Lock()
	verifier := flow.pkceVerifier
	flow.pollMu.Unlock()
	if verifier == "" {
		writeJSON(w, http.StatusConflict, models.APIError{Error: "no in-progress dropbox oauth flow for this account; call oauth/start first"})
		return
	}

	client := dropbox.New(s.storageOAuthHTTPClient(), s.dropboxSSRFChecker)
	res, err := client.ExchangeCode(r.Context(), acct.Config["app_key"], code, dropboxPKCEState(verifier))
	if err != nil {
		s.logger.Error("storage: dropbox code exchange failed", "account_id", acct.ID, "error", err)
		writeJSON(w, http.StatusBadGateway, models.APIError{Error: "code exchange failed; the code may be invalid or expired"})
		return
	}

	if acct.Secret == nil {
		acct.Secret = map[string]string{}
	}
	acct.Secret["refresh_token"] = res.RefreshToken
	updated, err := s.store.UpdateStorageAccount(r.Context(), acct)
	if err != nil {
		s.logger.Error("storage: persist dropbox refresh token failed", "account_id", acct.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	s.opts.Storage.clearFlow(acct.ID)
	if s.opts.Storage != nil {
		s.collectOneStorageAccount(r.Context(), updated, s.opts.Storage.now())
	}

	s.recordAudit(r.Context(), r, AuditActionStorageAccountLink, "storage_account", strconv.FormatInt(acct.ID, 10),
		map[string]string{"status": "pending_oauth"}, map[string]string{"status": "connected"})

	redacted := updated.Redacted()
	writeJSON(w, http.StatusOK, models.StorageOAuthCompleteResponse{Status: "connected", Account: &redacted})
}

// reportGoogleDeviceFlowStatus reports the background device-flow
// poller's current status without taking any action itself.
func (s *Server) reportGoogleDeviceFlowStatus(w http.ResponseWriter, acct models.StorageAccount) {
	flow := s.opts.Storage.flowState(acct.ID, acct.Provider)
	flow.pollMu.Lock()
	result := flow.pollResult
	pollInterval := flow.pollIntervalSec
	flow.pollMu.Unlock()

	if result == nil {
		writeJSON(w, http.StatusOK, models.StorageOAuthCompleteResponse{
			Status:          "authorization_pending",
			PollIntervalSec: pollInterval,
		})
		return
	}

	switch result.status {
	case "connected":
		s.opts.Storage.clearFlow(acct.ID)
		fresh, err := s.store.GetStorageAccount(context.Background(), acct.ID)
		if err != nil {
			s.logger.Error("storage: reload account after google device flow completion failed", "account_id", acct.ID, "error", err)
			writeJSON(w, http.StatusOK, models.StorageOAuthCompleteResponse{Status: "connected"})
			return
		}
		s.recordAuditAsync(acct.ID, fresh.Name)
		redacted := fresh.Redacted()
		writeJSON(w, http.StatusOK, models.StorageOAuthCompleteResponse{Status: "connected", Account: &redacted})
	case "denied", "expired":
		s.opts.Storage.clearFlow(acct.ID)
		writeJSON(w, http.StatusOK, models.StorageOAuthCompleteResponse{Status: result.status})
	default:
		s.opts.Storage.clearFlow(acct.ID)
		writeJSON(w, http.StatusBadGateway, models.APIError{Error: fmt.Sprintf("google device flow failed: %s", result.detail)})
	}
}

// recordAuditAsync records the storage_account.link audit entry for a
// Google device-flow completion — split out since
// reportGoogleDeviceFlowStatus has no *http.Request with a real remote
// address at hand the way completeDropboxPKCEFlow does (the completion
// happens on a background goroutine's own timeline, only surfaced to
// an HTTP caller after the fact) — recorded with an empty remote
// address/actor context, which recordAudit already tolerates.
func (s *Server) recordAuditAsync(accountID int64, name string) {
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/storage/accounts/"+strconv.FormatInt(accountID, 10)+"/oauth/complete", nil)
	req.RemoteAddr = "background:0"
	s.recordAudit(context.Background(), req, AuditActionStorageAccountLink, "storage_account", strconv.FormatInt(accountID, 10),
		map[string]string{"status": "pending_oauth"}, map[string]string{"status": "connected", "name": name})
}

// dropboxSSRFChecker validates a URL against storageusage's official
// Dropbox hosts, unless s.opts.Storage.AllowCustomEndpoints is set
// (test/smoke harness only — see config.Hub.StorageAllowCustomEndpoints),
// in which case a configured RedirectBase takes over routing entirely.
func (s *Server) dropboxSSRFChecker(rawURL string) (*url.URL, error) {
	if s.opts.Storage == nil {
		return storageusage.ValidateDropboxURL(rawURL)
	}
	return storageusage.ValidateDropboxURLWithOptions(rawURL, s.opts.Storage.AllowCustomEndpoints, s.opts.Storage.FakeBaseURL)
}

// dropboxPKCEState reconstructs a dropbox.PKCEState from a stored
// verifier string.
func dropboxPKCEState(verifier string) dropbox.PKCEState {
	return dropbox.PKCEState{CodeVerifier: verifier}
}

// handleStorageRefresh triggers an immediate poll of every connected
// storage account: POST /api/v1/storage/refresh (admin), rate-limited
// to once per minute (SPEC-v0.7 §3).
func (s *Server) handleStorageRefresh(w http.ResponseWriter, r *http.Request) {
	if s.opts.Storage == nil {
		writeJSON(w, http.StatusNotImplemented, models.APIError{Error: "storage accounts are not enabled on this hub"})
		return
	}

	allowed, retryAfter := s.opts.Storage.refreshAllowed()
	if !allowed {
		rateLimited(w, int(retryAfter.Seconds()))
		return
	}

	s.collectStorageOnce(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"status": "refreshed"})
}

// storageIntervalRequest is the body of PUT
// /api/v1/settings/storage/interval.
type storageIntervalRequest struct {
	Interval models.StorageInterval `json:"interval"`
}

// handleSetStorageInterval sets the hub-side storage-usage polling
// interval override: PUT /api/v1/settings/storage/interval (admin).
func (s *Server) handleSetStorageInterval(w http.ResponseWriter, r *http.Request) {
	if s.opts.Storage == nil {
		writeJSON(w, http.StatusNotImplemented, models.APIError{Error: "storage accounts are not enabled on this hub"})
		return
	}

	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxStorageBodyBytes)
	var req storageIntervalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}
	if !models.ValidStorageInterval(req.Interval) {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "interval must be one of 15m, 1h, 6h, 24h"})
		return
	}

	before := s.resolveStorageInterval(ctx)
	if err := s.store.SetSetting(ctx, SettingStorageInterval, string(req.Interval)); err != nil {
		s.logger.Error("set storage interval setting failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.recordAudit(ctx, r, AuditActionStorageIntervalChange, "storage_settings", "",
		map[string]string{"interval": string(before)},
		map[string]string{"interval": string(req.Interval)},
	)
	writeJSON(w, http.StatusOK, map[string]string{"interval": string(req.Interval)})
}
