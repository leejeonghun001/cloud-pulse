package channelhealth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

// testSenderFactory builds a notify.Sender via notify.New with custom
// endpoints allowed, matching the shape RunOptions.SenderFactory
// expects — every test in this file uses this instead of the
// package's unexported production default so it can point at an
// httptest server.
func testSenderFactory(ch models.NotifyChannel, client *http.Client) (notify.Sender, error) {
	return notify.New(ch, client, notify.WithAllowCustomEndpoints(true))
}

func TestRunSyntheticTest_Discord_VerifiedAndCleanedUp(t *testing.T) {
	t.Parallel()

	var gotDeletePath, gotDeleteMethod string
	deleteCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"999888777"}`))
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"999888777","attachments":[]}`))
		case r.Method == http.MethodDelete:
			deleteCalled = true
			gotDeletePath = r.URL.Path
			gotDeleteMethod = r.Method
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	ch := models.NotifyChannel{ID: 7, Type: models.NotifyChannelDiscord, Config: map[string]string{"webhook_url": srv.URL + "/api/webhooks/1/tok"}}
	result := RunSyntheticTest(t.Context(), ch, RunOptions{SenderFactory: testSenderFactory})

	if result.Outcome != models.SyntheticVerified {
		t.Errorf("Outcome = %q, want verified (detail: %s)", result.Outcome, result.Detail)
	}
	if result.ChannelID != 7 {
		t.Errorf("ChannelID = %d, want 7", result.ChannelID)
	}
	if !deleteCalled {
		t.Fatal("expected cleanup DELETE call to have been made")
	}
	if gotDeleteMethod != http.MethodDelete || !strings.HasSuffix(gotDeletePath, "/messages/999888777") {
		t.Errorf("delete call = %s %s, want DELETE .../messages/999888777", gotDeleteMethod, gotDeletePath)
	}
	if !strings.Contains(result.Detail, "cleaned up") {
		t.Errorf("Detail = %q, want it to mention cleanup", result.Detail)
	}
}

func TestRunSyntheticTest_Discord_SendFailureIsFailedWithDiagnosisAndNoSecret(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Unknown Webhook","code":10015}`))
	}))
	defer srv.Close()

	secretToken := "super-secret-webhook-token-value-should-not-leak"
	ch := models.NotifyChannel{ID: 1, Type: models.NotifyChannelDiscord, Config: map[string]string{"webhook_url": srv.URL + "/api/webhooks/1/" + secretToken}}
	result := RunSyntheticTest(t.Context(), ch, RunOptions{SenderFactory: testSenderFactory})

	if result.Outcome != models.SyntheticFailed {
		t.Fatalf("Outcome = %q, want failed", result.Outcome)
	}
	if result.Diagnosis != models.DiagnosisDiscordWebhookGone {
		t.Errorf("Diagnosis = %q, want %q", result.Diagnosis, models.DiagnosisDiscordWebhookGone)
	}
	if strings.Contains(result.Detail, secretToken) {
		t.Errorf("Detail leaked the secret token: %q", result.Detail)
	}
}

func TestRunSyntheticTest_Discord_ReadBackFailureIsFailed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"1"}`))
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Unknown Message","code":10008}`))
		}
	}))
	defer srv.Close()

	ch := models.NotifyChannel{ID: 1, Type: models.NotifyChannelDiscord, Config: map[string]string{"webhook_url": srv.URL + "/api/webhooks/1/tok"}}
	result := RunSyntheticTest(t.Context(), ch, RunOptions{SenderFactory: testSenderFactory})

	if result.Outcome != models.SyntheticFailed {
		t.Fatalf("Outcome = %q, want failed", result.Outcome)
	}
	if !strings.Contains(result.Detail, "read-back failed") {
		t.Errorf("Detail = %q, want it to mention read-back failure", result.Detail)
	}
}

func TestRunSyntheticTest_Telegram_VerifiedAndCleanedUp(t *testing.T) {
	t.Parallel()

	deleteCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/sendMessage"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42,"chat":{"id":-100}}}`))
		case strings.Contains(r.URL.Path, "/deleteMessage"):
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer srv.Close()

	ch := models.NotifyChannel{
		ID:   3,
		Type: models.NotifyChannelTelegram,
		Config: map[string]string{
			"bot_token": "123456:TEST-TOKEN", "chat_id": "-100",
			"api_base": srv.URL,
		},
	}
	result := RunSyntheticTest(t.Context(), ch, RunOptions{SenderFactory: testSenderFactory})

	// Telegram has no MessageReader (see internal/notify's package doc
	// comment) so a successful send stays "accepted", never "verified".
	if result.Outcome != models.SyntheticAccepted {
		t.Errorf("Outcome = %q, want accepted (detail: %s)", result.Outcome, result.Detail)
	}
	if !deleteCalled {
		t.Error("expected Telegram deleteMessage cleanup call to have been made")
	}
}

func TestRunSyntheticTest_Webhook_AcceptedNoCleanupAttempted(t *testing.T) {
	t.Parallel()

	deleteCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteCalled = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ch := models.NotifyChannel{ID: 4, Type: models.NotifyChannelWebhook, Config: map[string]string{"url": srv.URL}}
	result := RunSyntheticTest(t.Context(), ch, RunOptions{SenderFactory: testSenderFactory})

	if result.Outcome != models.SyntheticAccepted {
		t.Errorf("Outcome = %q, want accepted", result.Outcome)
	}
	if deleteCalled {
		t.Error("webhook channels have no delete capability; DELETE must never be called")
	}
	if strings.Contains(result.Detail, "cleaned up") {
		t.Errorf("Detail = %q, must not claim cleanup for an unsupported platform", result.Detail)
	}
}

func TestRunSyntheticTest_FactoryErrorIsFailed(t *testing.T) {
	t.Parallel()

	ch := models.NotifyChannel{ID: 1, Type: models.NotifyChannelType("bogus")}
	result := RunSyntheticTest(t.Context(), ch, RunOptions{SenderFactory: testSenderFactory})
	if result.Outcome != models.SyntheticFailed {
		t.Errorf("Outcome = %q, want failed for an unsupported channel type", result.Outcome)
	}
}

func TestRunSyntheticTest_NeverEmbedsConfigSecretsInResult(t *testing.T) {
	t.Parallel()

	// Every branch (success, send failure, read-back failure) must
	// produce a SyntheticResult whose Detail never contains the
	// channel's configured secret value, regardless of outcome —
	// exercised across several server behaviors in one table to keep
	// the "never leak the secret" property tested against every path.
	secret := "AbCdEf0123456789SECRETVALUEZZ"

	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"send_error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"401: Unauthorized","code":50027}`))
		}},
		{"send_ok_readback_error", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"1"}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			ch := models.NotifyChannel{ID: 1, Type: models.NotifyChannelDiscord, Config: map[string]string{"webhook_url": srv.URL + "/api/webhooks/1/" + secret}}
			result := RunSyntheticTest(t.Context(), ch, RunOptions{SenderFactory: testSenderFactory})
			if strings.Contains(result.Detail, secret) {
				t.Errorf("%s: Detail leaked the secret: %q", tc.name, result.Detail)
			}
		})
	}
}
