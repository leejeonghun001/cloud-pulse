package notify

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func discordChannel(webhookURL string) models.NotifyChannel {
	return models.NotifyChannel{
		ID:      1,
		Name:    "test discord",
		Type:    models.NotifyChannelDiscord,
		Enabled: true,
		Config:  map[string]string{"webhook_url": webhookURL},
	}
}

func TestDiscordSender_Send_MultipartStructure(t *testing.T) {
	t.Parallel()

	var gotContentType string
	var gotPayload map[string]any
	var gotFileBytes []byte
	var gotFileName string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_, params, err := mime.ParseMediaType(gotContentType)
		if err != nil {
			t.Errorf("parse content type: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("read multipart part: %v", err)
				return
			}
			switch part.FormName() {
			case "payload_json":
				data, _ := io.ReadAll(part)
				if err := json.Unmarshal(data, &gotPayload); err != nil {
					t.Errorf("unmarshal payload_json: %v", err)
				}
			case "files[0]":
				gotFileName = part.FileName()
				gotFileBytes, _ = io.ReadAll(part)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender, err := New(discordChannel(srv.URL), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	image := []byte{0x89, 0x50, 0x4e, 0x47} // fake PNG magic bytes, content doesn't matter for this test
	err = sender.Send(t.Context(), Message{
		Title:     "Host down",
		Text:      "host-1 has been offline for 2 minutes",
		Severity:  SeverityCritical,
		Fields:    []Field{{Name: "Host", Value: "host-1"}},
		URL:       "https://hub.example/#/hosts/host-1",
		Image:     image,
		ImageName: "chart.png",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want multipart/form-data prefix", gotContentType)
	}
	embeds, _ := gotPayload["embeds"].([]any)
	if len(embeds) != 1 {
		t.Fatalf("payload_json embeds = %v, want exactly 1", embeds)
	}
	embed := embeds[0].(map[string]any)
	if embed["title"] != "Host down" {
		t.Errorf("embed title = %v, want %q", embed["title"], "Host down")
	}
	imgField, _ := embed["image"].(map[string]any)
	if imgField["url"] != "attachment://chart.png" {
		t.Errorf("embed image url = %v, want attachment://chart.png", imgField["url"])
	}
	if gotFileName != "chart.png" {
		t.Errorf("files[0] filename = %q, want chart.png", gotFileName)
	}
	if string(gotFileBytes) != string(image) {
		t.Errorf("files[0] content mismatch")
	}
}

func TestDiscordSender_Send_NoImage_NoFilePart(t *testing.T) {
	t.Parallel()

	sawFilePart := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if part.FormName() == "files[0]" {
				sawFilePart = true
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender, err := New(discordChannel(srv.URL), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := sender.Send(t.Context(), Message{Title: "t", Text: "x"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if sawFilePart {
		t.Errorf("expected no files[0] part when Message has no image")
	}
}

func TestDiscordSender_Send_RateLimited(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited","retry_after":3}`))
	}))
	defer srv.Close()

	sender, err := New(discordChannel(srv.URL), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = sender.Send(t.Context(), Message{Title: "t"})
	var rae *RetryAfterError
	if !asRetryAfterError(err, &rae) {
		t.Fatalf("Send() error = %v, want *RetryAfterError", err)
	}
	if rae.After.Seconds() != 3 {
		t.Errorf("RetryAfterError.After = %v, want 3s", rae.After)
	}
}

func TestDiscordSender_Send_ErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Invalid Webhook Token"}`))
	}))
	defer srv.Close()

	sender, err := New(discordChannel(srv.URL), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = sender.Send(t.Context(), Message{Title: "t"})
	if err == nil {
		t.Fatal("Send() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "Invalid Webhook Token") {
		t.Errorf("Send() error = %v, want it to contain the platform error message", err)
	}
}

func TestDiscordSender_New_RejectsNonOfficialHostWithoutFlag(t *testing.T) {
	t.Parallel()

	_, err := New(discordChannel("https://evil.example.com/webhooks/1/abc"), nil)
	if err == nil {
		t.Fatal("New() error = nil, want rejection of non-official host")
	}
}

func TestDiscordSender_New_AcceptsOfficialHosts(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"discord.com", "discordapp.com"} {
		ch := discordChannel("https://" + host + "/api/webhooks/123/abc")
		if _, err := New(ch, nil); err != nil {
			t.Errorf("New() for host %q error = %v, want nil", host, err)
		}
	}
}

func TestDiscordSender_New_MissingWebhookURL(t *testing.T) {
	t.Parallel()

	ch := models.NotifyChannel{Type: models.NotifyChannelDiscord, Config: map[string]string{}}
	if _, err := New(ch, nil); err == nil {
		t.Fatal("New() error = nil, want validation error for missing webhook_url")
	}
}

func TestValidateDiscordConfig_FieldErrors(t *testing.T) {
	t.Parallel()

	errs := validateDiscordConfig(map[string]string{})
	if _, ok := errs["webhook_url"]; !ok {
		t.Errorf("validateDiscordConfig({}) missing webhook_url field error")
	}
}

// asRetryAfterError is a small helper avoiding an import of errors.As
// boilerplate in every test.
func asRetryAfterError(err error, target **RetryAfterError) bool {
	rae, ok := err.(*RetryAfterError)
	if !ok {
		return false
	}
	*target = rae
	return true
}
