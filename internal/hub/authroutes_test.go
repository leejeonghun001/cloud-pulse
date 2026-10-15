package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// --- POST /api/v1/auth/login ---

func TestLogin_Success(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPassword(t, store, "correct-password-1", false)
	s := newTestServer(t, testOptions(), store)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "correct-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.10:1", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.LoginResponse](t, rec.Body)
	if got.Token == "" {
		t.Error("expected non-empty token")
	}
	if got.Username != "admin" {
		t.Errorf("username = %q, want admin", got.Username)
	}
	if got.MustChangePassword {
		t.Error("MustChangePassword = true, want false (seeded with mustChange=false)")
	}
	if got.ExpiresAt <= time.Now().Unix() {
		t.Errorf("ExpiresAt = %d, want in the future", got.ExpiresAt)
	}
}

func TestLogin_MustChangeReflectedInResponse(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPassword(t, store, "changeme", true)
	s := newTestServer(t, testOptions(), store)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "changeme"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.11:1", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.LoginResponse](t, rec.Body)
	if !got.MustChangePassword {
		t.Error("MustChangePassword = false, want true")
	}
}

func TestLogin_WrongPassword401(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPassword(t, store, "correct-password-1", false)
	s := newTestServer(t, testOptions(), store)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "wrong"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.12:1", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "invalid_credentials" {
		t.Errorf("code = %q, want invalid_credentials", got.Code)
	}
}

func TestLogin_WrongUsername401_SameCodeAsWrongPassword(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPassword(t, store, "correct-password-1", false)
	s := newTestServer(t, testOptions(), store)

	body, _ := json.Marshal(loginRequest{Username: "not-admin", Password: "correct-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.13:1", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "invalid_credentials" {
		t.Errorf("code = %q, want invalid_credentials", got.Code)
	}
}

func TestLogin_NoPasswordSetYet401(t *testing.T) {
	t.Parallel()
	store := newFakeStore() // no password hash stored at all
	s := newTestServer(t, testOptions(), store)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "anything"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.14:1", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestLogin_InvalidBody400(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.15:1", "", []byte("not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestLogin_RateLimitedAfterFailures(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPassword(t, store, "correct-password-1", false)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "wrong"})
	const remote = "203.0.113.16:1"
	for i := 0; i < rateLimitMaxFailures; i++ {
		rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", remote, "", body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, rec.Code)
		}
	}

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", remote, "", body)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "rate_limited" {
		t.Errorf("code = %q, want rate_limited", got.Code)
	}
	if got.RetryAfterSeconds <= 0 {
		t.Errorf("RetryAfterSeconds = %d, want > 0", got.RetryAfterSeconds)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header")
	}

	// A correct password from the SAME IP is still rejected while locked
	// out (the lockout blocks the IP regardless of credentials).
	correctBody, _ := json.Marshal(loginRequest{Username: "admin", Password: "correct-password-1"})
	rec2 := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", remote, "", correctBody)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 even with correct credentials while locked out", rec2.Code)
	}
}

// TestLogin_RateLimitKeyedByHostNotEphemeralPort is a regression test
// for a bug where the rate limiter bucketed by r.RemoteAddr's full
// "ip:port" string: every real TCP connection uses a different
// ephemeral source port, so that keying scheme never accumulated
// failures across requests from the same client in practice (only
// caught by driving the hub over real loopback sockets, where each
// curl/http.Client request opens a new connection with a new source
// port — see SPEC-v0.4 §1, "per client IP (RemoteAddr)"). This test
// varies the port on every request, the same way real connections do,
// and asserts the lockout still triggers.
func TestLogin_RateLimitKeyedByHostNotEphemeralPort(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPassword(t, store, "correct-password-1", false)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "wrong"})
	const host = "203.0.113.20"
	for i := 0; i < rateLimitMaxFailures; i++ {
		remote := fmt.Sprintf("%s:%d", host, 40000+i) // distinct ephemeral port per request
		rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", remote, "", body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, rec.Code)
		}
	}

	// One more attempt, again from a brand-new ephemeral port on the
	// same host, must be locked out.
	remote := fmt.Sprintf("%s:%d", host, 49999)
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", remote, "", body)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body=%s) — rate limiter must key by host, not host:port", rec.Code, rec.Body.String())
	}
}

