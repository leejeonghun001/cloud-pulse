package hub

import (
	"encoding/json"
	"net/http"
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

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts", "203.0.113.1:1234", opts.UIToken, nil)
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

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/hosts/host-lim2", "203.0.113.1:1234", opts.UIToken, nil)
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

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/egress", "203.0.113.1:1234", opts.UIToken, nil)
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
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	store.setNotifyChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setAlertRule(models.AlertRule{
		ID: 1, Name: "egress_out 80", Enabled: true, Metric: models.AlertMetricEgressOutPct,
		Operator: models.AlertOperatorGTE, Threshold: 80, ChannelIDs: []int64{1},
	})
	store.setAlertRule(models.AlertRule{
		ID: 2, Name: "egress_in 80", Enabled: true, Metric: models.AlertMetricEgressInPct,
		Operator: models.AlertOperatorGTE, Threshold: 80, ChannelIDs: []int64{1},
	})

	opts := testOptions()
	opts.Now = fixedNow(now)
	rec := &alertMessageRecorder{}
	s, _ := newTestServerWithEngine(t, opts, store, rec.senderFactory())

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
	httpRec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if httpRec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200 (body=%s)", httpRec.Code, httpRec.Body.String())
	}
	s.Wait()

	if got := rec.callCount(); got != 1 {
		t.Fatalf("notify calls = %d, want 1 (inbound warning only, outbound unlimited)", got)
	}
	msgs := rec.messages()
	if msgs[0].message.Fields[1].Value != string(models.AlertMetricEgressInPct) {
		t.Errorf("fired rule metric = %q, want %q", msgs[0].message.Fields[1].Value, models.AlertMetricEgressInPct)
	}
}

func TestAlerts_BothDirectionsFireSeparately(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	store.setNotifyChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setAlertRule(models.AlertRule{
		ID: 1, Name: "egress_out 80", Enabled: true, Metric: models.AlertMetricEgressOutPct,
		Operator: models.AlertOperatorGTE, Threshold: 80, ChannelIDs: []int64{1},
	})
	store.setAlertRule(models.AlertRule{
		ID: 2, Name: "egress_in 80", Enabled: true, Metric: models.AlertMetricEgressInPct,
		Operator: models.AlertOperatorGTE, Threshold: 80, ChannelIDs: []int64{1},
	})

	opts := testOptions()
	opts.Now = fixedNow(now)
	rec := &alertMessageRecorder{}
	s, _ := newTestServerWithEngine(t, opts, store, rec.senderFactory())

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
	httpRec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if httpRec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200 (body=%s)", httpRec.Code, httpRec.Body.String())
	}
	s.Wait()

	if got := rec.callCount(); got != 2 {
		t.Fatalf("notify calls = %d, want 2 (one per direction)", got)
	}

	var sawOut, sawIn bool
	for _, m := range rec.messages() {
		for _, f := range m.message.Fields {
			if f.Value == string(models.AlertMetricEgressOutPct) {
				sawOut = true
			}
			if f.Value == string(models.AlertMetricEgressInPct) {
				sawIn = true
			}
		}
	}
	if !sawOut || !sawIn {
		t.Errorf("sawOut=%v sawIn=%v, want both true", sawOut, sawIn)
	}
}
