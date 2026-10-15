package main

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

var round30BridgeChart = []byte("\x89PNG\r\n\x1a\nround30-bridge-local-fixture")

type round30BridgeRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

type round30BridgeRecorder struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []round30BridgeRequest
}

func newRound30BridgeRecorder(t *testing.T) *round30BridgeRecorder {
	t.Helper()
	recorder := &round30BridgeRecorder{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, round30BridgeRequest{method: r.Method, path: r.URL.RequestURI(), header: r.Header.Clone(), body: body})
		recorder.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/media"):
			_, _ = io.WriteString(w, `{"id":"ROUND30_BRIDGE_MEDIA_ID"}`)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = io.WriteString(w, `{"messages":[{"id":"ROUND30_BRIDGE_WAMID"}]}`)
		case strings.Contains(r.URL.Path, "/api/webhooks/"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/sendPhoto"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":42},"photo":[{"file_id":"local"}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("start bridge recorder listener")
	}
	server.Listener = listener
	server.Start()
	recorder.server = server
	t.Cleanup(server.Close)
	return recorder
}

func (r *round30BridgeRecorder) snapshot() []round30BridgeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]round30BridgeRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

type round30BridgeDialStats struct {
	mu     sync.Mutex
	dials  int
	denied int
}

func round30BridgeClient(t *testing.T, server *httptest.Server) (*http.Client, *http.Transport, *round30BridgeDialStats) {
	t.Helper()
	allowed := strings.TrimPrefix(server.URL, "http://")
	pinned := server.Listener.Addr().String()
	stats := &round30BridgeDialStats{}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		stats.mu.Lock()
		if addr != allowed {
			stats.denied++
			stats.mu.Unlock()
			return nil, errors.New("round30 bridge transport refused non-recorder target")
		}
		host, _, err := net.SplitHostPort(addr)
		ip, parseErr := netip.ParseAddr(host)
		if err != nil || parseErr != nil || !ip.IsLoopback() || addr != pinned {
			stats.denied++
			stats.mu.Unlock()
			return nil, errors.New("round30 bridge transport refused unpinned target")
		}
		stats.mu.Unlock()
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, pinned)
		if err == nil {
			stats.mu.Lock()
			stats.dials++
			stats.mu.Unlock()
		}
		return conn, err
	}}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if transport.Proxy != nil {
		t.Fatal("bridge transport must not use a proxy")
	}
	t.Cleanup(transport.CloseIdleConnections)
	return client, transport, stats
}

func TestRound30NotifySenderBridgeToLocalRecorder(t *testing.T) {
	for _, provider := range []models.NotifyChannelType{models.NotifyChannelDiscord, models.NotifyChannelTelegram, models.NotifyChannelWhatsApp} {
		t.Run(string(provider), func(t *testing.T) {
			recorder := newRound30BridgeRecorder(t)
			client, transport, stats := round30BridgeClient(t, recorder.server)
			channel := round30BridgeChannel(provider, recorder.server.URL)
			sender, err := notify.New(channel, client, notify.WithAllowCustomEndpoints(true))
			if err != nil {
				t.Fatal("construct real notify adapter for bridge")
			}
			bridge := notifySenderBridge{sender: sender}
			message := alerting.Message{
				Title: "Round30 bridge proof", Text: "local bridge payload", Severity: alerting.SeverityWarning,
				Fields: []alerting.Field{{Name: "Host", Value: "local-only"}}, URL: "https://dashboard.invalid/local",
				Image: round30BridgeChart, ImageName: "chart.png",
			}
			if err := bridge.Send(context.Background(), message); err != nil {
				t.Fatal("real notify bridge send failed")
			}
			requests := recorder.snapshot()
			wantPaths := round30BridgePaths(provider)
			if len(requests) != len(wantPaths) {
				t.Fatalf("bridge request count=%d want=%d", len(requests), len(wantPaths))
			}
			for i, request := range requests {
				if request.method != http.MethodPost || request.path != wantPaths[i] {
					t.Fatalf("bridge request[%d] method/path mismatch", i)
				}
				if provider == models.NotifyChannelWhatsApp {
					if request.header.Get("Authorization") != "Bearer ROUND30_BRIDGE_WHATSAPP_TOKEN" {
						t.Fatal("WhatsApp bridge request missing bearer auth")
					}
				} else if request.header.Get("Authorization") != "" {
					t.Fatal("Discord/Telegram bridge request unexpectedly had Authorization")
				}
			}
			if provider == models.NotifyChannelWhatsApp {
				var body struct {
					Template struct {
						Components []struct {
							Parameters []struct {
								Image struct {
									ID string `json:"id"`
								} `json:"image"`
							} `json:"parameters"`
						} `json:"components"`
					} `json:"template"`
				}
				if err := json.Unmarshal(requests[1].body, &body); err != nil || len(body.Template.Components) == 0 || len(body.Template.Components[0].Parameters) == 0 || body.Template.Components[0].Parameters[0].Image.ID != "ROUND30_BRIDGE_MEDIA_ID" {
					t.Fatal("WhatsApp bridge message did not reference its local upload")
				}
			}
			stats.mu.Lock()
			gotDials, gotDenied := stats.dials, stats.denied
			stats.mu.Unlock()
			if gotDials == 0 || gotDenied != 0 || transport.Proxy != nil {
				t.Fatalf("bridge guard evidence invalid: pinned=%d refused=%d proxy=%t", gotDials, gotDenied, transport.Proxy != nil)
			}
			t.Logf("provider=%s bridge=notifySenderBridge adapter=notify.New requests=%d pinned_dials=%d external_dials=0", provider, len(requests), gotDials)
		})
	}
}

