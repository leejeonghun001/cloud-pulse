package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage"
)

func TestStorageRoutes_RequireAuth(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	tests := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/storage/accounts"},
		{"POST", "/api/v1/storage/accounts"},
		{"DELETE", "/api/v1/storage/accounts/1"},
		{"POST", "/api/v1/storage/accounts/1/oauth/start"},
		{"POST", "/api/v1/storage/accounts/1/oauth/complete"},
		{"POST", "/api/v1/storage/refresh"},
		{"PUT", "/api/v1/settings/storage/interval"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := doRequest(t, srv.Handler(), tt.method, tt.path, "127.0.0.1:1", "", nil)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("unauthenticated %s %s = %d, want 401", tt.method, tt.path, rec.Code)
			}
		})
	}
}

func TestHandleListStorageAccounts_RedactsSecretsAndAttachesSnapshot(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()

	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountGoogleDrive,
		Name:     "user@example.com",
		Config:   map[string]string{"client_id": "abc"},
		Secret:   map[string]string{"client_secret": "shh", "refresh_token": "shh2"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}
	if err := store.SetStorageAccountSnapshot(ctx, models.StorageAccountSnapshot{
		AccountID: acct.ID,
		Status:    models.StorageAccountOK,
		Quota:     models.StorageQuota{UsedBytes: 100, LimitBytes: 1000},
	}); err != nil {
		t.Fatalf("SetStorageAccountSnapshot: %v", err)
	}

	// A second account with no snapshot yet.
	if _, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountDropbox,
		Name:     "no-snapshot-yet",
	}); err != nil {
		t.Fatalf("CreateStorageAccount (second): %v", err)
	}

	srv := newTestServer(t, testOptions(), store)
	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/storage/accounts", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	resp := decodeJSON[struct {
		Accounts []models.StorageAccountView `json:"accounts"`
	}](t, bytes.NewBuffer(rec.Body.Bytes()))

	if len(resp.Accounts) != 2 {
		t.Fatalf("accounts len = %d, want 2", len(resp.Accounts))
	}

	var withSnapshot, withoutSnapshot *models.StorageAccountView
	for i := range resp.Accounts {
		if resp.Accounts[i].Account.ID == acct.ID {
			withSnapshot = &resp.Accounts[i]
		} else {
			withoutSnapshot = &resp.Accounts[i]
		}
	}
	if withSnapshot == nil || withoutSnapshot == nil {
		t.Fatalf("expected both accounts present: %+v", resp.Accounts)
	}

	if withSnapshot.Account.Secret != nil {
		t.Errorf("Account.Secret must be redacted (nil), got %#v", withSnapshot.Account.Secret)
	}
	if withSnapshot.Account.Config["client_id"] != "abc" {
		t.Errorf("Account.Config must not be redacted, got %#v", withSnapshot.Account.Config)
	}
	if withSnapshot.Snapshot == nil || withSnapshot.Snapshot.Quota.UsedBytes != 100 {
		t.Errorf("Snapshot not attached correctly: %+v", withSnapshot.Snapshot)
	}
	if withoutSnapshot.Snapshot != nil {
		t.Errorf("expected no Snapshot for account with none collected yet, got %+v", withoutSnapshot.Snapshot)
	}
}

func TestHandleListStorageAccounts_EmptyListNotNull(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())
	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/storage/accounts", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := decodeJSON[struct {
		Accounts []models.StorageAccountView `json:"accounts"`
	}](t, bytes.NewBuffer(rec.Body.Bytes()))
	if resp.Accounts == nil {
		t.Error("accounts must encode as [] not null when empty")
	}
	if len(resp.Accounts) != 0 {
		t.Errorf("accounts len = %d, want 0", len(resp.Accounts))
	}
}

// --- Account create/delete ---

