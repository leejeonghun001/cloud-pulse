package storageusage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage/googledrive"
)

// redirectingProvider wraps a googledrive.Client whose ssrfChecker
// redirects every call to srv, letting FetchQuota's two real endpoint
// calls (token exchange + about.get) both land on one fake server
// without needing to fake DNS/host rewriting.
func redirectingGoogleDriveProvider(srv *httptest.Server) *GoogleDriveProvider {
	redirect := func(rawURL string) (*url.URL, error) {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		target, err := url.Parse(srv.URL)
		if err != nil {
			return nil, err
		}
		target.Path = u.Path
		target.RawQuery = u.RawQuery
		return target, nil
	}
	return &GoogleDriveProvider{client: googledrive.New(srv.Client(), redirect)}
}

func TestGoogleDriveProvider_FetchQuota_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at1"})
		case "/drive/v3/about":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"storageQuota": map[string]string{"limit": "1000", "usage": "400", "usageInDriveTrash": "10"},
				"user":         map[string]string{"emailAddress": "u@example.com", "displayName": "U"},
			})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := redirectingGoogleDriveProvider(srv)

	q, err := p.FetchQuota(context.Background(), map[string]string{"client_id": "cid"},
		map[string]string{"client_secret": "csecret", "refresh_token": "rt1"})
	if err != nil {
		t.Fatalf("FetchQuota: %v", err)
	}
	if q.Quota.UsedBytes != 400 || q.Quota.LimitBytes != 1000 || q.Quota.TrashBytes != 10 {
		t.Errorf("unexpected quota: %+v", q.Quota)
	}
	if q.AccountEmail != "u@example.com" {
		t.Errorf("AccountEmail = %q", q.AccountEmail)
	}
}

func TestGoogleDriveProvider_FetchQuota_NotConfigured(t *testing.T) {
	t.Parallel()
	p := NewGoogleDriveProvider(http.DefaultClient, SSRFOptions{})
	_, err := p.FetchQuota(context.Background(), map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatal("expected error")
	}
	status, _ := p.Classify(err)
	if status != models.StorageAccountNotConfigured {
		t.Errorf("status = %q, want not_configured", status)
	}
}

func TestDropboxProvider_FetchQuota_NotConfigured(t *testing.T) {
	t.Parallel()
	p := NewDropboxProvider(http.DefaultClient, SSRFOptions{})
	_, err := p.FetchQuota(context.Background(), map[string]string{}, map[string]string{})
	if err == nil {
		t.Fatal("expected error")
	}
	status, _ := p.Classify(err)
	if status != models.StorageAccountNotConfigured {
		t.Errorf("status = %q, want not_configured", status)
	}
}

func TestSSRFValidateURL_RejectsNonOfficialHost(t *testing.T) {
	t.Parallel()
	_, err := validateURL("https://evil.example.com/steal", SSRFOptions{})
	if err == nil {
		t.Fatal("expected rejection")
	}
}

func TestSSRFValidateURL_AllowsOfficialHost(t *testing.T) {
	t.Parallel()
	for host := range officialHosts {
		u, err := validateURL("https://"+host+"/path", SSRFOptions{})
		if err != nil {
			t.Errorf("validateURL for official host %q: %v", host, err)
		}
		if u == nil || u.Hostname() != host {
			t.Errorf("unexpected parsed url for %q: %+v", host, u)
		}
	}
}

func TestSSRFValidateURL_RejectsPlainHTTPByDefault(t *testing.T) {
	t.Parallel()
	_, err := validateURL("http://www.dropbox.com/oauth2/authorize", SSRFOptions{})
	if err == nil {
		t.Fatal("expected rejection of non-https url")
	}
}

func TestSSRFValidateURL_TestOptionOverridesAllowlist(t *testing.T) {
	t.Parallel()
	opts := SSRFOptions{AllowedHosts: map[string]bool{"127.0.0.1": true}, AllowInsecure: true}
	u, err := validateURL("http://127.0.0.1:12345/x", opts)
	if err != nil {
		t.Fatalf("validateURL with test override: %v", err)
	}
	if u.Hostname() != "127.0.0.1" {
		t.Errorf("unexpected host: %v", u)
	}
}