func TestLogin_WrongMethod_404JSON(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	// Only POST is registered for /api/v1/auth/login; a GET here falls
	// through to the least-specific "GET /" static-asset route (see
	// handleStatic's doc comment on this same routing tradeoff), which
	// returns a JSON 404 rather than a 405 for this particular path.
	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/login", "203.0.113.17:1", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// --- GET /api/v1/auth/me ---

func TestMe_SessionAuth(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	token := loginAndGetToken(t, s, store, "correct-password-1")

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.18:1", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.MeResponse](t, rec.Body)
	if got.Username != "admin" {
		t.Errorf("username = %q, want admin", got.Username)
	}
	if got.AuthMethod != "session" {
		t.Errorf("AuthMethod = %q, want session", got.AuthMethod)
	}
	if got.SessionExpiresAt == 0 {
		t.Error("SessionExpiresAt = 0, want non-zero for a session")
	}
}

func TestMe_APITokenAuth(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.UIToken = "static-ui-token-1234"
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.19:1", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.MeResponse](t, rec.Body)
	if got.AuthMethod != "api_token" {
		t.Errorf("AuthMethod = %q, want api_token", got.AuthMethod)
	}
	if got.SessionExpiresAt != 0 {
		t.Errorf("SessionExpiresAt = %d, want 0 for api_token auth", got.SessionExpiresAt)
	}
	if got.MustChangePassword {
		t.Error("MustChangePassword = true, want false for api_token auth (never gated)")
	}
}

func TestMe_NoToken401(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.20:1", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "unauthorized" {
		t.Errorf("code = %q, want %q (no bearer token at all)", got.Code, "unauthorized")
	}
}

func TestMe_UnknownSessionToken_SessionExpired401(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.21:1", "not-a-real-session-token", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "session_expired" {
		t.Errorf("code = %q, want session_expired", got.Code)
	}
}

func TestMe_ExpiredSession_SessionExpired401(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)
	token := loginAndGetToken(t, s, store, "correct-password-1")

	// Advance well past the idle TTL.
	opts.Now = fixedNow(now.Add(sessionIdleTTL + time.Hour))
	s2 := New(opts, store, nil, nil, nil, testLogger())

	rec := doRequest(t, s2.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.22:1", token, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "session_expired" {
		t.Errorf("code = %q, want session_expired", got.Code)
	}
}

// --- must-change gate ---

func TestRequireUser_MustChangeGate(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPassword(t, store, "changeme", true)
	s := newTestServer(t, testOptions(), store)

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "changeme"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.23:1", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	token := decodeJSON[models.LoginResponse](t, rec.Body).Token

	t.Run("me_allowed", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.23:1", token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("logout_allowed", func(t *testing.T) {
		t.Parallel()
		// Use a fresh login so logging out doesn't affect other
		// subtests sharing the outer token.
		body, _ := json.Marshal(loginRequest{Username: "admin", Password: "changeme"})
		rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.23:1", "", body)
		tok := decodeJSON[models.LoginResponse](t, rec.Body).Token
		rec2 := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/logout", "203.0.113.23:1", tok, nil)
		if rec2.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
		}
	})

	t.Run("hosts_blocked", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.23:1", token, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
		}
		got := decodeJSON[models.APIError](t, rec.Body)
		if got.Code != "password_change_required" {
			t.Errorf("code = %q, want password_change_required", got.Code)
		}
	})

	t.Run("settings_blocked", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/settings", "203.0.113.23:1", token, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body=%s)", rec.Code, rec.Body.String())
		}
	})
}

func TestRequireUser_APITokenNeverGatedByMustChange(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPassword(t, store, "changeme", true) // must_change=1
	opts := testOptions()
	opts.UIToken = "static-ui-token-1234"
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.24:1", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (api_token must never be must-change-gated) (body=%s)", rec.Code, rec.Body.String())
	}
}

// --- POST /api/v1/auth/logout ---

func TestLogout_DeletesSession(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	token := loginAndGetToken(t, s, store, "correct-password-1")

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/logout", "203.0.113.25:1", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	// The same token must no longer work.
	rec2 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.25:1", token, nil)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("status after logout = %d, want 401", rec2.Code)
	}
}

func TestLogout_APITokenOK_NoOp(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.UIToken = "static-ui-token-1234"
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/logout", "203.0.113.26:1", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// --- POST /api/v1/auth/password ---

func TestChangePassword_Success(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	token := loginAndGetToken(t, s, store, "old-password-1")

	body, _ := json.Marshal(changePasswordRequest{CurrentPassword: "old-password-1", NewPassword: "brand-new-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/password", "203.0.113.27:1", token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.LoginResponse](t, rec.Body)
	if got.Token == "" || got.Token == token {
		t.Error("expected a fresh, different session token")
	}
	if got.MustChangePassword {
		t.Error("MustChangePassword = true, want false after a successful change")
	}

	// Old session token is now revoked.
	rec2 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.27:1", token, nil)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("old token status = %d, want 401 (revoked)", rec2.Code)
	}

	// New session token works, and the new password is now in effect.
	rec3 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.27:1", got.Token, nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("new token status = %d, want 200 (body=%s)", rec3.Code, rec3.Body.String())
	}
}

