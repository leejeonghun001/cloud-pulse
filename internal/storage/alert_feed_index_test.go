package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestActiveAlertFeedUsesOrderedStateIndex guards the scale-profile query:
// filtering firing events and ordering newest-first must not regress to a
// temporary SQLite sort as the event history grows.
func TestActiveAlertFeedUsesOrderedStateIndex(t *testing.T) {
	t.Parallel()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() }) // best-effort close of test database

	rows, err := db.sql.QueryContext(context.Background(), `
		EXPLAIN QUERY PLAN
		SELECT id FROM alert_events WHERE state = 'firing' ORDER BY started_at DESC
	`)
	if err != nil {
		t.Fatalf("explain active-alert query: %v", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close after scanning below

	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "idx_alert_events_state_started_at") {
		t.Fatalf("plan %q does not use ordered state index", plan)
	}
	if strings.Contains(strings.ToUpper(plan), "TEMP B-TREE") {
		t.Fatalf("plan %q unexpectedly sorts in a temp B-tree", plan)
	}
}