func round30BridgeChannel(provider models.NotifyChannelType, base string) models.NotifyChannel {
	config := map[string]string{}
	switch provider {
	case models.NotifyChannelDiscord:
		config["webhook_url"] = base + "/api/webhooks/ROUND30_BRIDGE_ID/ROUND30_BRIDGE_DISCORD_TOKEN"
	case models.NotifyChannelTelegram:
		config["bot_token"] = "ROUND30_BRIDGE_TELEGRAM_TOKEN"
		config["chat_id"] = "42"
		config["api_base"] = base
	case models.NotifyChannelWhatsApp:
		config["access_token"] = "ROUND30_BRIDGE_WHATSAPP_TOKEN"
		config["phone_number_id"] = "ROUND30_BRIDGE_PHONE_ID"
		config["to"] = "15550001111"
		config["api_base"] = base
		config["template_name"] = "round30_bridge_template"
	}
	return models.NotifyChannel{Type: provider, Config: config}
}

func round30BridgePaths(provider models.NotifyChannelType) []string {
	switch provider {
	case models.NotifyChannelDiscord:
		return []string{"/api/webhooks/ROUND30_BRIDGE_ID/ROUND30_BRIDGE_DISCORD_TOKEN"}
	case models.NotifyChannelTelegram:
		return []string{"/botROUND30_BRIDGE_TELEGRAM_TOKEN/sendPhoto"}
	default:
		return []string{"/v21.0/ROUND30_BRIDGE_PHONE_ID/media", "/v21.0/ROUND30_BRIDGE_PHONE_ID/messages"}
	}
}

func TestRound30NotifyFactoryRejectsIncompleteChannelsWithoutNetwork(t *testing.T) {
	factory := newNotifySenderFactory()
	cases := []models.NotifyChannel{
		{Type: models.NotifyChannelDiscord, Config: map[string]string{}},
		{Type: models.NotifyChannelTelegram, Config: map[string]string{}},
		{Type: models.NotifyChannelWhatsApp, Config: map[string]string{}},
	}
	for _, channel := range cases {
		sender, err := factory(channel)
		if err == nil || sender != nil {
			t.Fatalf("factory unexpectedly built an adapter from incomplete %s configuration", channel.Type)
		}
	}
	t.Log("factory=actual cmd/hub factory validation only network_calls=0 endpoints=none")
}

func TestRound30BridgeMultipartContainsMappedImage(t *testing.T) {
	// The bridge copies the image bytes and filename without replacing the
	// real adapter; make that mapping observable on an actual multipart send.
	recorder := newRound30BridgeRecorder(t)
	client, _, _ := round30BridgeClient(t, recorder.server)
	sender, err := notify.New(round30BridgeChannel(models.NotifyChannelDiscord, recorder.server.URL), client, notify.WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatal("construct local Discord adapter")
	}
	bridge := notifySenderBridge{sender: sender}
	if err := bridge.Send(context.Background(), alerting.Message{Title: "mapped", Image: round30BridgeChart, ImageName: "chart.png"}); err != nil {
		t.Fatal("send through actual bridge")
	}
	request := recorder.snapshot()[0]
	mediaType, params, err := mime.ParseMediaType(request.header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatal("bridge did not produce multipart adapter payload")
	}
	mr := multipart.NewReader(bytes.NewReader(request.body), params["boundary"])
	found := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal("read bridge multipart")
		}
		data, _ := io.ReadAll(part)
		if part.FormName() == "files[0]" && part.FileName() == "chart.png" && bytes.Equal(data, round30BridgeChart) {
			found = true
		}
		_ = part.Close()
	}
	if !found {
		t.Fatal("bridge did not preserve chart bytes into files[0]")
	}
	t.Log("bridge_field_mapping=title+image+filename real_adapter=notify.New")
}
