package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage/dropbox"
)

func TestFormatBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		bytes uint64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{1 << 20, "1.00 MiB"},
		{1 << 30, "1.00 GiB"},
		{1 << 40, "1.00 TiB"},
	}
	for _, tt := range tests {
		if got := formatBytes(tt.bytes); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestSelectStorageAccount_NoAccountsConfigured(t *testing.T) {
	t.Parallel()
	_, err := selectStorageAccount(nil, models.StorageAccountGoogleDrive, "")
	if err == nil {
		t.Fatal("expected an error when no accounts are configured")
	}
}

func TestSelectStorageAccount_SingleMatchUnconnectedRejected(t *testing.T) {
	t.Parallel()
	accounts := []models.StorageAccount{
		{ID: 1, Provider: models.StorageAccountDropbox, Name: "dbx", Secret: nil},
	}
	_, err := selectStorageAccount(accounts, models.StorageAccountDropbox, "")
	if err == nil {
		t.Fatal("expected an error for an account that never completed OAuth")
	}
}

func TestSelectStorageAccount_SingleMatchConnected(t *testing.T) {
	t.Parallel()
	accounts := []models.StorageAccount{
		{ID: 1, Provider: models.StorageAccountDropbox, Name: "dbx", Secret: map[string]string{"refresh_token": "rt"}},
	}
	got, err := selectStorageAccount(accounts, models.StorageAccountDropbox, "")
	if err != nil {
		t.Fatalf("selectStorageAccount: %v", err)
	}
	if got.ID != 1 {
		t.Errorf("got.ID = %d, want 1", got.ID)
	}
}

func TestSelectStorageAccount_AmbiguousWithoutNameRejected(t *testing.T) {
	t.Parallel()
	accounts := []models.StorageAccount{
		{ID: 1, Provider: models.StorageAccountDropbox, Name: "personal", Secret: map[string]string{"refresh_token": "rt"}},
		{ID: 2, Provider: models.StorageAccountDropbox, Name: "work", Secret: map[string]string{"refresh_token": "rt2"}},
	}
	_, err := selectStorageAccount(accounts, models.StorageAccountDropbox, "")
	if err == nil {
		t.Fatal("expected ambiguity error with two accounts of the same provider and no --name")
	}
}

func TestSelectStorageAccount_DisambiguatedByName(t *testing.T) {
	t.Parallel()
	accounts := []models.StorageAccount{
		{ID: 1, Provider: models.StorageAccountDropbox, Name: "personal", Secret: map[string]string{"refresh_token": "rt"}},
		{ID: 2, Provider: models.StorageAccountDropbox, Name: "work", Secret: map[string]string{"refresh_token": "rt2"}},
	}
	got, err := selectStorageAccount(accounts, models.StorageAccountDropbox, "work")
	if err != nil {
		t.Fatalf("selectStorageAccount: %v", err)
	}
	if got.ID != 2 {
		t.Errorf("got.ID = %d, want 2", got.ID)
	}
}

func TestSelectStorageAccount_UnknownNameRejected(t *testing.T) {
	t.Parallel()
	accounts := []models.StorageAccount{
		{ID: 1, Provider: models.StorageAccountDropbox, Name: "personal", Secret: map[string]string{"refresh_token": "rt"}},
	}
	_, err := selectStorageAccount(accounts, models.StorageAccountDropbox, "nonexistent")
	if err == nil {
		t.Fatal("expected an error for an unknown --name")
	}
}

func TestSelectStorageAccount_WrongProviderExcluded(t *testing.T) {
	t.Parallel()
	accounts := []models.StorageAccount{
		{ID: 1, Provider: models.StorageAccountGoogleDrive, Name: "gdrive", Secret: map[string]string{"refresh_token": "rt"}},
	}
	_, err := selectStorageAccount(accounts, models.StorageAccountDropbox, "")
	if err == nil {
		t.Fatal("expected an error when only a different provider's account exists")
	}
}

// TestVerifyStorageAccount_EndToEnd exercises the whole verify path
// against a real (test) SQLite database and a fake Dropbox HTTP
// server, confirming the CLI reuses the exact same
// internal/storageusage.Provider registered for production use rather
// than a separate code path.
func TestVerifyStorageAccount_EndToEnd(t *testing.T) {
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at1", "expires_in": 14400})
		case "/2/users/get_space_usage":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"used": 500, "allocation": map[string]any{".tag": "individual", "allocated": 1000},
			})
		case "/2/users/get_current_account":
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "u@example.com", "name": map[string]string{"display_name": "U"}})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer fakeSrv.Close()

	registry := storageusage.NewRegistry(newFakeDropboxProviderForTest(fakeSrv))

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cloud-pulse.db")
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if _, err := db.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountDropbox,
		Name:     "dbx",
		Config:   map[string]string{"app_key": "appkey"},
		Secret:   map[string]string{"refresh_token": "rt1"},
	}); err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	result, err := verifyStorageAccountWithTestRegistry(dbPath, models.StorageAccountDropbox, "", registry)
	if err != nil {
		t.Fatalf("verifyStorageAccountWithoutReregistering: %v", err)
	}
	if result.UsedBytes != 500 || result.LimitBytes != 1000 {
		t.Errorf("unexpected result: %+v", result)
	}
	if result.AccountEmail != "u@example.com" {
		t.Errorf("AccountEmail = %q", result.AccountEmail)
	}
}