func TestValidateGoogleURLWithOptions_DisabledMatchesProduction(t *testing.T) {
	t.Parallel()
	if _, err := ValidateGoogleURLWithOptions("http://127.0.0.1:9/x", false, "http://127.0.0.1:9"); err == nil {
		t.Fatal("expected rejection with allowCustomEndpoints=false (production behavior)")
	}
	u, err := ValidateGoogleURLWithOptions("https://oauth2.googleapis.com/token", false, "")
	if err != nil {
		t.Fatalf("official host should still be allowed: %v", err)
	}
	if u.Hostname() != "oauth2.googleapis.com" {
		t.Errorf("unexpected host: %v", u)
	}
}

func TestValidateGoogleURLWithOptions_EnabledRedirectsToFakeBase(t *testing.T) {
	t.Parallel()
	u, err := ValidateGoogleURLWithOptions("https://oauth2.googleapis.com/device/code?a=1", true, "http://127.0.0.1:18500")
	if err != nil {
		t.Fatalf("expected the request to be redirected to the fake base: %v", err)
	}
	if got := u.String(); got != "http://127.0.0.1:18500/device/code?a=1" {
		t.Errorf("redirected url = %q, want %q", got, "http://127.0.0.1:18500/device/code?a=1")
	}
}

func TestValidateGoogleURLWithOptions_EnabledButNoFakeBaseFallsBackToOfficialHosts(t *testing.T) {
	t.Parallel()
	if _, err := ValidateGoogleURLWithOptions("http://127.0.0.1:9/x", true, ""); err == nil {
		t.Fatal("expected rejection: allowCustomEndpoints=true with no RedirectBase should not accept an arbitrary host")
	}
	if _, err := ValidateGoogleURLWithOptions("https://oauth2.googleapis.com/token", true, ""); err != nil {
		t.Errorf("official host should still be allowed: %v", err)
	}
}

func TestValidateDropboxURLWithOptions_DisabledMatchesProduction(t *testing.T) {
	t.Parallel()
	if _, err := ValidateDropboxURLWithOptions("http://127.0.0.1:9/x", false, "http://127.0.0.1:9"); err == nil {
		t.Fatal("expected rejection with allowCustomEndpoints=false (production behavior)")
	}
}

func TestValidateDropboxURLWithOptions_EnabledRedirectsToFakeBase(t *testing.T) {
	t.Parallel()
	u, err := ValidateDropboxURLWithOptions("https://api.dropboxapi.com/oauth2/token", true, "http://127.0.0.1:18501")
	if err != nil {
		t.Fatalf("expected the request to be redirected to the fake base: %v", err)
	}
	if got := u.String(); got != "http://127.0.0.1:18501/oauth2/token" {
		t.Errorf("redirected url = %q, want %q", got, "http://127.0.0.1:18501/oauth2/token")
	}
}

func TestAllowCustomEndpointsFromEnv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"unset", map[string]string{}, false},
		{"zero", map[string]string{"CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS": "0"}, false},
		{"true_string_not_one", map[string]string{"CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS": "true"}, false},
		{"one", map[string]string{"CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS": "1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) {
				v, ok := tc.env[k]
				return v, ok
			}
			if got := AllowCustomEndpointsFromEnv(lookup); got != tc.want {
				t.Errorf("AllowCustomEndpointsFromEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRedirectBaseFromEnv(t *testing.T) {
	t.Parallel()
	lookup := func(k string) (string, bool) {
		if k == "CP_STORAGE_FAKE_BASE_URL" {
			return "http://127.0.0.1:18500", true
		}
		return "", false
	}
	if got := RedirectBaseFromEnv(lookup); got != "http://127.0.0.1:18500" {
		t.Errorf("RedirectBaseFromEnv() = %q, want %q", got, "http://127.0.0.1:18500")
	}
	empty := func(string) (string, bool) { return "", false }
	if got := RedirectBaseFromEnv(empty); got != "" {
		t.Errorf("RedirectBaseFromEnv() with unset env = %q, want empty", got)
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()
	p := NewGoogleDriveProvider(http.DefaultClient, SSRFOptions{})
	registry := NewRegistry(p)
	got, ok := registry.Get(string(models.StorageAccountGoogleDrive))
	if !ok || got == nil {
		t.Fatal("expected lookup to find registered provider")
	}
	if got.ID() != models.StorageAccountGoogleDrive {
		t.Errorf("ID = %q", got.ID())
	}
}

func TestRegistry_GetMissingReturnsFalse(t *testing.T) {
	t.Parallel()
	_, ok := NewRegistry().Get("no-such-provider")
	if ok {
		t.Error("expected ok=false for an unregistered provider id")
	}
}
