package hub

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func adminTestOptions(uiToken string) Options {
	opts := testOptions()
	opts.UIToken = uiToken
	return opts
}

// --- Auth matrix: applies to every admin route ---

func TestAdmin_AuthMatrix(t *testing.T) {
	t.Parallel()

	routes := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"get_settings", http.MethodGet, "/api/v1/settings", nil},
		{"get_agent_token", http.MethodGet, "/api/v1/settings/agent-token", nil},
		{"put_host_limits", http.MethodPut, "/api/v1/hosts/host-x/limits", []byte(`{}`)},
		{"put_alerts", http.MethodPut, "/api/v1/settings/alerts", []byte(`{}`)},
		{"post_alerts_test", http.MethodPost, "/api/v1/settings/alerts/test", nil},
	}

	for _, rt := range routes {
		rt := rt
		t.Run(rt.name, func(t *testing.T) {
			t.Parallel()

			t.Run("ui_token_unset_401_unauthorized", func(t *testing.T) {
				t.Parallel()
				store := newFakeStore()
				if rt.name == "put_host_limits" {
					mustUpsertHost(t, store, "host-x")
				}
				s := newTestServer(t, adminTestOptions(""), store)
				rec := doRequest(t, s.Handler(), rt.method, rt.path, "203.0.113.1:1234", "", rt.body)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401 (admin_disabled is removed per SPEC-v0.4 §1; body=%s)", rec.Code, rec.Body.String())
				}
			})

			t.Run("ui_token_unset_valid_session_200", func(t *testing.T) {
				t.Parallel()
				store := newFakeStore()
				if rt.name == "put_host_limits" {
					mustUpsertHost(t, store, "host-x")
				}
				opts := adminTestOptions("")
				if rt.name == "post_alerts_test" {
					opts.AlertWebhookURL = "https://hooks.example.com/webhook"
					s := New(opts, store, nil, &fakeNotifier{}, nil, testLogger())
					token := loginAndGetToken(t, s, store, "session-password-1")
					rec := doRequest(t, s.Handler(), rt.method, rt.path, "203.0.113.1:1234", token, rt.body)
					if rec.Code != http.StatusOK {
						t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
					}
					return
				}
				s := newTestServer(t, opts, store)
				token := loginAndGetToken(t, s, store, "session-password-1")
				rec := doRequest(t, s.Handler(), rt.method, rt.path, "203.0.113.1:1234", token, rt.body)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (a session works with no CP_UI_TOKEN configured; body=%s)", rec.Code, rec.Body.String())
				}
			})

			t.Run("ui_token_set_missing_token_401", func(t *testing.T) {
				t.Parallel()
				store := newFakeStore()
				if rt.name == "put_host_limits" {
					mustUpsertHost(t, store, "host-x")
				}
				s := newTestServer(t, adminTestOptions("admin-token-1234"), store)
				rec := doRequest(t, s.Handler(), rt.method, rt.path, "203.0.113.1:1234", "", rt.body)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
				}
			})

			t.Run("ui_token_set_wrong_token_401", func(t *testing.T) {
				t.Parallel()
				store := newFakeStore()
				if rt.name == "put_host_limits" {
					mustUpsertHost(t, store, "host-x")
				}
				s := newTestServer(t, adminTestOptions("admin-token-1234"), store)
				rec := doRequest(t, s.Handler(), rt.method, rt.path, "203.0.113.1:1234", "wrong-token-value", rt.body)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
				}
			})

			t.Run("ui_token_set_right_token_200", func(t *testing.T) {
				t.Parallel()
				store := newFakeStore()
				if rt.name == "put_host_limits" {
					mustUpsertHost(t, store, "host-x")
				}
				if rt.name == "post_alerts_test" {
					// Needs an effective webhook URL configured, else
					// 400 by design; covered separately. Skip 200 here
					// unless we configure one.
					opts := adminTestOptions("admin-token-1234")
					opts.AlertWebhookURL = "https://hooks.example.com/webhook"
					s := New(opts, store, nil, &fakeNotifier{}, nil, testLogger())
					rec := doRequest(t, s.Handler(), rt.method, rt.path, "203.0.113.1:1234", opts.UIToken, rt.body)
					if rec.Code != http.StatusOK {
						t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
					}
					return
				}
				s := newTestServer(t, adminTestOptions("admin-token-1234"), store)
				rec := doRequest(t, s.Handler(), rt.method, rt.path, "203.0.113.1:1234", "admin-token-1234", rt.body)
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
				}
			})
		})
	}
}