// verifyStorageAccountWithTestRegistry opens the fixture database and calls
// the same registry-aware verification helper as production with a local fake
// registry. No provider state escapes this test.
func verifyStorageAccountWithTestRegistry(dbPath string, provider models.StorageAccountProvider, name string, registry *storageusage.Registry) (*storageVerifyResult, error) {
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	return verifyStorageAccountWithRegistry(ctx, db, provider, name, registry)
}

// fakeDropboxProviderForTest wraps a real dropbox.Client pointed at a
// test server via a redirecting ssrfChecker, satisfying
// storageusage.Provider so it can be registered under the "dropbox" ID
// for TestVerifyStorageAccount_EndToEnd — the same
// FetchQuota/token-refresh logic storageusage.DropboxProvider itself
// runs, just constructed from this test file since
// storageusage.DropboxProvider's client field is unexported.
type fakeDropboxProviderForTest struct {
	client *dropbox.Client
}

func newFakeDropboxProviderForTest(srv *httptest.Server) *fakeDropboxProviderForTest {
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
	return &fakeDropboxProviderForTest{client: dropbox.New(srv.Client(), redirect)}
}

func (f *fakeDropboxProviderForTest) ID() models.StorageAccountProvider {
	return models.StorageAccountDropbox
}

func (f *fakeDropboxProviderForTest) FetchQuota(ctx context.Context, cfg, secret map[string]string) (storageusage.Quota, error) {
	tok, err := f.client.RefreshAccessToken(ctx, cfg["app_key"], secret["refresh_token"])
	if err != nil {
		return storageusage.Quota{}, err
	}
	usage, err := f.client.FetchSpaceUsage(ctx, tok.AccessToken)
	if err != nil {
		return storageusage.Quota{}, err
	}
	account, err := f.client.FetchCurrentAccount(ctx, tok.AccessToken)
	if err != nil {
		return storageusage.Quota{}, err
	}
	return storageusage.Quota{
		AccountEmail: account.Email,
		AccountName:  account.DisplayName,
		Quota: models.StorageQuota{
			UsedBytes:      usage.UsedBytes,
			LimitBytes:     usage.AllocatedBytes,
			AllocationType: usage.AllocationTag,
		},
	}, nil
}

func (f *fakeDropboxProviderForTest) Classify(error) (models.StorageAccountStatus, string) {
	return models.StorageAccountError, "test"
}

func (f *fakeDropboxProviderForTest) RevokeToken(context.Context, map[string]string, map[string]string) error {
	return nil
}
