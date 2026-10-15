package notify

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
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

const round30Timeout = 300 * time.Millisecond

var round30ChartPNG = []byte("\x89PNG\r\n\x1a\nround30-local-chart-fixture")

type round30CapturedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

type round30Response struct {
	status      int
	contentType string
	body        string
	location    string
	wait        bool
}

type round30Recorder struct {
	server     *httptest.Server
	mu         sync.Mutex
	requests   []round30CapturedRequest
	cancelSeen chan struct{}
	cancelOnce sync.Once
	stop       chan struct{}
}

func newRound30Recorder(t *testing.T, respond func(int, string) round30Response) *round30Recorder {
	t.Helper()
	r := &round30Recorder{cancelSeen: make(chan struct{}), stop: make(chan struct{})}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(req.Body, 2<<20))
		r.mu.Lock()
		r.requests = append(r.requests, round30CapturedRequest{
			method: req.Method,
			path:   req.URL.RequestURI(),
			header: req.Header.Clone(),
			body:   body,
		})
		count := len(r.requests)
		r.mu.Unlock()

		plan := respond(count, req.URL.Path)
		if plan.wait {
			select {
			case <-req.Context().Done():
				r.cancelOnce.Do(func() { close(r.cancelSeen) })
			case <-r.stop:
			}
			return
		}
		if plan.location != "" {
			http.Redirect(w, req, plan.location, plan.status)
			return
		}
		if plan.contentType != "" {
			w.Header().Set("Content-Type", plan.contentType)
		}
		if plan.status == 0 {
			plan.status = http.StatusOK
		}
		w.WriteHeader(plan.status)
		_, _ = io.WriteString(w, plan.body)
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("start local recorder listener")
	}
	srv.Listener = listener
	srv.Start()
	r.server = srv
	t.Cleanup(func() {
		close(r.stop)
		srv.Close()
	})
	return r
}

func (r *round30Recorder) snapshot() []round30CapturedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]round30CapturedRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

type round30DialGuard struct {
	mu          sync.Mutex
	allowed     string
	pinned      string
	allowedDial int
	actualDial  int
	refusedDial int
}

func newRound30Client(t *testing.T, server *httptest.Server) (*http.Client, *http.Transport, *round30DialGuard) {
	t.Helper()
	allowed := strings.TrimPrefix(server.URL, "http://")
	pinned := server.Listener.Addr().String()
	guard := &round30DialGuard{allowed: allowed, pinned: pinned}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			guard.mu.Lock()
			if addr != guard.allowed {
				guard.refusedDial++
				guard.mu.Unlock()
				return nil, errors.New("round30 transport refused a non-recorder target")
			}
			host, _, splitErr := net.SplitHostPort(addr)
			ip, parseErr := netip.ParseAddr(host)
			if splitErr != nil || parseErr != nil || !ip.IsLoopback() || guard.pinned != guard.allowed {
				guard.refusedDial++
				guard.mu.Unlock()
				return nil, errors.New("round30 transport refused an unpinned target")
			}
			guard.allowedDial++
			guard.mu.Unlock()

			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, guard.pinned)
			if err == nil {
				guard.mu.Lock()
				guard.actualDial++
				guard.mu.Unlock()
			}
			return conn, err
		},
	}
	if transport.Proxy != nil {
		t.Fatal("recorder transport must not use a proxy")
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   round30Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return client, transport, guard
}

func (g *round30DialGuard) counts() (allowed, actual, refused int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.allowedDial, g.actualDial, g.refusedDial
}

type round30Scenario struct {
	name       string
	provider   models.NotifyChannelType
	mode       string
	wantPaths  []string
	wantError  bool
	wantCancel bool
}