func TestHandleCreateStorageAccount_Google_Validates(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	body, _ := json.Marshal(models.StorageAccountCreate{
		Provider: models.StorageAccountGoogleDrive,
		Name:     "me@example.com",
		// Missing config.client_id and secret.client_secret.
	})
	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts", "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[models.APIError](t, bytes.NewBuffer(rec.Body.Bytes()))
	if resp.Details["config.client_id"] == "" || resp.Details["secret.client_secret"] == "" {
		t.Errorf("expected field errors for missing client_id/client_secret, got %+v", resp.Details)
	}
}

func TestHandleCreateStorageAccount_Dropbox_Success(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	body, _ := json.Marshal(models.StorageAccountCreate{
		Provider: models.StorageAccountDropbox,
		Name:     "My Dropbox",
		Config:   map[string]string{"app_key": "appkey123"},
	})
	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts", "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	acct := decodeJSON[models.StorageAccount](t, bytes.NewBuffer(rec.Body.Bytes()))
	if acct.ID == 0 {
		t.Error("expected a nonzero account ID")
	}
	if acct.Secret != nil {
		t.Error("response must redact Secret")
	}

	stored, err := store.GetStorageAccount(t.Context(), acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccount: %v", err)
	}
	if stored.Config["app_key"] != "appkey123" {
		t.Errorf("stored config = %+v", stored.Config)
	}
}

func TestHandleDeleteStorageAccount_RevokesAndDeletes(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()

	var revokeCalled bool
	fakeProvider := &fakeStorageProvider{
		id: models.StorageAccountDropbox,
		revoke: func(cfg, secret map[string]string) error {
			revokeCalled = true
			return nil
		},
	}

	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountDropbox,
		Name:     "to-delete",
		Config:   map[string]string{"app_key": "k"},
		Secret:   map[string]string{"refresh_token": "rt"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	opts := testOptions()
	opts.Storage = &StorageRuntime{Registry: storageusage.NewRegistry(fakeProvider)}
	srv := newTestServer(t, opts, store)

	rec := doRequest(t, srv.Handler(), "DELETE", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10), "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if !revokeCalled {
		t.Error("expected RevokeToken to be called")
	}
	if _, err := store.GetStorageAccount(ctx, acct.ID); err != models.ErrNotFound {
		t.Errorf("expected account to be deleted, got err=%v", err)
	}
}

