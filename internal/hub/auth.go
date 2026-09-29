package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// SettingPasswordHash and SettingMustChangePassword are the settings
// table keys backing the single admin account's credentials (see
// SPEC-v0.4 §1).
const (
	SettingPasswordHash       = "auth_password_hash"
	SettingMustChangePassword = "auth_must_change"
)

// adminUsername is the single dashboard admin account's username. The
// hub has exactly one account, so this is a constant rather than
// something stored or configurable.
const adminUsername = "admin"

// sessionTokenBytes is the number of random bytes making up a session
// bearer token before base64url encoding.
const sessionTokenBytes = 32

// sessionIdleTTL and sessionAbsoluteTTL bound a session's lifetime: it
// slides forward on activity up to sessionIdleTTL since last use, but
// never beyond sessionAbsoluteTTL since creation.
const (
	sessionIdleTTL     = 7 * 24 * time.Hour
	sessionAbsoluteTTL = 30 * 24 * time.Hour
)

// sessionTouchThrottle bounds how often TouchSession is called for the
// same session (at most once per this interval), per SPEC-v0.4 §1.
const sessionTouchThrottle = 1 * time.Minute

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header, returning ok=false if the header is missing or malformed.
func bearerToken(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	token := strings.TrimSpace(auth[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// constantTimeEqual reports whether a and b are equal, in constant time
// regardless of their lengths. It hashes both operands first so the
// subtle.ConstantTimeCompare call always compares equal-length buffers,
// avoiding a length-based timing side channel.
func constantTimeEqual(a, b string) bool {
	ah := sha256.Sum256([]byte(a))
	bh := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
}

// hashSessionToken returns the hex-encoded SHA-256 hash of token, the
// form stored server-side (and used to look up a session) so a stolen
// database backup does not expose usable bearer tokens.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// generateSessionToken returns a fresh 32-byte random token, base64url
// encoded without padding.
func generateSessionToken() (string, error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("hub: generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// unauthorized writes a 401 JSON response with a WWW-Authenticate
// header and code "unauthorized" (no bearer token presented at all).
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, models.APIError{Error: "unauthorized"})
}

// sessionExpired writes a 401 JSON response with code "session_expired"
// (a bearer token was presented but doesn't match any active session).
func sessionExpired(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSON(w, http.StatusUnauthorized, models.APIError{
		Error: "session expired; please sign in again",
		Code:  "session_expired",
	})
}

// invalidCredentials writes a 401 JSON response with a generic message
// and code "invalid_credentials", used for both an unknown username and
// a wrong password so the two cases are indistinguishable to a caller.
func invalidCredentials(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, models.APIError{
		Error: "invalid username or password",
		Code:  "invalid_credentials",
	})
}

// rateLimited writes a 429 JSON response with a Retry-After header and
// matching retry_after_seconds field.
func rateLimited(w http.ResponseWriter, retryAfterSeconds int) {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds))
	writeJSON(w, http.StatusTooManyRequests, models.APIError{
		Error:             "too many attempts; try again later",
		Code:              "rate_limited",
		RetryAfterSeconds: retryAfterSeconds,
	})
}

// passwordChangeRequired writes a 403 JSON response with code
// "password_change_required", used when an authenticated session whose
// account must change its password reaches a route other than
// /auth/me, /auth/password, or /auth/logout.
func passwordChangeRequired(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, models.APIError{
		Error: "password change required before continuing",
		Code:  "password_change_required",
	})
}

// requireAgentToken wraps next, requiring a valid bearer token matching
// s.opts.AgentToken.
func (s *Server) requireAgentToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok || !constantTimeEqual(token, s.opts.AgentToken) {
			unauthorized(w)
			return
		}
		next(w, r)
	}
}

// authContextKey is the type of context keys set by requireUser.
type authContextKey int

const (
	authMethodContextKey authContextKey = iota
	authMustChangeContextKey
	authSessionIDHashContextKey
	authSessionExpiresAtContextKey
)

// authMethodFromContext returns "session" or "api_token" as set by
// requireUser, or "" if the request was never authenticated by
// requireUser (e.g. agent/healthz/static routes).
func authMethodFromContext(ctx context.Context) string {
	v, _ := ctx.Value(authMethodContextKey).(string)
	return v
}

// mustChangeFromContext returns whether the authenticated session's
// account currently must change its password. Always false for
// api_token auth (the static token is never subject to the must-change
// gate).
func mustChangeFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(authMustChangeContextKey).(bool)
	return v
}

