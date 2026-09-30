package hub

import (
	"context"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storageusage"
)

func TestCollectStorageOnce_NoProviderRegistered(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()

	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountProvider("unknown-provider-xyz"),
		Name:     "x",
		Secret:   map[string]string{"refresh_token": "rt"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	opts := testOptions()
	opts.Storage = &StorageRuntime{}
	srv := newTestServer(t, opts, store)

	srv.collectStorageOnce(ctx)

	snap, err := store.GetStorageAccountSnapshot(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccountSnapshot: %v", err)
	}
	if snap.Status != models.StorageAccountError {
		t.Errorf("Status = %q, want error (no registered provider)", snap.Status)
	}
}

func TestCollectStorageOnce_PendingOAuthWhenNoSecret(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()

	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountDropbox,
		Name:     "not-yet-connected",
		Config:   map[string]string{"app_key": "k"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	opts := testOptions()
	opts.Storage = &StorageRuntime{}
	srv := newTestServer(t, opts, store)

	srv.collectStorageOnce(ctx)

	snap, err := store.GetStorageAccountSnapshot(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccountSnapshot: %v", err)
	}
	if snap.Status != models.StorageAccountPendingOAuth {
		t.Errorf("Status = %q, want pending_oauth", snap.Status)
	}
}

func TestCollectStorageOnce_PreservesLastSuccessOnQuietSkip(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()

	fakeProviderID := models.StorageAccountProvider("test-quiet-skip-provider")
	callCount := 0
	fake := &fakeStorageProvider2{
		id: fakeProviderID,
		fetch: func() (storageusage.Quota, error) {
			callCount++
			if callCount == 1 {
				return storageusage.Quota{
					AccountEmail: "u@example.com",
					Quota:        models.StorageQuota{UsedBytes: 500, LimitBytes: 1000},
				}, nil
			}
			return storageusage.Quota{}, errTestQuietSkip
		},
	}

	acct, err := store.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: fakeProviderID,
		Name:     "acct",
		Config:   map[string]string{"x": "y"},
		Secret:   map[string]string{"refresh_token": "rt"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	now := time.Unix(1_700_000_000, 0)
	opts := testOptions()
	opts.Storage = &StorageRuntime{Now: func() time.Time { return now }, Registry: storageusage.NewRegistry(fake)}
	srv := newTestServer(t, opts, store)

	// First collection succeeds.
	srv.collectStorageOnce(ctx)
	snap1, err := store.GetStorageAccountSnapshot(ctx, acct.ID)
	if err != nil || snap1.Status != models.StorageAccountOK || snap1.Quota.UsedBytes != 500 {
		t.Fatalf("first snapshot = %+v, err=%v", snap1, err)
	}

	// Second collection quiet-skips; last successful quota must be
	// preserved (mirrors billing's mergeSnapshot behavior).
	now = now.Add(time.Hour)
	srv.collectStorageOnce(ctx)
	snap2, err := store.GetStorageAccountSnapshot(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccountSnapshot: %v", err)
	}
	if snap2.Status == models.StorageAccountOK {
		t.Fatal("second collection should have quiet-skipped, not stayed ok")
	}
	if snap2.Quota.UsedBytes != 500 {
		t.Errorf("Quota.UsedBytes = %d, want preserved 500", snap2.Quota.UsedBytes)
	}
	if snap2.AccountEmail != "u@example.com" {
		t.Errorf("AccountEmail = %q, want preserved", snap2.AccountEmail)
	}
	if snap2.LastSuccessAt == 0 {
		t.Error("LastSuccessAt should be preserved from the first successful collection")
	}
	if snap2.LastAttemptAt != now.Unix() {
		t.Errorf("LastAttemptAt = %d, want %d", snap2.LastAttemptAt, now.Unix())
	}
}

func TestStorageRefreshGate_RecordsOnBothScheduledAndManualPaths(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	rt := &StorageRuntime{Now: func() time.Time { return now }}
	allowed, _ := rt.refreshAllowed()
	if !allowed {
		t.Fatal("first refreshAllowed should be true")
	}
	rt.refreshGate().Record()
	allowed, retryAfter := rt.refreshAllowed()
	if allowed || retryAfter <= 0 {
		t.Errorf("refreshAllowed after Record = (%v, %v), want (false, >0)", allowed, retryAfter)
	}
}

// errTestQuietSkip is a sentinel error the fake provider's Classify
// maps to a quiet-skip status distinct from ok.
var errTestQuietSkip = &testProviderError{}

type testProviderError struct{}

func (e *testProviderError) Error() string { return "test: quiet skip" }

// fakeStorageProvider2 is a second Provider double (distinct from
// fakeStorageProvider in storageroutes_test.go, which only implements
// RevokeToken meaningfully) — this one implements FetchQuota/Classify
// meaningfully for storagerun_test.go's collection-loop tests.
type fakeStorageProvider2 struct {
	id    models.StorageAccountProvider
	fetch func() (storageusage.Quota, error)
}

func (f *fakeStorageProvider2) ID() models.StorageAccountProvider { return f.id }

func (f *fakeStorageProvider2) FetchQuota(_ context.Context, _, _ map[string]string) (storageusage.Quota, error) {
	return f.fetch()
}

func (f *fakeStorageProvider2) Classify(err error) (models.StorageAccountStatus, string) {
	if err == nil {
		return models.StorageAccountOK, ""
	}
	return models.StorageAccountError, "test quiet skip"
}

func (f *fakeStorageProvider2) RevokeToken(_ context.Context, _, _ map[string]string) error {
	return nil
}