func TestHandleDeleteStorageAccount_NotFound(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())
	rec := doRequest(t, srv.Handler(), "DELETE", "/api/v1/storage/accounts/999", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// --- OAuth flows: Dropbox PKCE ---

func TestDropboxOAuthFlow_StartThenComplete(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()

	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountDropbox,
		Name:     "dbx",
		Config:   map[string]string{"app_key": "appkey"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at1", "refresh_token": "rt-granted", "expires_in": 14400,
			})
		case "/2/users/get_space_usage":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"used": 250000000,
				"allocation": map[string]any{
					".tag":      "individual",
					"allocated": 2000000000,
				},
			})
		case "/2/users/get_current_account":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":  map[string]any{"display_name": "Dropbox Test User"},
				"email": "dbxuser@example.test",
			})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeSrv.Close()

	// AllowCustomEndpoints (CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS, test/smoke
	// only) lets dropboxSSRFChecker accept fakeSrv's 127.0.0.1 host
	// instead of only the production oauth.dropbox.com/
	// api.dropboxapi.com hosts, so this test can drive the real HTTP
	// round trip end to end rather than only the flow-state plumbing —
	// mirrors scripts/smoke.sh's storage section, which needs the same
	// escape hatch against its own fake Dropbox server.
	opts := testOptions()
	opts.Storage = &StorageRuntime{HTTPClient: fakeSrv.Client(), AllowCustomEndpoints: true, FakeBaseURL: fakeSrv.URL}
	srv := newTestServer(t, opts, store)

	// The background poller uses this runtime's own registry, separate
	// from the OAuth handler's inline client, so give it the same fake
	// endpoint redirect.
	opts.Storage.Registry = storageusage.NewRegistry(storageusage.NewDropboxProvider(fakeSrv.Client(), storageusage.SSRFOptions{AllowInsecure: true, RedirectBase: fakeSrv.URL}))

	// oauth/start returns a PKCE authorize URL with no redirect_uri.
	startRec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10)+"/oauth/start",
		"127.0.0.1:1", testUIToken, nil)
	if startRec.Code != http.StatusOK {
		t.Fatalf("oauth/start status = %d, body=%s", startRec.Code, startRec.Body.String())
	}
	startResp := decodeJSON[models.StorageOAuthStartResponse](t, bytes.NewBuffer(startRec.Body.Bytes()))
	if startResp.Flow != "pkce" || startResp.AuthorizeURL == "" {
		t.Fatalf("unexpected start response: %+v", startResp)
	}
	if strings.Contains(startResp.AuthorizeURL, "redirect_uri") {
		t.Error("dropbox authorize URL must not include redirect_uri")
	}

	completeBody, _ := json.Marshal(models.StorageOAuthCompleteRequest{Code: "user-pasted-code"})
	completeRec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10)+"/oauth/complete",
		"127.0.0.1:1", testUIToken, completeBody)
	if completeRec.Code != http.StatusOK {
		t.Fatalf("oauth/complete status = %d, want 200, body=%s", completeRec.Code, completeRec.Body.String())
	}

	updated, err := store.GetStorageAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccount after complete: %v", err)
	}
	if updated.Secret["refresh_token"] != "rt-granted" {
		t.Errorf("refresh_token = %q, want %q", updated.Secret["refresh_token"], "rt-granted")
	}

	// completeDropboxPKCEFlow triggers an immediate quota collection
	// synchronously (unlike Google's async device-flow poller), so the
	// snapshot should already reflect the fake get_space_usage/
	// get_current_account responses by the time oauth/complete returns.
	snap, err := store.GetStorageAccountSnapshot(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccountSnapshot after complete: %v", err)
	}
	if snap.Status != models.StorageAccountOK {
		t.Fatalf("snapshot status = %q, want ok", snap.Status)
	}
	if snap.AccountEmail != "dbxuser@example.test" {
		t.Errorf("AccountEmail = %q, want %q", snap.AccountEmail, "dbxuser@example.test")
	}
	if snap.Quota.UsedBytes != 250000000 || snap.Quota.LimitBytes != 2000000000 {
		t.Errorf("unexpected quota: %+v", snap.Quota)
	}

	// The in-progress flow state must be cleared on successful
	// completion (a second complete call with no prior start should now
	// report "no flow in progress", not silently reuse the old verifier).
	secondRec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10)+"/oauth/complete",
		"127.0.0.1:1", testUIToken, completeBody)
	if secondRec.Code != http.StatusConflict {
		t.Errorf("second oauth/complete status = %d, want 409 (flow already cleared)", secondRec.Code)
	}
}

func TestHandleStorageOAuthComplete_Dropbox_MissingCodeRejected(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()
	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountDropbox,
		Name:     "dbx",
		Config:   map[string]string{"app_key": "appkey"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	opts := testOptions()
	opts.Storage = &StorageRuntime{}
	srv := newTestServer(t, opts, store)

	body, _ := json.Marshal(models.StorageOAuthCompleteRequest{Code: ""})
	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10)+"/oauth/complete",
		"127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleStorageOAuthComplete_Dropbox_NoFlowInProgress(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()
	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountDropbox,
		Name:     "dbx",
		Config:   map[string]string{"app_key": "appkey"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	opts := testOptions()
	opts.Storage = &StorageRuntime{}
	srv := newTestServer(t, opts, store)

	body, _ := json.Marshal(models.StorageOAuthCompleteRequest{Code: "some-code"})
	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10)+"/oauth/complete",
		"127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (no in-progress flow), body=%s", rec.Code, rec.Body.String())
	}
}

// --- OAuth flows: Google device flow ---