// sessionIDHashFromContext returns the current session's IDHash, or ""
// when authenticated via api_token.
func sessionIDHashFromContext(ctx context.Context) string {
	v, _ := ctx.Value(authSessionIDHashContextKey).(string)
	return v
}

// sessionExpiresAtFromContext returns the current session's ExpiresAt
// (unix seconds), or 0 when authenticated via api_token.
func sessionExpiresAtFromContext(ctx context.Context) int64 {
	v, _ := ctx.Value(authSessionExpiresAtContextKey).(int64)
	return v
}

// requireUser wraps next, requiring either:
//   - a bearer token equal to s.opts.UIToken (when UIToken is
//     non-empty): authenticated as "api_token", never subject to the
//     must-change gate; or
//   - a bearer token matching an active session's hashed id: authenticated
//     as "session". An expired/unknown session token is 401
//     "session_expired"; a missing bearer token at all is 401
//     "unauthorized".
//
// A session whose account must change its password is only allowed
// through to /api/v1/auth/me, /api/v1/auth/password, and
// /api/v1/auth/logout — every other route responds 403
// "password_change_required" for such a session. See SPEC-v0.4 §1.
func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			unauthorized(w)
			return
		}

		if s.opts.UIToken != "" && constantTimeEqual(token, s.opts.UIToken) {
			ctx := context.WithValue(r.Context(), authMethodContextKey, "api_token")
			next(w, r.WithContext(ctx))
			return
		}

		idHash := hashSessionToken(token)
		sess, err := s.store.GetSession(r.Context(), idHash)
		if err != nil {
			if !isNotFound(err) {
				s.logger.Error("get session failed", "error", err)
			}
			sessionExpired(w)
			return
		}
		now := s.opts.now()
		if now.Unix() >= sess.ExpiresAt {
			sessionExpired(w)
			return
		}
		s.touchSession(r.Context(), sess, now)

		mustChange := s.accountMustChangePassword(r.Context())

		if mustChange && !isMustChangeExemptPath(r.URL.Path) {
			passwordChangeRequired(w)
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, authMethodContextKey, "session")
		ctx = context.WithValue(ctx, authMustChangeContextKey, mustChange)
		ctx = context.WithValue(ctx, authSessionIDHashContextKey, idHash)
		ctx = context.WithValue(ctx, authSessionExpiresAtContextKey, sess.ExpiresAt)
		next(w, r.WithContext(ctx))
	}
}

// isMustChangeExemptPath reports whether path remains reachable by a
// session whose account must change its password.
func isMustChangeExemptPath(path string) bool {
	switch path {
	case "/api/v1/auth/me", "/api/v1/auth/password", "/api/v1/auth/logout":
		return true
	default:
		return false
	}
}

// touchSession updates sess's sliding expiry via s.store.TouchSession,
// throttled to at most once per sessionTouchThrottle per session. Errors
// are logged but never fail the request: a failed touch just means the
// session's idle expiry doesn't advance this time.
func (s *Server) touchSession(ctx context.Context, sess models.Session, now time.Time) {
	if now.Unix()-sess.LastSeen < int64(sessionTouchThrottle.Seconds()) {
		return
	}
	newExpiresAt := now.Add(sessionIdleTTL).Unix()
	if maxExpiresAt := sess.CreatedAt + int64(sessionAbsoluteTTL.Seconds()); newExpiresAt > maxExpiresAt {
		newExpiresAt = maxExpiresAt
	}
	if err := s.store.TouchSession(ctx, sess.IDHash, now.Unix(), newExpiresAt); err != nil && !isNotFound(err) {
		s.logger.Error("touch session failed", "error", err)
	}
}

// accountMustChangePassword reads the current must-change-password
// flag from settings. Any read error is treated as true (fail safe:
// force a password change / restrict access rather than silently
// granting full access on a storage hiccup).
func (s *Server) accountMustChangePassword(ctx context.Context) bool {
	value, ok, err := s.store.GetSetting(ctx, SettingMustChangePassword)
	if err != nil {
		s.logger.Error("get must-change-password setting failed", "error", err)
		return true
	}
	if !ok {
		return false
	}
	return value == "1"
}

// requireAdmin is an alias of requireUser: the hub has a single admin
// account, so every authenticated user is the admin (SPEC-v0.4 §1
// removes the old "admin_disabled" concept entirely).
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireUser(next)
}
