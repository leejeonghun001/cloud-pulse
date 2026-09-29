package hub

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// setupEgressRuleServer creates a Server wired to a real alerting
// engine with three egress_out_pct rules (80/95/100, matching the
// pre-v0.5.0 default seeding in migrations/0004_alerting.sql) attached
// to a single notify channel, for tests that exercise the engine-based
// replacement of the old direct-webhook egress alerting path.
func setupEgressRuleServer(t *testing.T, store *fakeStore, now time.Time) (*Server, *alertMessageRecorder) {
	t.Helper()
	rec := &alertMessageRecorder{}

	store.setNotifyChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	for i, threshold := range []float64{80, 95, 100} {
		store.setAlertRule(models.AlertRule{
			ID: int64(i + 1), Name: "egress", Enabled: true, Metric: models.AlertMetricEgressOutPct,
			Operator: models.AlertOperatorGTE, Threshold: threshold, ChannelIDs: []int64{1},
		})
	}

	opts := testOptions()
	opts.Now = fixedNow(now)
	s, _ := newTestServerWithEngine(t, opts, store, rec.senderFactory())
	return s, rec
}

func TestAlerts_EgressRules_FireOncePerLevel(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	s, rec := setupEgressRuleServer(t, store, now)

	const hostID = "host-alert"
	const limit = 1000 // bytes

	host := sampleHostInfo(hostID)
	host.EgressLimitBytes = limit
	report := models.AgentReport{Host: host, Samples: []models.Sample{{Timestamp: now.Unix(), NetTxBytes: 0}}}
	body, _ := json.Marshal(report)
	rec2 := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", s.opts.AgentToken, body)
	if rec2.Code != http.StatusOK {
		t.Fatalf("seed ingest status = %d", rec2.Code)
	}
	s.Wait()

	// Push tx to 85% (warning threshold, 80).
	ingestWithTS(t, s, hostID, now.Add(1*time.Minute).Unix(), 850, limit)
	s.Wait()
	if got := rec.callCount(); got != 1 {
		t.Fatalf("after warning: notify calls = %d, want 1", got)
	}

	// Same level again (still only the 80% rule satisfied) must not
	// re-notify (monthly dedupe per rule/threshold).
	ingestWithTS(t, s, hostID, now.Add(2*time.Minute).Unix(), 5, limit)
	s.Wait()
	if got := rec.callCount(); got != 1 {
		t.Fatalf("after repeat warning: notify calls = %d, want still 1", got)
	}

	// Push to 96% (also satisfies the 95% rule now).
	ingestWithTS(t, s, hostID, now.Add(3*time.Minute).Unix(), 100, limit)
	s.Wait()
	if got := rec.callCount(); got != 2 {
		t.Fatalf("after critical: notify calls = %d, want 2", got)
	}

	// Push to 106% (also satisfies the 100% rule now).
	ingestWithTS(t, s, hostID, now.Add(4*time.Minute).Unix(), 100, limit)
	s.Wait()
	if got := rec.callCount(); got != 3 {
		t.Fatalf("after exceeded: notify calls = %d, want 3", got)
	}

	// Repeat exceeded must not re-notify any of the three rules again.
	ingestWithTS(t, s, hostID, now.Add(5*time.Minute).Unix(), 50, limit)
	s.Wait()
	if got := rec.callCount(); got != 3 {
		t.Fatalf("after repeat exceeded: notify calls = %d, want still 3", got)
	}
}

// ingestWithTS posts one sample with the given tx bytes and re-asserts the
// host's egress limit (host info is sent on every ingest per the real
// agent protocol).
func ingestWithTS(t *testing.T, s *Server, hostID string, ts int64, tx, limit uint64) {
	t.Helper()
	host := sampleHostInfo(hostID)
	host.EgressLimitBytes = limit
	report := models.AgentReport{Host: host, Samples: []models.Sample{{Timestamp: ts, NetTxBytes: tx}}}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", s.opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAlerts_EgressRules_JumpAcrossThresholdsFiresEveryLevel(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	s, rec := setupEgressRuleServer(t, store, now)

	const hostID = "host-alert-jump"
	const limit = 1000
	ingestWithTS(t, s, hostID, now.Unix(), 1050, limit)
	s.Wait()

	if got := rec.callCount(); got != 3 {
		t.Fatalf("notify calls = %d, want 3 (80%%, 95%%, 100%% rules all satisfied at once)", got)
	}
}

func TestAlerts_EgressRules_NoChannelFactoryStillEvaluates(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	store.setAlertRule(models.AlertRule{
		ID: 1, Name: "egress 80", Enabled: true, Metric: models.AlertMetricEgressOutPct,
		Operator: models.AlertOperatorGTE, Threshold: 80,
		// No ChannelIDs at all — nothing to notify, but evaluation must
		// still succeed and not panic.
	})

	opts := testOptions()
	opts.Now = fixedNow(now)
	s, _ := newTestServerWithEngine(t, opts, store, nil)

	const hostID = "host-nil-notifier"
	const limit = 1000
	ingestWithTS(t, s, hostID, now.Unix(), 950, limit) // 95% -> both would-be levels satisfied
	s.Wait()
	// No panic; nothing to assert on delivery since no channel is
	// attached to the rule.
}

func TestAlerts_EgressRules_OKLevelDoesNotNotify(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	s, rec := setupEgressRuleServer(t, store, now)

	const hostID = "host-ok"
	const limit = 1000
	ingestWithTS(t, s, hostID, now.Unix(), 100, limit) // 10%, ok
	s.Wait()
	if got := rec.callCount(); got != 0 {
		t.Fatalf("notify calls = %d, want 0 for ok level", got)
	}
}

func TestAlerts_EgressRules_UnlimitedEgressNeverAlerts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	s, rec := setupEgressRuleServer(t, store, now)

	const hostID = "host-unlimited"
	ingestWithTS(t, s, hostID, now.Unix(), 1<<40, 0) // huge tx, limit=0 (unlimited)
	s.Wait()
	if got := rec.callCount(); got != 0 {
		t.Fatalf("notify calls = %d, want 0 for unlimited egress", got)
	}
}