func TestGoogleOAuthStart_MissingConfigRejected(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()
	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountGoogleDrive,
		Name:     "gdrive",
		// Missing client_id/client_secret.
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	opts := testOptions()
	opts.Storage = &StorageRuntime{}
	srv := newTestServer(t, opts, store)

	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10)+"/oauth/start",
		"127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestGoogleOAuthComplete_ReportsPendingWhenNoFlowStarted(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()
	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountGoogleDrive,
		Name:     "gdrive",
		Config:   map[string]string{"client_id": "cid"},
		Secret:   map[string]string{"client_secret": "csecret"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	opts := testOptions()
	opts.Storage = &StorageRuntime{}
	srv := newTestServer(t, opts, store)

	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10)+"/oauth/complete",
		"127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[models.StorageOAuthCompleteResponse](t, bytes.NewBuffer(rec.Body.Bytes()))
	if resp.Status != "authorization_pending" {
		t.Errorf("Status = %q, want authorization_pending", resp.Status)
	}
}

// TestGoogleOAuthFlow_StartThenPollThenComplete drives the full Google
// device-flow round trip through the real HTTP client (RequestDeviceCode
// against a fake .../device/code, the background poller's Poll against
// a fake .../token, then oauth/complete reporting the poller's result)
// via AllowCustomEndpoints+FakeBaseURL — the same escape hatch
// scripts/smoke.sh's storage section uses, since without it these
// requests would target the real oauth2.googleapis.com host.
func TestGoogleOAuthFlow_StartThenPollThenComplete(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()
	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountGoogleDrive,
		Name:     "gdrive",
		Config:   map[string]string{"client_id": "cid"},
		Secret:   map[string]string{"client_secret": "csecret"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device/code":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "dc1", "user_code": "ABCD-EFGH",
				"verification_url": "https://www.google.com/device", "expires_in": 1800, "interval": 1,
			})
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at1", "refresh_token": "rt-granted", "expires_in": 3600,
			})
		case "/drive/v3/about":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"storageQuota": map[string]any{"limit": "1000000000", "usage": "500000000"},
				"user":         map[string]any{"emailAddress": "user@example.test", "displayName": "Test User"},
			})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fakeSrv.Close()

	opts := testOptions()
	opts.Storage = &StorageRuntime{HTTPClient: fakeSrv.Client(), AllowCustomEndpoints: true, FakeBaseURL: fakeSrv.URL}
	srv := newTestServer(t, opts, store)

	// The background poller uses this runtime's own registry, separate
	// from the OAuth handler's inline client, so give it the same fake
	// endpoint redirect.
	opts.Storage.Registry = storageusage.NewRegistry(storageusage.NewGoogleDriveProvider(fakeSrv.Client(), storageusage.SSRFOptions{AllowInsecure: true, RedirectBase: fakeSrv.URL}))

	startRec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10)+"/oauth/start",
		"127.0.0.1:1", testUIToken, nil)
	if startRec.Code != http.StatusOK {
		t.Fatalf("oauth/start status = %d, body=%s", startRec.Code, startRec.Body.String())
	}
	startResp := decodeJSON[models.StorageOAuthStartResponse](t, bytes.NewBuffer(startRec.Body.Bytes()))
	if startResp.Flow != "device" || startResp.UserCode != "ABCD-EFGH" {
		t.Fatalf("unexpected start response: %+v", startResp)
	}

	// Wait for the background poller (1s interval from the fake
	// server's response above) to reach a terminal outcome, polling
	// oauth/complete rather than sleeping a fixed duration.
	deadline := time.Now().Add(10 * time.Second)
	var completeRec *httptest.ResponseRecorder
	var completeResp models.StorageOAuthCompleteResponse
	for time.Now().Before(deadline) {
		completeRec = doRequest(t, srv.Handler(), "POST", "/api/v1/storage/accounts/"+strconv.FormatInt(acct.ID, 10)+"/oauth/complete",
			"127.0.0.1:1", testUIToken, nil)
		if completeRec.Code != http.StatusOK {
			t.Fatalf("oauth/complete status = %d, body=%s", completeRec.Code, completeRec.Body.String())
		}
		completeResp = decodeJSON[models.StorageOAuthCompleteResponse](t, bytes.NewBuffer(completeRec.Body.Bytes()))
		if completeResp.Status != "authorization_pending" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if completeResp.Status != "connected" {
		t.Fatalf("final oauth/complete Status = %q, want connected (body=%s)", completeResp.Status, completeRec.Body.String())
	}

	updated, err := store.GetStorageAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccount after complete: %v", err)
	}
	if updated.Secret["refresh_token"] != "rt-granted" {
		t.Errorf("refresh_token = %q, want %q", updated.Secret["refresh_token"], "rt-granted")
	}

	// completeGoogleDeviceFlow triggers an immediate quota collection;
	// confirm the snapshot reflects the fake /drive/v3/about response.
	deadline = time.Now().Add(5 * time.Second)
	var snap models.StorageAccountSnapshot
	for time.Now().Before(deadline) {
		s, err := store.GetStorageAccountSnapshot(ctx, acct.ID)
		if err == nil && s.Status == models.StorageAccountOK {
			snap = s
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snap.Status != models.StorageAccountOK {
		t.Fatalf("snapshot status = %q, want ok", snap.Status)
	}
	if snap.AccountEmail != "user@example.test" {
		t.Errorf("AccountEmail = %q, want %q", snap.AccountEmail, "user@example.test")
	}
	if snap.Quota.UsedBytes != 500000000 || snap.Quota.LimitBytes != 1000000000 {
		t.Errorf("unexpected quota: %+v", snap.Quota)
	}
}

// --- Refresh + interval ---

func TestHandleStorageRefresh_NotEnabled(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())
	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/refresh", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 when Options.Storage is nil", rec.Code)
	}
}

