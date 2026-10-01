// channelhealthrun_test.go covers the delivery-failure streak trigger of
// the channel-failure fallback notification (SPEC-v0.8 §3-2): N
// (models.ConsecutiveFailureThreshold) consecutive failed deliveries open
// an incident and dispatch the fallback through another healthy channel,
// a recovery in the deliveries closes it, and a passing synthetic test
// does not close an incident whose delivery failures are ongoing.
package hub

import (
	"context"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// mustSeedDelivery creates one AlertEvent carrying a single delivery for
// channelID with the given outcome and timestamp.
func mustSeedDelivery(t *testing.T, store Store, channelID int64, ok bool, at time.Time) {
	t.Helper()
	_, err := store.CreateAlertEvent(context.Background(), models.AlertEvent{
		RuleID:    1,
		RuleName:  "test rule",
		HostID:    "demo-host-01",
		Hostname:  "demo-host-01",
		Metric:    models.AlertMetricCPU,
		State:     models.AlertEventFiring,
		StartedAt: at.Unix(),
		Deliveries: []models.Delivery{
			{ChannelID: channelID, OK: ok, At: at.Unix(), Error: deliveryErrString(ok)},
		},
	})
	if err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}
}

func deliveryErrString(ok bool) string {
	if ok {
		return ""
	}
	return "simulated delivery failure"
}

func TestChannelFailureDeliveryStreak_OpensIncidentAndNotifiesFallback(t *testing.T) {
	t.Parallel()
	recorder := &alertMessageRecorder{}
	s, store := channelHealthTestServer(t, nil, recorder)

	failing := mustCreateChannel(t, store, "failing-channel", true)
	healthy := mustCreateChannel(t, store, "healthy-channel", true)
	_ = healthy

	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < models.ConsecutiveFailureThreshold; i++ {
		mustSeedDelivery(t, store, failing.ID, false, base.Add(time.Duration(i)*time.Minute))
	}

	// The background tick evaluates every channel from persisted
	// deliveries alone — no synthetic test has ever run here.
	s.runDueChannelHealthChecks(context.Background())

	if recorder.callCount() != 1 {
		t.Fatalf("fallback callCount = %d, want 1", recorder.callCount())
	}
	msgs := recorder.messages()
	if msgs[0].channel.Name != "healthy-channel" {
		t.Errorf("fallback sent via %q, want healthy-channel", msgs[0].channel.Name)
	}
	inc, ok, err := store.GetOpenChannelFailureIncident(context.Background(), failing.ID)
	if err != nil || !ok || inc.NotifiedAt == 0 {
		t.Errorf("incident = %+v, ok=%v, err=%v, want an open, notified incident", inc, ok, err)
	}
}

func TestChannelFailureDeliveryStreak_FewerThanThresholdNeverOpensIncident(t *testing.T) {
	t.Parallel()
	recorder := &alertMessageRecorder{}
	s, store := channelHealthTestServer(t, nil, recorder)

	failing := mustCreateChannel(t, store, "failing-channel", true)
	healthy := mustCreateChannel(t, store, "healthy-channel", true)
	_ = healthy

	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < models.ConsecutiveFailureThreshold-1; i++ {
		mustSeedDelivery(t, store, failing.ID, false, base.Add(time.Duration(i)*time.Minute))
	}

	s.runDueChannelHealthChecks(context.Background())

	if recorder.callCount() != 0 {
		t.Fatalf("fallback callCount = %d, want 0 (streak shorter than threshold)", recorder.callCount())
	}
	_, ok, err := store.GetOpenChannelFailureIncident(context.Background(), failing.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if ok {
		t.Error("expected no incident for a sub-threshold failure streak")
	}
}

func TestChannelFailureDeliveryStreak_RecoveryClosesIncident(t *testing.T) {
	t.Parallel()
	recorder := &alertMessageRecorder{}
	s, store := channelHealthTestServer(t, nil, recorder)

	failing := mustCreateChannel(t, store, "failing-channel", true)
	healthy := mustCreateChannel(t, store, "healthy-channel", true)
	_ = healthy

	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < models.ConsecutiveFailureThreshold; i++ {
		mustSeedDelivery(t, store, failing.ID, false, base.Add(time.Duration(i)*time.Minute))
	}
	s.runDueChannelHealthChecks(context.Background())
	if recorder.callCount() != 1 {
		t.Fatalf("fallback callCount after failure streak = %d, want 1", recorder.callCount())
	}

	// Deliveries recover: the next attempt succeeds, so the streak is
	// broken and the incident closes (sending the optional recovery
	// message through the same fallback channel).
	mustSeedDelivery(t, store, failing.ID, true, base.Add(10*time.Minute))
	s.runDueChannelHealthChecks(context.Background())

	if recorder.callCount() != 2 {
		t.Fatalf("fallback callCount after recovery = %d, want 2 (failure + recovery message)", recorder.callCount())
	}
	_, ok, err := store.GetOpenChannelFailureIncident(context.Background(), failing.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if ok {
		t.Error("expected no open incident after the delivery streak recovered")
	}
}

func TestChannelFailureDispatch_SyntheticPassDoesNotRecoverWhileStreakFailing(t *testing.T) {
	t.Parallel()
	recorder := &alertMessageRecorder{}
	s, store := channelHealthTestServer(t, nil, recorder)

	failing := mustCreateChannel(t, store, "failing-channel", true)
	healthy := mustCreateChannel(t, store, "healthy-channel", true)
	_ = healthy

	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < models.ConsecutiveFailureThreshold; i++ {
		mustSeedDelivery(t, store, failing.ID, false, base.Add(time.Duration(i)*time.Minute))
	}
	// Open the incident via the delivery streak.
	s.runDueChannelHealthChecks(context.Background())
	if recorder.callCount() != 1 {
		t.Fatalf("fallback callCount after failure streak = %d, want 1", recorder.callCount())
	}

	// A passing synthetic test must NOT recover the incident while the
	// delivery-failure streak is still ongoing: the two signals are
	// independent, and only both clearing closes the incident.
	now := base.Add(time.Hour)
	s.opts.ChannelHealth.RunOptions.SenderFactory = fakeSyntheticSenderFactory(nil)
	s.runOneChannelSyntheticTest(context.Background(), failing.ID, now)

	_, ok, err := store.GetOpenChannelFailureIncident(context.Background(), failing.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if !ok {
		t.Error("expected the incident to stay open while the delivery streak is still failing")
	}

	// Deliveries recover → the next tick closes the incident.
	mustSeedDelivery(t, store, failing.ID, true, now.Add(time.Minute))
	s.runDueChannelHealthChecks(context.Background())
	_, ok, err = store.GetOpenChannelFailureIncident(context.Background(), failing.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if ok {
		t.Error("expected the incident to close once both signals cleared")
	}
}