func TestRound30LocalRecorderAdapters(t *testing.T) {
	cases := []round30Scenario{
		{name: "discord_success", provider: models.NotifyChannelDiscord, mode: "success", wantPaths: []string{"/api/webhooks/ROUND30_ID/ROUND30_DISCORD_TOKEN"}},
		{name: "discord_http_error", provider: models.NotifyChannelDiscord, mode: "http_error", wantPaths: []string{"/api/webhooks/ROUND30_ID/ROUND30_DISCORD_TOKEN"}, wantError: true},
		{name: "discord_timeout", provider: models.NotifyChannelDiscord, mode: "timeout", wantPaths: []string{"/api/webhooks/ROUND30_ID/ROUND30_DISCORD_TOKEN"}, wantError: true, wantCancel: true},
		{name: "telegram_success", provider: models.NotifyChannelTelegram, mode: "success", wantPaths: []string{"/botROUND30_TELEGRAM_TOKEN/sendPhoto"}},
		{name: "telegram_http_error", provider: models.NotifyChannelTelegram, mode: "http_error", wantPaths: []string{"/botROUND30_TELEGRAM_TOKEN/sendPhoto"}, wantError: true},
		{name: "telegram_timeout", provider: models.NotifyChannelTelegram, mode: "timeout", wantPaths: []string{"/botROUND30_TELEGRAM_TOKEN/sendPhoto"}, wantError: true, wantCancel: true},
		{name: "whatsapp_success", provider: models.NotifyChannelWhatsApp, mode: "success", wantPaths: []string{"/v21.0/ROUND30_PHONE_ID/media", "/v21.0/ROUND30_PHONE_ID/messages"}},
		{name: "whatsapp_media_error_stops", provider: models.NotifyChannelWhatsApp, mode: "media_error", wantPaths: []string{"/v21.0/ROUND30_PHONE_ID/media"}, wantError: true},
		{name: "whatsapp_media_timeout_stops", provider: models.NotifyChannelWhatsApp, mode: "media_timeout", wantPaths: []string{"/v21.0/ROUND30_PHONE_ID/media"}, wantError: true, wantCancel: true},
		{name: "whatsapp_messages_error", provider: models.NotifyChannelWhatsApp, mode: "messages_error", wantPaths: []string{"/v21.0/ROUND30_PHONE_ID/media", "/v21.0/ROUND30_PHONE_ID/messages"}, wantError: true},
		{name: "whatsapp_messages_timeout", provider: models.NotifyChannelWhatsApp, mode: "messages_timeout", wantPaths: []string{"/v21.0/ROUND30_PHONE_ID/media", "/v21.0/ROUND30_PHONE_ID/messages"}, wantError: true, wantCancel: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := newRound30Recorder(t, func(count int, path string) round30Response {
				if strings.HasSuffix(tc.mode, "timeout") && ((strings.HasPrefix(tc.mode, "media_") && strings.HasSuffix(path, "/media")) || (tc.mode == "timeout" && count == 1) || (tc.mode == "messages_timeout" && strings.HasSuffix(path, "/messages"))) {
					return round30Response{wait: true}
				}
				if tc.mode == "timeout" {
					return round30Response{wait: true}
				}
				if tc.provider == models.NotifyChannelWhatsApp {
					if strings.HasSuffix(path, "/media") {
						if tc.mode == "media_error" {
							return round30Response{status: http.StatusBadGateway, contentType: "application/json", body: `{"error":{"message":"local simulated error"}}`}
						}
						return round30Response{status: http.StatusOK, contentType: "application/json", body: `{"id":"ROUND30_LOCAL_MEDIA_ID"}`}
					}
					if tc.mode == "messages_error" {
						return round30Response{status: http.StatusBadGateway, contentType: "application/json", body: `{"error":{"message":"local simulated error"}}`}
					}
					return round30Response{status: http.StatusOK, contentType: "application/json", body: `{"messages":[{"id":"ROUND30_LOCAL_WAMID"}]}`}
				}
				if tc.mode == "http_error" {
					if tc.provider == models.NotifyChannelTelegram {
						return round30Response{status: http.StatusBadGateway, contentType: "application/json", body: `{"ok":false,"description":"local simulated error"}`}
					}
					return round30Response{status: http.StatusBadGateway, contentType: "application/json", body: `{"message":"local simulated error"}`}
				}
				if tc.provider == models.NotifyChannelTelegram {
					return round30Response{status: http.StatusOK, contentType: "application/json", body: `{"ok":true,"result":{"message_id":1,"chat":{"id":42},"photo":[{"file_id":"local"}]}}`}
				}
				return round30Response{status: http.StatusNoContent}
			})
			client, transport, guard := newRound30Client(t, recorder.server)
			channel := round30Channel(tc.provider, recorder.server.URL)
			sender, err := New(channel, client, WithAllowCustomEndpoints(true))
			if err != nil {
				t.Fatal("construct real notify adapter")
			}
			message := Message{
				Title: "Round30 local proof", Text: "Synthetic chart payload", Severity: SeverityCritical,
				Fields: []Field{{Name: "Host", Value: "local-fixture"}}, Image: round30ChartPNG, ImageName: "chart.png",
			}
			started := time.Now()
			sendErr := sender.Send(context.Background(), message)
			elapsed := time.Since(started)
			if (sendErr != nil) != tc.wantError {
				t.Fatalf("send error presence=%t want=%t", sendErr != nil, tc.wantError)
			}
			if tc.wantCancel {
				select {
				case <-recorder.cancelSeen:
				case <-time.After(2 * time.Second):
					t.Fatal("timeout recorder did not observe request cancellation")
				}
				if elapsed > 2*time.Second {
					t.Fatalf("client timeout was not bounded: elapsed=%s", elapsed)
				}
			}
			records := recorder.snapshot()
			if len(records) != len(tc.wantPaths) {
				t.Fatalf("request count=%d want=%d", len(records), len(tc.wantPaths))
			}
			for i, request := range records {
				if request.method != http.MethodPost || request.path != tc.wantPaths[i] {
					t.Fatalf("request[%d] method/path did not match expected route", i)
				}
				assertRound30Auth(t, tc.provider, request)
			}
			if tc.mode == "success" {
				assertRound30Payload(t, tc.provider, records)
			}
			allowed, actual, refused := guard.counts()
			if allowed == 0 || actual == 0 || refused != 0 {
				t.Fatalf("guard dial evidence invalid: allowed=%d pinned=%d refused=%d", allowed, actual, refused)
			}
			if transport.Proxy != nil {
				t.Fatal("recorder client unexpectedly has proxy fallback")
			}
			t.Logf("provider=%s case=%s requests=%d pinned_dials=%d external_dials=0", tc.provider, tc.mode, len(records), actual)
		})
	}
}

