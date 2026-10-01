package hub

// Tests for the stable machine-readable Code field on models.APIError
// responses (SPEC-v0.8 §3-3), added so the web dashboard can map a
// server error to a localized `errors.*` catalog key instead of
// displaying the server's (English-only) Error message verbatim.
// Unknown/future codes are expected to fall back to that Error message
// client-side; this file only asserts that the codes documented in
// docs/api.md are actually present on the responses that carry them.

import (
	"net/http"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// TestErrorCode_HostNotFound covers the most commonly hit not-found
// path (GET /api/v1/hosts/{id} for an unknown host).
func TestErrorCode_HostNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	token := loginAndGetToken(t, s, store, "correct-password-1")

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts/does-not-exist", "203.0.113.30:1", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "host_not_found" {
		t.Errorf("code = %q, want %q", got.Code, "host_not_found")
	}
	if got.Error == "" {
		t.Error("Error message must still be present for unknown-code fallback clients")
	}
}

// TestErrorCode_InvalidRequestBody covers the generic malformed-JSON
// path shared by most mutation endpoints.
func TestErrorCode_InvalidRequestBody(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	token := loginAndGetToken(t, s, store, "correct-password-1")

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels", "203.0.113.31:1", token, []byte("{not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "invalid_request_body" {
		t.Errorf("code = %q, want %q", got.Code, "invalid_request_body")
	}
}

// TestErrorCode_Unauthorized covers the no-bearer-token 401 path,
// distinct from the "session_expired" code used when a token was
// presented but doesn't match any active session.
func TestErrorCode_Unauthorized(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.32:1", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "unauthorized" {
		t.Errorf("code = %q, want %q", got.Code, "unauthorized")
	}
}

// TestErrorCode_MethodNotAllowed covers the shared handler wrapping
// http.ServeMux's method-not-allowed response in a models.APIError body.
func TestErrorCode_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)
	token := loginAndGetToken(t, s, store, "correct-password-1")

	rec := doRequest(t, s.Handler(), http.MethodPatch, "/api/v1/hosts", "203.0.113.33:1", token, nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Code != "method_not_allowed" {
		t.Errorf("code = %q, want %q", got.Code, "method_not_allowed")
	}
}
