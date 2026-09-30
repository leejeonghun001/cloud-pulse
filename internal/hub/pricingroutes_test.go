package hub

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// TestPricingRoutes_RequireAuth verifies every pricing-plan/host-
// pricing/network-billing/display-currency route rejects an
// unauthenticated request with 401 before reaching its handler.
func TestPricingRoutes_RequireAuth(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	routes := []struct {
		method, path string
	}{
		{"GET", "/api/v1/billing/plans"},
		{"POST", "/api/v1/billing/plans"},
		{"PUT", "/api/v1/billing/plans/1"},
		{"DELETE", "/api/v1/billing/plans/1"},
		{"PUT", "/api/v1/hosts/h1/pricing"},
		{"GET", "/api/v1/billing/network"},
		{"GET", "/api/v1/settings/billing/currency"},
		{"PUT", "/api/v1/settings/billing/currency"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rec := doRequest(t, srv.Handler(), rt.method, rt.path, "127.0.0.1:1", "", nil)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("unauthenticated %s %s = %d, want 401", rt.method, rt.path, rec.Code)
			}
		})
	}
}

func TestHandleListPricingPlans_IncludesBuiltins(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedBuiltinPlan(t, store, "AWS Free Tier (Internet egress, US)", "aws", 100, true)
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/billing/plans", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	plans := decodeJSON[[]models.PricingPlan](t, rec.Body)
	if len(plans) != 1 || !plans[0].Builtin {
		t.Errorf("plans = %+v, want one builtin plan", plans)
	}
}

func TestHandleCreatePricingPlan_ValidationErrors(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	body := []byte(`{"name":"","egress_tiers":[]}`)
	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/billing/plans", "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	apiErr := decodeJSON[models.APIError](t, rec.Body)
	if apiErr.Details == nil {
		t.Error("expected Details map with field errors")
	}
}

func TestHandleCreatePricingPlan_Success(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	body := []byte(`{"name":"Custom","provider":"other","egress_free_gb":50,
		"egress_tiers":[{"up_to_gb":0,"price_per_gb":0.1}],"pool_free_tier":false}`)
	rec := doRequest(t, srv.Handler(), "POST", "/api/v1/billing/plans", "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	created := decodeJSON[models.PricingPlan](t, rec.Body)
	if created.ID == 0 || created.Builtin {
		t.Errorf("created = %+v, want non-zero ID and Builtin=false", created)
	}

	// Audit entry recorded for the create.
	entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != AuditActionPlanCreate {
		t.Errorf("audit entries = %+v, want one plan.create entry", entries)
	}
	if entries[0].BeforeJSON != "" {
		t.Errorf("BeforeJSON = %q, want empty for a create", entries[0].BeforeJSON)
	}
	if entries[0].AfterJSON == "" {
		t.Error("AfterJSON must not be empty for a create")
	}
}

func TestHandleUpdatePricingPlan_BuiltinRejected(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := seedBuiltinPlan(t, store, "Builtin", "aws", 100, true)
	srv := newTestServer(t, testOptions(), store)

	body := []byte(`{"name":"Changed","egress_tiers":[{"up_to_gb":0,"price_per_gb":0.1}]}`)
	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/billing/plans/"+strconv.FormatInt(id, 10), "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	apiErr := decodeJSON[models.APIError](t, rec.Body)
	if apiErr.Code != "builtin_immutable" {
		t.Errorf("Code = %q, want builtin_immutable", apiErr.Code)
	}
}

func TestHandleUpdatePricingPlan_Success(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := seedBuiltinPlan(t, store, "Custom", "other", 10, false)
	srv := newTestServer(t, testOptions(), store)

	body := []byte(`{"name":"Renamed","provider":"other","egress_free_gb":20,
		"egress_tiers":[{"up_to_gb":0,"price_per_gb":0.2}]}`)
	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/billing/plans/"+strconv.FormatInt(id, 10), "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	updated := decodeJSON[models.PricingPlan](t, rec.Body)
	if updated.Name != "Renamed" || updated.EgressFreeGB != 20 {
		t.Errorf("updated = %+v, want Name=Renamed EgressFreeGB=20", updated)
	}

	entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != AuditActionPlanUpdate {
		t.Fatalf("audit entries = %+v, want one plan.update entry", entries)
	}
	if entries[0].BeforeJSON == "" || entries[0].AfterJSON == "" {
		t.Error("both BeforeJSON and AfterJSON must be populated for an update")
	}
}

func TestHandleUpdatePricingPlan_NoOpSaveSkipsAudit(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := seedBuiltinPlan(t, store, "Custom", "other", 10, false)
	srv := newTestServer(t, testOptions(), store)

	// Re-save with the exact same values.
	body := []byte(`{"name":"Custom","provider":"other","egress_free_gb":10,
		"egress_tiers":[{"up_to_gb":0,"price_per_gb":0}]}`)
	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/billing/plans/"+strconv.FormatInt(id, 10), "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("audit entries = %+v, want none for a no-op save", entries)
	}
}

