package hub

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// testLogger returns a slog.Logger that discards output but can be
// swapped for a buffer-backed one when a test wants to inspect log
// lines.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func testOptions() Options {
	return Options{
		AgentToken:    "test-agent-token-12345",
		OfflineAfter:  60 * time.Second,
		CloudInterval: 15 * time.Minute,
	}
}

func newTestServer(t *testing.T, opts Options, store Store) *Server {
	t.Helper()
	return New(opts, store, nil, nil, nil, testLogger())
}

func decodeJSON[T any](t *testing.T, body *bytes.Buffer) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		t.Fatalf("decode JSON: %v (body=%q)", err, body.String())
	}
	return v
}

func doRequest(t *testing.T, h http.Handler, method, path, remoteAddr, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthz_NoAuthRequired(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/healthz", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON[map[string]string](t, rec.Body)
	if got["status"] != "ok" {
		t.Errorf("status field = %q, want ok", got["status"])
	}
}

func TestSecurityHeaders_Present(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/healthz", "203.0.113.1:1234", "", nil)

	cases := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
	}
	for header, want := range cases {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("Content-Security-Policy header missing")
	}
}

func TestVersion_Endpoint(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/version", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON[map[string]string](t, rec.Body)
	for _, key := range []string{"version", "commit", "date"} {
		if _, ok := got[key]; !ok {
			t.Errorf("response missing key %q: %v", key, got)
		}
	}
}

func TestUnknownAPIPath_404JSON(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/nonexistent", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Error == "" {
		t.Error("expected non-empty error field")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestStaticServing_NilAssets404(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := New(testOptions(), store, nil, nil, nil, testLogger())

	rec := doRequest(t, s.Handler(), http.MethodGet, "/", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Error == "" {
		t.Error("expected non-empty error field")
	}
}

func TestStaticServing_FromMapFS(t *testing.T) {
	t.Parallel()
	assets := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>hello</html>")},
		"app.js":     &fstest.MapFile{Data: []byte("console.log('hi')")},
	}
	store := newFakeStore()
	s := New(testOptions(), store, nil, nil, assets, testLogger())

	rec := doRequest(t, s.Handler(), http.MethodGet, "/", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Errorf("body = %q, want it to contain index.html content", rec.Body.String())
	}

	rec2 := doRequest(t, s.Handler(), http.MethodGet, "/app.js", "203.0.113.1:1234", "", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /app.js status = %d, want 200", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "console.log") {
		t.Errorf("body = %q, want app.js content", rec2.Body.String())
	}
}

func TestRecoverMiddleware_PanicBecomes500(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	// Wrap a panicking handler through the same middleware stack used by
	// the real server, bypassing routing.
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom") // test-only: verifies recover middleware
	})

	var h http.Handler = panicHandler
	h = s.securityHeaders(h)
	h = s.cidrAllowlist(h)
	h = s.requestLogger(h)
	h = s.recoverMiddleware(h)

	rec := doRequest(t, h, http.MethodGet, "/panics", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Error == "" {
		t.Error("expected non-empty error field")
	}
}

func TestCIDRAllowlist(t *testing.T) {
	t.Parallel()

	tailscaleAndLoopback := []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}

	cases := []struct {
		name       string
		remoteAddr string
		wantStatus int
	}{
		{"denied_public_ipv4", "10.0.0.1:5555", http.StatusForbidden},
		{"allowed_tailscale_ipv4", "100.101.102.103:5555", http.StatusOK},
		{"allowed_ipv6_loopback", "[::1]:5555", http.StatusOK},
		{"allowed_ipv4_mapped_ipv6_loopback", "[::ffff:127.0.0.1]:5555", http.StatusOK},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := testOptions()
			opts.AllowedCIDRs = tailscaleAndLoopback
			store := newFakeStore()
			s := newTestServer(t, opts, store)

			rec := doRequest(t, s.Handler(), http.MethodGet, "/healthz", tc.remoteAddr, "", nil)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusForbidden {
				got := decodeJSON[models.APIError](t, rec.Body)
				if got.Error == "" {
					t.Error("expected non-empty error field")
				}
			}
		})
	}

	t.Run("nil_allowlist_allows_all", func(t *testing.T) {
		t.Parallel()
		opts := testOptions()
		opts.AllowedCIDRs = nil
		store := newFakeStore()
		s := newTestServer(t, opts, store)

		rec := doRequest(t, s.Handler(), http.MethodGet, "/healthz", "203.0.113.1:1234", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
}

func TestWrongMethod_405(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/hosts", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	got := decodeJSON[models.APIError](t, rec.Body)
	if got.Error != "method not allowed" {
		t.Errorf("error = %q, want method not allowed", got.Error)
	}
}
