package models

import (
	"encoding/json"
	"testing"
)

func TestSessionJSON(t *testing.T) {
	t.Parallel()

	s := Session{
		IDHash:    "should-not-appear",
		CreatedAt: 100,
		LastSeen:  200,
		ExpiresAt: 300,
		Remote:    "192.0.2.1",
		UserAgent: "test-agent",
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(b); contains(got, "should-not-appear") {
		t.Errorf("Session JSON must not expose IDHash, got %s", got)
	}

	var decoded Session
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	decoded.IDHash = s.IDHash // IDHash is json:"-", restore for comparison
	if decoded != s {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, s)
	}
}

func TestSessionViewJSON(t *testing.T) {
	t.Parallel()

	v := SessionView{
		ID:        "deadbeef",
		CreatedAt: 1,
		LastSeen:  2,
		ExpiresAt: 3,
		Remote:    "203.0.113.5",
		UserAgent: "curl/8.0",
		Current:   true,
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded SessionView
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded != v {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, v)
	}

	want := map[string]bool{
		"id": true, "created_at": true, "last_seen": true, "expires_at": true,
		"remote": true, "user_agent": true, "current": true,
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for k := range want {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing JSON field %q", k)
		}
	}
}

func TestLoginResponseJSON(t *testing.T) {
	t.Parallel()

	lr := LoginResponse{
		Token:              "tok",
		ExpiresAt:          123,
		MustChangePassword: true,
		Username:           "admin",
	}
	b, err := json.Marshal(lr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, field := range []string{"token", "expires_at", "must_change_password", "username"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing JSON field %q", field)
		}
	}

	var decoded LoginResponse
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded != lr {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, lr)
	}
}

func TestMeResponseJSON(t *testing.T) {
	t.Parallel()

	m := MeResponse{
		Username:           "admin",
		MustChangePassword: false,
		AuthMethod:         "session",
		SessionExpiresAt:   999,
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded MeResponse
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded != m {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, m)
	}
}

func TestAPIErrorRetryAfterSecondsOmitted(t *testing.T) {
	t.Parallel()

	e := APIError{Error: "unauthorized"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if contains(string(b), "retry_after_seconds") {
		t.Errorf("retry_after_seconds must be omitted when zero, got %s", b)
	}

	e2 := APIError{Error: "rate limited", Code: "rate_limited", RetryAfterSeconds: 30}
	b2, err := json.Marshal(e2)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !contains(string(b2), `"retry_after_seconds":30`) {
		t.Errorf("expected retry_after_seconds:30 in %s", b2)
	}
}

// contains reports whether substr occurs within s. A tiny local helper
// to avoid importing strings solely for this.
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
