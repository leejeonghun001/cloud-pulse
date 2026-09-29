package hub

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// TestEffectiveLimits_HubOverrideAppliedInListHosts verifies that a
// hub-stored limit override changes the effective egress/ingress limits
// (and their sources) reported by GET /api/v1/hosts, without the
// handler making a ListHostLimits call per host.
func TestEffectiveLimits_HubOverrideAppliedInListHosts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	host := sampleHostInfo("host-lim")
	host.EgressLimitBytes = 1000
	if err := store.UpsertHost(t.Context(), host, now.Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	override := uint64(5000)
	ingress := uint64(2000)
	if err := store.SetHostLimits(t.Context(), models.HostLimits{
		HostID: "host-lim", EgressLimitBytes: &override, IngressLimitBytes: &ingress,
	}); err != nil {
		t.Fatalf("SetHostLimits: %v", err)
	}

	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Hosts []models.HostSummary `json:"hosts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Hosts) != 1 {
		t.Fatalf("hosts = %+v, want 1", got.Hosts)
	}
	egress := got.Hosts[0].Egress
	if egress.LimitBytes != override || egress.LimitSource != "hub" {
		t.Errorf("tx limit = %d/%s, want %d/hub", egress.LimitBytes, egress.LimitSource, override)
	}
	if egress.RxLimitBytes != ingress || egress.RxLimitSource != "hub" {
		t.Errorf("rx limit = %d/%s, want %d/hub", egress.RxLimitBytes, egress.RxLimitSource, ingress)
	}
}

// TestEffectiveLimits_HubOverrideAppliedInGetHost verifies the same
// override precedence on the single-host detail endpoint.
func TestEffectiveLimits_HubOverrideAppliedInGetHost(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	host := sampleHostInfo("host-lim2")
	host.EgressLimitBytes = 1000
	if err := store.UpsertHost(t.Context(), host, now.Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	override := uint64(9999)
	if err := store.SetHostLimits(t.Context(), models.HostLimits{HostID: "host-lim2", EgressLimitBytes: &override}); err != nil {
		t.Fatalf("SetHostLimits: %v", err)
	}

	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts/host-lim2", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decodeJSON[models.HostSummary](t, rec.Body)
	if got.Egress.LimitBytes != override || got.Egress.LimitSource != "hub" {
		t.Errorf("tx limit = %d/%s, want %d/hub", got.Egress.LimitBytes, got.Egress.LimitSource, override)
	}
	if got.Egress.RxLimitSource != "none" || got.Egress.RxLimitBytes != 0 {
		t.Errorf("rx limit = %d/%s, want 0/none (no ingress override)", got.Egress.RxLimitBytes, got.Egress.RxLimitSource)
	}
}

// TestEffectiveLimits_HubOverrideAppliedInEgress verifies the same
// override precedence on GET /api/v1/egress.
func TestEffectiveLimits_HubOverrideAppliedInEgress(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	host := sampleHostInfo("host-lim3")
	host.EgressLimitBytes = 1000
	if err := store.UpsertHost(t.Context(), host, now.Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	store.setEgress("host-lim3", "2026-02", 500, 100)
	ingress := uint64(300)
	if err := store.SetHostLimits(t.Context(), models.HostLimits{HostID: "host-lim3", IngressLimitBytes: &ingress}); err != nil {
		t.Fatalf("SetHostLimits: %v", err)
	}

	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/egress", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Hosts []egressHostEntry `json:"hosts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Hosts) != 1 {
		t.Fatalf("hosts = %+v, want 1", got.Hosts)
	}
	e := got.Hosts[0].Egress
	if e.LimitBytes != 1000 || e.LimitSource != "agent" {
		t.Errorf("tx limit = %d/%s, want 1000/agent (no override)", e.LimitBytes, e.LimitSource)
	}
	if e.RxLimitBytes != ingress || e.RxLimitSource != "hub" {
		t.Errorf("rx limit = %d/%s, want %d/hub", e.RxLimitBytes, e.RxLimitSource, ingress)
	}
}

// --- Two-direction alerts fire independently ---

func TestAlerts_InboundFiresIndependentlyOfOutbound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	notifier := &fakeNotifier{}
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := New(opts, store, nil, notifier, nil, testLogger())

	const hostID = "host-inbound"
	ingress := uint64(1000)
	if err := store.SetHostLimits(t.Context(), models.HostLimits{HostID: hostID, IngressLimitBytes: &ingress}); err != nil {
		t.Fatalf("SetHostLimits: %v", err)
	}

	// tx well under any limit (agent limit 0 = unlimited outbound), rx at 85% of the 1000-byte inbound limit.
	host := sampleHostInfo(hostID)
	host.EgressLimitBytes = 0
	report := models.AgentReport{Host: host, Samples: []models.Sample{{Timestamp: now.Unix(), NetTxBytes: 10, NetRxBytes: 850}}}
	body, _ := json.Marshal(report)
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	s.Wait()

	if got := notifier.callCount(); got != 1 {
		t.Fatalf("notify calls = %d, want 1 (inbound warning only, outbound unlimited)", got)
	}

	month := models.MonthOf(now)
	firstOut, err := store.MarkAlertSent(t.Context(), hostID, month, models.DirectionOut, models.EgressWarning)
	if err != nil {
		t.Fatalf("MarkAlertSent(out): %v", err)
	}
	if !firstOut {
		t.Error("expected outbound warning to NOT have been marked sent (outbound is unlimited)")
	}
	firstIn, err := store.MarkAlertSent(t.Context(), hostID, month, models.DirectionIn, models.EgressWarning)
	if err != nil {
		t.Fatalf("MarkAlertSent(in): %v", err)
	}
	if firstIn {
		t.Error("expected inbound warning to already have been marked sent by afterIngest")
	}
}

func TestAlerts_BothDirectionsFireSeparately(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	notifier := &fakeNotifier{}
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := New(opts, store, nil, notifier, nil, testLogger())

	const hostID = "host-both"
	ingress := uint64(1000)
	if err := store.SetHostLimits(t.Context(), models.HostLimits{HostID: hostID, IngressLimitBytes: &ingress}); err != nil {
		t.Fatalf("SetHostLimits: %v", err)
	}

	host := sampleHostInfo(hostID)
	host.EgressLimitBytes = 1000
	// tx 85% (warning), rx 85% (warning): 2 independent notifications.
	report := models.AgentReport{Host: host, Samples: []models.Sample{{Timestamp: now.Unix(), NetTxBytes: 850, NetRxBytes: 850}}}
	body, _ := json.Marshal(report)
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	s.Wait()

	if got := notifier.callCount(); got != 2 {
		t.Fatalf("notify calls = %d, want 2 (one per direction)", got)
	}

	titles := make([]string, len(notifier.calls))
	for i, c := range notifier.calls {
		titles[i] = c.title
	}
	var sawOut, sawIn bool
	for _, title := range titles {
		if strings.Contains(title, "outbound") {
			sawOut = true
		}
		if strings.Contains(title, "inbound") {
			sawIn = true
		}
	}
	if !sawOut || !sawIn {
		t.Errorf("titles = %v, want one mentioning outbound and one mentioning inbound", titles)
	}
}
