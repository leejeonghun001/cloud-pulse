package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

type round30RouteRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

type round30RouteRecorder struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []round30RouteRequest
}

func newRound30RouteRecorder(t *testing.T) *round30RouteRecorder {
	t.Helper()
	r := &round30RouteRecorder{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(req.Body, 2<<20))
		r.mu.Lock()
		r.requests = append(r.requests, round30RouteRequest{method: req.Method, path: req.URL.RequestURI(), header: req.Header.Clone(), body: body})
		count := len(r.requests)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(req.URL.Path, "/media"):
			_, _ = io.WriteString(w, `{"id":"ROUND30_ROUTE_MEDIA_ID"}`)
		case strings.HasSuffix(req.URL.Path, "/messages"):
			_, _ = io.WriteString(w, `{"messages":[{"id":"ROUND30_ROUTE_WAMID"}]}`)
		case count == 1 && strings.Contains(req.URL.Path, "/api/webhooks/"):
			w.WriteHeader(http.StatusNoContent)
		case count == 1 && strings.HasSuffix(req.URL.Path, "/sendPhoto"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":42},"photo":[{"file_id":"local"}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("start route recorder listener")
	}
	srv.Listener = listener
	srv.Start()
	r.server = srv
	t.Cleanup(srv.Close)
	return r
}

func (r *round30RouteRecorder) snapshot() []round30RouteRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]round30RouteRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

type round30RouteGuard struct {
	mu      sync.Mutex
	allowed string
	pinned  string
	dials   int
	denied  int
}

func newRound30RouteClient(t *testing.T, recorder *httptest.Server) (*http.Client, *http.Transport, *round30RouteGuard) {
	t.Helper()
	allowed := strings.TrimPrefix(recorder.URL, "http://")
	pinned := recorder.Listener.Addr().String()
	guard := &round30RouteGuard{allowed: allowed, pinned: pinned}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		guard.mu.Lock()
		if addr != guard.allowed {
			guard.denied++
			guard.mu.Unlock()
			return nil, errors.New("round30 route transport refused a non-recorder target")
		}
		host, _, err := net.SplitHostPort(addr)
		ip, parseErr := netip.ParseAddr(host)
		if err != nil || parseErr != nil || !ip.IsLoopback() || addr != guard.pinned {
			guard.denied++
			guard.mu.Unlock()
			return nil, errors.New("round30 route transport refused an unpinned target")
		}
		guard.mu.Unlock()
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, guard.pinned)
		if err == nil {
			guard.mu.Lock()
			guard.dials++
			guard.mu.Unlock()
		}
		return conn, err
	}}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if transport.Proxy != nil {
		t.Fatal("route recorder transport must not use a proxy")
	}
	t.Cleanup(transport.CloseIdleConnections)
	return client, transport, guard
}

type round30RouteAdapterBridge struct{ sender notify.Sender }

func (b round30RouteAdapterBridge) Send(ctx context.Context, msg alerting.Message) error {
	fields := make([]notify.Field, len(msg.Fields))
	for i, field := range msg.Fields {
		fields[i] = notify.Field{Name: field.Name, Value: field.Value}
	}
	return b.sender.Send(ctx, notify.Message{
		Title: msg.Title, Text: msg.Text, Severity: notify.Severity(msg.Severity), Fields: fields,
		URL: msg.URL, Image: msg.Image, ImageName: msg.ImageName,
	})
}

