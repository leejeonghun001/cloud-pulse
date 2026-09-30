package hub

import (
	"net/http"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestAuditRoute_RequireAuth(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/audit", "127.0.0.1:1", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /api/v1/audit = %d, want 401", rec.Code)
	}
}

func TestHandleListAuditEntries_Pagination(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()

	// Insert 5 entries with distinct, ascending At values.
	for i := int64(1); i <= 5; i++ {
		if _, err := store.CreateAuditEntry(ctx, models.AuditEntry{
			At:         1_700_000_000 + i,
			Actor:      "admin",
			Action:     "pricing_plan.create",
			EntityType: "pricing_plan",
			EntityID:   "1",
			AfterJSON:  `{"n":1}`,
		}); err != nil {
			t.Fatalf("seed audit entry %d: %v", i, err)
		}
	}

	srv := newTestServer(t, testOptions(), store)

	// First page, limit 2: newest first (At=5, At=4), HasMore true.
	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/audit?limit=2", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	page1 := decodeJSON[models.AuditListView](t, rec.Body)
	if len(page1.Entries) != 2 {
		t.Fatalf("page1 entries = %d, want 2", len(page1.Entries))
	}
	if page1.Entries[0].At != 1_700_000_005 || page1.Entries[1].At != 1_700_000_004 {
		t.Errorf("page1 order = %+v, want At=5 then At=4 (newest first)", page1.Entries)
	}
	if !page1.HasMore {
		t.Error("page1.HasMore = false, want true (3 more entries remain)")
	}

	// Second page using the cursor: before the oldest At seen so far.
	cursor := page1.Entries[len(page1.Entries)-1].At
	rec2 := doRequest(t, srv.Handler(), "GET",
		"/api/v1/audit?limit=2&before="+formatInt(cursor), "127.0.0.1:1", testUIToken, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	page2 := decodeJSON[models.AuditListView](t, rec2.Body)
	if len(page2.Entries) != 2 {
		t.Fatalf("page2 entries = %d, want 2", len(page2.Entries))
	}
	if page2.Entries[0].At != 1_700_000_003 || page2.Entries[1].At != 1_700_000_002 {
		t.Errorf("page2 order = %+v, want At=3 then At=2", page2.Entries)
	}
	if !page2.HasMore {
		t.Error("page2.HasMore = false, want true (1 more entry remains)")
	}

	// Third page: exactly the last entry, HasMore false.
	cursor2 := page2.Entries[len(page2.Entries)-1].At
	rec3 := doRequest(t, srv.Handler(), "GET",
		"/api/v1/audit?limit=2&before="+formatInt(cursor2), "127.0.0.1:1", testUIToken, nil)
	page3 := decodeJSON[models.AuditListView](t, rec3.Body)
	if len(page3.Entries) != 1 {
		t.Fatalf("page3 entries = %d, want 1", len(page3.Entries))
	}
	if page3.HasMore {
		t.Error("page3.HasMore = true, want false (no entries remain)")
	}
}

func TestHandleListAuditEntries_FilterByEntityType(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()

	if _, err := store.CreateAuditEntry(ctx, models.AuditEntry{
		At: 1, Actor: "admin", Action: "pricing_plan.create", EntityType: "pricing_plan",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.CreateAuditEntry(ctx, models.AuditEntry{
		At: 2, Actor: "admin", Action: "host_pricing.change", EntityType: "host_pricing",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := newTestServer(t, testOptions(), store)
	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/audit?entity_type=host_pricing", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	view := decodeJSON[models.AuditListView](t, rec.Body)
	if len(view.Entries) != 1 || view.Entries[0].EntityType != "host_pricing" {
		t.Errorf("entries = %+v, want only the host_pricing entry", view.Entries)
	}
}

func TestHandleListAuditEntries_LimitCappedAtMax(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()
	for i := 0; i < 3; i++ {
		if _, err := store.CreateAuditEntry(ctx, models.AuditEntry{At: int64(i + 1), Actor: "admin", Action: "x", EntityType: "y"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/audit?limit=99999", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	view := decodeJSON[models.AuditListView](t, rec.Body)
	if len(view.Entries) != 3 {
		t.Errorf("entries = %d, want 3 (all seeded rows, limit request just capped)", len(view.Entries))
	}
}

func TestPruneAuditEntries_RetentionWindow(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := t.Context()

	now := time.Unix(1_700_000_000, 0)
	oldAt := now.AddDate(0, 0, -(models.AuditRetentionDays + 1)).Unix()
	recentAt := now.AddDate(0, 0, -(models.AuditRetentionDays - 1)).Unix()

	if _, err := store.CreateAuditEntry(ctx, models.AuditEntry{At: oldAt, Actor: "admin", Action: "x", EntityType: "y"}); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if _, err := store.CreateAuditEntry(ctx, models.AuditEntry{At: recentAt, Actor: "admin", Action: "x", EntityType: "y"}); err != nil {
		t.Fatalf("seed recent: %v", err)
	}

	if err := store.PruneAuditEntries(ctx, now); err != nil {
		t.Fatalf("prune: %v", err)
	}

	entries, err := store.ListAuditEntries(ctx, "", 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].At != recentAt {
		t.Errorf("entries after prune = %+v, want only the recent one", entries)
	}
}

// formatInt is a tiny local helper avoiding an extra strconv import
// line collision risk in this small test file (mirrors the existing
// itoa-style helpers already present elsewhere in this test package).
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