// TestChangePassword_RecordsAudit covers the audit_log gap this route
// used to have: a successful rotation wrote the new hash but no
// AuditEntry, so the change history in Settings could not show who
// rotated the admin credential, from where, or that the other sessions
// were revoked.
func TestChangePassword_RecordsAudit(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	tokenA := loginAndGetToken(t, s, store, "old-password-1")

	// A second session, so the entry can prove the revocation count.
	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "old-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.36:2", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("second login status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	changeBody, _ := json.Marshal(changePasswordRequest{CurrentPassword: "old-password-1", NewPassword: "brand-new-password-1"})
	rec2 := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/password", "203.0.113.36:1", tokenA, changeBody)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}

	entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("audit entries = %+v, want exactly one password_change entry", entries)
	}
	entry := entries[0]
	if entry.Action != AuditActionPasswordChange {
		t.Errorf("Action = %q, want %q", entry.Action, AuditActionPasswordChange)
	}
	if entry.EntityType != "admin_password" || entry.EntityID != "" {
		t.Errorf("EntityType/EntityID = %q/%q, want admin_password and a singleton empty ID", entry.EntityType, entry.EntityID)
	}
	if entry.Actor != "admin" {
		t.Errorf("Actor = %q, want admin (a session-authenticated request)", entry.Actor)
	}
	if entry.Remote != "203.0.113.36" {
		t.Errorf("Remote = %q, want the client host without its port", entry.Remote)
	}
	if entry.BeforeJSON != "" {
		t.Errorf("BeforeJSON = %q, want empty (there is no previous non-secret state worth recording)", entry.BeforeJSON)
	}

	var after struct {
		PasswordChanged   *bool  `json:"password_changed"`
		MustChangeCleared *bool  `json:"must_change_cleared"`
		SessionsRevoked   *int   `json:"sessions_revoked"`
		SessionIssued     bool   `json:"session_issued"`
		RevocationStatus  string `json:"session_revocation_status"`
		FailureStage      string `json:"failure_stage"`
	}
	if err := json.Unmarshal([]byte(entry.AfterJSON), &after); err != nil {
		t.Fatalf("unmarshal AfterJSON %q: %v", entry.AfterJSON, err)
	}
	if after.PasswordChanged == nil || !*after.PasswordChanged {
		t.Errorf("AfterJSON password_changed = %v, want true", after.PasswordChanged)
	}
	if after.MustChangeCleared == nil || !*after.MustChangeCleared {
		t.Errorf("AfterJSON must_change_cleared = %v, want true", after.MustChangeCleared)
	}
	// Two sessions existed before the rotation (tokenA's and the
	// second login's) and the fresh one issued by this request is
	// kept, so exactly the two older ones are revoked.
	if after.SessionsRevoked == nil || *after.SessionsRevoked != 2 {
		t.Errorf("AfterJSON sessions_revoked = %v, want 2 (both pre-rotation sessions)", after.SessionsRevoked)
	}
	if !after.SessionIssued || after.RevocationStatus != "completed" || after.FailureStage != "" {
		t.Error("successful rotation must report session issuance and completed revocation without a failure stage")
	}
}

