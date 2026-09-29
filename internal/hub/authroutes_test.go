package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	if got.Code != "" {
		t.Errorf("code = %q, want empty (no bearer token at all)", got.Code)
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
