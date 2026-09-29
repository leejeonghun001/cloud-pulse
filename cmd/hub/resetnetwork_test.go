package main

import (
	"context"
	"os"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

func TestResetNetwork_ClearsStoredOverride(t *testing.T) {
	t.Parallel()
	dbPath := tempDBPath(t)

	ctx := context.Background()
	seedDB, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if err := seedDB.SetSetting(ctx, hub.SettingNetworkConfig, `{"mode":"custom","addresses":["192.168.1.5"],"port":9090,"allowed_cidrs":["*"]}`); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := resetNetwork(dbPath); err != nil {
		t.Fatalf("resetNetwork: %v", err)
	}

	verifyDB, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open (verify): %v", err)
	}
	defer func() { _ = verifyDB.Close() }()

	_, ok, err := verifyDB.GetSetting(ctx, hub.SettingNetworkConfig)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if ok {
		t.Error("expected network_config setting to be cleared")
	}
}

func TestResetNetwork_NoOpWhenNoOverrideStored(t *testing.T) {
	t.Parallel()
	dbPath := tempDBPath(t)

	// No prior setting at all; resetNetwork must succeed idempotently.
	if err := resetNetwork(dbPath); err != nil {
		t.Fatalf("resetNetwork on a fresh DB: %v", err)
	}
	if err := resetNetwork(dbPath); err != nil {
		t.Fatalf("resetNetwork called twice: %v", err)
	}
}

func TestResetNetwork_WorksOnAlreadyMigratedDB(t *testing.T) {
	t.Parallel()
	dbPath := tempDBPath(t)

	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := resetNetwork(dbPath); err != nil {
		t.Fatalf("resetNetwork on an already-migrated DB: %v", err)
	}
}

func TestPrintResetNetworkSummary_DoesNotPanicWithoutAgentToken(t *testing.T) {
	t.Parallel()
	// config.LoadHub requires CP_AGENT_TOKEN; printResetNetworkSummary
	// must degrade gracefully (fallback message) rather than panic when
	// run in an environment without it configured (e.g. this test's
	// process env).
	f, err := os.CreateTemp(t.TempDir(), "summary")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() { _ = f.Close() }()

	printResetNetworkSummary(f)
}
