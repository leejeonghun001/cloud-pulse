package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestSessions_CRUD(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	ctx := context.Background()
	original := models.Session{
		IDHash: "hash-a", CreatedAt: 100, LastSeen: 110, ExpiresAt: 200,
		Remote: "127.0.0.1", UserAgent: "test-agent",
	}
	if err := db.CreateSession(ctx, original); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := db.GetSession(ctx, original.IDHash)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("GetSession = %+v, want %+v", got, original)
	}

	if err := db.TouchSession(ctx, original.IDHash, 120, 300); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	got, err = db.GetSession(ctx, original.IDHash)
	if err != nil {
		t.Fatalf("GetSession after touch: %v", err)
	}
	if got.LastSeen != 120 || got.ExpiresAt != 300 {
		t.Errorf("touched session = %+v, want LastSeen=120 ExpiresAt=300", got)
	}

	if err := db.DeleteSession(ctx, original.IDHash); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := db.GetSession(ctx, original.IDHash); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("GetSession after delete error = %v, want models.ErrNotFound", err)
	}
	if err := db.DeleteSession(ctx, original.IDHash); err != nil {
		t.Errorf("DeleteSession missing: %v", err)
	}
}

func TestSessions_NotFoundAndListOrder(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.GetSession(ctx, "missing"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetSession missing error = %v, want models.ErrNotFound", err)
	}
	if err := db.TouchSession(ctx, "missing", 1, 2); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("TouchSession missing error = %v, want models.ErrNotFound", err)
	}

	for _, id := range []string{"hash-c", "hash-a", "hash-b"} {
		if err := db.CreateSession(ctx, models.Session{IDHash: id, CreatedAt: 1, LastSeen: 1, ExpiresAt: 100}); err != nil {
			t.Fatalf("CreateSession(%q): %v", id, err)
		}
	}
	got, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	want := []string{"hash-a", "hash-b", "hash-c"}
	if len(got) != len(want) {
		t.Fatalf("ListSessions length = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].IDHash != id {
			t.Errorf("ListSessions()[%d].IDHash = %q, want %q", i, got[i].IDHash, id)
		}
	}
}

func TestSessions_DeleteExcept(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"keep", "remove-a", "remove-b"} {
		if err := db.CreateSession(ctx, models.Session{IDHash: id, CreatedAt: 1, LastSeen: 1, ExpiresAt: 100}); err != nil {
			t.Fatalf("CreateSession(%q): %v", id, err)
		}
	}

	deleted, err := db.DeleteSessionsExcept(ctx, "keep")
	if err != nil {
		t.Fatalf("DeleteSessionsExcept keep: %v", err)
	}
	if deleted != 2 {
		t.Errorf("DeleteSessionsExcept keep deleted = %d, want 2", deleted)
	}
	remaining, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions after keep: %v", err)
	}
	if len(remaining) != 1 || remaining[0].IDHash != "keep" {
		t.Fatalf("remaining sessions = %+v, want only keep", remaining)
	}

	deleted, err = db.DeleteSessionsExcept(ctx, "")
	if err != nil {
		t.Fatalf("DeleteSessionsExcept all: %v", err)
	}
	if deleted != 1 {
		t.Errorf("DeleteSessionsExcept all deleted = %d, want 1", deleted)
	}
	remaining, err = db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions after all delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("remaining sessions = %+v, want none", remaining)
	}
}

func TestSessions_PruneExpiresAtBoundary(t *testing.T) {
	t.Parallel()

	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	for _, sess := range []models.Session{
		{IDHash: "expired", CreatedAt: 1, LastSeen: 1, ExpiresAt: now.Add(-time.Second).Unix()},
		{IDHash: "boundary", CreatedAt: 1, LastSeen: 1, ExpiresAt: now.Unix()},
		{IDHash: "active", CreatedAt: 1, LastSeen: 1, ExpiresAt: now.Add(time.Second).Unix()},
	} {
		if err := db.CreateSession(ctx, sess); err != nil {
			t.Fatalf("CreateSession(%q): %v", sess.IDHash, err)
		}
	}
	if err := db.PruneSessions(ctx, now); err != nil {
		t.Fatalf("PruneSessions: %v", err)
	}
	got, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions after prune: %v", err)
	}
	if len(got) != 1 || got[0].IDHash != "active" {
		t.Errorf("sessions after prune = %+v, want only active", got)
	}
}
