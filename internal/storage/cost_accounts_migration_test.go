package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// TestOpen_UpgradeFrom0011KeysCostSnapshotsByAccount: a hub row keeps
// account "", an agent row gets a host: placeholder account.
func TestOpen_UpgradeFrom0011KeysCostSnapshotsByAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-0011.db")
	legacy, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for i, e := range entries {
		var version int
		if _, err := fmt.Sscanf(e.Name(), "%04d_", &version); err != nil || version > 11 {
			continue
		}
		content, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if _, err := legacy.ExecContext(ctx, string(content)); err != nil {
			t.Fatalf("apply %s: %v", e.Name(), err)
		}
		if _, err := legacy.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, 1000+i); err != nil {
			t.Fatalf("record %d: %v", version, err)
		}
	}
	for _, q := range []string{
		`INSERT INTO cloud_cost_snapshots (provider, status, mtd_cost, last_success_at) VALUES ('aws', 'ok', 5, 10)`,
		`INSERT INTO cloud_cost_snapshots (provider, status, mtd_cost, last_success_at, source_host) VALUES ('oci', 'ok', 7, 10, 'ted-vinc')`,
	} {
		if _, err := legacy.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	aws, err := db.GetCloudCostSnapshot(ctx, models.CloudBillingAWS, "")
	if err != nil || aws.MTDCost != 5 || aws.LastSuccessAt != 10 {
		t.Errorf("hub aws row = %+v, %v; want kept under account \"\"", aws, err)
	}
	oci, err := db.GetCloudCostSnapshot(ctx, models.CloudBillingOCI, "host:ted-vinc")
	if err != nil || oci.MTDCost != 7 || oci.SourceHost != "ted-vinc" {
		t.Errorf("agent oci row = %+v, %v; want moved to host:ted-vinc", oci, err)
	}
}
