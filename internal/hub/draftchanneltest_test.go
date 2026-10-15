package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestDraftChannelTest_SecretMergeDestinationReplacementAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored models.NotifyChannel
		body   string
		status int
		config map[string]string
	}{
		{"telegram edited destination", models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "12:t0", "chat_id": "old"}}, `{"name":"draft","type":"telegram","existing_channel_id":1,"config":{"bot_token":"***","chat_id":"456"}}`, 200, map[string]string{"bot_token": "12:t0", "chat_id": "456"}},
		{"telegram omitted secret", models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "12:t0", "chat_id": "old"}}, `{"name":"draft","type":"telegram","existing_channel_id":1,"config":{"chat_id":"456"}}`, 200, map[string]string{"bot_token": "12:t0", "chat_id": "456"}},
		{"whatsapp edited destination", models.NotifyChannel{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ", "phone_number_id": "old", "to": "old"}}, `{"name":"draft","type":"whatsapp","existing_channel_id":1,"config":{"access_token":"***","phone_number_id":"123","to":"456"}}`, 200, map[string]string{"access_token": "xQ", "phone_number_id": "123", "to": "456"}},
		{"whatsapp replacement token", models.NotifyChannel{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ"}}, `{"name":"draft","type":"whatsapp","existing_channel_id":1,"config":{"access_token":"new-secret","phone_number_id":"123","to":"456"}}`, 200, map[string]string{"access_token": "new-secret", "phone_number_id": "123", "to": "456"}},
		{"placeholder without id", models.NotifyChannel{}, `{"name":"draft","type":"telegram","config":{"bot_token":"***","chat_id":"456"}}`, 400, nil},
		{"missing stored id", models.NotifyChannel{}, `{"name":"draft","type":"telegram","existing_channel_id":999,"config":{"chat_id":"456"}}`, 404, nil},
		{"negative id", models.NotifyChannel{}, `{"name":"draft","type":"telegram","existing_channel_id":-1,"config":{"chat_id":"456"}}`, 400, nil},
		{"zero id", models.NotifyChannel{}, `{"name":"draft","type":"telegram","existing_channel_id":0,"config":{"chat_id":"456"}}`, 400, nil},
		{"invalid id type", models.NotifyChannel{}, `{"name":"draft","type":"telegram","existing_channel_id":"1","config":{"chat_id":"456"}}`, 400, nil},
		{"type mismatch", models.NotifyChannel{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ"}}, `{"name":"draft","type":"telegram","existing_channel_id":1,"config":{"chat_id":"456"}}`, 400, nil},
		{"missing stored secret", models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{}}, `{"name":"draft","type":"telegram","existing_channel_id":1,"config":{"bot_token":"***","chat_id":"456"}}`, 400, nil},
		{"stored placeholder", models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "***"}}, `{"name":"draft","type":"telegram","existing_channel_id":1,"config":{"chat_id":"456"}}`, 400, nil},
		{"missing secret no id", models.NotifyChannel{}, `{"name":"draft","type":"whatsapp","config":{"phone_number_id":"123","to":"456"}}`, 400, nil},
		{"invalid name", models.NotifyChannel{}, `{"name":" ","type":"telegram","config":{"bot_token":"12:t0","chat_id":"456"}}`, 400, nil},
		{"invalid configuration", models.NotifyChannel{}, `{"name":"draft","type":"whatsapp","config":{"access_token":"xQ","phone_number_id":"123","to":"+456"}}`, 400, nil},
		{"malformed secret URL", models.NotifyChannel{}, `{"name":"draft","type":"discord","config":{"webhook_url":"https://discord.com/api/webhooks/1/xQ/%zz"}}`, 400, nil},
		{"backwards compatible new draft", models.NotifyChannel{}, `{"name":"draft","type":"telegram","config":{"bot_token":"12:t0","chat_id":"456"}}`, 200, map[string]string{"bot_token": "12:t0", "chat_id": "456"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			if tc.stored.Type != "" {
				tc.stored.ID = 1
				tc.stored.Name = "saved"
				store.channels[1] = tc.stored
			}
			before, _ := json.Marshal(store.channels)
			var got models.NotifyChannel
			calls := 0
			opts := testOptions()
			opts.NotifyFactory = newAlertingSenderFactory(func(ch models.NotifyChannel) (alerting.Sender, error) { got = ch; calls++; return failingSender{}, nil })
			s := newTestServer(t, opts, store)
			rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/test", "203.0.113.1:1234", testUIToken, []byte(tc.body))
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.config != nil && (calls != 1 || !reflect.DeepEqual(got.Config, tc.config)) {
				t.Fatalf("factory got=%+v calls=%d", got, calls)
			}
			if tc.config == nil && calls != 0 {
				t.Fatal("invalid draft reached sender factory")
			}
			after, _ := json.Marshal(store.channels)
			if !bytes.Equal(before, after) || store.nextChanID != 0 || len(store.alertEvents) != 0 || len(store.syntheticResults) != 0 {
				t.Fatal("draft test mutated persistent data")
			}
			for _, secret := range []string{"12:t0", "xQ", "new-secret"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatal("response leaked secret")
				}
			}
		})
	}
}

type draftReadFailureStore struct{ Store }

func (s draftReadFailureStore) GetNotifyChannel(context.Context, int64) (models.NotifyChannel, error) {
	return models.NotifyChannel{}, errors.New("store failed secret-xQ")
}

func TestDraftChannelTest_StoreFailureAndFactorySendErrorsSecretFree(t *testing.T) {
	for _, branch := range []string{"store", "constructor", "send"} {
		t.Run(branch, func(t *testing.T) {
			store := newFakeStore()
			var backend Store = store
			if branch == "store" {
				backend = draftReadFailureStore{store}
			}
			var logs bytes.Buffer
			opts := testOptions()
			opts.NotifyFactory = newAlertingSenderFactory(func(ch models.NotifyChannel) (alerting.Sender, error) {
				err := errors.New("send request failed xQ https://api.telegram.org/botxQ/sendPhoto")
				if branch == "constructor" {
					return nil, err
				}
				return failingSender{err: err}, nil
			})
			s := New(opts, backend, nil, nil, nil, slog.New(slog.NewTextHandler(&logs, nil)))
			body := `{"name":"draft","type":"telegram","config":{"bot_token":"xQ","chat_id":"456"}}`
			if branch == "store" {
				body = `{"name":"draft","type":"telegram","existing_channel_id":1,"config":{"bot_token":"xQ","chat_id":"456"}}`
			}
			rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/test", "203.0.113.1:1234", testUIToken, []byte(body))
			status := map[string]int{"store": 500, "constructor": 400, "send": 200}[branch]
			if rec.Code != status || strings.Contains(rec.Body.String()+logs.String(), "xQ") {
				t.Fatalf("unsafe failure status=%d body=%s logs=%s", rec.Code, rec.Body.String(), logs.String())
			}
		})
	}
}
