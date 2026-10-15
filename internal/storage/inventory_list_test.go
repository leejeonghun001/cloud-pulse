package storage

import (
	"context"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestListHostInventoryCounts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	got, err := db.ListHostInventoryCounts(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty ListHostInventoryCounts = %v, %v; want empty map, nil", got, err)
	}
	for _, id := range []string{"h1", "h2"} {
		if err := db.UpsertHost(ctx, models.HostInfo{ID: id, Hostname: id}, 1000); err != nil {
			t.Fatalf("UpsertHost %s: %v", id, err)
		}
	}
	if err := db.SetHostInventory(ctx, "h2", models.Inventory{}); err != nil {
		t.Fatalf("SetHostInventory h2: %v", err)
	}
	inv := models.Inventory{CollectedAt: 1000, Ports: []models.ListeningPort{{Proto: "tcp", Port: 22}, {Proto: "udp", Port: 53}}}
	inv.Docker.Containers = []models.Container{{Name: "a", State: "running"}, {Name: "b", State: "exited"}, {Name: "c", State: "running"}}
	if err := db.SetHostInventory(ctx, "h1", inv); err != nil {
		t.Fatalf("SetHostInventory: %v", err)
	}
	got, err = db.ListHostInventoryCounts(ctx)
	if err != nil {
		t.Fatalf("ListHostInventories: %v", err)
	}
	want := inv.Counts()
	if len(got) != 2 || got["h2"] != (models.InventoryCounts{}) || got["h1"] != want || want != (models.InventoryCounts{ContainersRunning: 2, ListeningPorts: 2}) {
		t.Fatalf("ListHostInventoryCounts = %+v, want h2 zero and h1 = %+v (2 running, 2 ports)", got, want)
	}

}

func TestListHostInventoryCountsSkipsUndecodableSnapshot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"good", "bad"} {
		if err := db.UpsertHost(ctx, models.HostInfo{ID: id, Hostname: id}, 1000); err != nil {
			t.Fatalf("UpsertHost %s: %v", id, err)
		}
	}
	if err := db.SetHostInventory(ctx, "good", models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 22}}}); err != nil {
		t.Fatalf("SetHostInventory: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO host_inventory (host_id, collected_at, json) VALUES ('bad', 1000, '{not json')`); err != nil {
		t.Fatalf("insert corrupt row: %v", err)
	}
	got, err := db.ListHostInventoryCounts(ctx)
	if err == nil {
		t.Fatalf("err = nil, want an error naming the undecodable host")
	}
	if got["good"].ListeningPorts != 1 {
		t.Fatalf("good host counts = %+v, want 1 port despite the corrupt row", got)
	}
	if _, ok := got["bad"]; ok {
		t.Fatalf("corrupt host present in counts: %+v", got)
	}
}