func mustUpsertHost(t *testing.T, store *fakeStore, id string) {
	t.Helper()
	if err := store.UpsertHost(t.Context(), sampleHostInfo(id), time.Now().Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
}

// --- JSON 405 on new routes ---

func TestAdmin_WrongMethod405(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, adminTestOptions("admin-token-1234"), store)

	cases := []struct {
		path   string
		method string
	}{
		{"/api/v1/settings", http.MethodPost},
		{"/api/v1/settings/agent-token", http.MethodPost},
		{"/api/v1/hosts/host-x/limits", http.MethodPost},
		{"/api/v1/settings/alerts", http.MethodPost},
		{"/api/v1/settings/alerts/test", http.MethodPut},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.method+"_"+tc.path, func(t *testing.T) {
			t.Parallel()
			rec := doRequest(t, s.Handler(), tc.method, tc.path, "203.0.113.1:1234", "admin-token-1234", nil)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405 (body=%s)", rec.Code, rec.Body.String())
			}
			got := decodeJSON[models.APIError](t, rec.Body)
			if got.Error != "method not allowed" {
				t.Errorf("error = %q, want method not allowed", got.Error)
			}
		})
	}
}

// --- GET /api/v1/settings ---

func TestGetSettings_View(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	mustUpsertHost(t, store, "host-a")

	opts := adminTestOptions("admin-token-1234")
	opts.AlertWebhookURL = "https://hooks.example.com/env"
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/settings", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.SettingsView](t, rec.Body)
	if !got.UIAuthEnabled {
		t.Error("UIAuthEnabled = false, want true")
	}
	if got.AlertWebhookURL != opts.AlertWebhookURL || got.AlertWebhookSource != "env" {
		t.Errorf("webhook = %q/%q, want %q/env", got.AlertWebhookURL, got.AlertWebhookSource, opts.AlertWebhookURL)
	}
	if len(got.Hosts) != 1 || got.Hosts[0].HostID != "host-a" {
		t.Errorf("hosts = %+v, want 1 entry for host-a", got.Hosts)
	}
	if !strings.Contains(got.AgentTokenHint, "…") {
		t.Errorf("AgentTokenHint = %q, want masked hint", got.AgentTokenHint)
	}
	if strings.Contains(got.AgentTokenHint, opts.AgentToken) {
		t.Error("AgentTokenHint must not contain the full token")
	}
}

func TestGetSettings_HostsEmptyNotNull(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/settings", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"hosts":[]`) {
		t.Errorf("expected hosts:[] in body, got %s", rec.Body.String())
	}
}

func TestGetSettings_NoWebhookConfigured(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/settings", "203.0.113.1:1234", opts.UIToken, nil)
	got := decodeJSON[models.SettingsView](t, rec.Body)
	if got.AlertWebhookURL != "" || got.AlertWebhookSource != "none" {
		t.Errorf("webhook = %q/%q, want empty/none", got.AlertWebhookURL, got.AlertWebhookSource)
	}
}

// --- GET /api/v1/settings/agent-token ---

func TestGetAgentToken_InstallCommandUsesRequestHost(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	rec := doRequestWithHost(t, s.Handler(), "hub.example.internal:8090", opts.UIToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.AgentTokenView](t, rec.Body)
	if got.AgentToken != opts.AgentToken {
		t.Errorf("AgentToken = %q, want %q", got.AgentToken, opts.AgentToken)
	}
	wantURL := "http://hub.example.internal:8090"
	if !strings.Contains(got.InstallCommand, wantURL) {
		t.Errorf("InstallCommand = %q, want it to contain %q", got.InstallCommand, wantURL)
	}
	if !strings.Contains(got.InstallCommand, opts.AgentToken) {
		t.Errorf("InstallCommand = %q, want it to contain the token", got.InstallCommand)
	}
}