func round30Channel(provider models.NotifyChannelType, base string) models.NotifyChannel {
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
	return models.NotifyChannel{Type: provider, Config: config}
}

func assertRound30Auth(t *testing.T, provider models.NotifyChannelType, request round30CapturedRequest) {
	t.Helper()
	got := request.header.Get("Authorization")
	switch provider {
	case models.NotifyChannelWhatsApp:
		if got != "Bearer ROUND30_WHATSAPP_TOKEN" {
			t.Fatal("WhatsApp request did not use the configured bearer authentication")
		}
	default:
		if got != "" {
			t.Fatal("Discord/Telegram unexpectedly used Authorization")
		}
	}
}

func assertRound30Payload(t *testing.T, provider models.NotifyChannelType, records []round30CapturedRequest) {
	t.Helper()
	switch provider {
	case models.NotifyChannelDiscord:
		fields := parseRound30Multipart(t, records[0])
		var payload struct {
			Embeds []struct {
				Image struct {
					URL string `json:"url"`
				} `json:"image"`
			} `json:"embeds"`
		}
		if err := json.Unmarshal([]byte(fields.values["payload_json"]), &payload); err != nil || len(payload.Embeds) != 1 || payload.Embeds[0].Image.URL != "attachment://chart.png" {
			t.Fatal("Discord payload_json did not reference the chart embed")
		}
		assertRound30File(t, fields, "files[0]")
	case models.NotifyChannelTelegram:
		fields := parseRound30Multipart(t, records[0])
		if fields.values["chat_id"] != "42" || fields.values["parse_mode"] != "HTML" || !strings.Contains(fields.values["caption"], "Round30 local proof") {
			t.Fatal("Telegram sendPhoto fields did not include the configured destination/HTML caption")
		}
		assertRound30File(t, fields, "photo")
	case models.NotifyChannelWhatsApp:
		media := parseRound30Multipart(t, records[0])
		if media.values["messaging_product"] != "whatsapp" {
			t.Fatal("WhatsApp media multipart was missing messaging_product")
		}
		assertRound30File(t, media, "file")
		var body struct {
			To       string `json:"to"`
			Type     string `json:"type"`
			Template struct {
				Name       string `json:"name"`
				Components []struct {
					Type       string `json:"type"`
					Parameters []struct {
						Type  string `json:"type"`
						Image struct {
							ID string `json:"id"`
						} `json:"image"`
					} `json:"parameters"`
				} `json:"components"`
			} `json:"template"`
		}
		if err := json.Unmarshal(records[1].body, &body); err != nil || body.To != "15550001111" || body.Type != "template" || body.Template.Name != "round30_local_template" || len(body.Template.Components) == 0 || body.Template.Components[0].Type != "header" || len(body.Template.Components[0].Parameters) == 0 || body.Template.Components[0].Parameters[0].Image.ID != "ROUND30_LOCAL_MEDIA_ID" {
			t.Fatal("WhatsApp template did not reference the media ID returned by the recorder")
		}
	}
}

