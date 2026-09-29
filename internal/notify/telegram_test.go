package notify

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func telegramChannel(apiBase string, extra map[string]string) models.NotifyChannel {
	cfg := map[string]string{
		"bot_token": "123456:TEST-TOKEN",
		"chat_id":   "-100123456",
		"api_base":  apiBase,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return models.NotifyChannel{ID: 1, Name: "test telegram", Type: models.NotifyChannelTelegram, Enabled: true, Config: cfg}
}

func TestTelegramSender_SendMessage_HTMLEscaped(t *testing.T) {
	t.Parallel()

	var gotForm url.Values
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = sender.Send(t.Context(), Message{
		Title: "<script>alert(1)</script>",
		Text:  "5 > 3 & 2 < 4",
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if !strings.HasSuffix(gotPath, "/sendMessage") {
		t.Errorf("request path = %q, want suffix /sendMessage", gotPath)
	}
	text := gotForm.Get("text")
	if strings.Contains(text, "<script>") {
		t.Errorf("text = %q, HTML was not escaped", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") {
		t.Errorf("text = %q, want escaped <script> tag", text)
	}
	if !strings.Contains(text, "&amp;") || !strings.Contains(text, "&lt;") || !strings.Contains(text, "&gt;") {
		t.Errorf("text = %q, want &, <, > all escaped", text)
	}
	if gotForm.Get("parse_mode") != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", gotForm.Get("parse_mode"))
	}
}

func TestTelegramSender_SendPhoto_MultipartWithCaption(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotCaption string
	var gotFileBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("parse content type: %v", err)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			switch part.FormName() {
			case "caption":
				data, _ := io.ReadAll(part)
				gotCaption = string(data)
			case "photo":
				gotFileBytes, _ = io.ReadAll(part)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	image := []byte{1, 2, 3, 4}
	err = sender.Send(t.Context(), Message{Title: "Host down", Image: image, ImageName: "chart.png"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if !strings.HasSuffix(gotPath, "/sendPhoto") {
		t.Errorf("request path = %q, want suffix /sendPhoto", gotPath)
	}
	if !strings.Contains(gotCaption, "Host down") {
		t.Errorf("caption = %q, want it to contain the title", gotCaption)
	}
	if string(gotFileBytes) != string(image) {
		t.Errorf("photo part content mismatch")
	}
}

func TestTelegramSender_SendPhoto_CaptionTruncatedAt1024(t *testing.T) {
	t.Parallel()

	var gotCaption string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if part.FormName() == "caption" {
				data, _ := io.ReadAll(part)
				gotCaption = string(data)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	longText := strings.Repeat("x", 2000)
	err = sender.Send(t.Context(), Message{Title: "t", Text: longText, Image: []byte{1}, ImageName: "c.png"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len([]rune(gotCaption)) > telegramCaptionLimit {
		t.Errorf("caption length = %d, want <= %d", len([]rune(gotCaption)), telegramCaptionLimit)
	}
}

func TestTelegramSender_MessageThreadID(t *testing.T) {
	t.Parallel()

	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, map[string]string{"message_thread_id": "42"}), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := sender.Send(t.Context(), Message{Title: "t"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotForm.Get("message_thread_id") != "42" {
		t.Errorf("message_thread_id = %q, want 42", gotForm.Get("message_thread_id"))
	}
}

func TestTelegramSender_RateLimited(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = sender.Send(t.Context(), Message{Title: "t"})
	var rae *RetryAfterError
	if !asRetryAfterError(err, &rae) {
		t.Fatalf("Send() error = %v, want *RetryAfterError", err)
	}
	if rae.After.Seconds() != 7 {
		t.Errorf("RetryAfterError.After = %v, want 7s", rae.After)
	}
}

func TestTelegramSender_ErrorDescription(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = sender.Send(t.Context(), Message{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("Send() error = %v, want it to contain the platform description", err)
	}
}

func TestTelegramSender_New_RejectsCustomAPIBaseWithoutFlag(t *testing.T) {
	t.Parallel()

	ch := telegramChannel("https://internal.example.com", nil)
	if _, err := New(ch, nil); err == nil {
		t.Fatal("New() error = nil, want rejection of api_base override without the SSRF-guard flag")
	}
}

func TestTelegramSender_New_DefaultAPIBase(t *testing.T) {
	t.Parallel()

	ch := models.NotifyChannel{
		Type:   models.NotifyChannelTelegram,
		Config: map[string]string{"bot_token": "123456:TEST-TOKEN", "chat_id": "1"},
	}
	if _, err := New(ch, nil); err != nil {
		t.Errorf("New() error = %v, want nil for default api_base", err)
	}
}

func TestValidateTelegramConfig_FieldErrors(t *testing.T) {
	t.Parallel()

	errs := validateTelegramConfig(map[string]string{})
	if _, ok := errs["bot_token"]; !ok {
		t.Errorf("missing bot_token field error")
	}
	if _, ok := errs["chat_id"]; !ok {
		t.Errorf("missing chat_id field error")
	}
}

func TestValidateTelegramConfig_BadThreadID(t *testing.T) {
	t.Parallel()

	errs := validateTelegramConfig(map[string]string{
		"bot_token":         "123456:TEST-TOKEN",
		"chat_id":           "1",
		"message_thread_id": "not-a-number",
	})
	if _, ok := errs["message_thread_id"]; !ok {
		t.Errorf("expected message_thread_id field error for non-integer value")
	}
}

func TestEscapeHTML(t *testing.T) {
	t.Parallel()

	got := escapeHTML(`<a> & "b"`)
	want := `&lt;a&gt; &amp; "b"`
	if got != want {
		t.Errorf("escapeHTML() = %q, want %q", got, want)
	}
}

func TestTruncateCaption_ShortStringUnchanged(t *testing.T) {
	t.Parallel()

	s := "short"
	if got := truncateCaption(s); got != s {
		t.Errorf("truncateCaption(%q) = %q, want unchanged", s, got)
	}
}

// jsonRoundTrip guards against accidental drift in telegramResponse's
// field tags.
func TestTelegramResponse_JSONFields(t *testing.T) {
	t.Parallel()

	var r telegramResponse
	if err := json.Unmarshal([]byte(`{"ok":true,"description":"d","parameters":{"retry_after":5}}`), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !r.OK || r.Description != "d" || r.Parameters == nil || r.Parameters.RetryAfter != 5 {
		t.Errorf("unexpected decode: %+v", r)
	}
}