// TestChangePassword_AuditNeverContainsSecrets is the security
// counterpart of TestChangePassword_RecordsAudit: the audit row is
// readable by every admin session and API-token holder, so neither the
// password itself nor either PBKDF2 hash may appear in it.
func TestChangePassword_AuditNeverContainsSecrets(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := New(testOptions(), store, nil, nil, nil, logger)
	token := loginAndGetToken(t, s, store, "old-password-1")
	oldHash, ok, err := store.GetSetting(t.Context(), SettingPasswordHash)
	if err != nil || !ok {
		t.Fatal("could not capture the old password hash")
	}

	body, _ := json.Marshal(changePasswordRequest{CurrentPassword: "old-password-1", NewPassword: "brand-new-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/password", "203.0.113.37:1", token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var response models.LoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Token == "" {
		t.Fatal("could not capture the new session token")
	}

	entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
	if err != nil {
		t.Fatal("could not list audit entries")
	}
	if len(entries) != 1 {
		t.Fatalf("audit entry count = %d, want exactly one entry", len(entries))
	}
	storedHash, ok, err := store.GetSetting(t.Context(), SettingPasswordHash)
	if err != nil || !ok {
		t.Fatal("could not capture the new password hash")
	}
	serialized, err := json.Marshal(entries[0])
	if err != nil {
		t.Fatal("could not serialize audit entry")
	}
	assertPasswordChangeSecretsAbsent(t, string(serialized), logBuf.String(), []string{
		"old-password-1", "brand-new-password-1", oldHash, storedHash,
		token, hashSessionToken(token), response.Token, hashSessionToken(response.Token),
	})
}

// TestChangePassword_FailedChangeRecordsNoAudit guards the negative
// case: only a rotation that actually happened may leave a trace, so a
// wrong current password or a rejected new password must not look like
// a credential change in the history.
func TestChangePassword_FailedChangeRecordsNoAudit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		req    changePasswordRequest
		status int
		code   string
	}{
		{"wrong_current", changePasswordRequest{CurrentPassword: "totally-wrong", NewPassword: "brand-new-password-1"}, http.StatusUnauthorized, "invalid_credentials"},
		{"weak_new", changePasswordRequest{CurrentPassword: "old-password-1", NewPassword: "short"}, http.StatusBadRequest, "weak_password"},
		{"same_as_current", changePasswordRequest{CurrentPassword: "old-password-1", NewPassword: "old-password-1"}, http.StatusBadRequest, "weak_password"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &passwordChangeFaultStore{fakeStore: newFakeStore()}
			s := newTestServer(t, testOptions(), store)
			token := loginAndGetToken(t, s, store, "old-password-1")
			oldHash, _, _ := store.GetSetting(t.Context(), SettingPasswordHash)
			beforeSessions, _ := store.ListSessions(t.Context())
			beforeFlag, _, _ := store.GetSetting(t.Context(), SettingMustChangePassword)
			store.settingWrites, store.sessionCreates, store.revocationCalls = 0, 0, 0
			body, _ := json.Marshal(tc.req)
			rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/password", "203.0.113.38:1", token, body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			var response models.APIError
			if json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.Code != tc.code {
				t.Error("rejection error code changed")
			}
			afterHash, _, _ := store.GetSetting(t.Context(), SettingPasswordHash)
			afterSessions, _ := store.ListSessions(t.Context())
			afterFlag, _, _ := store.GetSetting(t.Context(), SettingMustChangePassword)
			if afterHash != oldHash || afterFlag != beforeFlag || !reflect.DeepEqual(beforeSessions, afterSessions) {
				t.Error("rejected change mutated credentials, must-change flag, or sessions")
			}
			if store.settingWrites != 0 || store.sessionCreates != 0 || store.revocationCalls != 0 || len(store.auditAttempts) != 0 {
				t.Error("rejected change attempted a state write or audit")
			}
			entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
			if err != nil {
				t.Fatalf("list audit entries: %v", err)
			}
			if len(entries) != 0 {
				t.Errorf("audit entry count = %d, want none for a rejected change", len(entries))
			}
		})
	}
}

// passwordChangeFaultStore is route-local: seed the embedded store before
// enabling faults so bootstrap/login cannot consume the injected failure.
type passwordChangeFaultStore struct {
	*fakeStore
	fault                  string
	failureSecrets         string
	deleteBeforeError      bool
	cancelOnFailure        context.CancelFunc
	settingWrites          int
	sessionCreates         int
	revocationCalls        int
	attemptedPasswordHash  string
	attemptedSessionHashes []string
	auditAttempts          []models.AuditEntry
	auditContextErr        error
	auditDeadline          time.Time
	auditHasDeadline       bool
	auditContextValue      any
}

type passwordChangeAuditTestContextKey struct{}

func (f *passwordChangeFaultStore) injectedError(extra string) error {
	if f.cancelOnFailure != nil {
		f.cancelOnFailure()
	}
	// Deliberately secret-bearing errors prove the route does not log raw
	// error text. Test failure messages must not print these errors either.
	return fmt.Errorf("injected store failure: %s %s %s", f.failureSecrets, f.attemptedPasswordHash, extra)
}

