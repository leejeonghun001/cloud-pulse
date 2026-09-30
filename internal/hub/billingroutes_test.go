package hub

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// TestHandleGetBilling_BillingDisabled verifies GET /api/v1/billing
// works (200, empty snapshots) even when Options.Billing is nil —
// billing being disabled must never turn the read endpoint into an
// error.
func TestHandleGetBilling_BillingDisabled(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/billing", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	view := decodeJSON[models.BillingView](t, rec.Body)
	if len(view.Snapshots) != 0 {
		t.Errorf("Snapshots = %+v, want empty when billing is disabled", view.Snapshots)
	}
}

// TestHandleGetBilling_CombinesSnapshotsAndHosts verifies the happy
// path: a persisted AWS snapshot combined with a host whose
// CloudInstanceID matches one of its PerResource entries.
func TestHandleGetBilling_CombinesSnapshotsAndHosts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	ctx := context.Background()
	if err := store.SetCloudCostSnapshot(ctx, models.CloudCostSnapshot{
		Provider:     models.CloudBillingAWS,
		Status:       models.CloudBillingOK,
		Currency:     "USD",
		MTDCost:      100,
		ForecastCost: 200,
		PerResource:  map[string]float64{"i-abc": 100},
	}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := store.UpsertHost(ctx, models.HostInfo{
		ID: "host1", Hostname: "web1", Provider: models.ProviderAWS, CloudInstanceID: "i-abc",
	}, time.Now().Unix()); err != nil {
		t.Fatalf("seed host: %v", err)
	}

	runtime := &BillingRuntime{
		Collector:   billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()}),
		EnvInterval: models.BillingInterval24h,
	}
	opts := testOptions()
	opts.Billing = runtime
	srv := newTestServer(t, opts, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/billing", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	view := decodeJSON[models.BillingView](t, rec.Body)
	if len(view.Snapshots) != 1 || view.Snapshots[0].Provider != models.CloudBillingAWS {
		t.Fatalf("Snapshots = %+v, want one AWS snapshot", view.Snapshots)
	}
	if len(view.Hosts) != 1 || !view.Hosts[0].Matched {
		t.Fatalf("Hosts = %+v, want host1 matched", view.Hosts)
	}
	if view.IntervalSeconds != 86400 {
		t.Errorf("IntervalSeconds = %d, want 86400 (24h default)", view.IntervalSeconds)
	}
}

// TestHandleGetBilling_RequiresAuth verifies the standard 401/200 auth
// gate applies.
func TestHandleGetBilling_RequiresAuth(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/billing", "127.0.0.1:1", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", rec.Code)
	}
}

// TestHandlePostBillingRefresh_DisabledReturns501 verifies a hub with
// billing disabled reports 501 for the refresh endpoint (matching the
// original placeholder behavior for a disabled feature).
func TestHandlePostBillingRefresh_DisabledReturns501(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/billing/refresh", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}

// TestHandlePostBillingRefresh_RequiresAdmin verifies the endpoint is
// unauthenticated-rejecting even when Billing is configured.
func TestHandlePostBillingRefresh_RequiresAdmin(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.Billing = &BillingRuntime{Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()})}
	srv := newTestServer(t, opts, store)

	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/billing/refresh", "127.0.0.1:1", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestHandlePostBillingRefresh_RunsCollectorAndPersists verifies a
// successful refresh runs the collector and returns its snapshots.
func TestHandlePostBillingRefresh_RunsCollectorAndPersists(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runner := &noopRunner{}
	opts := testOptions()
	opts.Billing = &BillingRuntime{
		Collector: billing.New(store, billing.Options{Runner: runner, DataDir: t.TempDir()}),
	}
	srv := newTestServer(t, opts, store)

	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/billing/refresh", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if runner.calls == 0 {
		t.Error("collector's runner was never invoked")
	}
}