func TestHandleDeletePricingPlan_BuiltinRejected(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := seedBuiltinPlan(t, store, "Builtin", "aws", 100, true)
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "DELETE", "/api/v1/billing/plans/"+strconv.FormatInt(id, 10), "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeletePricingPlan_Success(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := seedBuiltinPlan(t, store, "Custom", "other", 10, false)
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "DELETE", "/api/v1/billing/plans/"+strconv.FormatInt(id, 10), "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != AuditActionPlanDelete {
		t.Fatalf("audit entries = %+v, want one plan.delete entry", entries)
	}
	if entries[0].AfterJSON != "" {
		t.Errorf("AfterJSON = %q, want empty for a delete", entries[0].AfterJSON)
	}
}

func TestHandleSetHostPricing(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := seedBuiltinPlan(t, store, "Custom", "other", 10, false)
	seedHost(t, store, "host1", models.ProviderOther)
	srv := newTestServer(t, testOptions(), store)

	body := []byte(`{"plan_id":` + strconv.FormatInt(id, 10) + `}`)
	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/hosts/host1/pricing", "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.HostPricing](t, rec.Body)
	if got.PlanID != id {
		t.Errorf("PlanID = %d, want %d", got.PlanID, id)
	}

	entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != AuditActionHostPricingChange {
		t.Fatalf("audit entries = %+v, want one host_pricing.change entry", entries)
	}
}

func TestHandleSetHostPricing_UnknownHost(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/hosts/missing/pricing", "127.0.0.1:1", testUIToken, []byte(`{"plan_id":0}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSetHostPricing_UnknownPlan(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedHost(t, store, "host1", models.ProviderOther)
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/hosts/host1/pricing", "127.0.0.1:1", testUIToken, []byte(`{"plan_id":9999}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetNetworkBilling_DefaultsToProviderPlan(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	seedPricedBuiltinPlan(t, store, "AWS Free Tier", "aws", 100, 0.09)
	seedHostWithEgress(t, store, "host1", models.ProviderAWS, 150*1_000_000_000)
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/billing/network", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSON[struct {
		Month string                    `json:"month"`
		Hosts []networkBillingHostEntry `json:"hosts"`
	}](t, rec.Body)
	if len(body.Hosts) != 1 {
		t.Fatalf("hosts = %+v, want 1 entry", body.Hosts)
	}
	if body.Hosts[0].PlanName != "AWS Free Tier" {
		t.Errorf("PlanName = %q, want default AWS plan assigned", body.Hosts[0].PlanName)
	}
	if body.Hosts[0].Cost.MTD <= 0 {
		t.Errorf("Cost.MTD = %v, want > 0 for 150 GB usage over a 100 GB free tier", body.Hosts[0].Cost.MTD)
	}
}

func TestHandleGetNetworkBilling_InvalidMonth(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/billing/network?month=not-a-month", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDisplayCurrency_GetDefault(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	rec := doRequest(t, srv.Handler(), "GET", "/api/v1/settings/billing/currency", "127.0.0.1:1", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.DisplayCurrencySettings](t, rec.Body)
	if got.Currency != models.DisplayCurrencyUSD {
		t.Errorf("Currency = %q, want USD default", got.Currency)
	}
}

func TestHandleSetDisplayCurrency_KRWRequiresRate(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, testOptions(), newFakeStore())

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/billing/currency", "127.0.0.1:1", testUIToken, []byte(`{"currency":"KRW"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	apiErr := decodeJSON[models.APIError](t, rec.Body)
	if apiErr.Code != "rate_required" {
		t.Errorf("Code = %q, want rate_required", apiErr.Code)
	}
}

