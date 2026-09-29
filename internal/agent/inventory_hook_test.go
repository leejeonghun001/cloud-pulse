package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// fakeInventoryProvider is a scriptable InventoryProvider for
// inventoryReportHook tests.
type fakeInventoryProvider struct {
	invs  []models.Inventory
	errs  []error
	idx   int
	calls int
}

func (f *fakeInventoryProvider) Collect(_ context.Context, _ time.Time) (models.Inventory, error) {
	f.calls++
	i := f.idx
	if i >= len(f.invs) {
		i = len(f.invs) - 1
	}
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	inv := f.invs[i]
	f.idx++
	return inv, err
}

// invHookClock is a simple mutable-time helper for inventoryReportHook
// tests, avoiding any real sleeping.
type invHookClock struct {
	t time.Time
}

func (c *invHookClock) now() time.Time          { return c.t }
func (c *invHookClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestInventoryReportHook_NilProviderAlwaysNil(t *testing.T) {
	h := newInventoryReportHook(nil)
	if got := h.next(context.Background(), nil); got != nil {
		t.Fatalf("next() = %v, want nil for a nil provider", got)
	}
}

func TestInventoryReportHook_FirstCallAlwaysAttaches(t *testing.T) {
	provider := &fakeInventoryProvider{invs: []models.Inventory{{Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}}}
	h := newInventoryReportHook(provider)
	clock := &invHookClock{t: time.Unix(1000, 0)}
	h.now = clock.now

	got := h.next(context.Background(), nil)
	if got == nil {
		t.Fatal("next() = nil on first call, want a snapshot")
	}
}

func TestInventoryReportHook_UnchangedNotResentBeforeMaxAge(t *testing.T) {
	inv := models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	provider := &fakeInventoryProvider{invs: []models.Inventory{inv, inv, inv}}
	h := newInventoryReportHook(provider)
	clock := &invHookClock{t: time.Unix(1000, 0)}
	h.now = clock.now

	first := h.next(context.Background(), nil)
	if first == nil {
		t.Fatal("expected first call to attach")
	}
	h.markSent(*first)

	// Advance past the collection interval (so next() re-collects) but
	// well short of the max-age resend window.
	clock.advance(90 * time.Second)
	got := h.next(context.Background(), nil)
	if got != nil {
		t.Fatalf("next() = %+v, want nil (unchanged, not yet stale)", got)
	}
}

func TestInventoryReportHook_ChangedContentReattaches(t *testing.T) {
	invA := models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	invB := models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 443}}}
	provider := &fakeInventoryProvider{invs: []models.Inventory{invA, invB}}
	h := newInventoryReportHook(provider)
	clock := &invHookClock{t: time.Unix(1000, 0)}
	h.now = clock.now

	first := h.next(context.Background(), nil)
	if first == nil {
		t.Fatal("expected first call to attach")
	}
	h.markSent(*first)

	clock.advance(inventoryCollectInterval) // force a re-collect
	second := h.next(context.Background(), nil)
	if second == nil {
		t.Fatal("next() = nil, want attach on changed content")
	}
	if len(second.Ports) != 1 || second.Ports[0].Port != 443 {
		t.Errorf("got %+v, want the changed (port 443) snapshot", second)
	}
}

func TestInventoryReportHook_ResentAfterMaxAgeEvenIfUnchanged(t *testing.T) {
	inv := models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	provider := &fakeInventoryProvider{invs: []models.Inventory{inv, inv, inv, inv}}
	h := newInventoryReportHook(provider)
	clock := &invHookClock{t: time.Unix(1000, 0)}
	h.now = clock.now

	first := h.next(context.Background(), nil)
	h.markSent(*first)

	clock.advance(inventoryReportMaxAge)
	got := h.next(context.Background(), nil)
	if got == nil {
		t.Fatal("next() = nil, want attach after max age elapsed even though unchanged")
	}
}

func TestInventoryReportHook_CollectionRateLimited(t *testing.T) {
	inv := models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	provider := &fakeInventoryProvider{invs: []models.Inventory{inv, inv, inv}}
	h := newInventoryReportHook(provider)
	clock := &invHookClock{t: time.Unix(1000, 0)}
	h.now = clock.now

	h.next(context.Background(), nil)
	if provider.calls != 1 {
		t.Fatalf("calls = %d, want 1 after first next()", provider.calls)
	}

	// A second call well within inventoryCollectInterval must not
	// re-invoke Collect.
	clock.advance(5 * time.Second)
	h.next(context.Background(), nil)
	if provider.calls != 1 {
		t.Fatalf("calls = %d, want still 1 (rate-limited)", provider.calls)
	}

	clock.advance(inventoryCollectInterval)
	h.next(context.Background(), nil)
	if provider.calls != 2 {
		t.Fatalf("calls = %d, want 2 after collection interval elapsed", provider.calls)
	}
}

func TestInventoryReportHook_CollectErrorFallsBackToPrevious(t *testing.T) {
	invA := models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	provider := &fakeInventoryProvider{
		invs: []models.Inventory{invA, {}},
		errs: []error{nil, errors.New("boom")},
	}
	h := newInventoryReportHook(provider)
	clock := &invHookClock{t: time.Unix(1000, 0)}
	h.now = clock.now

	first := h.next(context.Background(), nil)
	if first == nil {
		t.Fatal("expected first call to attach")
	}
	h.markSent(*first)

	clock.advance(inventoryCollectInterval)
	got := h.next(context.Background(), nil)
	if got != nil {
		t.Fatalf("next() = %+v, want nil: collection failed and previous snapshot was already sent+unchanged", got)
	}
}

func TestInventoryReportHook_MarkSentNilReceiverSafe(t *testing.T) {
	var h *inventoryReportHook
	h.markSent(models.Inventory{}) // must not panic
}

func TestInventoryContentHash_IgnoresCollectedAt(t *testing.T) {
	a := models.Inventory{CollectedAt: 100, Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	b := models.Inventory{CollectedAt: 200, Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	if inventoryContentHash(a) != inventoryContentHash(b) {
		t.Fatal("hashes differ despite only CollectedAt changing")
	}
}

func TestInventoryContentHash_DiffersOnRealChange(t *testing.T) {
	a := models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	b := models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 443}}}
	if inventoryContentHash(a) == inventoryContentHash(b) {
		t.Fatal("hashes match despite different ports")
	}
}