func TestHandleStorageRefresh_RateLimited(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	opts := testOptions()
	opts.Storage = &StorageRuntime{Now: func() time.Time { return now }}
	srv := newTestServer(t, opts, newFakeStore())

	rec1 := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/refresh", "127.0.0.1:1", testUIToken, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d, body=%s", rec1.Code, rec1.Body.String())
	}
	rec2 := doRequest(t, srv.Handler(), "POST", "/api/v1/storage/refresh", "127.0.0.1:1", testUIToken, nil)
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("second immediate refresh status = %d, want 429", rec2.Code)
	}
}

func TestHandleSetStorageInterval_ValidatesAndPersists(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.Storage = &StorageRuntime{}
	srv := newTestServer(t, opts, store)

	body, _ := json.Marshal(storageIntervalRequest{Interval: models.StorageInterval6h})
	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/storage/interval", "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	raw, ok, err := store.GetSetting(t.Context(), SettingStorageInterval)
	if err != nil || !ok || raw != "6h" {
		t.Errorf("stored setting = %q, ok=%v, err=%v", raw, ok, err)
	}
}

func TestHandleSetStorageInterval_RejectsInvalid(t *testing.T) {
	t.Parallel()
	opts := testOptions()
	opts.Storage = &StorageRuntime{}
	srv := newTestServer(t, opts, newFakeStore())

	body, _ := json.Marshal(storageIntervalRequest{Interval: models.StorageInterval("2h")})
	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/storage/interval", "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// --- test helpers ---

// fakeStorageProvider is a minimal storageusage.Provider double for
// exercising handleDeleteStorageAccount's revoke-then-delete path
// without a real network call.
type fakeStorageProvider struct {
	id     models.StorageAccountProvider
	revoke func(cfg, secret map[string]string) error
}

func (f *fakeStorageProvider) ID() models.StorageAccountProvider { return f.id }

func (f *fakeStorageProvider) FetchQuota(_ context.Context, _, _ map[string]string) (storageusage.Quota, error) {
	return storageusage.Quota{}, nil
}

func (f *fakeStorageProvider) Classify(error) (models.StorageAccountStatus, string) {
	return models.StorageAccountOK, ""
}

func (f *fakeStorageProvider) RevokeToken(_ context.Context, cfg, secret map[string]string) error {
	if f.revoke != nil {
		return f.revoke(cfg, secret)
	}
	return nil
}
