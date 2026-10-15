package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// maxAuthBodyBytes bounds the size of dashboard authentication request
// bodies (login/password change payloads are tiny JSON), per SPEC-v0.4
// §1 ("bodies <= 4 KiB").
const maxAuthBodyBytes = 4 << 10 // 4 KiB

// registerAuthRoutes registers the dashboard authentication endpoints
// (POST /api/v1/auth/login, /logout, GET /api/v1/auth/me, POST
// /api/v1/auth/password, GET /api/v1/auth/sessions, POST
// /api/v1/auth/sessions/revoke-others — see SPEC-v0.4 §1) on mux.
func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireUser(s.handleLogout))
	mux.HandleFunc("GET /api/v1/auth/me", s.requireUser(s.handleMe))
	mux.HandleFunc("POST /api/v1/auth/password", s.requireUser(s.handleChangePassword))
	mux.HandleFunc("GET /api/v1/auth/sessions", s.requireUser(s.handleListSessions))
	mux.HandleFunc("POST /api/v1/auth/sessions/revoke-others", s.requireUser(s.handleRevokeOtherSessions))
}

// clientIPForRateLimit returns the key the rate limiter buckets by: the
// host part of r.RemoteAddr only (never a client-supplied header),
// matching the same trust boundary as the CIDR allowlist. The port must
// be stripped — r.RemoteAddr's port is the client's ephemeral source
// port, which is different for every TCP connection even from the same
// client, so keying on the full "ip:port" string would make the limiter
// never accumulate failures across requests (see clientIP in
// networkroutes.go, which has the same requirement for a different
// purpose).
func clientIPForRateLimit(r *http.Request) string {
	return clientIP(r)
}

