package notify

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func webhookChannel(url string, extra map[string]string) models.NotifyChannel {
	cfg := map[string]string{"url": url}
	for k, v := range extra {
		cfg[k] = v
	}
	return models.NotifyChannel{ID: 1, Name: "test webhook", Type: models.NotifyChannelWebhook, Enabled: true, Config: cfg}
}

func TestWebhookSender_Send_JSONPayload(t *testing.T) {
	t.Parallel()

	var gotPayload webhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender, err := New(webhookChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = sender.Send(t.Context(), Message{
		Title:    "Egress warning",
		Text:     "host-1 is at 82% of its outbound limit",
		Severity: SeverityWarning,
		Fields:   []Field{{Name: "Host", Value: "host-1"}},
		URL:      "https://hub.example/#/hosts/host-1",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotPayload.Title != "Egress warning" {
		t.Errorf("Title = %q, want Egress warning", gotPayload.Title)
	}
	if gotPayload.Text != gotPayload.Content {
		t.Errorf("Text and Content should be equal for Slack/Discord compat, got %q vs %q", gotPayload.Text, gotPayload.Content)
	}
	if gotPayload.Severity != "warning" {
		t.Errorf("Severity = %q, want warning", gotPayload.Severity)
	}
	if len(gotPayload.Fields) != 1 || gotPayload.Fields[0].Name != "Host" {
		t.Errorf("Fields = %+v, want [{Host host-1}]", gotPayload.Fields)
	}
	if gotPayload.ImagePNGBase64 != "" {
		t.Errorf("ImagePNGBase64 should be empty when include_image is not set")
	}
}

func TestWebhookSender_Send_IncludeImage(t *testing.T) {
	t.Parallel()

	var gotPayload webhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender, err := New(webhookChannel(srv.URL, map[string]string{"include_image": "true"}), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	image := []byte{1, 2, 3, 4, 5}
	if err := sender.Send(t.Context(), Message{Title: "t", Image: image}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(gotPayload.ImagePNGBase64)
	if err != nil {
		t.Fatalf("decode ImagePNGBase64: %v", err)
	}
	if string(decoded) != string(image) {
		t.Errorf("decoded image mismatch")
	}
}

func TestWebhookSender_Send_ExcludeImageByDefault(t *testing.T) {
	t.Parallel()

	var gotPayload webhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender, err := New(webhookChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := sender.Send(t.Context(), Message{Title: "t", Image: []byte{1, 2, 3}}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotPayload.ImagePNGBase64 != "" {
		t.Errorf("ImagePNGBase64 should be empty when include_image is not explicitly enabled")
	}
}

func TestWebhookSender_RateLimited(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	sender, err := New(webhookChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = sender.Send(t.Context(), Message{Title: "t"})
	var rae *RetryAfterError
	if !asRetryAfterError(err, &rae) {
		t.Fatalf("Send() error = %v, want *RetryAfterError", err)
	}
	if rae.After.Seconds() != 9 {
		t.Errorf("RetryAfterError.After = %v, want 9s", rae.After)
	}
}

func TestWebhookSender_ErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	sender, err := New(webhookChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := sender.Send(t.Context(), Message{Title: "t"}); err == nil {
		t.Fatal("Send() error = nil, want non-nil for a 500 response")
	}
}

func TestWebhookSender_New_RequiresHTTPS(t *testing.T) {
	t.Parallel()

	if _, err := New(webhookChannel("http://example.com/hook", nil), nil); err == nil {
		t.Fatal("New() error = nil, want rejection of a plain-http webhook url in production mode")
	}
}

func TestValidateWebhookConfig_FieldErrors(t *testing.T) {
	t.Parallel()

	errs := validateWebhookConfig(map[string]string{})
	if _, ok := errs["url"]; !ok {
		t.Errorf("missing url field error")
	}

	errs = validateWebhookConfig(map[string]string{"url": "http://example.com"})
	if _, ok := errs["url"]; !ok {
		t.Errorf("expected a url field error for a non-https url")
	}
}
