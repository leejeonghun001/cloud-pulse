package hub

import (
	"encoding/json"
	"net/http"
	"testing"
)

// seedPassword stores password's hash (and the given must-change flag)
// directly in store, bypassing HTTP, so tests can set up an account
// state without going through EnsureDefaultCredentials or a live login
// round trip.
func seedPassword(t *testing.T, store Store, password string, mustChange bool) {
	t.Helper()
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if err := store.SetSetting(t.Context(), SettingPasswordHash, hash); err != nil {
		t.Fatalf("SetSetting(password hash): %v", err)
	}
	value := "0"
	if mustChange {
		value = "1"
	}
	if err := store.SetSetting(t.Context(), SettingMustChangePassword, value); err != nil {
		t.Fatalf("SetSetting(must change): %v", err)
	}
}

// loginAndGetToken seeds password (must_change=false) and performs a
// real HTTP login against h, returning the session bearer token. Tests
// use this to exercise requireUser's session path end-to-end rather
// than only via the static CP_UI_TOKEN path.
func loginAndGetToken(t *testing.T, s *Server, store Store, password string) string {
	t.Helper()
	seedPassword(t, store, password, false)

	body, _ := json.Marshal(loginRequest{Username: adminUsername, Password: password})
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/auth/login", "203.0.113.9:1", "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[loginResponseForTest](t, rec.Body)
	if got.Token == "" {
		t.Fatal("login response missing token")
	}
	return got.Token
}

// loginResponseForTest mirrors models.LoginResponse; using a distinct
// local type here (rather than models.LoginResponse everywhere) keeps
// this helper file self-contained for readability, but the JSON tags
// must stay in sync with models.LoginResponse.
type loginResponseForTest struct {
	Token              string `json:"token"`
	ExpiresAt          int64  `json:"expires_at"`
	MustChangePassword bool   `json:"must_change_password"`
	Username           string `json:"username"`
}