func TestGetAgentToken_MaliciousHostHeaderUsesPlaceholder(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	malicious := "evil.com; rm -rf / #"
	rec := doRequestWithHost(t, s.Handler(), malicious, opts.UIToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.AgentTokenView](t, rec.Body)
	if strings.Contains(got.InstallCommand, malicious) {
		t.Errorf("InstallCommand = %q, must not contain the malicious host header", got.InstallCommand)
	}
	if !strings.Contains(got.InstallCommand, "<HUB_URL>") {
		t.Errorf("InstallCommand = %q, want placeholder <HUB_URL>", got.InstallCommand)
	}
}

func TestGetAgentToken_TokenNeverLogged(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := New(opts, store, nil, nil, nil, logger)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/settings/agent-token", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	logged := logBuf.String()
	if strings.Contains(logged, opts.AgentToken) {
		t.Errorf("log output contains the agent token: %s", logged)
	}
	if !strings.Contains(logged, "agent token revealed") {
		t.Errorf("log output missing reveal audit line: %s", logged)
	}
}

// requestOpts is unused; kept out. doRequestWithHost builds a GET
// request to /api/v1/settings/agent-token with req.Host set explicitly
// (httptest.NewRequest would otherwise derive Host from the target
// URL), to exercise installCommand's Host-header handling.
func doRequestWithHost(t *testing.T, h http.Handler, host, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/agent-token", nil)
	req.RemoteAddr = "203.0.113.1:1234"
	req.Host = host
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// --- PUT /api/v1/hosts/{id}/limits ---

func TestSetHostLimits_UnknownHost404(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/hosts/nope/limits", "203.0.113.1:1234", opts.UIToken, []byte(`{}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestSetHostLimits_ExceedsMax400(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	mustUpsertHost(t, store, "host-a")
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	tooBig := uint64(1)<<62 + 1
	body, _ := json.Marshal(map[string]uint64{"egress_limit_bytes": tooBig})
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/hosts/host-a/limits", "203.0.113.1:1234", opts.UIToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}

	body2, _ := json.Marshal(map[string]uint64{"ingress_limit_bytes": tooBig})
	rec2 := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/hosts/host-a/limits", "203.0.113.1:1234", opts.UIToken, body2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("ingress status = %d, want 400 (body=%s)", rec2.Code, rec2.Body.String())
	}
}

func TestSetHostLimits_AtMaxAccepted(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	mustUpsertHost(t, store, "host-a")
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	atMax := uint64(1) << 62
	body, _ := json.Marshal(map[string]uint64{"egress_limit_bytes": atMax})
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/hosts/host-a/limits", "203.0.113.1:1234", opts.UIToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestSetHostLimits_SetAndClear(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	mustUpsertHost(t, store, "host-a")
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	egress := uint64(5000)
	ingress := uint64(2000)
	body, _ := json.Marshal(models.HostLimits{EgressLimitBytes: &egress, IngressLimitBytes: &ingress})
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/hosts/host-a/limits", "203.0.113.1:1234", opts.UIToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.HostLimits](t, rec.Body)
	if got.EgressLimitBytes == nil || *got.EgressLimitBytes != egress {
		t.Errorf("EgressLimitBytes = %v, want %d", got.EgressLimitBytes, egress)
	}
	if got.IngressLimitBytes == nil || *got.IngressLimitBytes != ingress {
		t.Errorf("IngressLimitBytes = %v, want %d", got.IngressLimitBytes, ingress)
	}

	// Absent fields (both null) clears the override.
	rec2 := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/hosts/host-a/limits", "203.0.113.1:1234", opts.UIToken, []byte(`{}`))
	if rec2.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}
	got2 := decodeJSON[models.HostLimits](t, rec2.Body)
	if got2.EgressLimitBytes != nil || got2.IngressLimitBytes != nil {
		t.Errorf("after clear = %+v, want both nil", got2)
	}
}

