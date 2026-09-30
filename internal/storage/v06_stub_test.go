package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func openTestDBForV06(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "v06.db"))
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

func TestCloudCostSnapshot_GetDefaultsToNotConfigured(t *testing.T) {
	t.Parallel()
	db := openTestDBForV06(t)
	ctx := context.Background()

	snap, err := db.GetCloudCostSnapshot(ctx, models.CloudBillingAWS)
	if err != nil {
		t.Fatalf("GetCloudCostSnapshot: %v", err)
	}
	if snap.Provider != models.CloudBillingAWS || snap.Status != models.CloudBillingNotConfigured {
		t.Errorf("GetCloudCostSnapshot (no row) = %+v, want Provider=aws Status=not_configured", snap)
	}
}

func TestCloudCostSnapshot_SetAndGetRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDBForV06(t)
	ctx := context.Background()

	snap := models.CloudCostSnapshot{
		Provider:       models.CloudBillingAWS,
		Status:         models.CloudBillingOK,
		Currency:       "USD",
		MTDCost:        12.5,
		ForecastCost:   30,
		ForecastMethod: "api",
		AccountLevel:   false,
		PerResource:    map[string]float64{"i-abc": 5.5},
		CollectedAt:    1000,
		LastSuccessAt:  1000,
		LastAttemptAt:  1000,
	}
	if err := db.SetCloudCostSnapshot(ctx, snap); err != nil {
		t.Fatalf("SetCloudCostSnapshot: %v", err)
	}

	got, err := db.GetCloudCostSnapshot(ctx, models.CloudBillingAWS)
	if err != nil {
		t.Fatalf("GetCloudCostSnapshot: %v", err)
	}
	if got.Status != snap.Status || got.MTDCost != snap.MTDCost || got.PerResource["i-abc"] != 5.5 {
		t.Errorf("GetCloudCostSnapshot round-trip = %+v, want %+v", got, snap)
	}

	// Upsert (second write for the same provider must replace, not
	// duplicate).
	snap.MTDCost = 20
	if err := db.SetCloudCostSnapshot(ctx, snap); err != nil {
		t.Fatalf("SetCloudCostSnapshot (upsert): %v", err)
	}
	list, err := db.ListCloudCostSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListCloudCostSnapshots: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListCloudCostSnapshots len = %d, want 1 (upsert, not duplicate)", len(list))
	}
	if list[0].MTDCost != 20 {
		t.Errorf("MTDCost after upsert = %v, want 20", list[0].MTDCost)
	}
}

func TestUpdateJob_CreateGetUpdateList(t *testing.T) {
	t.Parallel()
	db := openTestDBForV06(t)
	ctx := context.Background()

	job := models.UpdateJob{BatchID: "batch-1", HostID: "host-1", Target: "latest", State: models.UpdateJobQueued}
	created, err := db.CreateUpdateJob(ctx, job)
	if err != nil {
		t.Fatalf("CreateUpdateJob: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("CreateUpdateJob: ID not assigned")
	}
	if created.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1 (defaulted)", created.Attempt)
	}

	got, err := db.GetUpdateJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUpdateJob: %v", err)
	}
	if got.HostID != "host-1" || got.State != models.UpdateJobQueued {
		t.Errorf("GetUpdateJob = %+v, want HostID=host-1 State=queued", got)
	}

	got.State = models.UpdateJobInProgress
	if err := db.UpdateUpdateJob(ctx, got); err != nil {
		t.Fatalf("UpdateUpdateJob: %v", err)
	}
	reloaded, err := db.GetUpdateJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUpdateJob (reload): %v", err)
	}
	if reloaded.State != models.UpdateJobInProgress {
		t.Errorf("State after update = %q, want in_progress", reloaded.State)
	}

	if _, err := db.GetUpdateJob(ctx, created.ID+999); err == nil {
		t.Error("GetUpdateJob (missing id): want error")
	}
	if err := db.UpdateUpdateJob(ctx, models.UpdateJob{ID: created.ID + 999}); err == nil {
		t.Error("UpdateUpdateJob (missing id): want models.ErrNotFound")
	}

	// A second job for the same host must become the "latest".
	job2 := models.UpdateJob{BatchID: "batch-2", HostID: "host-1", Target: "v0.6.1", State: models.UpdateJobQueued}
	created2, err := db.CreateUpdateJob(ctx, job2)
	if err != nil {
		t.Fatalf("CreateUpdateJob (2nd): %v", err)
	}
	latest, err := db.GetLatestUpdateJobForHost(ctx, "host-1")
	if err != nil {
		t.Fatalf("GetLatestUpdateJobForHost: %v", err)
	}
	if latest.ID != created2.ID {
		t.Errorf("GetLatestUpdateJobForHost = job %d, want %d", latest.ID, created2.ID)
	}

	jobs, err := db.ListUpdateJobs(ctx, "batch-1", "")
	if err != nil {
		t.Fatalf("ListUpdateJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != created.ID {
		t.Errorf("ListUpdateJobs(batch-1) = %+v, want just job %d", jobs, created.ID)
	}

	if _, err := db.GetLatestUpdateJobForHost(ctx, "no-such-host"); err == nil {
		t.Error("GetLatestUpdateJobForHost (unknown host): want error")
	}
}