type round30Multipart struct {
	values map[string]string
	files  map[string]round30File
}

type round30File struct {
	name string
	data []byte
}

func parseRound30Multipart(t *testing.T, request round30CapturedRequest) round30Multipart {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(request.header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatal("expected multipart/form-data request")
	}
	r := multipart.NewReader(bytes.NewReader(request.body), params["boundary"])
	out := round30Multipart{values: make(map[string]string), files: make(map[string]round30File)}
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("parse multipart request")
		}
		data, readErr := io.ReadAll(io.LimitReader(part, 1<<20))
		_ = part.Close()
		if readErr != nil {
			t.Fatal("read multipart part")
		}
		if filename := part.FileName(); filename != "" {
			out.files[part.FormName()] = round30File{name: filename, data: data}
		} else {
			out.values[part.FormName()] = string(data)
		}
	}
	return out
}

func assertRound30File(t *testing.T, fields round30Multipart, name string) {
	t.Helper()
	file, ok := fields.files[name]
	if !ok || file.name != "chart.png" || !bytes.Equal(file.data, round30ChartPNG) {
		t.Fatalf("multipart chart file %q missing or changed", name)
	}
}

func TestRound30LocalRecorderTransportDenials(t *testing.T) {
	t.Run("redirect is not followed", func(t *testing.T) {
		recorder := newRound30Recorder(t, func(int, string) round30Response {
			return round30Response{status: http.StatusTemporaryRedirect, location: "http://example.invalid/round30-redirect"}
		})
		client, _, guard := newRound30Client(t, recorder.server)
		sender, err := New(round30Channel(models.NotifyChannelDiscord, recorder.server.URL), client, WithAllowCustomEndpoints(true))
		if err != nil {
			t.Fatal("construct real Discord adapter")
		}
		if err := sender.Send(context.Background(), Message{Title: "redirect fixture", Image: round30ChartPNG, ImageName: "chart.png"}); err == nil {
			t.Fatal("redirect response unexpectedly succeeded")
		}
		if got := recorder.snapshot(); len(got) != 1 {
			t.Fatalf("redirect request count=%d want=1", len(got))
		}
		_, actual, refused := guard.counts()
		if actual != 1 || refused != 0 {
			t.Fatalf("redirect attempted another target: pinned=%d refused=%d", actual, refused)
		}
		t.Log("redirect=not-followed recorder_requests=1 external_dials=0")
	})

	t.Run("external target refused before dialing", func(t *testing.T) {
		recorder := newRound30Recorder(t, func(int, string) round30Response {
			return round30Response{status: http.StatusNoContent}
		})
		client, transport, guard := newRound30Client(t, recorder.server)
		channel := models.NotifyChannel{Type: models.NotifyChannelDiscord, Config: map[string]string{
			"webhook_url": "http://external.example.invalid/api/webhooks/ROUND30_ID/ROUND30_DISCORD_TOKEN",
		}}
		sender, err := New(channel, client, WithAllowCustomEndpoints(true))
		if err != nil {
			t.Fatal("construct real Discord adapter for transport-denial check")
		}
		if err := sender.Send(context.Background(), Message{Title: "denial fixture", Image: round30ChartPNG, ImageName: "chart.png"}); err == nil {
			t.Fatal("non-recorder target unexpectedly succeeded")
		}
		allowed, actual, refused := guard.counts()
		if allowed != 0 || actual != 0 || refused != 1 || len(recorder.snapshot()) != 0 {
			t.Fatalf("external refusal evidence invalid: allowed=%d pinned=%d refused=%d recorder_requests=%d", allowed, actual, refused, len(recorder.snapshot()))
		}
		if transport.Proxy != nil {
			t.Fatal("proxy fallback was configured")
		}
		t.Log("external_target=refused_before_dial recorder_requests=0 proxy=nil fallback=none")
	})
}

func TestRound30MultipartPartNamesAreParsedAsMIME(t *testing.T) {
	// Guard a small parsing detail used by the wire assertions: filenames and
	// field names come from MIME Content-Disposition, not payload text.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="files[0]"; filename="chart.png"`)
	part, err := w.CreatePart(h)
	if err != nil {
		t.Fatal("create fixture multipart part")
	}
	_, _ = part.Write(round30ChartPNG)
	_ = w.Close()
	header := make(http.Header)
	header.Set("Content-Type", w.FormDataContentType())
	parsed := parseRound30Multipart(t, round30CapturedRequest{header: header, body: body.Bytes()})
	assertRound30File(t, parsed, "files[0]")
}