func TestHandleSetDisplayCurrency_KRWWithRate(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/billing/currency", "127.0.0.1:1", testUIToken,
		[]byte(`{"currency":"KRW","krw_per_usd":1385.5}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.DisplayCurrencySettings](t, rec.Body)
	if got.Currency != models.DisplayCurrencyKRW || got.KRWPerUSD != 1385.5 {
		t.Errorf("got = %+v, want KRW @ 1385.5", got)
	}
	if got.RateUpdatedAt == 0 {
		t.Error("RateUpdatedAt must be set when the rate changes")
	}

	entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Action != AuditActionDisplayCurrencyChange {
		t.Fatalf("audit entries = %+v, want one billing_settings.display_currency_change entry", entries)
	}
}

func TestHandleSetDisplayCurrency_ResaveSameRateSkipsAudit(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	srv := newTestServer(t, testOptions(), store)

	body := []byte(`{"currency":"KRW","krw_per_usd":1385.5}`)
	rec := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/billing/currency", "127.0.0.1:1", testUIToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("first save status = %d, want 200", rec.Code)
	}
	rec2 := doRequest(t, srv.Handler(), "PUT", "/api/v1/settings/billing/currency", "127.0.0.1:1", testUIToken, body)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second save status = %d, want 200", rec2.Code)
	}

	entries, err := store.ListAuditEntries(t.Context(), "", 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("audit entries = %+v, want exactly 1 (second identical save is a no-op)", entries)
	}
}

// --- test helpers ---

// seedBuiltinPlan creates a minimal pricing plan directly in store and
// returns its assigned ID.
func seedBuiltinPlan(t *testing.T, store Store, name, provider string, freeGB float64, builtin bool) int64 {
	t.Helper()
	created, err := store.CreatePricingPlan(t.Context(), models.PricingPlan{
		Name:         name,
		Provider:     provider,
		Currency:     "USD",
		EgressFreeGB: freeGB,
		EgressTiers:  []models.PricingTier{{UpToGB: 0, PricePerGB: 0}},
	})
	if err != nil {
		t.Fatalf("seed pricing plan: %v", err)
	}
	if builtin {
		// The fake store's CreatePricingPlan always stores Builtin=false
		// (matching the real storage.DB contract); tests that need a
		// builtin row poke the map directly to simulate a
		// migration-seeded plan (CreatePricingPlan/UpdatePricingPlan
		// deliberately can't produce Builtin=true themselves).
		if fs, ok := store.(*fakeStore); ok {
			fs.mu.Lock()
			p := fs.pricingPlans[created.ID]
			p.Builtin = true
			fs.pricingPlans[created.ID] = p
			fs.mu.Unlock()
		}
	}
	return created.ID
}

// seedPricedBuiltinPlan creates a builtin plan with a single unbounded
// tier at pricePerGB (used by tests that need a real, non-zero cost to
// assert against).
func seedPricedBuiltinPlan(t *testing.T, store Store, name, provider string, freeGB, pricePerGB float64) int64 {
	t.Helper()
	created, err := store.CreatePricingPlan(t.Context(), models.PricingPlan{
		Name:         name,
		Provider:     provider,
		Currency:     "USD",
		EgressFreeGB: freeGB,
		EgressTiers:  []models.PricingTier{{UpToGB: 0, PricePerGB: pricePerGB}},
	})
	if err != nil {
		t.Fatalf("seed priced pricing plan: %v", err)
	}
	if fs, ok := store.(*fakeStore); ok {
		fs.mu.Lock()
		p := fs.pricingPlans[created.ID]
		p.Builtin = true
		fs.pricingPlans[created.ID] = p
		fs.mu.Unlock()
	}
	return created.ID
}

// seedHost upserts a minimal host record.
func seedHost(t *testing.T, store Store, id string, provider models.Provider) {
	t.Helper()
	if err := store.UpsertHost(t.Context(), models.HostInfo{ID: id, Hostname: id, Provider: provider}, 0); err != nil {
		t.Fatalf("seed host: %v", err)
	}
}

// seedHostWithEgress upserts a host and its current-month tx egress
// total.
func seedHostWithEgress(t *testing.T, store Store, id string, provider models.Provider, txBytes uint64) {
	t.Helper()
	seedHost(t, store, id, provider)
	if fs, ok := store.(*fakeStore); ok {
		fs.mu.Lock()
		month := models.MonthOf(time.Now())
		fs.egress[egressKey(id, month)] = models.EgressRecord{HostID: id, Month: month, TxBytes: txBytes}
		fs.mu.Unlock()
	}
}