// TestHandlePostBillingRefresh_RateLimited verifies the 10-minute
// throttle rejects a second refresh with 429 + Retry-After.
func TestHandlePostBillingRefresh_RateLimited(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Billing = &BillingRuntime{
		Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()}),
		Now:       func() time.Time { return now },
	}
	srv := newTestServer(t, opts, store)

	rec1 := doRequest(t, srv.Handler(), "POST", "/api/v1/billing/refresh", "127.0.0.1:1", testUIToken, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d, want 200", rec1.Code)
	}

	rec2 := doRequest(t, srv.Handler(), "POST", "/api/v1/billing/refresh", "127.0.0.1:1", testUIToken, nil)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second immediate refresh status = %d, want 429", rec2.Code)
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After header")
	}
	body := decodeJSON[models.APIError](t, rec2.Body)
	if body.Code != "rate_limited" {
		t.Errorf("Code = %q, want rate_limited", body.Code)
	}
}

// TestHandlePostBillingRefresh_AllowedAfterInterval verifies a refresh
// 10+ minutes after the last one is allowed again.
func TestHandlePostBillingRefresh_AllowedAfterInterval(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	current := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Billing = &BillingRuntime{
		Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()}),
		Now:       func() time.Time { return current },
	}
	srv := newTestServer(t, opts, store)

	rec1 := doRequest(t, srv.Handler(), "POST", "/api/v1/billing/refresh", "127.0.0.1:1", testUIToken, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d, want 200", rec1.Code)
	}

	current = current.Add(10*time.Minute + time.Second)
	rec2 := doRequest(t, srv.Handler(), "POST", "/api/v1/billing/refresh", "127.0.0.1:1", testUIToken, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second refresh after interval status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
}

// noopRunner is a billing.CommandRunner that always reports the CLI as
// not installed — sufficient for route-level tests that only care
// about the handler's own logic, not collection outcomes.
type noopRunner struct {
	calls int
}

func (r *noopRunner) Run(_ context.Context, _ string, _ []string, _ string, _ ...string) (billing.Result, error) {
	r.calls++
	return billing.Result{NotFound: true}, nil
}

// TestHandleSetBillingInterval_DisabledReturns501 verifies the
// interval-setting endpoint reports 501 when billing is disabled, same
// as refresh.
func TestHandleSetBillingInterval_DisabledReturns501(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/billing/interval", "127.0.0.1:1", testUIToken, []byte(`{"interval":"6h"}`))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}

// TestHandleSetBillingInterval_RequiresAdmin verifies the standard
// 401/200 auth gate applies.
func TestHandleSetBillingInterval_RequiresAdmin(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.Billing = &BillingRuntime{Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()})}
	srv := newTestServer(t, opts, store)

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/billing/interval", "127.0.0.1:1", "", []byte(`{"interval":"6h"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestHandleSetBillingInterval_RejectsInvalidValue verifies only
// 6h/12h/24h are accepted.
func TestHandleSetBillingInterval_RejectsInvalidValue(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.Billing = &BillingRuntime{Collector: billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()})}
	srv := newTestServer(t, opts, store)

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/billing/interval", "127.0.0.1:1", testUIToken, []byte(`{"interval":"1h"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleSetBillingInterval_PersistsAndTakesEffectImmediately
// verifies a valid interval change is persisted (resolveBillingInterval
// reflects it on the very next call, i.e. without a restart) and
// recorded to the audit log with before/after values.
func TestHandleSetBillingInterval_PersistsAndTakesEffectImmediately(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	opts.Billing = &BillingRuntime{
		Collector:   billing.New(store, billing.Options{Runner: &noopRunner{}, DataDir: t.TempDir()}),
		EnvInterval: models.BillingInterval24h,
	}
	srv := newTestServer(t, opts, store)

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/billing/interval", "127.0.0.1:1", testUIToken, []byte(`{"interval":"6h"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got := srv.resolveBillingInterval(context.Background())
	if got != models.BillingInterval6h {
		t.Errorf("resolveBillingInterval = %q, want 6h", got)
	}

	entries, err := store.ListAuditEntries(context.Background(), "", 0, 10)
	if err != nil {
		t.Fatalf("ListAuditEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Action != AuditActionBillingIntervalChange {
		t.Errorf("Action = %q, want %q", entries[0].Action, AuditActionBillingIntervalChange)
	}
	if entries[0].BeforeJSON == "" || entries[0].AfterJSON == "" {
		t.Errorf("expected non-empty before/after JSON, got before=%q after=%q", entries[0].BeforeJSON, entries[0].AfterJSON)
	}
}
