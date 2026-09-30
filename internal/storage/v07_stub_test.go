package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func openTestDBForV07(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "v07.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

func TestStorageAccount_CreateGetUpdateDelete(t *testing.T) {
	t.Parallel()
	db := openTestDBForV07(t)
	ctx := context.Background()

	created, err := db.CreateStorageAccount(ctx, models.StorageAccount{
		Provider: models.StorageAccountGoogleDrive,
		Name:     "user@example.com",
		Config:   map[string]string{"client_id": "abc"},
		Secret:   map[string]string{"client_secret": "shh"},
	})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("CreateStorageAccount: ID not assigned")
	}
	if created.CreatedAt == 0 || created.UpdatedAt == 0 {
		t.Errorf("CreateStorageAccount: CreatedAt/UpdatedAt not populated: %+v", created)
	}

	got, err := db.GetStorageAccount(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetStorageAccount: %v", err)
	}
	if got.Name != "user@example.com" || got.Config["client_id"] != "abc" || got.Secret["client_secret"] != "shh" {
		t.Errorf("GetStorageAccount round-trip = %+v", got)
	}

	got.Name = "renamed@example.com"
	updated, err := db.UpdateStorageAccount(ctx, got)
	if err != nil {
		t.Fatalf("UpdateStorageAccount: %v", err)
	}
	if updated.Name != "renamed@example.com" {
		t.Errorf("UpdateStorageAccount name = %q, want renamed@example.com", updated.Name)
	}
	if updated.CreatedAt != created.CreatedAt {
		t.Errorf("UpdateStorageAccount must preserve CreatedAt: got %d, want %d", updated.CreatedAt, created.CreatedAt)
	}

	list, err := db.ListStorageAccounts(ctx)
	if err != nil {
		t.Fatalf("ListStorageAccounts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListStorageAccounts len = %d, want 1", len(list))
	}

	if err := db.DeleteStorageAccount(ctx, created.ID); err != nil {
		t.Fatalf("DeleteStorageAccount: %v", err)
	}
	if _, err := db.GetStorageAccount(ctx, created.ID); err != models.ErrNotFound {
		t.Errorf("GetStorageAccount after delete = %v, want models.ErrNotFound", err)
	}
}

func TestStorageAccount_UpdateNonExistentReturnsNotFound(t *testing.T) {
	t.Parallel()
	db := openTestDBForV07(t)
	ctx := context.Background()

	_, err := db.UpdateStorageAccount(ctx, models.StorageAccount{ID: 999, Name: "x"})
	if err != models.ErrNotFound {
		t.Errorf("UpdateStorageAccount (nonexistent) = %v, want models.ErrNotFound", err)
	}
}

func TestStorageAccount_DeleteCascadesSnapshot(t *testing.T) {
	t.Parallel()
	db := openTestDBForV07(t)
	ctx := context.Background()

	acct, err := db.CreateStorageAccount(ctx, models.StorageAccount{Provider: models.StorageAccountDropbox, Name: "acct"})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}
	if err := db.SetStorageAccountSnapshot(ctx, models.StorageAccountSnapshot{
		AccountID: acct.ID,
		Status:    models.StorageAccountOK,
		Quota:     models.StorageQuota{UsedBytes: 100, LimitBytes: 1000},
	}); err != nil {
		t.Fatalf("SetStorageAccountSnapshot: %v", err)
	}

	if err := db.DeleteStorageAccount(ctx, acct.ID); err != nil {
		t.Fatalf("DeleteStorageAccount: %v", err)
	}

	snap, err := db.GetStorageAccountSnapshot(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccountSnapshot after delete: %v", err)
	}
	if snap.Status != models.StorageAccountNotConfigured {
		t.Errorf("GetStorageAccountSnapshot after cascade delete = %+v, want Status=not_configured (no row)", snap)
	}
}

func TestStorageAccountSnapshot_GetDefaultsToNotConfigured(t *testing.T) {
	t.Parallel()
	db := openTestDBForV07(t)
	ctx := context.Background()

	snap, err := db.GetStorageAccountSnapshot(ctx, 42)
	if err != nil {
		t.Fatalf("GetStorageAccountSnapshot: %v", err)
	}
	if snap.AccountID != 42 || snap.Status != models.StorageAccountNotConfigured {
		t.Errorf("GetStorageAccountSnapshot (no row) = %+v, want AccountID=42 Status=not_configured", snap)
	}
}