func TestRound30LocalRecorderChannelTestRoutesUseRealAdapters(t *testing.T) {
	cases := []struct {
		provider models.NotifyChannelType
		path     string
		requests []string
	}{
		{provider: models.NotifyChannelDiscord, path: "saved", requests: []string{"/api/webhooks/ROUND30_ID/ROUND30_DISCORD_TOKEN"}},
		{provider: models.NotifyChannelTelegram, path: "draft", requests: []string{"/botROUND30_TELEGRAM_TOKEN/sendPhoto"}},
		{provider: models.NotifyChannelWhatsApp, path: "draft", requests: []string{"/v21.0/ROUND30_PHONE_ID/media", "/v21.0/ROUND30_PHONE_ID/messages"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.provider)+"_"+tc.path, func(t *testing.T) {
			recorder := newRound30RouteRecorder(t)
			client, transport, guard := newRound30RouteClient(t, recorder.server)
			channel := round30RouteChannel(tc.provider, recorder.server.URL)
			store := newFakeStore()
			requestPath := "/api/v1/alerts/channels/test"
			var body []byte
			var savedBefore models.NotifyChannel
			if tc.path == "saved" {
				saved, err := store.CreateNotifyChannel(t.Context(), channel)
				if err != nil {
					t.Fatal("save in-memory channel fixture")
				}
				savedBefore = saved
				requestPath = "/api/v1/alerts/channels/" + idPath(saved.ID) + "/test"
			} else {
				var err error
				body, err = json.Marshal(map[string]any{"name": "round30 local fixture", "type": string(tc.provider), "enabled": true, "config": channel.Config})
				if err != nil {
					t.Fatal("encode in-memory draft fixture")
				}
			}

			opts := testOptions()
			opts.NotifyFactory = WrapNotifySenderFactory(func(ch models.NotifyChannel) (alerting.Sender, error) {
				sender, err := notify.New(ch, client, notify.WithAllowCustomEndpoints(true))
				if err != nil {
					return nil, err
				}
				return round30RouteAdapterBridge{sender: sender}, nil
			})
			server := newTestServer(t, opts, store)
			response := doRequest(t, server.Handler(), http.MethodPost, requestPath, "203.0.113.1:1234", testUIToken, body)
			if response.Code != http.StatusOK {
				t.Fatalf("channel-test route status=%d", response.Code)
			}
			result := decodeJSON[models.TestResult](t, response.Body)
			if !result.OK || result.Diagnosis != nil {
				t.Fatal("local adapter channel-test did not report its simulated success")
			}
			requests := recorder.snapshot()
			if len(requests) != len(tc.requests) {
				t.Fatalf("local recorder request count=%d want=%d", len(requests), len(tc.requests))
			}
			for i, request := range requests {
				if request.method != http.MethodPost || request.path != tc.requests[i] {
					t.Fatalf("wire request[%d] method/path differs from expected adapter route", i)
				}
				assertRound30RouteAuth(t, tc.provider, request)
			}
			assertRound30RoutePayload(t, tc.provider, requests)

			guard.mu.Lock()
			dials, denied := guard.dials, guard.denied
			guard.mu.Unlock()
			if dials == 0 || denied != 0 || transport.Proxy != nil {
				t.Fatalf("guard evidence invalid: pinned_dials=%d refused=%d proxy_configured=%t", dials, denied, transport.Proxy != nil)
			}
			store.mu.Lock()
			channelCount, eventCount := len(store.channels), len(store.alertEvents)
			syntheticCount, healthCount := len(store.syntheticResults), len(store.channelHealthSettings)
			incidentCount, nextChannelID, nextSyntheticID := len(store.failureIncidents), store.nextChanID, store.nextSyntheticID
			store.mu.Unlock()
			if eventCount != 0 || syntheticCount != 0 || healthCount != 0 || incidentCount != 0 || nextSyntheticID != 0 || (tc.path == "draft" && (channelCount != 0 || nextChannelID != 0)) || (tc.path == "saved" && (channelCount != 1 || nextChannelID != 1)) {
				t.Fatalf("channel-test changed saved/draft or health state: channels=%d next_channel=%d events=%d synthetic=%d health_settings=%d incidents=%d", channelCount, nextChannelID, eventCount, syntheticCount, healthCount, incidentCount)
			}
			if tc.path == "saved" {
				savedAfter, err := store.GetNotifyChannel(t.Context(), savedBefore.ID)
				if err != nil || !reflect.DeepEqual(savedAfter, savedBefore) {
					t.Fatal("saved channel changed during local channel-test route")
				}
			}
			t.Logf("provider=%s route=%s result=local-adapter-dry-run requests=%d pinned_dials=%d state_mutations=0", tc.provider, tc.path, len(requests), dials)
		})
	}
}

func round30RouteChannel(provider models.NotifyChannelType, base string) models.NotifyChannel {
	config := map[string]string{}
	switch provider {
	case models.NotifyChannelDiscord:
		config["webhook_url"] = base + "/api/webhooks/ROUND30_ID/ROUND30_DISCORD_TOKEN"
	case models.NotifyChannelTelegram:
		config["bot_token"] = "ROUND30_TELEGRAM_TOKEN"
		config["chat_id"] = "42"
		config["api_base"] = base
	case models.NotifyChannelWhatsApp:
		config["access_token"] = "ROUND30_WHATSAPP_TOKEN"
		config["phone_number_id"] = "ROUND30_PHONE_ID"
		config["to"] = "15550001111"
		config["api_base"] = base
		config["template_name"] = "round30_local_template"
	}
	return models.NotifyChannel{Type: provider, Name: "round30 local fixture", Enabled: true, Config: config}
}

