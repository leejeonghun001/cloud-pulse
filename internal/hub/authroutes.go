package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

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
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
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
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
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
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
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
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
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
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
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
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
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
		s.logger.Error("hash new password failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	if err := s.store.SetSetting(ctx, SettingPasswordHash, newHash); err != nil {
		s.logger.Error("store new password hash failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	if err := s.store.SetSetting(ctx, SettingMustChangePassword, "0"); err != nil {
		s.logger.Error("clear must-change-password flag failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	resp, err := s.createSession(ctx, r, false)
	if err != nil {
		s.logger.Error("create session failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	// Revoke every other session (including the one this request was
	// authenticated with, if any) now that the new one exists.
	if _, err := s.store.DeleteSessionsExcept(ctx, hashSessionToken(resp.Token)); err != nil {
		s.logger.Error("revoke other sessions after password change failed", "error", err)
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
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
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
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"revoked": revoked})
}