func (f *passwordChangeFaultStore) SetSetting(ctx context.Context, key, value string) error {
	f.settingWrites++
	if key == SettingPasswordHash {
		f.attemptedPasswordHash = value
		if f.fault == "hash_write" {
			return f.injectedError(value)
		}
	}
	if key == SettingMustChangePassword && f.fault == "must_change_clear" {
		return f.injectedError("")
	}
	return f.fakeStore.SetSetting(ctx, key, value)
}

func (f *passwordChangeFaultStore) CreateSession(ctx context.Context, sess models.Session) error {
	f.sessionCreates++
	f.attemptedSessionHashes = append(f.attemptedSessionHashes, sess.IDHash)
	if f.fault == "session_issue" {
		return f.injectedError(sess.IDHash)
	}
	return f.fakeStore.CreateSession(ctx, sess)
}

func (f *passwordChangeFaultStore) DeleteSessionsExcept(ctx context.Context, keepIDHash string) (int, error) {
	f.revocationCalls++
	if f.fault == "session_revocation" {
		if f.deleteBeforeError {
			if _, err := f.fakeStore.DeleteSessionsExcept(ctx, keepIDHash); err != nil {
				return 0, err
			}
		}
		// A RowsAffected failure can return zero even when DELETE succeeded.
		return 0, f.injectedError(keepIDHash)
	}
	return f.fakeStore.DeleteSessionsExcept(ctx, keepIDHash)
}

func (f *passwordChangeFaultStore) CreateAuditEntry(ctx context.Context, entry models.AuditEntry) (models.AuditEntry, error) {
	f.auditAttempts = append(f.auditAttempts, entry)
	f.auditContextErr = ctx.Err()
	f.auditDeadline, f.auditHasDeadline = ctx.Deadline()
	f.auditContextValue = ctx.Value(passwordChangeAuditTestContextKey{})
	if f.fault == "audit_write" {
		return models.AuditEntry{}, f.injectedError(strings.Join(f.attemptedSessionHashes, " "))
	}
	return f.fakeStore.CreateAuditEntry(ctx, entry)
}

func assertPasswordChangeSecretsAbsent(t *testing.T, serializedEntry, logs string, secrets []string) {
	t.Helper()
	for i, secret := range secrets {
		if secret == "" {
			continue
		}
		if strings.Contains(serializedEntry, secret) {
			t.Errorf("serialized audit entry contains secret at index %d", i)
		}
		if strings.Contains(logs, secret) {
			t.Errorf("log output contains secret at index %d", i)
		}
	}
}