func assertRound30RouteAuth(t *testing.T, provider models.NotifyChannelType, request round30RouteRequest) {
	t.Helper()
	authorization := request.header.Get("Authorization")
	if provider == models.NotifyChannelWhatsApp {
		if authorization != "Bearer ROUND30_WHATSAPP_TOKEN" {
			t.Fatal("WhatsApp route request did not use bearer auth")
		}
	} else if authorization != "" {
		t.Fatal("Discord/Telegram route request unexpectedly used Authorization")
	}
}

func assertRound30RoutePayload(t *testing.T, provider models.NotifyChannelType, requests []round30RouteRequest) {
	t.Helper()
	switch provider {
	case models.NotifyChannelDiscord:
		fields := parseRound30RouteMultipart(t, requests[0])
		var payload struct {
			Embeds []struct {
				Image struct {
					URL string `json:"url"`
				} `json:"image"`
			} `json:"embeds"`
		}
		if err := json.Unmarshal([]byte(fields.values["payload_json"]), &payload); err != nil || len(payload.Embeds) != 1 || payload.Embeds[0].Image.URL != "attachment://chart.png" {
			t.Fatal("Discord route did not send payload_json embed attachment reference")
		}
		assertRound30RouteChart(t, fields, "files[0]")
	case models.NotifyChannelTelegram:
		fields := parseRound30RouteMultipart(t, requests[0])
		if fields.values["chat_id"] != "42" || fields.values["parse_mode"] != "HTML" || !strings.Contains(fields.values["caption"], "test notification") {
			t.Fatal("Telegram route sendPhoto did not include destination and HTML caption")
		}
		assertRound30RouteChart(t, fields, "photo")
	case models.NotifyChannelWhatsApp:
		media := parseRound30RouteMultipart(t, requests[0])
		if media.values["messaging_product"] != "whatsapp" {
			t.Fatal("WhatsApp route media upload missing product field")
		}
		assertRound30RouteChart(t, media, "file")
		var body struct {
			Type     string `json:"type"`
			Template struct {
				Components []struct {
					Type       string `json:"type"`
					Parameters []struct {
						Image struct {
							ID string `json:"id"`
						} `json:"image"`
					} `json:"parameters"`
				} `json:"components"`
			} `json:"template"`
		}
		if err := json.Unmarshal(requests[1].body, &body); err != nil || body.Type != "template" || len(body.Template.Components) == 0 || len(body.Template.Components[0].Parameters) == 0 || body.Template.Components[0].Parameters[0].Image.ID != "ROUND30_ROUTE_MEDIA_ID" {
			t.Fatal("WhatsApp route message did not reference the returned media ID")
		}
	}
}

type round30RouteMultipart struct {
	values map[string]string
	files  map[string]round30RouteFile
}

type round30RouteFile struct {
	name   string
	data   []byte
	header http.Header
}

func parseRound30RouteMultipart(t *testing.T, request round30RouteRequest) round30RouteMultipart {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(request.header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatal("expected route request to be multipart/form-data")
	}
	r := multipart.NewReader(bytes.NewReader(request.body), params["boundary"])
	out := round30RouteMultipart{values: make(map[string]string), files: make(map[string]round30RouteFile)}
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("parse route multipart body")
		}
		data, readErr := io.ReadAll(io.LimitReader(part, 2<<20))
		_ = part.Close()
		if readErr != nil {
			t.Fatal("read route multipart part")
		}
		if filename := part.FileName(); filename != "" {
			out.files[part.FormName()] = round30RouteFile{name: filename, data: data, header: http.Header(part.Header).Clone()}
		} else {
			out.values[part.FormName()] = string(data)
		}
	}
	return out
}

func assertRound30RouteChart(t *testing.T, multipart round30RouteMultipart, field string) {
	t.Helper()
	file, ok := multipart.files[field]
	if !ok || file.name != "chart.png" || len(file.data) < 8 || !bytes.HasPrefix(file.data, []byte("\x89PNG\r\n\x1a\n")) || file.header.Get("Content-Type") != "image/png" {
		t.Fatalf("route chart upload field %q missing or invalid", field)
	}
}