func TestSetHostLimits_InvalidBody400(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	mustUpsertHost(t, store, "host-a")
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/hosts/host-a/limits", "203.0.113.1:1234", opts.UIToken, []byte("not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// --- PUT /api/v1/settings/alerts ---

func TestSetAlertWebhook_ValidatesURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"valid_https", `{"webhook_url":"https://hooks.example.com/x"}`, http.StatusOK},
		{"valid_http", `{"webhook_url":"http://hooks.example.com/x"}`, http.StatusOK},
		{"empty_clears", `{"webhook_url":""}`, http.StatusOK},
		{"bad_scheme", `{"webhook_url":"ftp://hooks.example.com/x"}`, http.StatusBadRequest},
		{"no_host", `{"webhook_url":"https:///x"}`, http.StatusBadRequest},
		{"whitespace", `{"webhook_url":"https://hooks.example.com/x y"}`, http.StatusBadRequest},
		{"control_char", "{\"webhook_url\":\"https://hooks.example.com/x\ty\"}", http.StatusBadRequest},
		{"too_long", `{"webhook_url":"https://hooks.example.com/` + strings.Repeat("a", 2100) + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			opts := adminTestOptions("admin-token-1234")
			s := newTestServer(t, opts, store)
			rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/alerts", "203.0.113.1:1234", opts.UIToken, []byte(tc.body))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestSetAlertWebhook_PrecedenceOverEnv(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")
	opts.AlertWebhookURL = "https://hooks.example.com/env"
	s := newTestServer(t, opts, store)

	// Before any override: source=env.
	rec0 := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/settings", "203.0.113.1:1234", opts.UIToken, nil)
	got0 := decodeJSON[models.SettingsView](t, rec0.Body)
	if got0.AlertWebhookSource != "env" || got0.AlertWebhookURL != opts.AlertWebhookURL {
		t.Fatalf("initial = %q/%q, want env/%q", got0.AlertWebhookURL, got0.AlertWebhookSource, opts.AlertWebhookURL)
	}

	// Set a hub override: source=hub, hub value takes precedence.
	hubURL := "https://hooks.example.com/hub"
	body, _ := json.Marshal(map[string]string{"webhook_url": hubURL})
	rec1 := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/alerts", "203.0.113.1:1234", opts.UIToken, body)
	if rec1.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200 (body=%s)", rec1.Code, rec1.Body.String())
	}
	got1 := decodeJSON[models.SettingsView](t, rec1.Body)
	if got1.AlertWebhookSource != "hub" || got1.AlertWebhookURL != hubURL {
		t.Fatalf("after set = %q/%q, want hub/%q", got1.AlertWebhookURL, got1.AlertWebhookSource, hubURL)
	}

	// Clear the override ("" body): falls back to env again.
	body2, _ := json.Marshal(map[string]string{"webhook_url": ""})
	rec2 := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/settings/alerts", "203.0.113.1:1234", opts.UIToken, body2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}
	got2 := decodeJSON[models.SettingsView](t, rec2.Body)
	if got2.AlertWebhookSource != "env" || got2.AlertWebhookURL != opts.AlertWebhookURL {
		t.Fatalf("after clear = %q/%q, want env/%q", got2.AlertWebhookURL, got2.AlertWebhookSource, opts.AlertWebhookURL)
	}
}

// --- POST /api/v1/settings/alerts/test ---

func TestTestAlertWebhook_NoneConfigured400(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/settings/alerts/test", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestTestAlertWebhook_UsesFakeNotifierViaNotifierFor(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")
	opts.AlertWebhookURL = "https://hooks.example.com/env"

	fake := &fakeNotifier{}
	var capturedURL string
	opts.NotifierFor = func(url string) Notifier {
		capturedURL = url
		return fake
	}
	s := New(opts, store, nil, nil, nil, testLogger())

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/settings/alerts/test", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[map[string]bool](t, rec.Body)
	if !got["sent"] {
		t.Errorf("sent = %v, want true", got["sent"])
	}
	if fake.callCount() != 1 {
		t.Errorf("notify calls = %d, want 1", fake.callCount())
	}
	if capturedURL != opts.AlertWebhookURL {
		t.Errorf("NotifierFor called with %q, want %q", capturedURL, opts.AlertWebhookURL)
	}
}

func TestTestAlertWebhook_NotifyErrorReturns502(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := adminTestOptions("admin-token-1234")
	opts.AlertWebhookURL = "https://hooks.example.com/env"
	fake := &fakeNotifier{err: errFakeStore}
	s := New(opts, store, nil, fake, nil, testLogger())

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/settings/alerts/test", "203.0.113.1:1234", opts.UIToken, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%s)", rec.Code, rec.Body.String())
	}
}