func TestChangePassword_AuditConfirmedOutcomes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		fault             string
		status            int
		mustChangeCleared bool
		sessionIssued     bool
		revocationStatus  string
		failureStage      string
		revoked           int // -1 means the JSON value must be null, not zero.
		deleteBeforeError bool
		cancelRequest     bool
		apiToken          bool
	}{
		{name: "success", status: 200, mustChangeCleared: true, sessionIssued: true, revocationStatus: "completed", revoked: 1},
		{name: "hash_write_failure", fault: "hash_write", status: 500, revoked: -1},
		{name: "flag_failure", fault: "must_change_clear", status: 500, revocationStatus: "not_attempted", failureStage: "must_change_clear", revoked: -1},
		{name: "session_failure", fault: "session_issue", status: 500, mustChangeCleared: true, revocationStatus: "not_attempted", failureStage: "session_issue", revoked: -1},
		{name: "revocation_failure", fault: "session_revocation", status: 200, mustChangeCleared: true, sessionIssued: true, revocationStatus: "failed", failureStage: "session_revocation", revoked: -1},
		{name: "revocation_count_failure_after_delete", fault: "session_revocation", status: 200, mustChangeCleared: true, sessionIssued: true, revocationStatus: "failed", failureStage: "session_revocation", revoked: -1, deleteBeforeError: true},
		{name: "audit_write_failure", fault: "audit_write", status: 200, mustChangeCleared: true, sessionIssued: true, revocationStatus: "completed", revoked: 1},
		{name: "canceled_flag_failure", fault: "must_change_clear", status: 500, revocationStatus: "not_attempted", failureStage: "must_change_clear", revoked: -1, cancelRequest: true},
		{name: "api_token_actor_zero_revocations", status: 200, mustChangeCleared: true, sessionIssued: true, revocationStatus: "completed", revoked: 0, apiToken: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const oldPassword = "round28-old-password-1"
			const newPassword = "round28-new-password-2"
			base := newFakeStore()
			seedPassword(t, base, oldPassword, true)
			oldHash, _, _ := base.GetSetting(t.Context(), SettingPasswordHash)
			store := &passwordChangeFaultStore{fakeStore: base}
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			opts := testOptions()
			s := New(opts, store, nil, nil, nil, logger)
			var oldToken string
			bearer := opts.UIToken
			if !tc.apiToken {
				body, _ := json.Marshal(loginRequest{Username: adminUsername, Password: oldPassword})
				rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.40:1", "", body)
				var response models.LoginResponse
				if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.Token == "" {
					t.Fatal("seed login failed")
				}
				oldToken, bearer = response.Token, response.Token
			}
			beforeSessions, _ := base.ListSessions(t.Context())
			store.settingWrites, store.sessionCreates, store.revocationCalls = 0, 0, 0
			store.attemptedSessionHashes = nil
			store.fault, store.deleteBeforeError = tc.fault, tc.deleteBeforeError
			store.failureSecrets = strings.Join([]string{oldPassword, newPassword, oldHash, bearer, hashSessionToken(bearer)}, " ")
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), passwordChangeAuditTestContextKey{}, "preserved"), time.Hour)
			defer cancel()
			if tc.cancelRequest {
				store.cancelOnFailure = cancel
			}
			body, _ := json.Marshal(changePasswordRequest{CurrentPassword: oldPassword, NewPassword: newPassword})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", bytes.NewReader(body)).WithContext(ctx)
			req.RemoteAddr = "203.0.113.40:2"
			req.Header.Set("Authorization", "Bearer "+bearer)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			newHash, _, _ := base.GetSetting(t.Context(), SettingPasswordHash)
			flag, _, _ := base.GetSetting(t.Context(), SettingMustChangePassword)
			afterSessions, _ := base.ListSessions(t.Context())
			var response models.LoginResponse
			if tc.status == http.StatusOK {
				var responseKeys map[string]json.RawMessage
				if json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.Token == "" || response.Token == bearer || response.MustChangePassword || response.Username != adminUsername || response.ExpiresAt <= s.opts.now().Unix() {
					t.Error("successful rotation response contract changed")
				}
				if json.Unmarshal(rec.Body.Bytes(), &responseKeys) != nil || len(responseKeys) != 4 {
					t.Error("successful rotation must retain exactly the four existing response fields")
				}
				if _, err := base.GetSession(t.Context(), hashSessionToken(response.Token)); err != nil {
					t.Error("issued session was not retained")
				}
			} else {
				var response models.APIError
				if json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.Code != "internal_error" || response.Error != "internal server error" {
					t.Error("failure response must keep the existing generic internal error")
				}
			}
			wantFlag := "1"
			if tc.mustChangeCleared {
				wantFlag = "0"
			}
			if flag != wantFlag {
				t.Error("persisted must-change flag does not match the confirmed outcome")
			}
			if tc.fault == "hash_write" {
				if newHash != oldHash || !reflect.DeepEqual(beforeSessions, afterSessions) || len(store.auditAttempts) != 0 || store.settingWrites != 1 || store.sessionCreates != 0 || store.revocationCalls != 0 {
					t.Error("failed hash write must not mutate credentials/sessions or attempt later steps/audit")
				}
			} else {
				if newHash == oldHash || !verifyPassword(newPassword, newHash) || verifyPassword(oldPassword, newHash) {
					t.Error("confirmed hash write must persist the new credential even after a later failure")
				}
				if len(store.auditAttempts) != 1 {
					t.Fatalf("audit attempts = %d, want one", len(store.auditAttempts))
				}
				entry := store.auditAttempts[0]
				wantActor := "admin"
				if tc.apiToken {
					wantActor = "api_token"
				}
				if entry.Action != AuditActionPasswordChange || entry.EntityType != "admin_password" || entry.EntityID != "" || entry.BeforeJSON != "" || entry.Actor != wantActor || entry.Remote != "203.0.113.40" {
					t.Error("audit identity, actor, remote, or empty-before contract changed")
				}
				var after map[string]any
				if json.Unmarshal([]byte(entry.AfterJSON), &after) != nil {
					t.Fatal("could not decode audit outcome")
				}
				want := map[string]any{
					"password_changed": true, "must_change_cleared": tc.mustChangeCleared,
					"sessions_revoked": nil, "session_issued": tc.sessionIssued,
					"session_revocation_status": tc.revocationStatus,
				}
				if tc.revoked >= 0 {
					want["sessions_revoked"] = float64(tc.revoked)
				}
				if tc.failureStage != "" {
					want["failure_stage"] = tc.failureStage
				}
				if !reflect.DeepEqual(after, want) {
					t.Error("audit metadata must match only confirmed outcomes, with null for unknown counts")
				}
				if store.auditContextErr != nil || !store.auditHasDeadline || time.Until(store.auditDeadline) > 2*time.Second || store.auditContextValue != "preserved" {
					t.Error("audit context must preserve values, ignore request cancellation/deadline, and have a short bound")
				}
				if tc.cancelRequest && ctx.Err() != context.Canceled {
					t.Error("test did not cancel the request at the injected failure")
				}
				wantCreates, wantRevocations := 0, 0
				if tc.mustChangeCleared {
					wantCreates = 1
				}
				if tc.sessionIssued {
					wantRevocations = 1
				}
				if store.settingWrites != 2 || store.sessionCreates != wantCreates || store.revocationCalls != wantRevocations {
					t.Error("unexpected later-stage attempt count")
				}
				if !tc.sessionIssued && !reflect.DeepEqual(beforeSessions, afterSessions) {
					t.Error("failure before session issuance must leave existing sessions intact")
				}
				if tc.sessionIssued && oldToken != "" {
					_, oldSessionErr := base.GetSession(t.Context(), hashSessionToken(oldToken))
					wantOldSession := tc.fault == "session_revocation" && !tc.deleteBeforeError
					if (oldSessionErr == nil) != wantOldSession {
						t.Error("old session persistence does not match the injected revocation outcome")
					}
				}
			}
			entries, err := base.ListAuditEntries(t.Context(), "", 0, 10)
			if err != nil {
				t.Fatal("could not list persisted audit entries")
			}
			wantEntries := 1
			if tc.fault == "hash_write" || tc.fault == "audit_write" {
				wantEntries = 0
			}
			if len(entries) != wantEntries {
				t.Errorf("persisted audit entry count = %d, want %d", len(entries), wantEntries)
			}
			if tc.fault == "audit_write" && !strings.Contains(logBuf.String(), "audit: create entry failed") {
				t.Error("audit write failure must leave a safe failure log")
			}
			serialized, err := json.Marshal(store.auditAttempts)
			if err != nil {
				t.Fatal("could not serialize attempted audit entries")
			}
			secrets := []string{oldPassword, newPassword, oldHash, newHash, store.attemptedPasswordHash, bearer, hashSessionToken(bearer)}
			if response.Token != "" {
				secrets = append(secrets, response.Token, hashSessionToken(response.Token))
			}
			secrets = append(secrets, store.attemptedSessionHashes...)
			assertPasswordChangeSecretsAbsent(t, string(serialized), logBuf.String(), secrets)
		})
	}
}