// loginRequest is the body of POST /api/v1/auth/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin authenticates a username/password pair and issues a fresh
// session: POST /api/v1/auth/login (unauthenticated route, its own
// rate limiting).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	clientIP := clientIPForRateLimit(r)
	if ok, retryAfter := s.limiter.allow(clientIP); !ok {
		rateLimited(w, retryAfter)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body", Code: "invalid_request_body"})
		return
	}

	release, ok := s.limiter.acquirePBKDF2()
	if !ok {
		rateLimited(w, int(pbkdf2WaitTimeout.Seconds()))
		return
	}
	defer release()

	ctx := r.Context()
	hash, ok, err := s.store.GetSetting(ctx, SettingPasswordHash)
	if err != nil {
		s.logger.Error("get password hash failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	if !ok {
		hash = ""
	}

	// Always run the password verification (which always performs a
	// PBKDF2 computation, even against a malformed/empty hash) so a
	// wrong username takes the same time as a wrong password for the
	// real username — see verifyPassword's doc comment.
	validUsername := constantTimeEqual(req.Username, adminUsername)
	validPassword := verifyPassword(req.Password, hash)
	if !validUsername || !validPassword {
		s.limiter.recordFailure(clientIP)
		s.logger.Warn("login failed", "remote", r.RemoteAddr)
		invalidCredentials(w)
		return
	}
	s.limiter.recordSuccess(clientIP)

	mustChange := s.accountMustChangePassword(ctx)
	resp, err := s.createSession(ctx, r, mustChange)
	if err != nil {
		s.logger.Error("create session failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// createSession generates a fresh session token, persists it, and
// returns the LoginResponse describing it. now is taken from
// s.opts.now() so tests can control session expiry deterministically.
func (s *Server) createSession(ctx context.Context, r *http.Request, mustChange bool) (models.LoginResponse, error) {
	token, err := generateSessionToken()
	if err != nil {
		return models.LoginResponse{}, err
	}
	now := s.opts.now()
	expiresAt := now.Add(sessionIdleTTL).Unix()

	sess := models.Session{
		IDHash:    hashSessionToken(token),
		CreatedAt: now.Unix(),
		LastSeen:  now.Unix(),
		ExpiresAt: expiresAt,
		Remote:    r.RemoteAddr,
		UserAgent: r.UserAgent(),
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return models.LoginResponse{}, fmt.Errorf("hub: create session: %w", err)
	}

	return models.LoginResponse{
		Token:              token,
		ExpiresAt:          expiresAt,
		MustChangePassword: mustChange,
		Username:           adminUsername,
	}, nil
}

// handleLogout deletes the caller's current session: POST
// /api/v1/auth/logout. A caller authenticated via api_token has no
// session to delete; this still responds 200 {"ok":true}.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	idHash := sessionIDHashFromContext(r.Context())
	if idHash != "" {
		if err := s.store.DeleteSession(r.Context(), idHash); err != nil {
			s.logger.Error("delete session failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMe identifies the authenticated caller: GET /api/v1/auth/me.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	method := authMethodFromContext(ctx)

	resp := models.MeResponse{
		Username:   adminUsername,
		AuthMethod: method,
	}
	if method == "session" {
		resp.MustChangePassword = mustChangeFromContext(ctx)
		resp.SessionExpiresAt = sessionExpiresAtFromContext(ctx)
	}
	writeJSON(w, http.StatusOK, resp)
}

// changePasswordRequest is the body of POST /api/v1/auth/password.
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// AuditActionPasswordChange records an admin password change made
// through POST /api/v1/auth/password (SPEC-v0.6 §3 개선 c: any
// admin action that changes persisted state is audited). The entry
// deliberately carries only non-secret metadata — never the old or new
// password, and never either PBKDF2 hash: models.AuditEntry's before/
// after JSON is readable by every admin session and by anyone holding
// an exported API token, so a leaked row must not help anyone offline
// attack the stored hash.
//
// The CLI reset path (cmd/hub/resetpassword.go) writes the same setting
// directly against the store and has no request context to resolve an
// actor or remote address from, so it stays unaudited; it is a local,
// already-authenticated shell action whose own stdout is the receipt.
const AuditActionPasswordChange models.AuditAction = "auth.password_change"

// handleChangePassword verifies the current password, stores a new
// password hash, clears the must-change flag, revokes every other
// session, and issues a fresh session for the caller: POST
// /api/v1/auth/password.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	clientIP := clientIPForRateLimit(r)
	if ok, retryAfter := s.limiter.allow(clientIP); !ok {
		rateLimited(w, retryAfter)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body", Code: "invalid_request_body"})
		return
	}

	release, ok := s.limiter.acquirePBKDF2()
	if !ok {
		rateLimited(w, int(pbkdf2WaitTimeout.Seconds()))
		return
	}
	defer release()

	ctx := r.Context()
	hash, ok, err := s.store.GetSetting(ctx, SettingPasswordHash)
	if err != nil {
		s.logger.Error("get password hash failed")
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	if !ok {
		hash = ""
	}
	if !verifyPassword(req.CurrentPassword, hash) {
		s.limiter.recordFailure(clientIP)
		s.logger.Warn("password change failed: wrong current password", "remote", r.RemoteAddr)
		invalidCredentials(w)
		return
	}
	s.limiter.recordSuccess(clientIP)

	if err := validatePassword(req.NewPassword, req.CurrentPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: err.Error(), Code: "weak_password"})
		return
	}

	newHash, err := hashPassword(req.NewPassword)
	if err != nil {
		s.logger.Error("hash new password failed")
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	if err := s.store.SetSetting(ctx, SettingPasswordHash, newHash); err != nil {
		s.logger.Error("store new password hash failed")
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	// The hash is already persisted. Attempt exactly one audit even if a
	// later, nontransactional step fails or the request is canceled. Only
	// confirmed outcomes go in the entry; nil is an unknown revocation count.
	after := struct {
		PasswordChanged         bool   `json:"password_changed"`
		MustChangeCleared       bool   `json:"must_change_cleared"`
		SessionsRevoked         *int   `json:"sessions_revoked"`
		SessionIssued           bool   `json:"session_issued"`
		SessionRevocationStatus string `json:"session_revocation_status"`
		FailureStage            string `json:"failure_stage,omitempty"`
	}{PasswordChanged: true, SessionRevocationStatus: "not_attempted"}
	defer func() {
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		afterJSON, err := json.Marshal(after)
		if err != nil {
			s.logger.Error("audit: marshal password change failed", "action", AuditActionPasswordChange)
			return
		}
		entry := models.AuditEntry{
			At:         s.opts.now().Unix(),
			Actor:      auditActorFor(auditCtx),
			Remote:     clientIP,
			Action:     AuditActionPasswordChange,
			EntityType: "admin_password",
			AfterJSON:  string(afterJSON),
		}
		// Store errors can contain credentials or tokens; never log their
		// text. Audit failure must not change the rotation's HTTP response.
		if _, err := s.store.CreateAuditEntry(auditCtx, entry); err != nil {
			s.logger.Error("audit: create entry failed", "action", AuditActionPasswordChange)
			return
		}
		s.logger.Info("audit", "actor", entry.Actor, "remote", entry.Remote,
			"action", string(entry.Action), "entity_type", entry.EntityType, "entity_id", entry.EntityID)
	}()

	if err := s.store.SetSetting(ctx, SettingMustChangePassword, "0"); err != nil {
		after.FailureStage = "must_change_clear"
		s.logger.Error("clear must-change-password flag failed")
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	after.MustChangeCleared = true

	resp, err := s.createSession(ctx, r, false)
	if err != nil {
		after.FailureStage = "session_issue"
		s.logger.Error("create session failed")
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	after.SessionIssued = true

	// Revoke every other session (including the one this request was
	// authenticated with, if any) now that the new one exists.
	revoked, err := s.store.DeleteSessionsExcept(ctx, hashSessionToken(resp.Token))
	if err != nil {
		after.SessionRevocationStatus = "failed"
		after.FailureStage = "session_revocation"
		s.logger.Error("revoke other sessions after password change failed")
	} else {
		after.SessionRevocationStatus = "completed"
		after.SessionsRevoked = &revoked
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleListSessions responds with every active session, marking the
// caller's own: GET /api/v1/auth/sessions.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessions, err := s.store.ListSessions(ctx)
	if err != nil {
		s.logger.Error("list sessions failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	current := sessionIDHashFromContext(ctx)

	views := make([]models.SessionView, 0, len(sessions))
	for _, sess := range sessions {
		views = append(views, models.SessionView{
			ID:        sessionShortID(sess.IDHash),
			CreatedAt: sess.CreatedAt,
			LastSeen:  sess.LastSeen,
			ExpiresAt: sess.ExpiresAt,
			Remote:    sess.Remote,
			UserAgent: sess.UserAgent,
			Current:   sess.IDHash == current,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].CreatedAt < views[j].CreatedAt })
	writeJSON(w, http.StatusOK, views)
}

// sessionShortID returns the first 8 hex characters of idHash for
// display, per SPEC-v0.4 §1.
func sessionShortID(idHash string) string {
	if len(idHash) <= 8 {
		return idHash
	}
	return idHash[:8]
}

// handleRevokeOtherSessions deletes every session except the caller's
// own: POST /api/v1/auth/sessions/revoke-others.
func (s *Server) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	current := sessionIDHashFromContext(ctx)
	revoked, err := s.store.DeleteSessionsExcept(ctx, current)
	if err != nil {
		s.logger.Error("revoke other sessions failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error", Code: "internal_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"revoked": revoked})
}
