package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/billing"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// maxPricingBodyBytes bounds the size of pricing-plan/host-pricing/
// display-currency request bodies (all small JSON payloads).
const maxPricingBodyBytes = 16 << 10 // 16 KiB

// SettingDisplayCurrency, SettingKRWPerUSD, and SettingRateUpdatedAt are
// the settings table keys backing DisplayCurrencySettings (SPEC-v0.6
// §3): "billing_display_currency", "billing_krw_per_usd",
// "billing_rate_updated_at".
const (
	SettingDisplayCurrency = "billing_display_currency"
	SettingKRWPerUSD       = "billing_krw_per_usd"
	SettingRateUpdatedAt   = "billing_rate_updated_at"
)

// registerPricingRoutes registers the network cost pricing-plan API
// (SPEC-v0.6 §3): plan CRUD, per-host plan assignment, the network-only
// billing view, and display-currency settings.
func (s *Server) registerPricingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/billing/plans", s.requireUser(s.handleListPricingPlans))
	mux.HandleFunc("POST /api/v1/billing/plans", s.requireAdmin(s.handleCreatePricingPlan))
	mux.HandleFunc("PUT /api/v1/billing/plans/{id}", s.requireAdmin(s.handleUpdatePricingPlan))
	mux.HandleFunc("DELETE /api/v1/billing/plans/{id}", s.requireAdmin(s.handleDeletePricingPlan))
	mux.HandleFunc("PUT /api/v1/hosts/{id}/pricing", s.requireAdmin(s.handleSetHostPricing))
	mux.HandleFunc("GET /api/v1/billing/network", s.requireUser(s.handleGetNetworkBilling))

	mux.HandleFunc("GET /api/v1/settings/billing/currency", s.requireAdmin(s.handleGetDisplayCurrency))
	mux.HandleFunc("PUT /api/v1/settings/billing/currency", s.requireAdmin(s.handleSetDisplayCurrency))
}

// parsePricingID parses a path value into a pricing plan ID.
func parsePricingID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id %q: %w", raw, err)
	}
	return id, nil
}

// --- Pricing plan CRUD ---

// handleListPricingPlans responds with every configured pricing plan:
// GET /api/v1/billing/plans.
func (s *Server) handleListPricingPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := s.store.ListPricingPlans(r.Context())
	if err != nil {
		s.logger.Error("list pricing plans failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, plans)
}

// pricingPlanRequest is the body of POST /api/v1/billing/plans and PUT
// /api/v1/billing/plans/{id}.
type pricingPlanRequest struct {
	Name              string               `json:"name"`
	Provider          string               `json:"provider"`
	EgressFreeGB      float64              `json:"egress_free_gb"`
	EgressTiers       []models.PricingTier `json:"egress_tiers"`
	IngressPricePerGB float64              `json:"ingress_price_per_gb"`
	PoolFreeTier      bool                 `json:"pool_free_tier"`
}

// toPlan converts req into a models.PricingPlan for validation/storage;
// id/builtin/timestamps are the caller's responsibility.
func (req pricingPlanRequest) toPlan() models.PricingPlan {
	return models.PricingPlan{
		Name:              req.Name,
		Provider:          req.Provider,
		Currency:          "USD",
		EgressFreeGB:      req.EgressFreeGB,
		EgressTiers:       req.EgressTiers,
		IngressPricePerGB: req.IngressPricePerGB,
		PoolFreeTier:      req.PoolFreeTier,
	}
}

// handleCreatePricingPlan creates a new (always non-builtin) pricing
// plan: POST /api/v1/billing/plans (admin).
func (s *Server) handleCreatePricingPlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxPricingBodyBytes)
	var req pricingPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	plan := req.toPlan()
	if details := billing.ValidatePlan(plan); len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "validation failed", Details: details})
		return
	}

	created, err := s.store.CreatePricingPlan(ctx, plan)
	if err != nil {
		s.logger.Error("create pricing plan failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.recordAudit(ctx, r, AuditActionPlanCreate, "pricing_plan", strconv.FormatInt(created.ID, 10), nil, created)
	writeJSON(w, http.StatusOK, created)
}

// handleUpdatePricingPlan updates an existing, non-builtin pricing
// plan: PUT /api/v1/billing/plans/{id} (admin). A builtin plan is
// rejected with 403 "builtin_immutable" per SPEC-v0.6 §3 ("내장본은
// 수정·삭제 불가").
func (s *Server) handleUpdatePricingPlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parsePricingID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}

	existing, err := s.store.GetPricingPlan(ctx, id)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "pricing plan not found"})
			return
		}
		s.logger.Error("get pricing plan failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	if billing.IsBuiltinImmutable(existing) {
		writeJSON(w, http.StatusForbidden, models.APIError{Error: "builtin pricing plans cannot be modified", Code: "builtin_immutable"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPricingBodyBytes)
	var req pricingPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	plan := req.toPlan()
	if details := billing.ValidatePlan(plan); len(details) > 0 {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "validation failed", Details: details})
		return
	}
	plan.ID = id
	plan.Builtin = false

	updated, err := s.store.UpdatePricingPlan(ctx, plan)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "pricing plan not found"})
			return
		}
		s.logger.Error("update pricing plan failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.recordAudit(ctx, r, AuditActionPlanUpdate, "pricing_plan", strconv.FormatInt(id, 10), existing, updated)
	writeJSON(w, http.StatusOK, updated)
}

