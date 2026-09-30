package hub

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/channelhealth"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

// --- Test helpers ---

// fakeInternalNotifySender always succeeds/fails per its configured
// err, recording every message it's asked to send. Satisfies
// notify.Sender for channelhealth.RunOptions.SenderFactory.
type fakeInternalNotifySender struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (f *fakeInternalNotifySender) Send(_ context.Context, _ notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func fakeSyntheticSenderFactory(err error) channelhealth.SenderFactory {
	return func(_ models.NotifyChannel, _ *http.Client) (notify.Sender, error) {
		return &fakeInternalNotifySender{err: err}, nil
	}
}

// channelHealthTestServer wires a Server with both a channel-health
// RunOptions.SenderFactory (for synthetic tests) and an
// alerting.SenderFactory-backed NotifyFactory (for fallback
// notifications), sharing one alertMessageRecorder so fallback sends
// are observable.
func channelHealthTestServer(t *testing.T, syntheticErr error, fallbackRecorder *alertMessageRecorder) (*Server, Store) {
	t.Helper()
	store := newFakeStore()
	opts := testOptions()
	opts.ChannelHealth = &ChannelHealthRuntime{
		RunOptions: channelhealth.RunOptions{SenderFactory: fakeSyntheticSenderFactory(syntheticErr)},
	}
	if fallbackRecorder != nil {
		opts.NotifyFactory = newAlertingSenderFactory(fallbackRecorder.senderFactory())
	}
	s := New(opts, store, nil, nil, nil, testLogger())
	return s, store
}

func mustCreateChannel(t *testing.T, store Store, name string, enabled bool) models.NotifyChannel {
	t.Helper()
	ch, err := store.CreateNotifyChannel(context.Background(), models.NotifyChannel{
		Name: name, Type: models.NotifyChannelWebhook, Enabled: enabled, Config: map[string]string{"url": "https://example.invalid/hook"},
	})
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}
	return ch
}

// --- GET /api/v1/alerts/channels/health ---

func TestChannelsHealth_RequiresAdmin(t *testing.T) {
	t.Parallel()
	s, _ := channelHealthTestServer(t, nil, nil)
	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/channels/health", "203.0.113.1:1234", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestChannelsHealth_EmptyWhenNoChannels(t *testing.T) {
	t.Parallel()
	s, _ := channelHealthTestServer(t, nil, nil)
	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/channels/health", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.ChannelHealthReport](t, rec.Body)
	if len(got.Channels) != 0 || got.BannerNeeded {
		t.Errorf("got %+v, want empty report", got)
	}
}

func TestChannelsHealth_UnknownStateWithNoHistory(t *testing.T) {
	t.Parallel()
	s, store := channelHealthTestServer(t, nil, nil)
	mustCreateChannel(t, store, "c1", true)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/channels/health", "203.0.113.1:1234", testUIToken, nil)
	got := decodeJSON[models.ChannelHealthReport](t, rec.Body)
	if len(got.Channels) != 1 || got.Channels[0].State != models.ChannelUnknown {
		t.Errorf("got %+v, want one channel in state unknown", got)
	}
}

// --- POST /api/v1/alerts/channels/{id}/synthetic-test ---

func TestRunSyntheticTest_NotFound(t *testing.T) {
	t.Parallel()
	s, _ := channelHealthTestServer(t, nil, nil)
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/9999/synthetic-test", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRunSyntheticTest_SuccessPersistsAcceptedResult(t *testing.T) {
	t.Parallel()
	s, store := channelHealthTestServer(t, nil, nil)
	ch := mustCreateChannel(t, store, "c1", true)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/"+idPath(ch.ID)+"/synthetic-test", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.SyntheticResult](t, rec.Body)
	if got.Outcome != models.SyntheticAccepted {
		t.Errorf("Outcome = %q, want accepted", got.Outcome)
	}
	if got.ID == 0 || got.FinishedAt == 0 {
		t.Errorf("got %+v, want a persisted, finished result", got)
	}

	list, err := store.ListSyntheticResults(context.Background(), ch.ID, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSyntheticResults: %v, %+v", err, list)
	}
}

