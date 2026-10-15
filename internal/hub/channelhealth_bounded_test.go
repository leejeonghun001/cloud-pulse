package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/channelhealth"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

type boundedHubSender func(context.Context, any) error

func (f boundedHubSender) Send(ctx context.Context, m any) error { return f(ctx, m) }

type boundedHubTransport func(*http.Request) (*http.Response, error)

func (f boundedHubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWhatsAppFallback_RealSenderGeneratedPNGMediaReference(t *testing.T) {
	store := newFakeStore()
	opts := testOptions()
	var calls int
	var uploaded []byte
	opts.NotifyFactory = func(ch models.NotifyChannel) (NotifySender, error) {
		client := &http.Client{Transport: boundedHubTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			body := `{"id":"fallback-media-id"}`
			if strings.HasSuffix(r.URL.Path, "/media") {
				_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				reader := multipart.NewReader(r.Body, params["boundary"])
				for {
					part, err := reader.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if part.FormName() == "file" {
						uploaded, _ = io.ReadAll(part)
						if _, err := png.Decode(bytes.NewReader(uploaded)); err != nil {
							t.Fatal(err)
						}
					}
				}
			} else {
				var msg struct {
					Image struct {
						ID string `json:"id"`
					} `json:"image"`
				}
				if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
					t.Fatal(err)
				}
				if msg.Image.ID != "fallback-media-id" {
					t.Fatal("message did not reference uploaded media")
				}
				body = `{"messages":[{"id":"accepted-message"}]}`
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		sender, err := notify.New(ch, client, notify.WithAllowCustomEndpoints(true))
		return boundedHubSender(func(ctx context.Context, m any) error {
			message := m.(alerting.Message)
			return sender.Send(ctx, notify.Message{Title: message.Title, Text: message.Text, Severity: notify.Severity(message.Severity), Image: message.Image, ImageName: message.ImageName})
		}), err
	}
	s := newTestServer(t, opts, store)
	ch := models.NotifyChannel{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ", "phone_number_id": "123", "to": "456"}}
	if !s.sendFallbackNotification(t.Context(), ch, "generated channel health test") {
		t.Fatal("WhatsApp fallback did not send")
	}
	expected, _ := notify.GeneratedTestChartPNG()
	if calls != 2 || !bytes.Equal(uploaded, expected) {
		t.Fatal("fallback did not use exactly the generated demo chart")
	}
}

func TestSyntheticRoute_ParentDeadlineBoundsBlockingFallbackAndPersistsResult(t *testing.T) {
	store := newFakeStore()
	failing, _ := store.CreateNotifyChannel(t.Context(), models.NotifyChannel{Name: "failing", Type: models.NotifyChannelWebhook, Enabled: true, Config: map[string]string{"url": "https://hooks.example.invalid/fail"}})
	_, _ = store.CreateNotifyChannel(t.Context(), models.NotifyChannel{Name: "fallback", Type: models.NotifyChannelWebhook, Enabled: true, Config: map[string]string{"url": "https://hooks.example.invalid/fallback"}})
	opts := testOptions()
	opts.ChannelHealth = &ChannelHealthRuntime{RunOptions: channelhealth.RunOptions{SenderFactory: fakeSyntheticSenderFactory(errors.New("send failed"))}}
	var fallbackDeadline time.Time
	opts.NotifyFactory = func(models.NotifyChannel) (NotifySender, error) {
		return boundedHubSender(func(ctx context.Context, _ any) error {
			fallbackDeadline, _ = ctx.Deadline()
			<-ctx.Done()
			return ctx.Err()
		}), nil
	}
	s := newTestServer(t, opts, store)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/channels/1/synthetic-test", nil).WithContext(ctx)
	r.SetPathValue("id", idPath(failing.ID))
	w := httptest.NewRecorder()
	start := time.Now()
	s.handleRunSyntheticTest(w, r)
	if time.Since(start) > time.Second || !fallbackDeadline.Equal(deadline) {
		t.Fatalf("fallback escaped parent deadline: %s", time.Since(start))
	}
	results, _ := store.ListSyntheticResults(t.Context(), failing.ID, 0)
	if w.Code != 200 || len(results) != 1 || results[0].Outcome != models.SyntheticFailed || results[0].FinishedAt == 0 {
		t.Fatalf("result was not persisted before bounded fallback: %s %+v", w.Body.String(), results)
	}
}

func TestSyntheticRouteAndFallbackErrors_PersistedReadbackJSONLogsSecretFree(t *testing.T) {
	store := newFakeStore()
	ch, _ := store.CreateNotifyChannel(t.Context(), models.NotifyChannel{Name: "test", Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ", "api_base": "https://graph.example.invalid/private"}})
	err := errors.New("send request failed xQ https://graph.example.invalid/private")
	opts := testOptions()
	opts.ChannelHealth = &ChannelHealthRuntime{RunOptions: channelhealth.RunOptions{SenderFactory: fakeSyntheticSenderFactory(err)}}
	var logs bytes.Buffer
	s := New(opts, store, nil, nil, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	w := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/alerts/channels/"+idPath(ch.ID)+"/synthetic-test", "203.0.113.1:1234", testUIToken, nil)
	health := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/alerts/channels/health", "203.0.113.1:1234", testUIToken, nil)
	results, _ := store.ListSyntheticResults(t.Context(), ch.ID, 0)
	persisted, _ := json.Marshal(results)
	for _, factoryFail := range []bool{true, false} {
		s.opts.NotifyFactory = func(models.NotifyChannel) (NotifySender, error) {
			if factoryFail {
				return nil, err
			}
			return boundedHubSender(func(context.Context, any) error { return err }), nil
		}
		if s.sendFallbackNotification(t.Context(), ch, "generated fallback text") {
			t.Error("failed fallback reported success")
		}
	}
	combined := w.Body.String() + health.Body.String() + string(persisted) + logs.String()
	if w.Code != 200 || health.Code != 200 || strings.Contains(combined, "xQ") || strings.Contains(combined, "graph.example.invalid") {
		t.Fatalf("unsafe HTTP/persisted/log failure: %s", combined)
	}
}
