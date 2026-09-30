package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// --- Channels: auth ---

func TestAlertChannels_RequireAdmin(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/alerts/channels"},
		{http.MethodPost, "/api/v1/alerts/channels"},
		{http.MethodPut, "/api/v1/alerts/channels/1"},
		{http.MethodDelete, "/api/v1/alerts/channels/1"},
		{http.MethodPost, "/api/v1/alerts/channels/1/test"},
		{http.MethodPost, "/api/v1/alerts/channels/test"},
		{http.MethodGet, "/api/v1/alerts/rules"},
		{http.MethodPost, "/api/v1/alerts/rules"},
		{http.MethodPut, "/api/v1/alerts/rules/1"},
		{http.MethodDelete, "/api/v1/alerts/rules/1"},
		{http.MethodPost, "/api/v1/alerts/rules/1/preview"},
	}
	for _, tc := range cases {
		rec := doRequest(t, s.Handler(), tc.method, tc.path, "203.0.113.1:1234", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401 (no token)", tc.method, tc.path, rec.Code)
		}
	}
}

func TestAlertEvents_RequireUser(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	for _, path := range []string{"/api/v1/alerts/events", "/api/v1/alerts/active"} {
		rec := doRequest(t, s.Handler(), http.MethodGet, path, "203.0.113.1:1234", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status = %d, want 401 (no token)", path, rec.Code)
		}
		rec2 := doRequest(t, s.Handler(), http.MethodGet, path, "203.0.113.1:1234", testUIToken, nil)
		if rec2.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 (with token)", path, rec2.Code)
		}
	}
}

// --- Channels: CRUD + redaction ---

func TestAlertChannels_CreateListRedactsSecret(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	body := `{"name":"ops discord","type":"discord","enabled":true,"config":{"webhook_url":"https://discord.com/api/webhooks/123/abc"}}`
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels", "203.0.113.1:1234", testUIToken, []byte(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	created := decodeJSON[models.NotifyChannel](t, rec.Body)
	if created.ID == 0 {
		t.Fatal("created channel has no ID")
	}
	if created.Config["webhook_url"] != models.RedactedConfigValue {
		t.Errorf("create response webhook_url = %q, want redacted", created.Config["webhook_url"])
	}

	listRec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/channels", "203.0.113.1:1234", testUIToken, nil)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listRec.Code)
	}
	list := decodeJSON[[]models.NotifyChannel](t, listRec.Body)
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	if list[0].Config["webhook_url"] != models.RedactedConfigValue {
		t.Errorf("list webhook_url = %q, want redacted", list[0].Config["webhook_url"])
	}

	// The underlying stored value must remain unredacted.
	stored, err := store.GetNotifyChannel(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetNotifyChannel: %v", err)
	}
	if stored.Config["webhook_url"] != "https://discord.com/api/webhooks/123/abc" {
		t.Errorf("stored webhook_url = %q, want the real value", stored.Config["webhook_url"])
	}
}