func TestChangePassword_RevokesOtherSessions(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	tokenA := loginAndGetToken(t, s, store, "old-password-1")

	// A second, independent session for the same account.
	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "old-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.28:1", "", body)
	tokenB := decodeJSON[models.LoginResponse](t, rec.Body).Token

	changeBody, _ := json.Marshal(changePasswordRequest{CurrentPassword: "old-password-1", NewPassword: "brand-new-password-1"})
	rec2 := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/password", "203.0.113.28:1", tokenA, changeBody)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}

	rec3 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.28:1", tokenB, nil)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("tokenB status = %d, want 401 (revoked by password change)", rec3.Code)
	}
}

func TestChangePassword_WrongCurrent401(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	token := loginAndGetToken(t, s, store, "old-password-1")

	body, _ := json.Marshal(changePasswordRequest{CurrentPassword: "totally-wrong", NewPassword: "brand-new-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/password", "203.0.113.29:1", token, body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "invalid_credentials" {
		t.Errorf("code = %q, want invalid_credentials", got.Code)
	}

	// Original session must still work (no change applied).
	rec2 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.29:1", token, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (session should survive a rejected change)", rec2.Code)
	}
}

func TestChangePassword_WeakPassword400(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	token := loginAndGetToken(t, s, store, "old-password-1")

	cases := []struct {
		name string
		new  string
	}{
		{"too_short", "short"},
		{"is_changeme", "changeme"},
		{"same_as_current", "old-password-1"},
		{"all_whitespace", "        "},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body, _ := json.Marshal(changePasswordRequest{CurrentPassword: "old-password-1", NewPassword: tc.new})
			rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/password", "203.0.113.30:1", token, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			got := decodeJSON[models.APIError](t, rec.Body)
			if got.Code != "weak_password" {
				t.Errorf("code = %q, want weak_password", got.Code)
			}
		})
	}
}