func TestPricingPlan_BuiltinsSeededAndCRUD(t *testing.T) {
	t.Parallel()
	db := openTestDBForV06(t)
	ctx := context.Background()

	plans, err := db.ListPricingPlans(ctx)
	if err != nil {
		t.Fatalf("ListPricingPlans: %v", err)
	}
	if len(plans) != 4 {
		t.Fatalf("ListPricingPlans (fresh db) = %d plans, want 4 built-ins", len(plans))
	}
	for _, p := range plans {
		if !p.Builtin {
			t.Errorf("plan %q Builtin = false, want true (seeded)", p.Name)
		}
		if len(p.EgressTiers) == 0 {
			t.Errorf("plan %q EgressTiers is empty", p.Name)
		}
	}

	created, err := db.CreatePricingPlan(ctx, models.PricingPlan{
		Name:         "Custom plan",
		Provider:     "aws",
		Currency:     "USD",
		EgressFreeGB: 50,
		EgressTiers:  []models.PricingTier{{UpToGB: 0, PricePerGB: 0.1}},
		Builtin:      true, // must be forced false by CreatePricingPlan
	})
	if err != nil {
		t.Fatalf("CreatePricingPlan: %v", err)
	}
	if created.Builtin {
		t.Error("CreatePricingPlan: Builtin was not forced to false")
	}

	got, err := db.GetPricingPlan(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetPricingPlan: %v", err)
	}
	if got.EgressFreeGB != 50 || len(got.EgressTiers) != 1 || got.EgressTiers[0].PricePerGB != 0.1 {
		t.Errorf("GetPricingPlan round-trip = %+v, want EgressFreeGB=50 with 1 tier @0.1", got)
	}

	got.Name = "Custom plan (renamed)"
	updated, err := db.UpdatePricingPlan(ctx, got)
	if err != nil {
		t.Fatalf("UpdatePricingPlan: %v", err)
	}
	if updated.Name != "Custom plan (renamed)" {
		t.Errorf("UpdatePricingPlan Name = %q, want renamed", updated.Name)
	}

	if err := db.DeletePricingPlan(ctx, created.ID); err != nil {
		t.Fatalf("DeletePricingPlan: %v", err)
	}
	if _, err := db.GetPricingPlan(ctx, created.ID); err == nil {
		t.Error("GetPricingPlan after delete: want error")
	}
	// Deleting a non-existent plan is not an error.
	if err := db.DeletePricingPlan(ctx, created.ID); err != nil {
		t.Errorf("DeletePricingPlan (already deleted): %v", err)
	}

	if _, err := db.UpdatePricingPlan(ctx, models.PricingPlan{ID: 999999, Name: "nope"}); err == nil {
		t.Error("UpdatePricingPlan (missing id): want models.ErrNotFound")
	}
}

func TestHostPricing_GetDefaultsAndSetRoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDBForV06(t)
	ctx := context.Background()

	hp, err := db.GetHostPricing(ctx, "host-1")
	if err != nil {
		t.Fatalf("GetHostPricing: %v", err)
	}
	if hp.HostID != "host-1" || hp.PlanID != 0 {
		t.Errorf("GetHostPricing (no row) = %+v, want HostID=host-1 PlanID=0", hp)
	}

	if err := db.SetHostPricing(ctx, models.HostPricing{HostID: "host-1", PlanID: 2}); err != nil {
		t.Fatalf("SetHostPricing: %v", err)
	}
	got, err := db.GetHostPricing(ctx, "host-1")
	if err != nil {
		t.Fatalf("GetHostPricing (after set): %v", err)
	}
	if got.PlanID != 2 {
		t.Errorf("PlanID = %d, want 2", got.PlanID)
	}

	// Upsert: setting again for the same host must replace, not
	// duplicate.
	if err := db.SetHostPricing(ctx, models.HostPricing{HostID: "host-1", PlanID: 3}); err != nil {
		t.Fatalf("SetHostPricing (upsert): %v", err)
	}
	list, err := db.ListHostPricing(ctx)
	if err != nil {
		t.Fatalf("ListHostPricing: %v", err)
	}
	if len(list) != 1 || list[0].PlanID != 3 {
		t.Errorf("ListHostPricing = %+v, want single row with PlanID=3", list)
	}
}

func TestAuditLog_CreateListAndPrune(t *testing.T) {
	t.Parallel()
	db := openTestDBForV06(t)
	ctx := context.Background()

	now := time.Now().Unix()
	old := now - int64(models.AuditRetentionDays+1)*24*3600

	if _, err := db.CreateAuditEntry(ctx, models.AuditEntry{At: old, Actor: "admin", Action: "pricing_plan.create", EntityType: "pricing_plan", EntityID: "1"}); err != nil {
		t.Fatalf("CreateAuditEntry (old): %v", err)
	}
	recent, err := db.CreateAuditEntry(ctx, models.AuditEntry{At: now, Actor: "admin", Action: "pricing_plan.update", EntityType: "pricing_plan", EntityID: "1", BeforeJSON: `{"a":1}`, AfterJSON: `{"a":2}`})
	if err != nil {
		t.Fatalf("CreateAuditEntry (recent): %v", err)
	}
	if recent.ID == 0 {
		t.Fatal("CreateAuditEntry: ID not assigned")
	}

	entries, err := db.ListAuditEntries(ctx, "pricing_plan", 0, 10)
	if err != nil {
		t.Fatalf("ListAuditEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ListAuditEntries = %d entries, want 2", len(entries))
	}
	if entries[0].At != now {
		t.Errorf("ListAuditEntries[0].At = %d, want %d (newest first)", entries[0].At, now)
	}

	if err := db.PruneAuditEntries(ctx, time.Now()); err != nil {
		t.Fatalf("PruneAuditEntries: %v", err)
	}
	afterPrune, err := db.ListAuditEntries(ctx, "", 0, 10)
	if err != nil {
		t.Fatalf("ListAuditEntries (after prune): %v", err)
	}
	if len(afterPrune) != 1 {
		t.Fatalf("ListAuditEntries after prune = %d entries, want 1 (old entry pruned)", len(afterPrune))
	}
	if afterPrune[0].At != now {
		t.Errorf("surviving entry At = %d, want %d", afterPrune[0].At, now)
	}
}