func TestAlertChannels_CreateValidation(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	cases := []struct {
		name string
		body string
	}{
		{"missing_name", `{"type":"webhook","config":{}}`},
		{"invalid_type", `{"name":"x","type":"carrier-pigeon","config":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels", "203.0.113.1:1234", testUIToken, []byte(tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			got := decodeJSON[models.APIError](t, rec.Body)
			if len(got.Details) == 0 {
				t.Error("expected field-level Details in APIError")
			}
		})
	}
}

func TestAlertChannels_UpdatePreservesSecretOnOmit(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	created, err := store.CreateNotifyChannel(t.Context(), models.NotifyChannel{
		Name: "telegram ops", Type: models.NotifyChannelTelegram, Enabled: true,
		Config: map[string]string{"bot_token": "123456:TEST-TOKEN", "chat_id": "42"},
	})
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}

	// Update omitting bot_token entirely and renaming; bot_token must be
	// preserved.
	body := `{"name":"telegram ops (renamed)","type":"telegram","enabled":true,"config":{"chat_id":"43"}}`
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/alerts/channels/"+idPath(created.ID), "203.0.113.1:1234", testUIToken, []byte(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	updated := decodeJSON[models.NotifyChannel](t, rec.Body)
	if updated.Config["bot_token"] != models.RedactedConfigValue {
		t.Errorf("response bot_token = %q, want redacted", updated.Config["bot_token"])
	}

	stored, err := store.GetNotifyChannel(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetNotifyChannel: %v", err)
	}
	if stored.Config["bot_token"] != "123456:TEST-TOKEN" {
		t.Errorf("stored bot_token = %q, want preserved original", stored.Config["bot_token"])
	}
	if stored.Config["chat_id"] != "43" {
		t.Errorf("stored chat_id = %q, want 43 (updated)", stored.Config["chat_id"])
	}
	if stored.Name != "telegram ops (renamed)" {
		t.Errorf("stored name = %q, want renamed", stored.Name)
	}

	// Sending back the literal redacted placeholder must also preserve
	// the secret (not store the literal "***").
	body2 := `{"name":"telegram ops (renamed)","type":"telegram","enabled":true,"config":{"bot_token":"***","chat_id":"44"}}`
	rec2 := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/alerts/channels/"+idPath(created.ID), "203.0.113.1:1234", testUIToken, []byte(body2))
	if rec2.Code != http.StatusOK {
		t.Fatalf("update2 status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}
	stored2, err := store.GetNotifyChannel(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetNotifyChannel (2nd): %v", err)
	}
	if stored2.Config["bot_token"] != "123456:TEST-TOKEN" {
		t.Errorf("stored bot_token after '***' update = %q, want preserved original", stored2.Config["bot_token"])
	}
}

func TestAlertChannels_UpdateNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	body := `{"name":"x","type":"webhook","enabled":true,"config":{}}`
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/alerts/channels/99999", "203.0.113.1:1234", testUIToken, []byte(body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAlertChannels_Delete(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	created, err := store.CreateNotifyChannel(t.Context(), models.NotifyChannel{Name: "x", Type: models.NotifyChannelWebhook, Enabled: true})
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}
	rec := doRequest(t, s.Handler(), http.MethodDelete, "/api/v1/alerts/channels/"+idPath(created.ID), "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if _, err := store.GetNotifyChannel(t.Context(), created.ID); !isNotFound(err) {
		t.Errorf("channel still exists after delete: err = %v", err)
	}
}

// --- Channels: test endpoints ---

func TestAlertChannels_TestExisting_NoFactoryConfigured(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store) // no NotifyFactory wired

	created, err := store.CreateNotifyChannel(t.Context(), models.NotifyChannel{Name: "x", Type: models.NotifyChannelWebhook, Enabled: true})
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/"+idPath(created.ID)+"/test", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAlertChannels_TestExisting_NotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/99999/test", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAlertChannels_TestDraft_SendsAndReportsResult(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	rec := &alertMessageRecorder{}
	opts := testOptions()
	s, _ := newTestServerWithEngine(t, opts, store, rec.senderFactory())

	body := `{"name":"draft","type":"webhook","enabled":true,"config":{}}`
	httpRec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/test", "203.0.113.1:1234", testUIToken, []byte(body))
	if httpRec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", httpRec.Code, httpRec.Body.String())
	}
	got := decodeJSON[models.TestResult](t, httpRec.Body)
	if !got.OK {
		t.Errorf("ok = %v, want true", got.OK)
	}
	if got.Diagnosis != nil {
		t.Errorf("diagnosis = %+v, want nil on success", got.Diagnosis)
	}
	if rec.callCount() != 1 {
		t.Errorf("sender calls = %d, want 1", rec.callCount())
	}
	messages := rec.messages()
	if len(messages) != 1 || !messages[0].message.HasImage() {
		t.Errorf("channel test message = %+v, want one message with a chart image", messages)
	}
}

// failingSenderFactory builds an alerting.SenderFactory whose Sender
// always fails Send with err — used to exercise sendTestMessage's
// failure path (TestResult.OK=false, Diagnosis populated).
func failingSenderFactory(err error) alerting.SenderFactory {
	return func(ch models.NotifyChannel) (alerting.Sender, error) {
		return failingSender{err: err}, nil
	}
}

type failingSender struct{ err error }

func (s failingSender) Send(context.Context, alerting.Message) error { return s.err }

func TestAlertChannels_TestDraft_FailureReturnsDiagnosis(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	opts := testOptions()
	s, _ := newTestServerWithEngine(t, opts, store, failingSenderFactory(&net.DNSError{Err: "no such host", Name: "hooks.example.invalid", IsNotFound: true}))

	body := `{"name":"draft","type":"webhook","enabled":true,"config":{"url":"https://hooks.example.invalid/x"}}`
	httpRec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/test", "203.0.113.1:1234", testUIToken, []byte(body))
	if httpRec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", httpRec.Code, httpRec.Body.String())
	}
	got := decodeJSON[models.TestResult](t, httpRec.Body)
	if got.OK {
		t.Errorf("ok = %v, want false", got.OK)
	}
	if got.Diagnosis == nil {
		t.Fatal("diagnosis = nil, want non-nil on failure")
	}
	if got.Diagnosis.Code != models.DiagnosisDNSFailure {
		t.Errorf("diagnosis.code = %q, want %q", got.Diagnosis.Code, models.DiagnosisDNSFailure)
	}
}

// --- Rules: CRUD + validation ---

func TestAlertRules_CreateListUpdateDelete(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	body := `{"name":"CPU high","enabled":true,"metric":"cpu","operator":">","threshold":90,"duration_sec":300,"channel_ids":[]}`
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/rules", "203.0.113.1:1234", testUIToken, []byte(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	created := decodeJSON[models.AlertRule](t, rec.Body)
	if created.CooldownSec != alertingDefaultCooldownSec {
		t.Errorf("CooldownSec = %d, want default %d (cooldown_sec omitted)", created.CooldownSec, alertingDefaultCooldownSec)
	}

	listRec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/rules", "203.0.113.1:1234", testUIToken, nil)
	list := decodeJSON[[]models.AlertRule](t, listRec.Body)
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	updateBody := `{"name":"CPU high (updated)","enabled":false,"metric":"cpu","operator":">=","threshold":95,"duration_sec":60,"cooldown_sec":0,"channel_ids":[]}`
	updRec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/alerts/rules/"+idPath(created.ID), "203.0.113.1:1234", testUIToken, []byte(updateBody))
	if updRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body=%s)", updRec.Code, updRec.Body.String())
	}
	updated := decodeJSON[models.AlertRule](t, updRec.Body)
	if updated.CooldownSec != 0 {
		t.Errorf("CooldownSec after explicit 0 = %d, want 0 (explicit zero must not become the default)", updated.CooldownSec)
	}
	if updated.Enabled {
		t.Error("Enabled = true, want false after update")
	}

	delRec := doRequest(t, s.Handler(), http.MethodDelete, "/api/v1/alerts/rules/"+idPath(created.ID), "203.0.113.1:1234", testUIToken, nil)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", delRec.Code)
	}
	if _, err := store.GetAlertRule(t.Context(), created.ID); !isNotFound(err) {
		t.Errorf("rule still exists after delete: err = %v", err)
	}
}

func TestAlertRules_CreateValidation(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	cases := []struct {
		name string
		body string
	}{
		{"missing_name", `{"metric":"cpu","operator":">","threshold":90}`},
		{"invalid_metric", `{"name":"x","metric":"gpu","operator":">","threshold":90}`},
		{"invalid_operator", `{"name":"x","metric":"cpu","operator":"==","threshold":90}`},
		{"negative_duration", `{"name":"x","metric":"cpu","operator":">","threshold":90,"duration_sec":-5}`},
		{"negative_cooldown", `{"name":"x","metric":"cpu","operator":">","threshold":90,"cooldown_sec":-5}`},
		{"invalid_host_id", `{"name":"x","metric":"cpu","operator":">","threshold":90,"host_id":"bad id with spaces"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/rules", "203.0.113.1:1234", testUIToken, []byte(tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			got := decodeJSON[models.APIError](t, rec.Body)
			if len(got.Details) == 0 {
				t.Error("expected field-level Details in APIError")
			}
		})
	}
}