func TestChangePassword_InvalidBody400(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	token := loginAndGetToken(t, s, store, "old-password-1")

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/password", "203.0.113.31:1", token, []byte("not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestChangePassword_NoAuth401(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	body, _ := json.Marshal(changePasswordRequest{CurrentPassword: "x", NewPassword: "brand-new-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/password", "203.0.113.32:1", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// --- GET /api/v1/auth/sessions & revoke-others ---

func TestListSessions_MarksCurrent(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	tokenA := loginAndGetToken(t, s, store, "correct-password-1")

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "correct-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.33:2", "", body)
	tokenB := decodeJSON[models.LoginResponse](t, rec.Body).Token

	rec2 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/sessions", "203.0.113.33:1", tokenA, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}
	got := decodeJSON[[]models.SessionView](t, rec2.Body)
	if len(got) != 2 {
		t.Fatalf("sessions = %+v, want 2 entries", got)
	}
	currentCount := 0
	for _, sv := range got {
		if sv.Current {
			currentCount++
		}
		if len(sv.ID) != 8 {
			t.Errorf("session id = %q, want 8 hex characters", sv.ID)
		}
	}
	if currentCount != 1 {
		t.Errorf("current count = %d, want exactly 1", currentCount)
	}
	_ = tokenB
}

func TestRevokeOtherSessions(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	tokenA := loginAndGetToken(t, s, store, "correct-password-1")

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "correct-password-1"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.34:2", "", body)
	tokenB := decodeJSON[models.LoginResponse](t, rec.Body).Token

	rec2 := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/sessions/revoke-others", "203.0.113.34:1", tokenA, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}
	got := decodeJSON[map[string]int](t, rec2.Body)
	if got["revoked"] != 1 {
		t.Errorf("revoked = %d, want 1", got["revoked"])
	}

	// tokenA still works, tokenB does not.
	recA := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.34:1", tokenA, nil)
	if recA.Code != http.StatusOK {
		t.Fatalf("tokenA status = %d, want 200", recA.Code)
	}
	recB := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/auth/me", "203.0.113.34:1", tokenB, nil)
	if recB.Code != http.StatusUnauthorized {
		t.Fatalf("tokenB status = %d, want 401 (revoked)", recB.Code)
	}
}

// --- EnsureDefaultCredentials / bootstrap ---

func TestEnsureDefaultCredentials_BootstrapsOnFreshStore(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	if err := s.EnsureDefaultCredentials(t.Context()); err != nil {
		t.Fatalf("EnsureDefaultCredentials: %v", err)
	}

	hash, ok, err := store.GetSetting(t.Context(), SettingPasswordHash)
	if err != nil || !ok {
		t.Fatalf("password hash not stored: ok=%v err=%v", ok, err)
	}
	if !verifyPassword("changeme", hash) {
		t.Error("bootstrapped hash does not verify against \"changeme\"")
	}
	mustChange, ok, err := store.GetSetting(t.Context(), SettingMustChangePassword)
	if err != nil || !ok || mustChange != "1" {
		t.Errorf("must_change = %q (ok=%v), want \"1\"", mustChange, ok)
	}
}

func TestEnsureDefaultCredentials_NoOpWhenAlreadySet(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPassword(t, store, "already-set-password-1", false)
	s := newTestServer(t, testOptions(), store)

	if err := s.EnsureDefaultCredentials(t.Context()); err != nil {
		t.Fatalf("EnsureDefaultCredentials: %v", err)
	}

	hash, _, _ := store.GetSetting(t.Context(), SettingPasswordHash)
	if !verifyPassword("already-set-password-1", hash) {
		t.Error("EnsureDefaultCredentials must not overwrite an existing password hash")
	}
}

func TestEnsureDefaultCredentials_TokensAndPasswordsNeverLogged(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	opts := testOptions()
	s := New(opts, store, nil, nil, nil, logger)

	if err := s.EnsureDefaultCredentials(t.Context()); err != nil {
		t.Fatalf("EnsureDefaultCredentials: %v", err)
	}

	body, _ := json.Marshal(loginRequest{Username: "admin", Password: "changeme"})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.35:1", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	token := decodeJSON[models.LoginResponse](t, rec.Body).Token

	logged := logBuf.String()
	if strings.Contains(logged, "changeme") {
		t.Errorf("log output contains the password: %s", logged)
	}
	if strings.Contains(logged, token) {
		t.Errorf("log output contains the session token: %s", logged)
	}
}
