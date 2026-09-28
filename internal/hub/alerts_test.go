package hub

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestAlerts_FireOncePerLevel(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	notifier := &fakeNotifier{}

	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := New(opts, store, nil, notifier, nil, testLogger())

	const hostID = "host-alert"
	const limit = 1000 // bytes

	// Seed the host with a small egress limit via an initial ingest.
	host := sampleHostInfo(hostID)
	host.EgressLimitBytes = limit
	report := models.AgentReport{Host: host, Samples: []models.Sample{{Timestamp: now.Unix(), NetTxBytes: 0}}}
	body, _ := json.Marshal(report)
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed ingest status = %d", rec.Code)
	}

	waitForAlerts := func() {
		t.Helper()
		s.Wait()
	}

	// Push tx to 85% (warning).
	ingestWithTS(t, s, hostID, now.Add(1*time.Minute).Unix(), 850, limit)
	waitForAlerts()
	if got := notifier.callCount(); got != 1 {
		t.Fatalf("after warning: notify calls = %d, want 1", got)
	}

	// Same level again (still warning-range) must not re-notify.
	ingestWithTS(t, s, hostID, now.Add(2*time.Minute).Unix(), 5, limit)
	waitForAlerts()
	if got := notifier.callCount(); got != 1 {
		t.Fatalf("after repeat warning: notify calls = %d, want still 1", got)
	}

	// Push to 96% (critical).
	ingestWithTS(t, s, hostID, now.Add(3*time.Minute).Unix(), 100, limit)
	waitForAlerts()
	if got := notifier.callCount(); got != 2 {
		t.Fatalf("after critical: notify calls = %d, want 2", got)
	}

	// Push to 105% (exceeded).
	ingestWithTS(t, s, hostID, now.Add(4*time.Minute).Unix(), 100, limit)
	waitForAlerts()
	if got := notifier.callCount(); got != 3 {
		t.Fatalf("after exceeded: notify calls = %d, want 3", got)
	}

	// Repeat exceeded must not re-notify.
	ingestWithTS(t, s, hostID, now.Add(5*time.Minute).Unix(), 50, limit)
	waitForAlerts()
	if got := notifier.callCount(); got != 3 {
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

func TestAlerts_NotifierNilSafe(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := New(opts, store, nil, nil, nil, testLogger()) // notifier is nil

	const hostID = "host-nil-notifier"
	const limit = 1000
	ingestWithTS(t, s, hostID, now.Unix(), 950, limit) // 95% -> critical (>= 95%)
	s.Wait()
	// No panic, no notifier call possible; just verify the alert was
	// still recorded so a later notifier wouldn't double-fire.
	first, err := store.MarkAlertSent(t.Context(), hostID, models.MonthOf(now), models.EgressCritical)
	if err != nil {
		t.Fatalf("MarkAlertSent: %v", err)
	}
	if first {
		t.Error("expected alert to already be marked sent by afterIngest, got first=true")
	}
}

func TestAlerts_OKLevelDoesNotNotify(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	notifier := &fakeNotifier{}
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := New(opts, store, nil, notifier, nil, testLogger())

	const hostID = "host-ok"
	const limit = 1000
	ingestWithTS(t, s, hostID, now.Unix(), 100, limit) // 10%, ok
	s.Wait()
	if got := notifier.callCount(); got != 0 {
		t.Fatalf("notify calls = %d, want 0 for ok level", got)
	}
}

func TestAlerts_UnlimitedEgressNeverAlerts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	notifier := &fakeNotifier{}
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := New(opts, store, nil, notifier, nil, testLogger())

	const hostID = "host-unlimited"
	ingestWithTS(t, s, hostID, now.Unix(), 1<<40, 0) // huge tx, limit=0 (unlimited)
	s.Wait()
	if got := notifier.callCount(); got != 0 {
		t.Fatalf("notify calls = %d, want 0 for unlimited egress", got)
	}
}