func TestAlertRules_UpdateNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	body := `{"name":"x","metric":"cpu","operator":">","threshold":90}`
	rec := doRequest(t, s.Handler(), http.MethodPut, "/api/v1/alerts/rules/99999", "203.0.113.1:1234", testUIToken, []byte(body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAlertRules_Preview(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s, _ := newTestServerWithEngine(t, opts, store, nil)

	if err := store.UpsertHost(t.Context(), sampleHostInfo("h1"), now.Unix()); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	if _, err := store.InsertSamples(t.Context(), "h1", []models.Sample{{Timestamp: now.Unix(), CPUPercent: 95}}); err != nil {
		t.Fatalf("InsertSamples: %v", err)
	}

	rule, err := store.CreateAlertRule(t.Context(), models.AlertRule{
		Name: "CPU high", Enabled: true, Metric: models.AlertMetricCPU, Operator: models.AlertOperatorGT, Threshold: 50,
	})
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/rules/"+idPath(rule.ID)+"/preview", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[map[string]bool](t, rec.Body)
	if !got["h1"] {
		t.Errorf("preview[h1] = %v, want true (95 > 50)", got["h1"])
	}
}

func TestAlertRules_Preview_NoEngineConfigured(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store) // no Alerting engine wired

	rule, err := store.CreateAlertRule(t.Context(), models.AlertRule{Name: "x", Metric: models.AlertMetricCPU, Operator: models.AlertOperatorGT, Threshold: 50})
	if err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}
	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/rules/"+idPath(rule.ID)+"/preview", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body=%s)", rec.Code, rec.Body.String())
	}
}