func TestRunSyntheticTest_FailureNeverExposesSecret(t *testing.T) {
	t.Parallel()
	s, store := channelHealthTestServer(t, context.DeadlineExceeded, nil)
	ch := mustCreateChannel(t, store, "c1", true)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/"+idPath(ch.ID)+"/synthetic-test", "203.0.113.1:1234", testUIToken, nil)
	got := decodeJSON[models.SyntheticResult](t, rec.Body)
	if got.Outcome != models.SyntheticFailed {
		t.Errorf("Outcome = %q, want failed", got.Outcome)
	}
}

// --- PUT /api/v1/alerts/channels/{id}/health-settings ---

func TestSetChannelHealthSettings_ValidationErrors(t *testing.T) {
	t.Parallel()
	s, store := channelHealthTestServer(t, nil, nil)
	ch := mustCreateChannel(t, store, "c1", true)

	body := `{"enabled":true,"weekday":9,"minute_of_day":9999}`
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/alerts/channels/"+idPath(ch.ID)+"/health-settings", "203.0.113.1:1234", testUIToken, []byte(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestSetChannelHealthSettings_RoundTrip(t *testing.T) {
	t.Parallel()
	s, store := channelHealthTestServer(t, nil, nil)
	ch := mustCreateChannel(t, store, "c1", true)

	body := `{"enabled":true,"weekday":2,"minute_of_day":540}`
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/alerts/channels/"+idPath(ch.ID)+"/health-settings", "203.0.113.1:1234", testUIToken, []byte(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.ChannelHealthSettings](t, rec.Body)
	if !got.Enabled || got.Weekday != 2 || got.MinuteOfDay != 540 {
		t.Errorf("got %+v, want enabled weekday=2 minute=540", got)
	}

	stored, err := store.GetChannelHealthSettings(context.Background(), ch.ID)
	if err != nil {
		t.Fatalf("GetChannelHealthSettings: %v", err)
	}
	if stored != got {
		t.Errorf("stored = %+v, want %+v", stored, got)
	}
}

