package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestHostCloudNetwork_RoundTrip(t *testing.T) {
	t.Parallel()
	db := openTestDBForV06(t)
	ctx := context.Background()

	if _, err := db.GetHostCloudNetwork(ctx, "h1"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("Get before set err = %v, want ErrNotFound", err)
	}
	u := models.CloudNetworkUsage{Provider: models.CloudBillingOCI, Month: "2026-10", InBytes: 24 << 30, OutBytes: 5 << 30, Status: models.CloudBillingOK, LastSuccessAt: 10, LastAttemptAt: 10}
	if err := db.SetHostCloudNetwork(ctx, "h1", u); err != nil {
		t.Fatalf("Set: %v", err)
	}
	u.OutBytes = 6 << 30
	if err := db.SetHostCloudNetwork(ctx, "h1", u); err != nil {
		t.Fatalf("Set (upsert): %v", err)
	}
	got, err := db.GetHostCloudNetwork(ctx, "h1")
	if err != nil || got != u {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, u)
	}
	all, err := db.ListHostCloudNetwork(ctx)
	if err != nil || len(all) != 1 || all["h1"] != u {
		t.Fatalf("List = %+v, %v; want just h1", all, err)
	}
}
