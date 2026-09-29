package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// inventoryReportMaxAge is the maximum time between two AgentReports
// that carry an Inventory snapshot, even when nothing has changed
// (SPEC-v0.5 §C: "sent ... when it has changed or every 10 minutes,
// whichever comes first").
const inventoryReportMaxAge = 10 * time.Minute

// inventoryCollectInterval is the minimum time between two actual
// InventoryCollector.Collect calls, independent of the sample
// interval/Flush cadence (SPEC-v0.5 §C: "collected every 60s (not every
// sample)"). A Flush that happens sooner than this reuses the
// previously collected snapshot instead of re-collecting.
const inventoryCollectInterval = 60 * time.Second

// inventoryReportHook decides, once per Reporter.Flush call, whether to
// attach a models.Inventory snapshot to the outbound AgentReport. It
// internally rate-limits actual collection to inventoryCollectInterval
// regardless of how often Flush itself is called (e.g. a 5s sample
// interval must not re-run the Docker/ports collector on every report),
// and separately decides whether the most recently collected snapshot
// should be attached: on change (content hash, ignoring CollectedAt so
// a re-collection of unchanged state doesn't count as "changed") or
// after inventoryReportMaxAge has elapsed since the last one was
// actually sent (confirmed via markSent).
type inventoryReportHook struct {
	provider InventoryProvider
	now      func() time.Time

	lastCollectedAt time.Time
	lastCollected   models.Inventory
	haveCollected   bool

	lastSentHash string
	lastSentAt   time.Time
}

// newInventoryReportHook wraps provider. A nil provider disables
// inventory reporting entirely: next always returns nil.
func newInventoryReportHook(provider InventoryProvider) *inventoryReportHook {
	return &inventoryReportHook{provider: provider, now: time.Now}
}

// next returns the inventory snapshot that should be attached to the
// next AgentReport, or nil when none should be (no provider configured,
// collection failed with no previous snapshot to fall back on, or
// neither the changed nor max-age condition holds). See the type doc
// comment for the collection-cadence and attach-decision rules.
// Collection failures are logged and treated as "no new snapshot this
// round" — inventory reporting must never block or fail the sample
// flush path — falling back to the last successfully collected
// snapshot, if any, for the attach decision.
func (h *inventoryReportHook) next(ctx context.Context, logger *slog.Logger) *models.Inventory {
	if h == nil || h.provider == nil {
		return nil
	}

	now := h.now()
	if !h.haveCollected || now.Sub(h.lastCollectedAt) >= inventoryCollectInterval {
		inv, err := h.provider.Collect(ctx, now)
		if err != nil {
			if logger != nil {
				logger.WarnContext(ctx, "inventory collection failed", "error", err)
			}
		} else {
			h.lastCollected = inv
			h.lastCollectedAt = now
			h.haveCollected = true
		}
	}
	if !h.haveCollected {
		return nil
	}

	hash := inventoryContentHash(h.lastCollected)
	changed := hash != h.lastSentHash
	stale := h.lastSentAt.IsZero() || now.Sub(h.lastSentAt) >= inventoryReportMaxAge
	if !changed && !stale {
		return nil
	}
	inv := h.lastCollected
	return &inv
}

// markSent records that inv (as returned by the most recent next() call)
// was successfully accepted by the hub, so subsequent next() calls
// compare against it and reset the max-age timer.
func (h *inventoryReportHook) markSent(inv models.Inventory) {
	if h == nil {
		return
	}
	h.lastSentHash = inventoryContentHash(inv)
	h.lastSentAt = h.now()
}

// inventoryContentHash hashes inv's JSON encoding after zeroing
// CollectedAt, so two collections of an unchanged inventory taken at
// different times hash identically.
func inventoryContentHash(inv models.Inventory) string {
	inv.CollectedAt = 0
	data, err := json.Marshal(inv)
	if err != nil {
		// Marshal of this package's own struct cannot realistically
		// fail; fall back to a value that never matches a previous
		// hash so a hypothetical failure degrades to "always send"
		// rather than "never send".
		return ""
	}
	sum := sha256.Sum256(data)
	return string(sum[:])
}