func TestSetChannelHealthSettings_NotFound(t *testing.T) {
	t.Parallel()
	s, _ := channelHealthTestServer(t, nil, nil)
	body := `{"enabled":true,"weekday":0,"minute_of_day":0}`
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/alerts/channels/9999/health-settings", "203.0.113.1:1234", testUIToken, []byte(body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// --- Fallback dispatch (end-to-end via the scheduler helper functions) ---

func TestChannelFailureFallback_DispatchesThroughHealthyChannelOnSyntheticFailure(t *testing.T) {
	t.Parallel()
	recorder := &alertMessageRecorder{}
	s, store := channelHealthTestServer(t, context.DeadlineExceeded, recorder)

	failing := mustCreateChannel(t, store, "failing-channel", true)
	healthy := mustCreateChannel(t, store, "healthy-channel", true)
	_ = healthy

	now := time.Unix(1_700_000_000, 0).UTC()
	s.runOneChannelSyntheticTest(context.Background(), failing.ID, now)

	if recorder.callCount() != 1 {
		t.Fatalf("fallback callCount = %d, want 1", recorder.callCount())
	}
	msgs := recorder.messages()
	if msgs[0].channel.Name != "healthy-channel" {
		t.Errorf("fallback sent via %q, want healthy-channel", msgs[0].channel.Name)
	}
	if msgs[0].message.Text == "" {
		t.Error("fallback message text is empty")
	}

	inc, ok, err := store.GetOpenChannelFailureIncident(context.Background(), failing.ID)
	if err != nil || !ok || inc.NotifiedAt == 0 {
		t.Errorf("incident = %+v, ok=%v, err=%v, want an open, notified incident", inc, ok, err)
	}
}

func TestChannelFailureFallback_DeduplicatesWithinSameIncident(t *testing.T) {
	t.Parallel()
	recorder := &alertMessageRecorder{}
	s, store := channelHealthTestServer(t, context.DeadlineExceeded, recorder)

	failing := mustCreateChannel(t, store, "failing-channel", true)
	mustCreateChannel(t, store, "healthy-channel", true)

	now := time.Unix(1_700_000_000, 0).UTC()
	s.runOneChannelSyntheticTest(context.Background(), failing.ID, now)
	s.runOneChannelSyntheticTest(context.Background(), failing.ID, now.Add(time.Minute))

	if recorder.callCount() != 1 {
		t.Fatalf("fallback callCount = %d, want exactly 1 (de-duplicated within the same open incident)", recorder.callCount())
	}
}

func TestChannelFailureFallback_NoEligibleFallbackLeavesIncidentUnnotified(t *testing.T) {
	t.Parallel()
	recorder := &alertMessageRecorder{}
	s, store := channelHealthTestServer(t, context.DeadlineExceeded, recorder)

	failing := mustCreateChannel(t, store, "only-channel", true)

	now := time.Unix(1_700_000_000, 0).UTC()
	s.runOneChannelSyntheticTest(context.Background(), failing.ID, now)

	if recorder.callCount() != 0 {
		t.Fatalf("fallback callCount = %d, want 0 (no other channel exists)", recorder.callCount())
	}
	inc, ok, err := store.GetOpenChannelFailureIncident(context.Background(), failing.ID)
	if err != nil || !ok || inc.NotifiedAt != 0 {
		t.Errorf("incident = %+v, ok=%v, err=%v, want an open, un-notified incident", inc, ok, err)
	}

	// The health report's banner must now reflect this.
	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/channels/health", "203.0.113.1:1234", testUIToken, nil)
	got := decodeJSON[models.ChannelHealthReport](t, rec.Body)
	if !got.BannerNeeded || len(got.BannerChannelNames) != 1 || got.BannerChannelNames[0] != "only-channel" {
		t.Errorf("report = %+v, want banner needed for only-channel", got)
	}
}

func TestChannelFailureFallback_RecoverySendsRecoveryMessage(t *testing.T) {
	t.Parallel()
	recorder := &alertMessageRecorder{}
	s, store := channelHealthTestServer(t, context.DeadlineExceeded, recorder)

	failing := mustCreateChannel(t, store, "failing-channel", true)
	mustCreateChannel(t, store, "healthy-channel", true)

	now := time.Unix(1_700_000_000, 0).UTC()
	s.runOneChannelSyntheticTest(context.Background(), failing.ID, now)
	if recorder.callCount() != 1 {
		t.Fatalf("fallback callCount after failure = %d, want 1", recorder.callCount())
	}

	// Now the channel recovers (synthetic test succeeds).
	s.opts.ChannelHealth.RunOptions.SenderFactory = fakeSyntheticSenderFactory(nil)
	s.runOneChannelSyntheticTest(context.Background(), failing.ID, now.Add(time.Hour))

	if recorder.callCount() != 2 {
		t.Fatalf("fallback callCount after recovery = %d, want 2 (failure + recovery message)", recorder.callCount())
	}
	_, ok, err := store.GetOpenChannelFailureIncident(context.Background(), failing.ID)
	if err != nil {
		t.Fatalf("GetOpenChannelFailureIncident: %v", err)
	}
	if ok {
		t.Error("expected no open incident after recovery")
	}
}

func TestChannelFailureFallback_SelfNeverChosenAsFallback(t *testing.T) {
	t.Parallel()
	recorder := &alertMessageRecorder{}
	s, store := channelHealthTestServer(t, context.DeadlineExceeded, recorder)

	failing := mustCreateChannel(t, store, "failing-channel", true)
	disabled := mustCreateChannel(t, store, "disabled-channel", false)
	_ = disabled

	now := time.Unix(1_700_000_000, 0).UTC()
	s.runOneChannelSyntheticTest(context.Background(), failing.ID, now)

	if recorder.callCount() != 0 {
		t.Fatalf("fallback callCount = %d, want 0 (only candidate besides self is disabled)", recorder.callCount())
	}
}