// --- Events ---

func TestAlertEvents_ListAndPaginate(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	for i, ts := range []int64{100, 200, 300} {
		if _, err := store.CreateAlertEvent(t.Context(), models.AlertEvent{
			RuleID: 1, HostID: "h1", Metric: models.AlertMetricCPU,
			State: models.AlertEventFiring, StartedAt: ts,
		}); err != nil {
			t.Fatalf("CreateAlertEvent %d: %v", i, err)
		}
	}

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/events?limit=2", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Events []models.AlertEvent `json:"events"`
	}
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("events len = %d, want 2 (limit)", len(got.Events))
	}
	if got.Events[0].StartedAt != 300 {
		t.Errorf("first event StartedAt = %d, want 300 (newest first)", got.Events[0].StartedAt)
	}
}

func TestAlertEvents_InvalidStateRejected(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/events?state=bogus", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestAlertActive_ListsOnlyFiring(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	if _, err := store.CreateAlertEvent(t.Context(), models.AlertEvent{RuleID: 1, HostID: "h1", Metric: models.AlertMetricCPU, State: models.AlertEventFiring, StartedAt: 100}); err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}
	if _, err := store.CreateAlertEvent(t.Context(), models.AlertEvent{RuleID: 1, HostID: "h2", Metric: models.AlertMetricCPU, State: models.AlertEventResolved, StartedAt: 200}); err != nil {
		t.Fatalf("CreateAlertEvent: %v", err)
	}

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/active", "203.0.113.1:1234", testUIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Events []models.AlertEvent `json:"events"`
	}
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Events) != 1 || got.Events[0].HostID != "h1" {
		t.Fatalf("active events = %+v, want only h1's firing event", got.Events)
	}
}

// idPath formats an int64 ID for use in a URL path.
func idPath(id int64) string {
	return strconv.FormatInt(id, 10)
}
