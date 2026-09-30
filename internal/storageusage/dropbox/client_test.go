package dropbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// newTestServer starts an httptest server and returns a Client whose
// ssrfChecker redirects every request to that server.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
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
	return srv, New(srv.Client(), redirect)
}

func TestNewPKCEState_VerifierMatchesRFC7636CharacterSet(t *testing.T) {
	t.Parallel()
	state, err := NewPKCEState()
	if err != nil {
		t.Fatalf("NewPKCEState: %v", err)
	}
	if len(state.CodeVerifier) < 43 || len(state.CodeVerifier) > 128 {
		t.Errorf("CodeVerifier length = %d, want 43-128", len(state.CodeVerifier))
	}
	valid := regexp.MustCompile(`^[A-Za-z0-9\-._~]+$`)
	if !valid.MatchString(state.CodeVerifier) {
		t.Errorf("CodeVerifier %q contains characters outside RFC 7636's allowed set", state.CodeVerifier)
	}
}

func TestNewPKCEState_DifferentEachCall(t *testing.T) {
	t.Parallel()
	a, err := NewPKCEState()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewPKCEState()
	if err != nil {
		t.Fatal(err)
	}
	if a.CodeVerifier == b.CodeVerifier {
		t.Error("two consecutive NewPKCEState calls produced the same verifier")
	}
}

func TestCodeChallenge_KnownVector(t *testing.T) {
	t.Parallel()
	// RFC 7636 appendix B's worked example.
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := codeChallenge(verifier); got != want {
		t.Errorf("codeChallenge(%q) = %q, want %q", verifier, got, want)
	}
}

func TestAuthorizeURL_HasNoRedirectURIAndOfflineAccess(t *testing.T) {
	t.Parallel()
	state, err := NewPKCEState()
	if err != nil {
		t.Fatal(err)
	}
	raw := AuthorizeURL("app-key-123", state)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	if u.Query().Has("redirect_uri") {
		t.Error("authorize URL must not include redirect_uri (no-redirect flow)")
	}
	if got := u.Query().Get("token_access_type"); got != "offline" {
		t.Errorf("token_access_type = %q, want offline", got)
	}
	if got := u.Query().Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	if got := u.Query().Get("client_id"); got != "app-key-123" {
		t.Errorf("client_id = %q", got)
	}
	if u.Query().Get("code_challenge") == "" {
		t.Error("code_challenge must not be empty")
	}
}

func TestExchangeCode_UsesVerifierNotClientSecret(t *testing.T) {
	t.Parallel()
	state, err := NewPKCEState()
	if err != nil {
		t.Fatal(err)
	}
	var gotForm url.Values
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at1", "refresh_token": "rt1", "expires_in": 14400,
		})
	})

	res, err := client.ExchangeCode(context.Background(), "app-key", "pasted-code", state)
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if res.AccessToken != "at1" || res.RefreshToken != "rt1" {
		t.Errorf("unexpected result: %+v", res)
	}
	if gotForm.Get("client_secret") != "" {
		t.Error("PKCE exchange must never send a client_secret")
	}
	if gotForm.Get("code_verifier") != state.CodeVerifier {
		t.Error("code_verifier must match the PKCE state")
	}
	if gotForm.Get("redirect_uri") != "" {
		t.Error("no-redirect flow must not send redirect_uri at exchange time")
	}
}

func TestRefreshAccessToken_Success(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at2", "expires_in": 14400})
	})
	res, err := client.RefreshAccessToken(context.Background(), "app-key", "rt1")
	if err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	if res.AccessToken != "at2" {
		t.Errorf("AccessToken = %q, want at2", res.AccessToken)
	}
}

func TestRefreshAccessToken_InvalidGrant(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "refresh token revoked"})
	})
	_, err := client.RefreshAccessToken(context.Background(), "app-key", "rt-revoked")
	if err == nil {
		t.Fatal("expected error")
	}
	var statusErr *HTTPStatusError
	if !extractHTTPStatusError(err, &statusErr) {
		t.Fatalf("error is not *HTTPStatusError: %v", err)
	}
	if statusErr.ErrorTag != "invalid_grant" {
		t.Errorf("ErrorTag = %q, want invalid_grant", statusErr.ErrorTag)
	}
}

func TestFetchSpaceUsage_Individual(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer at1" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"used": 12345,
			"allocation": map[string]any{
				".tag": "individual", "allocated": uint64(2_000_000_000_000),
			},
		})
	})
	usage, err := client.FetchSpaceUsage(context.Background(), "at1")
	if err != nil {
		t.Fatalf("FetchSpaceUsage: %v", err)
	}
	if usage.UsedBytes != 12345 || usage.AllocatedBytes != 2_000_000_000_000 || usage.AllocationTag != "individual" {
		t.Errorf("unexpected usage: %+v", usage)
	}
}

func TestFetchSpaceUsage_Team(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"used": 999,
			"allocation": map[string]any{
				".tag": "team", "allocated": uint64(5_000_000_000_000), "used": 999,
			},
		})
	})
	usage, err := client.FetchSpaceUsage(context.Background(), "at1")
	if err != nil {
		t.Fatalf("FetchSpaceUsage: %v", err)
	}
	if usage.AllocationTag != "team" {
		t.Errorf("AllocationTag = %q, want team", usage.AllocationTag)
	}
}

func TestFetchCurrentAccount(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4)
		_, _ = r.Body.Read(body)
		if string(body) != "null" {
			t.Errorf("body should be literal null, got %q", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"email": "user@example.com",
			"name":  map[string]string{"display_name": "Test User"},
		})
	})
	acct, err := client.FetchCurrentAccount(context.Background(), "at1")
	if err != nil {
		t.Fatalf("FetchCurrentAccount: %v", err)
	}
	if acct.Email != "user@example.com" || acct.DisplayName != "Test User" {
		t.Errorf("unexpected account: %+v", acct)
	}
}

func TestFetchSpaceUsage_RateLimited(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error_summary": "too_many_requests/..."}`))
	})
	_, err := client.FetchSpaceUsage(context.Background(), "at1")
	if err == nil {
		t.Fatal("expected error")
	}
	var statusErr *HTTPStatusError
	if !extractHTTPStatusError(err, &statusErr) {
		t.Fatalf("error is not *HTTPStatusError: %v", err)
	}
	if statusErr.Status != http.StatusTooManyRequests || statusErr.RetryAfter != "30" {
		t.Errorf("unexpected status error: %+v", statusErr)
	}
}

func TestFetchSpaceUsage_Unauthorized(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error_summary": "invalid_access_token/..."}`))
	})
	_, err := client.FetchSpaceUsage(context.Background(), "bad-token")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want to mention 401", err)
	}
}

func TestRevokeToken_Success(t *testing.T) {
	t.Parallel()
	var gotAuth string
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})
	if err := client.RevokeToken(context.Background(), "at1"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if gotAuth != "Bearer at1" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

func extractHTTPStatusError(err error, target **HTTPStatusError) bool {
	e, ok := err.(*HTTPStatusError)
	if !ok {
		return false
	}
	*target = e
	return true
}
