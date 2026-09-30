package hub

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// SettingBillingInterval is the settings table key for the hub-side
// cloud billing polling interval override (SPEC-v0.6 §1): one of
// models.BillingInterval6h/12h/24h. Its absence means "use
// BillingRuntime.EnvInterval" (the CP_BILLING_INTERVAL value at
// startup).
const SettingBillingInterval = "billing_interval"

// billingRefreshMinInterval is the minimum time between POST
// /api/v1/billing/refresh calls (SPEC-v0.6 §1: "최소 10분 간격을
// 강제").
const billingRefreshMinInterval = 10 * time.Minute

// BillingRuntime wires SPEC-v0.6 §1's cloud billing collector into the
// hub: the collector itself, the network-cost stage's estimator hook,
// and the scheduler's interval-vs-settings resolution. Constructed by
// cmd/hub/billing.go and passed as Options.Billing; nil disables billing
// entirely (see registerBillingRoutes/handleGetBilling).
type BillingRuntime struct {
	// Collector runs the actual AWS/OCI CLI collection.
	Collector *billing.Collector
	// EnvInterval is the CP_BILLING_INTERVAL value at hub startup, used
	// whenever no "billing_interval" setting has been confirmed from
	// the dashboard yet.
	EnvInterval models.BillingInterval
	// NetworkEstimator computes a host's network cost estimate (the
	// network-cost v0.6.0 stage's hook — see billing.NetworkEstimator's
	// doc comment). nil uses billing.ZeroNetworkEstimator.
	NetworkEstimator billing.NetworkEstimator
	// Now returns the current time; nil defaults to time.Now. Tests
	// inject a fixed clock.
	Now func() time.Time

	// lastRefresh guards the 10-minute throttle on POST
	// /api/v1/billing/refresh — stored as unix nanoseconds so it can be
	// read/written atomically without a mutex.
	lastRefresh atomic.Int64

	// runMu serializes collector runs so a manual refresh and the
	// background scheduler tick never race on the same CLI/HOME dir.
	runMu sync.Mutex
}

func (b *BillingRuntime) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// networkEstimator returns b.NetworkEstimator, defaulting to
// billing.ZeroNetworkEstimator.
func (b *BillingRuntime) networkEstimator() billing.NetworkEstimator {
	if b.NetworkEstimator != nil {
		return b.NetworkEstimator
	}
	return billing.ZeroNetworkEstimator
}

// run executes one collection pass, serialized against concurrent
// calls via runMu.
func (b *BillingRuntime) run(ctx context.Context, interval models.BillingInterval) ([]models.CloudCostSnapshot, error) {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	return b.Collector.Run(ctx, interval)
}

// resolveBillingInterval returns the effective billing polling interval:
// the hub-side "billing_interval" setting if present and valid,
// otherwise b.EnvInterval.
func (s *Server) resolveBillingInterval(ctx context.Context) models.BillingInterval {
	if s.opts.Billing == nil {
		return models.BillingIntervalDefault
	}
	raw, ok, err := s.store.GetSetting(ctx, SettingBillingInterval)
	if err == nil && ok {
		v := models.BillingInterval(raw)
		if models.ValidBillingInterval(v) {
			return v
		}
	}
	if models.ValidBillingInterval(s.opts.Billing.EnvInterval) {
		return s.opts.Billing.EnvInterval
	}
	return models.BillingIntervalDefault
}

// billingIntervalDuration converts a models.BillingInterval to a
// time.Duration, defaulting to 24h for an unrecognized value.
func billingIntervalDuration(v models.BillingInterval) time.Duration {
	switch v {
	case models.BillingInterval6h:
		return 6 * time.Hour
	case models.BillingInterval12h:
		return 12 * time.Hour
	case models.BillingInterval24h:
		return 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// RunBillingLoop runs the cloud billing collector on a timer that
// re-reads the effective interval (env default or hub-side setting)
// before every tick, so a live interval change (SPEC-v0.6 §1: "즉시
// 스케줄러에 반영") takes effect on the very next scheduling decision
// without a restart. It blocks until ctx is canceled. A nil
// Options.Billing makes this a no-op — callers should still be able to
// invoke it unconditionally.
func (s *Server) RunBillingLoop(ctx context.Context) {
	if s.opts.Billing == nil {
		return
	}

	s.collectBillingOnce(ctx)

	for {
		interval := billingIntervalDuration(s.resolveBillingInterval(ctx))
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.collectBillingOnce(ctx)
		}
	}
}

// collectBillingOnce runs the billing collector once, logging (never
// panicking) on failure.
func (s *Server) collectBillingOnce(ctx context.Context) {
	if s.opts.Billing == nil {
		return
	}
	interval := s.resolveBillingInterval(ctx)
	if _, err := s.opts.Billing.run(ctx, interval); err != nil {
		s.logger.Error("billing: scheduled collection failed", "error", err)
	}
	s.opts.Billing.lastRefresh.Store(s.opts.Billing.now().UnixNano())
}

// billingRefreshAllowed reports whether a manual POST
// /api/v1/billing/refresh should be allowed right now, given the
// 10-minute throttle (SPEC-v0.6 §1), and if not, how long the caller
// must wait.
func (b *BillingRuntime) refreshAllowed() (allowed bool, retryAfter time.Duration) {
	last := b.lastRefresh.Load()
	if last == 0 {
		return true, 0
	}
	elapsed := b.now().Sub(time.Unix(0, last))
	if elapsed >= billingRefreshMinInterval {
		return true, 0
	}
	return false, billingRefreshMinInterval - elapsed
}
