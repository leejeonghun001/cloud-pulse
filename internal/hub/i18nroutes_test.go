package hub

import (
	"net/http"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/i18n"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// TestHandleGetNotifyLanguage_DefaultsToEnglish verifies GET
// /api/v1/settings/notify-language returns "en" when no setting has
// ever been written.
func TestHandleGetNotifyLanguage_DefaultsToEnglish(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/settings/notify-language", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[notifyLanguageResponse](t, rec.Body)
	if resp.Language != string(i18n.DefaultLang) {
		t.Errorf("Language = %q, want %q", resp.Language, i18n.DefaultLang)
	}
}

// TestHandleSetNotifyLanguage_RoundTrip verifies PUT persists the
// value and a subsequent GET reflects it.
func TestHandleSetNotifyLanguage_RoundTrip(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/notify-language", "127.0.0.1:1", testUIToken,
		[]byte(`{"language":"ko"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	putResp := decodeJSON[notifyLanguageResponse](t, rec.Body)
	if putResp.Language != "ko" {
		t.Errorf("PUT Language = %q, want %q", putResp.Language, "ko")
	}

	rec = doRequest(t, srv.Handler(), "GET", "/api/v1/settings/notify-language", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	getResp := decodeJSON[notifyLanguageResponse](t, rec.Body)
	if getResp.Language != "ko" {
		t.Errorf("GET Language after PUT = %q, want %q", getResp.Language, "ko")
	}
}

// TestHandleSetNotifyLanguage_CaseInsensitive verifies "EN"/"KO" are
// accepted and normalized to lowercase on write.
func TestHandleSetNotifyLanguage_CaseInsensitive(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/notify-language", "127.0.0.1:1", testUIToken,
		[]byte(`{"language":"KO"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[notifyLanguageResponse](t, rec.Body)
	if resp.Language != "ko" {
		t.Errorf("Language = %q, want %q", resp.Language, "ko")
	}
}

// TestHandleSetNotifyLanguage_RejectsInvalidValue verifies an
// unsupported language value is rejected with 400 and does not change
// the persisted setting.
func TestHandleSetNotifyLanguage_RejectsInvalidValue(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/notify-language", "127.0.0.1:1", testUIToken,
		[]byte(`{"language":"fr"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	apiErr := decodeJSON[models.APIError](t, rec.Body)
	if apiErr.Error == "" {
		t.Error("expected a non-empty error message")
	}

	rec = doRequest(t, srv.Handler(), "GET", "/api/v1/settings/notify-language", "127.0.0.1:1", testUIToken, nil)
	resp := decodeJSON[notifyLanguageResponse](t, rec.Body)
	if resp.Language != string(i18n.DefaultLang) {
		t.Errorf("Language after rejected PUT = %q, want unchanged default %q", resp.Language, i18n.DefaultLang)
	}
}

// TestHandleSetNotifyLanguage_RejectsMalformedBody verifies invalid
// JSON is rejected with 400 rather than a panic or 500.
func TestHandleSetNotifyLanguage_RejectsMalformedBody(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/notify-language", "127.0.0.1:1", testUIToken,
		[]byte(`not json`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestNotifyLanguageRoutes_RequireAdmin verifies both endpoints reject
// an unauthenticated request with 401.
func TestNotifyLanguageRoutes_RequireAdmin(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/settings/notify-language", "127.0.0.1:1", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET status = %d, want 401", rec.Code)
	}

	rec = doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/notify-language", "127.0.0.1:1", "",
		[]byte(`{"language":"ko"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("PUT status = %d, want 401", rec.Code)
	}
}
