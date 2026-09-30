package googledrive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestServer starts an httptest server and returns a Client whose
// ssrfChecker redirects every request to that server (preserving the
// original request's path so RequestDeviceCode/Poll/etc. can still be
// distinguished by path if a test handler wants to).
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
	client := New(srv.Client(), redirect)
	return srv, client
}

func TestRequestDeviceCode_Success(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device/code" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") != "client123" {
			t.Errorf("client_id = %q", r.Form.Get("client_id"))
		}
		if r.Form.Get("scope") != driveFileScope {
			t.Errorf("scope = %q, want %q", r.Form.Get("scope"), driveFileScope)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode: "dc1", UserCode: "ABCD-EFGH",
			VerificationURL: "https://www.google.com/device", ExpiresIn: 1800, Interval: 5,
		})
	})

	resp, err := client.RequestDeviceCode(context.Background(), "client123")
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if resp.DeviceCode != "dc1" || resp.UserCode != "ABCD-EFGH" || resp.Interval != 5 {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestPoll_Granted(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at1", "refresh_token": "rt1", "expires_in": 3600,
		})
	})
	res := client.Poll(context.Background(), "cid", "csecret", "dc1")
	if res.Outcome != PollGranted || res.AccessToken != "at1" || res.RefreshToken != "rt1" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestPoll_AuthorizationPending(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionRequired)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
	})
	res := client.Poll(context.Background(), "cid", "csecret", "dc1")
	if res.Outcome != PollAuthorizationPending {
		t.Errorf("Outcome = %v, want PollAuthorizationPending", res.Outcome)
	}
}

func TestPoll_SlowDown(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "slow_down"})
	})
	res := client.Poll(context.Background(), "cid", "csecret", "dc1")
	if res.Outcome != PollSlowDown {
		t.Errorf("Outcome = %v, want PollSlowDown", res.Outcome)
	}
}

func TestPoll_AccessDenied(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
	})
	res := client.Poll(context.Background(), "cid", "csecret", "dc1")
	if res.Outcome != PollAccessDenied {
		t.Errorf("Outcome = %v, want PollAccessDenied", res.Outcome)
	}
}

func TestPoll_ExpiredToken(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "expired_token"})
	})
	res := client.Poll(context.Background(), "cid", "csecret", "dc1")
	if res.Outcome != PollExpired {
		t.Errorf("Outcome = %v, want PollExpired", res.Outcome)
	}
}

func TestPoll_InvalidGrantTreatedAsExpired(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	})
	res := client.Poll(context.Background(), "cid", "csecret", "dc1")
	if res.Outcome != PollExpired {
		t.Errorf("Outcome = %v, want PollExpired", res.Outcome)
	}
}

func TestPoll_UnknownErrorIsClassifiedAsError(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "admin_policy_enforced"})
	})
	res := client.Poll(context.Background(), "cid", "csecret", "dc1")
	if res.Outcome != PollError || res.ErrorDetail != "admin_policy_enforced" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestRefreshAccessToken_Success(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at2"})
	})
	token, err := client.RefreshAccessToken(context.Background(), "cid", "csecret", "rt1")
	if err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	if token != "at2" {
		t.Errorf("token = %q, want at2", token)
	}
}

func TestRefreshAccessToken_InvalidGrant(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	})
	_, err := client.RefreshAccessToken(context.Background(), "cid", "csecret", "rt1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error = %v, want to contain invalid_grant", err)
	}
}

func TestRevokeToken_Success(t *testing.T) {
	t.Parallel()
	var gotToken string
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotToken = r.Form.Get("token")
		w.WriteHeader(http.StatusOK)
	})
	if err := client.RevokeToken(context.Background(), "at1"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if gotToken != "at1" {
		t.Errorf("gotToken = %q, want at1", gotToken)
	}
}

func TestFetchAbout_LimitedQuota(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer at1" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"storageQuota": map[string]string{
				"limit": "1000000000", "usage": "500000000",
				"usageInDrive": "480000000", "usageInDriveTrash": "20000000",
			},
			"user": map[string]string{"emailAddress": "user@example.com", "displayName": "Test User"},
		})
	})
	about, err := client.FetchAbout(context.Background(), "at1")
	if err != nil {
		t.Fatalf("FetchAbout: %v", err)
	}
	if about.Unlimited {
		t.Error("Unlimited should be false when limit is present")
	}
	if about.LimitBytes != 1_000_000_000 || about.UsageBytes != 500_000_000 {
		t.Errorf("unexpected quota: %+v", about)
	}
	if about.AccountEmail != "user@example.com" {
		t.Errorf("AccountEmail = %q", about.AccountEmail)
	}
}

func TestFetchAbout_UnlimitedQuota(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"storageQuota": map[string]string{"usage": "500"},
			"user":         map[string]string{"emailAddress": "u@example.com"},
		})
	})
	about, err := client.FetchAbout(context.Background(), "at1")
	if err != nil {
		t.Fatalf("FetchAbout: %v", err)
	}
	if !about.Unlimited {
		t.Error("Unlimited should be true when limit is absent")
	}
	if about.LimitBytes != 0 {
		t.Errorf("LimitBytes = %d, want 0", about.LimitBytes)
	}
}

func TestFetchAbout_HTTPErrorStatus(t *testing.T) {
	t.Parallel()
	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid Credentials"}}`))
	})
	_, err := client.FetchAbout(context.Background(), "expired-token")
	if err == nil {
		t.Fatal("expected error")
	}
	var statusErr *HTTPStatusError
	if !asHTTPStatusError(err, &statusErr) {
		t.Fatalf("error is not *HTTPStatusError: %v", err)
	}
	if statusErr.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want 401", statusErr.Status)
	}
}

func asHTTPStatusError(err error, target **HTTPStatusError) bool {
	e, ok := err.(*HTTPStatusError)
	if !ok {
		return false
	}
	*target = e
	return true
}

func TestSSRFGuard_RejectsNonOfficialHost(t *testing.T) {
	t.Parallel()
	client := New(http.DefaultClient, func(rawURL string) (*url.URL, error) {
		return nil, &HTTPStatusError{Status: 0} // simulate a rejecting checker
	})
	_, err := client.RequestDeviceCode(context.Background(), "cid")
	if err == nil {
		t.Fatal("expected ssrf-rejection error")
	}
}