// handleDeletePricingPlan deletes a non-builtin pricing plan: DELETE
// /api/v1/billing/plans/{id} (admin).
func (s *Server) handleDeletePricingPlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := parsePricingID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid id"})
		return
	}

	existing, err := s.store.GetPricingPlan(ctx, id)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "pricing plan not found"})
			return
		}
		s.logger.Error("get pricing plan failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	if billing.IsBuiltinImmutable(existing) {
		writeJSON(w, http.StatusForbidden, models.APIError{Error: "builtin pricing plans cannot be deleted", Code: "builtin_immutable"})
		return
	}

	if err := s.store.DeletePricingPlan(ctx, id); err != nil {
		s.logger.Error("delete pricing plan failed", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.recordAudit(ctx, r, AuditActionPlanDelete, "pricing_plan", strconv.FormatInt(id, 10), existing, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// --- Host pricing assignment ---

// hostPricingRequest is the body of PUT /api/v1/hosts/{id}/pricing.
type hostPricingRequest struct {
	PlanID int64 `json:"plan_id"`
}

// handleSetHostPricing assigns hostID to a pricing plan: PUT
// /api/v1/hosts/{id}/pricing (admin). plan_id 0 clears the assignment,
// reverting to the provider-based default (see
// resolveHostPricingPlan).
func (s *Server) handleSetHostPricing(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hostID := r.PathValue("id")

	if _, err := s.store.GetHost(ctx, hostID); err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "host not found"})
			return
		}
		s.logger.Error("get host failed", "host_id", hostID, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPricingBodyBytes)
	var req hostPricingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	if req.PlanID != 0 {
		if _, err := s.store.GetPricingPlan(ctx, req.PlanID); err != nil {
			if isNotFound(err) {
				writeJSON(w, http.StatusBadRequest, models.APIError{Error: "plan_id does not exist"})
				return
			}
			s.logger.Error("get pricing plan failed", "id", req.PlanID, "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
	}

	before, err := s.store.GetHostPricing(ctx, hostID)
	if err != nil {
		s.logger.Error("get host pricing failed", "host_id", hostID, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	after := models.HostPricing{HostID: hostID, PlanID: req.PlanID}
	if err := s.store.SetHostPricing(ctx, after); err != nil {
		s.logger.Error("set host pricing failed", "host_id", hostID, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	s.recordAudit(ctx, r, AuditActionHostPricingChange, "host_pricing", hostID, before, after)
	writeJSON(w, http.StatusOK, after)
}

// --- Network billing view ---

// resolveHostPricingPlan returns the pricing plan hostID should use:
// its explicit HostPricing assignment if one exists and still resolves
// to a real plan, otherwise the provider-based default (see
// billing.DefaultPlanProvider) matched against plansByProvider. Returns
// ok=false if no plan (explicit or default) can be resolved — the host
// gets zero network cost in that case.
func resolveHostPricingPlan(info models.HostInfo, assigned models.HostPricing, plansByID map[int64]models.PricingPlan, plansByProvider map[string]models.PricingPlan) (models.PricingPlan, bool) {
	if assigned.PlanID != 0 {
		if plan, ok := plansByID[assigned.PlanID]; ok {
			return plan, true
		}
	}
	defaultProvider := billing.DefaultPlanProvider(info.Provider, "")
	if plan, ok := plansByProvider[defaultProvider]; ok {
		return plan, true
	}
	return models.PricingPlan{}, false
}

// networkBillingHostEntry is one host's entry in the
// GET /api/v1/billing/network response.
type networkBillingHostEntry struct {
	HostID   string               `json:"host_id"`
	Hostname string               `json:"hostname"`
	PlanID   int64                `json:"plan_id,omitempty"`
	PlanName string               `json:"plan_name,omitempty"`
	Cost     models.EstimatedCost `json:"cost"`
}

// handleGetNetworkBilling responds with every host's estimated network
// egress cost for a month: GET /api/v1/billing/network?month=YYYY-MM
// (default current month). Hosts sharing a PoolFreeTier plan have
// their free allowance pooled and the resulting cost allocated
// proportionally (see billing.EstimateNetworkCost).
func (s *Server) handleGetNetworkBilling(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.opts.now()

	month := r.URL.Query().Get("month")
	if month == "" {
		month = models.MonthOf(now)
	}
	if _, _, err := models.MonthBounds(month); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid month"})
		return
	}

	hosts, costByHost, hostPlan, err := s.computeNetworkCostByHost(ctx, month, now)
	if err != nil {
		s.logger.Error("compute network cost by host failed", "month", month, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	entries := make([]networkBillingHostEntry, 0, len(hosts))
	for _, h := range hosts {
		entry := networkBillingHostEntry{
			HostID:   h.Info.ID,
			Hostname: h.Info.Hostname,
			Cost:     costByHost[h.Info.ID],
		}
		if plan, ok := hostPlan[h.Info.ID]; ok {
			entry.PlanID = plan.ID
			entry.PlanName = plan.Name
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Hostname < entries[j].Hostname })

	writeJSON(w, http.StatusOK, map[string]any{
		"month": month,
		"hosts": entries,
	})
}

// computeNetworkCostByHost computes every host's estimated network
// egress cost for month (grouping pooled-free-tier plans across all
// their assigned hosts, see billing.EstimateNetworkCost), returning the
// host list itself (so callers don't need a second ListHosts call),
// the per-host cost map, and the per-host resolved pricing plan map.
// Shared by handleGetNetworkBilling and networkEstimatorFor's per-host
// billing.NetworkEstimator closure so both the dedicated
// network-billing endpoint and the combined GET /api/v1/billing view
// (internal/billing.BuildBillingView) reflect the same pricing-plan
// assignments.
func (s *Server) computeNetworkCostByHost(ctx context.Context, month string, now time.Time) ([]models.HostRecord, map[string]models.EstimatedCost, map[string]models.PricingPlan, error) {
	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hub: list hosts: %w", err)
	}
	records, err := s.store.ListEgress(ctx, month)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hub: list egress for %s: %w", month, err)
	}
	plans, err := s.store.ListPricingPlans(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hub: list pricing plans: %w", err)
	}
	assignments, err := s.store.ListHostPricing(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hub: list host pricing: %w", err)
	}

	egressByHost := make(map[string]models.EgressRecord, len(records))
	for _, rec := range records {
		egressByHost[rec.HostID] = rec
	}
	plansByID := make(map[int64]models.PricingPlan, len(plans))
	plansByProvider := make(map[string]models.PricingPlan, len(plans))
	for _, p := range plans {
		plansByID[p.ID] = p
		// First plan wins per provider string (builtin plans are seeded
		// once per provider string, so this only matters if a hub admin
		// somehow ends up with two plans sharing a provider string,
		// which ValidatePlan does not forbid — the default mapping is a
		// convenience, not a uniqueness guarantee).
		if _, exists := plansByProvider[p.Provider]; !exists {
			plansByProvider[p.Provider] = p
		}
	}
	assignmentByHost := make(map[string]models.HostPricing, len(assignments))
	for _, a := range assignments {
		assignmentByHost[a.HostID] = a
	}

	// Group hosts by resolved plan ID so pooled plans share their free
	// tier across every host assigned to (explicitly or by default) the
	// same plan.
	usagesByPlan := make(map[int64][]billing.HostUsage)
	planByPlanID := make(map[int64]models.PricingPlan)
	hostPlan := make(map[string]models.PricingPlan, len(hosts))

	for _, h := range hosts {
		assigned := assignmentByHost[h.Info.ID]
		plan, ok := resolveHostPricingPlan(h.Info, assigned, plansByID, plansByProvider)
		if !ok {
			continue
		}
		hostPlan[h.Info.ID] = plan
		planByPlanID[plan.ID] = plan

		rec := egressByHost[h.Info.ID]
		projectedTx, projectedRx := projectedBytesFor(month, now, rec.TxBytes, rec.RxBytes)
		usagesByPlan[plan.ID] = append(usagesByPlan[plan.ID], billing.HostUsage{
			HostID:           h.Info.ID,
			TxBytes:          rec.TxBytes,
			RxBytes:          rec.RxBytes,
			ProjectedTxBytes: projectedTx,
			ProjectedRxBytes: projectedRx,
		})
	}

	costByHost := make(map[string]models.EstimatedCost, len(hosts))
	for planID, usages := range usagesByPlan {
		plan := planByPlanID[planID]
		for hostID, cost := range billing.EstimateNetworkCost(plan, usages) {
			costByHost[hostID] = cost
		}
	}

	return hosts, costByHost, hostPlan, nil
}

// networkEstimatorFor returns a billing.NetworkEstimator closure over
// s, used to wire the real per-host network-cost calculation into
// internal/billing.BuildBillingView's combined GET /api/v1/billing
// response (see cmd/hub/main.go, which assigns this into
// hub.BillingRuntime.NetworkEstimator once s exists). Recomputes every
// host's cost map once, up front, and returns a closure over that
// single snapshot — BuildBillingView calls the returned
// billing.NetworkEstimator once per host, so computing the shared
// per-plan map on every individual call would be quadratic in host
// count; this amortizes it to once per GET /api/v1/billing request,
// the same cost handleGetNetworkBilling already pays per its own
// request.
//
// A lookup failure (store error) returns billing.ZeroNetworkEstimator
// rather than propagating an error: per billing.NetworkEstimator's own
// doc comment, a network cost estimate is always best-effort and must
// never fail the whole Costs page.
func (s *Server) networkEstimatorFor(ctx context.Context) billing.NetworkEstimator {
	now := s.opts.now()
	month := models.MonthOf(now)
	_, costByHost, _, err := s.computeNetworkCostByHost(ctx, month, now)
	if err != nil {
		s.logger.Error("network estimator: compute network cost by host failed", "month", month, "error", err)
		return billing.ZeroNetworkEstimator
	}
	return func(hostID string) models.EstimatedCost {
		return costByHost[hostID]
	}
}

// NetworkEstimatorFor is the exported wrapper cmd/hub/main.go uses to
// wire s's real network-cost estimator into hub.BillingRuntime.
// NetworkEstimator once s has been constructed (see New's own doc
// comment on why this can't be passed in at hub.Options
// construction time — s doesn't exist yet then).
func (s *Server) NetworkEstimatorFor(ctx context.Context) billing.NetworkEstimator {
	return s.networkEstimatorFor(ctx)
}

// hostCostInputs computes, once per request, the two inputs
// attachHostCost needs to populate a models.HostSummary.Cost field:
// every provider's latest snapshot (indexed for billing.BuildHostCost)
// and the same real network-cost estimator networkEstimatorFor builds
// for the combined GET /api/v1/billing view. Returns a nil map and
// billing.ZeroNetworkEstimator when billing is disabled hub-wide
// (s.opts.Billing == nil) or the snapshot list fails to load — either
// way, attachHostCost's own nil-map check then leaves
// HostSummary.Cost unset, matching HostCost's "billing disabled"
// omitempty contract (see internal/models/host.go's doc comment on
// HostSummary.Cost).
func (s *Server) hostCostInputs(ctx context.Context) (map[models.CloudBillingProvider]models.CloudCostSnapshot, billing.NetworkEstimator) {
	if s.opts.Billing == nil {
		return nil, billing.ZeroNetworkEstimator
	}
	snapshots, err := s.store.ListCloudCostSnapshots(ctx)
	if err != nil {
		s.logger.Error("host cost inputs: list cloud cost snapshots failed", "error", err)
		return nil, billing.ZeroNetworkEstimator
	}
	return billing.SnapshotsByProvider(snapshots), s.networkEstimatorFor(ctx)
}

// attachHostCost sets summary.Cost from billing.BuildHostCost when
// billing is enabled (costByProvider is non-nil), mirroring the same
// per-host cost calculation the combined GET /api/v1/billing view
// uses (internal/billing.BuildBillingView) so a host's Cost figure is
// identical whether read from GET /api/v1/hosts, GET
// /api/v1/hosts/{id}, or the Costs page. Leaves summary.Cost unset
// (nil) when billing is disabled, matching its omitempty contract.
func (s *Server) attachHostCost(summary *models.HostSummary, costByProvider map[models.CloudBillingProvider]models.CloudCostSnapshot, estimateNetwork billing.NetworkEstimator) {
	if costByProvider == nil {
		return
	}
	cost := billing.BuildHostCost(summary.Host, costByProvider, estimateNetwork)
	summary.Cost = &cost
}

// --- Display currency settings ---

// loadDisplayCurrencySettings reads the three billing_* settings keys
// into a models.DisplayCurrencySettings, defaulting to USD (no rate)
// when none have ever been set.
func (s *Server) loadDisplayCurrencySettings(ctx context.Context) (models.DisplayCurrencySettings, error) {
	out := models.DisplayCurrencySettings{Currency: models.DisplayCurrencyUSD}

	currency, ok, err := s.store.GetSetting(ctx, SettingDisplayCurrency)
	if err != nil {
		return models.DisplayCurrencySettings{}, fmt.Errorf("hub: get display currency setting: %w", err)
	}
	if ok && models.ValidDisplayCurrency(models.DisplayCurrency(currency)) {
		out.Currency = models.DisplayCurrency(currency)
	}

	rateStr, ok, err := s.store.GetSetting(ctx, SettingKRWPerUSD)
	if err != nil {
		return models.DisplayCurrencySettings{}, fmt.Errorf("hub: get krw_per_usd setting: %w", err)
	}
	if ok {
		if rate, err := strconv.ParseFloat(rateStr, 64); err == nil {
			out.KRWPerUSD = rate
		}
	}

	updatedStr, ok, err := s.store.GetSetting(ctx, SettingRateUpdatedAt)
	if err != nil {
		return models.DisplayCurrencySettings{}, fmt.Errorf("hub: get rate_updated_at setting: %w", err)
	}
	if ok {
		if updated, err := strconv.ParseInt(updatedStr, 10, 64); err == nil {
			out.RateUpdatedAt = updated
		}
	}

	return out, nil
}

// handleGetDisplayCurrency responds with the hub's current display
// currency settings: GET /api/v1/settings/billing/currency (admin).
func (s *Server) handleGetDisplayCurrency(w http.ResponseWriter, r *http.Request) {
	settings, err := s.loadDisplayCurrencySettings(r.Context())
	if err != nil {
		s.logger.Error("load display currency settings failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// displayCurrencyRequest is the body of PUT
// /api/v1/settings/billing/currency.
type displayCurrencyRequest struct {
	Currency  string  `json:"currency"`
	KRWPerUSD float64 `json:"krw_per_usd"`
}

// handleSetDisplayCurrency sets the hub's display currency and (for
// KRW) its manually entered exchange rate: PUT
// /api/v1/settings/billing/currency (admin). Selecting KRW without a
// positive krw_per_usd is rejected with 400 "rate_required" per
// SPEC-v0.6 §3. RateUpdatedAt is refreshed to now only when the rate
// value itself actually changes (so re-saving the same rate doesn't
// spuriously bump its recorded entry time, and — per the "no entry on
// no-op saves" rule — doesn't create an audit entry either).
func (s *Server) handleSetDisplayCurrency(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, maxPricingBodyBytes)
	var req displayCurrencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: "invalid request body"})
		return
	}

	if err := billing.ValidateDisplayCurrencySettings(req.Currency, req.KRWPerUSD); err != nil {
		if err == billing.ErrRateRequired {
			writeJSON(w, http.StatusBadRequest, models.APIError{Error: err.Error(), Code: "rate_required"})
			return
		}
		writeJSON(w, http.StatusBadRequest, models.APIError{Error: err.Error()})
		return
	}

	before, err := s.loadDisplayCurrencySettings(ctx)
	if err != nil {
		s.logger.Error("load display currency settings failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	after := models.DisplayCurrencySettings{
		Currency:      models.DisplayCurrency(req.Currency),
		KRWPerUSD:     before.KRWPerUSD,
		RateUpdatedAt: before.RateUpdatedAt,
	}
	if req.Currency == string(models.DisplayCurrencyKRW) && req.KRWPerUSD != before.KRWPerUSD {
		after.KRWPerUSD = req.KRWPerUSD
		after.RateUpdatedAt = s.opts.now().Unix()
	}

	if err := s.store.SetSetting(ctx, SettingDisplayCurrency, string(after.Currency)); err != nil {
		s.logger.Error("set display currency setting failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}
	if after.KRWPerUSD != 0 {
		if err := s.store.SetSetting(ctx, SettingKRWPerUSD, strconv.FormatFloat(after.KRWPerUSD, 'f', -1, 64)); err != nil {
			s.logger.Error("set krw_per_usd setting failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
		if err := s.store.SetSetting(ctx, SettingRateUpdatedAt, strconv.FormatInt(after.RateUpdatedAt, 10)); err != nil {
			s.logger.Error("set rate_updated_at setting failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
			return
		}
	}

	s.recordAudit(ctx, r, AuditActionDisplayCurrencyChange, "billing_settings", "", before, after)
	writeJSON(w, http.StatusOK, after)
}

// projectedBytesFor linearly projects tx and rx bytes for month to
// month-end as of now, reusing models.ComputeEgress's own projection
// logic (limits are irrelevant to a bare projection, so 0/0 unlimited
// limits are passed and only the Projected* fields of the result are
// used).
func projectedBytesFor(month string, now time.Time, tx, rx uint64) (projectedTx, projectedRx uint64) {
	usage := models.ComputeEgress(month, tx, rx, 0, 0, now)
	return usage.ProjectedTxBytes, usage.ProjectedRxBytes
}