func TestStorageAccountSnapshot_SetAndGetRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDBForV07(t)
	ctx := context.Background()

	acct, err := db.CreateStorageAccount(ctx, models.StorageAccount{Provider: models.StorageAccountGoogleDrive, Name: "acct"})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}

	snap := models.StorageAccountSnapshot{
		AccountID:    acct.ID,
		Status:       models.StorageAccountOK,
		AccountEmail: "user@example.com",
		AccountName:  "User",
		Quota: models.StorageQuota{
			UsedBytes:      100,
			LimitBytes:     1000,
			TrashBytes:     10,
			AllocationType: "individual",
		},
		CollectedAt:   1000,
		LastSuccessAt: 1000,
		LastAttemptAt: 1000,
	}
	if err := db.SetStorageAccountSnapshot(ctx, snap); err != nil {
		t.Fatalf("SetStorageAccountSnapshot: %v", err)
	}

	got, err := db.GetStorageAccountSnapshot(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccountSnapshot: %v", err)
	}
	if got != snap {
		t.Errorf("GetStorageAccountSnapshot round-trip = %+v, want %+v", got, snap)
	}

	// Upsert (second write for the same account must replace, not
	// duplicate).
	snap.Quota.UsedBytes = 200
	if err := db.SetStorageAccountSnapshot(ctx, snap); err != nil {
		t.Fatalf("SetStorageAccountSnapshot (upsert): %v", err)
	}
	list, err := db.ListStorageAccountSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListStorageAccountSnapshots: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListStorageAccountSnapshots len = %d, want 1 (upsert, not duplicate)", len(list))
	}
	if list[0].Quota.UsedBytes != 200 {
		t.Errorf("UsedBytes after upsert = %d, want 200", list[0].Quota.UsedBytes)
	}
}

func TestStorageAccount_UnlimitedQuotaRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDBForV07(t)
	ctx := context.Background()

	acct, err := db.CreateStorageAccount(ctx, models.StorageAccount{Provider: models.StorageAccountGoogleDrive, Name: "acct"})
	if err != nil {
		t.Fatalf("CreateStorageAccount: %v", err)
	}
	snap := models.StorageAccountSnapshot{
		AccountID: acct.ID,
		Status:    models.StorageAccountOK,
		Quota:     models.StorageQuota{UsedBytes: 500, Unlimited: true},
	}
	if err := db.SetStorageAccountSnapshot(ctx, snap); err != nil {
		t.Fatalf("SetStorageAccountSnapshot: %v", err)
	}
	got, err := db.GetStorageAccountSnapshot(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetStorageAccountSnapshot: %v", err)
	}
	if !got.Quota.Unlimited {
		t.Errorf("Quota.Unlimited round-trip failed: got %+v", got.Quota)
	}
}

// TestUpdateJob_PlatformColumnRoundTrip covers migration 0006's
// update_jobs.platform column (SPEC-v0.7 §1/§6, owned by
// agent-platforms): CreateUpdateJob/GetUpdateJob/ListUpdateJobs/
// UpdateUpdateJob must all persist and return the Platform field
// (added alongside the storage-account tables in the same migration,
// hence living in this v0.7-owned test file rather than the v0.6
// update_jobs test file).
func TestUpdateJob_PlatformColumnRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDBForV07(t)
	ctx := context.Background()

	created, err := db.CreateUpdateJob(ctx, models.UpdateJob{
		BatchID: "batch-plat", HostID: "mac-1", Target: "latest",
		State: models.UpdateJobQueued, Platform: "darwin",
	})
	if err != nil {
		t.Fatalf("CreateUpdateJob: %v", err)
	}
	if created.Platform != "darwin" {
		t.Errorf("CreateUpdateJob result Platform = %q, want darwin", created.Platform)
	}

	got, err := db.GetUpdateJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.Platform != "darwin" {
		t.Errorf("GetUpdateJob Platform = %q, want darwin", got.Platform)
	}

	got.Platform = "windows"
	got.State = models.UpdateJobInProgress
	if err := db.UpdateUpdateJob(ctx, got); err != nil {
		t.Fatalf("UpdateUpdateJob: %v", err)
	}
	reloaded, err := db.GetUpdateJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUpdateJob (reload): %v", err)
	}
	if reloaded.Platform != "windows" {
		t.Errorf("Platform after update = %q, want windows", reloaded.Platform)
	}

	list, err := db.ListUpdateJobs(ctx, "batch-plat", "")
	if err != nil {
		t.Fatalf("ListUpdateJobs: %v", err)
	}
	if len(list) != 1 || list[0].Platform != "windows" {
		t.Fatalf("ListUpdateJobs = %+v, want one job with Platform=windows", list)
	}

	// A job created without an explicit Platform (a pre-v0.7.0 caller,
	// or a linux host whose fallback already resolved to "linux" at
	// the hub layer before this call) round-trips as the empty string
	// exactly as stored, never silently defaulted at the storage layer
	// — the fallback-to-HostInfo.OS logic belongs to
	// hub.createUpdateBatch, not this layer.
	noPlat, err := db.CreateUpdateJob(ctx, models.UpdateJob{BatchID: "batch-plat2", HostID: "h2", Target: "latest", State: models.UpdateJobQueued})
	if err != nil {
		t.Fatalf("CreateUpdateJob (no platform): %v", err)
	}
	if noPlat.Platform != "" {
		t.Errorf("CreateUpdateJob (no platform) Platform = %q, want empty", noPlat.Platform)
	}
}
