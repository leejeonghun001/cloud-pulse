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

func whatsappChannel(apiBase string, extra map[string]string) models.NotifyChannel {
	cfg := map[string]string{
		"access_token":    "TEST-ACCESS-TOKEN",
		"phone_number_id": "1234567890",
		"to":              "15551234567",
		"api_base":        apiBase,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return models.NotifyChannel{ID: 1, Name: "test whatsapp", Type: models.NotifyChannelWhatsApp, Enabled: true, Config: cfg}
}

// whatsappTestServer wires up a fake Graph API: media upload returns
// a fixed media id, and message send captures the JSON body.
func whatsappTestServer(t *testing.T, onMessage func(body map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/media"):
			_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil {
				t.Errorf("parse content type: %v", err)
				return
			}
			mr := multipart.NewReader(r.Body, params["boundary"])
			var sawMessagingProduct, sawFile bool
			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				switch part.FormName() {
				case "messaging_product":
					data, _ := io.ReadAll(part)
					if string(data) == "whatsapp" {
						sawMessagingProduct = true
					}
				case "file":
					sawFile = true
					_, _ = io.ReadAll(part)
				}
			}
			if !sawMessagingProduct || !sawFile {
				t.Errorf("media upload missing messaging_product or file field")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"MEDIA123"}`))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode message body: %v", err)
				return
			}
			if onMessage != nil {
				onMessage(body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.1"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestWhatsAppSender_ImageMessage(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	srv := whatsappTestServer(t, func(body map[string]any) { gotBody = body })
	defer srv.Close()

	sender, err := New(whatsappChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = sender.Send(t.Context(), Message{Title: "Host down", Text: "host-1 offline", Image: []byte{1, 2, 3}, ImageName: "chart.png"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotBody["type"] != "image" {
		t.Errorf("type = %v, want image", gotBody["type"])
	}
	if gotBody["to"] != "15551234567" {
		t.Errorf("to = %v, want 15551234567", gotBody["to"])
	}
	img, _ := gotBody["image"].(map[string]any)
	if img["id"] != "MEDIA123" {
		t.Errorf("image.id = %v, want MEDIA123", img["id"])
	}
	caption, _ := img["caption"].(string)
	if !strings.Contains(caption, "Host down") {
		t.Errorf("caption = %q, want it to contain the title", caption)
	}
}

func TestWhatsAppSender_TemplateMessage(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	srv := whatsappTestServer(t, func(body map[string]any) { gotBody = body })
	defer srv.Close()

	sender, err := New(whatsappChannel(srv.URL, map[string]string{"template_name": "alert_chart"}), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = sender.Send(t.Context(), Message{Title: "Host down", Text: "host-1 offline", Image: []byte{1, 2, 3}, ImageName: "chart.png"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotBody["type"] != "template" {
		t.Errorf("type = %v, want template", gotBody["type"])
	}
	tmpl, _ := gotBody["template"].(map[string]any)
	if tmpl["name"] != "alert_chart" {
		t.Errorf("template.name = %v, want alert_chart", tmpl["name"])
	}
	lang, _ := tmpl["language"].(map[string]any)
	if lang["code"] != whatsappDefaultTemplateLang {
		t.Errorf("template.language.code = %v, want %s", lang["code"], whatsappDefaultTemplateLang)
	}
	components, _ := tmpl["components"].([]any)
	if len(components) != 2 {
		t.Fatalf("template.components length = %d, want 2 (header, body)", len(components))
	}
	header, _ := components[0].(map[string]any)
	if header["type"] != "header" {
		t.Errorf("components[0].type = %v, want header", header["type"])
	}
	headerParams, _ := header["parameters"].([]any)
	headerParam0, _ := headerParams[0].(map[string]any)
	headerImage, _ := headerParam0["image"].(map[string]any)
	if headerImage["id"] != "MEDIA123" {
		t.Errorf("header image id = %v, want MEDIA123", headerImage["id"])
	}
	body, _ := components[1].(map[string]any)
	if body["type"] != "body" {
		t.Errorf("components[1].type = %v, want body", body["type"])
	}
}

func TestWhatsAppSender_RequiresImage(t *testing.T) {
	t.Parallel()

	srv := whatsappTestServer(t, nil)
	defer srv.Close()

	sender, err := New(whatsappChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = sender.Send(t.Context(), Message{Title: "t"})
	if err == nil {
		t.Fatal("Send() error = nil, want error for a message with no image")
	}
}

func TestWhatsAppSender_MediaUploadRateLimited(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"OAuthException","code":4}}`))
	}))
	defer srv.Close()

	sender, err := New(whatsappChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = sender.Send(t.Context(), Message{Title: "t", Image: []byte{1}})
	var rae *RetryAfterError
	if !asRetryAfterError(err, &rae) {
		t.Fatalf("Send() error = %v, want *RetryAfterError", err)
	}
	if rae.After.Seconds() != 12 {
		t.Errorf("RetryAfterError.After = %v, want 12s", rae.After)
	}
}

func TestWhatsAppSender_MessageSendError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/media") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"MEDIA123"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid parameter","type":"OAuthException","code":100}}`))
	}))
	defer srv.Close()

	sender, err := New(whatsappChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = sender.Send(t.Context(), Message{Title: "t", Image: []byte{1}})
	if err == nil || !strings.Contains(err.Error(), "Invalid parameter") {
		t.Errorf("Send() error = %v, want it to contain the Graph API error message", err)
	}
}

func TestValidateWhatsAppConfig_FieldErrors(t *testing.T) {
	t.Parallel()

	errs := validateWhatsAppConfig(map[string]string{})
	for _, field := range []string{"access_token", "phone_number_id", "to"} {
		if _, ok := errs[field]; !ok {
			t.Errorf("missing %s field error", field)
		}
	}
}

func TestValidateWhatsAppConfig_ToMustBeDigitsOnly(t *testing.T) {
	t.Parallel()

	errs := validateWhatsAppConfig(map[string]string{
		"access_token":    "t",
		"phone_number_id": "1",
		"to":              "+1 (555) 123-4567",
	})
	if _, ok := errs["to"]; !ok {
		t.Errorf("expected a \"to\" field error for a non-digits-only value")
	}
}

func TestWhatsAppSender_New_DefaultVersionAndLang(t *testing.T) {
	t.Parallel()

	ch := models.NotifyChannel{
		Type: models.NotifyChannelWhatsApp,
		Config: map[string]string{
			"access_token":    "t",
			"phone_number_id": "1",
			"to":              "15551234567",
		},
	}
	sender, err := New(ch, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ws := sender.(*whatsappSender)
	if ws.apiVersion != whatsappDefaultAPIVersion {
		t.Errorf("apiVersion = %q, want %q", ws.apiVersion, whatsappDefaultAPIVersion)
	}
	if ws.templateLang != whatsappDefaultTemplateLang {
		t.Errorf("templateLang = %q, want %q", ws.templateLang, whatsappDefaultTemplateLang)
	}
}

func TestTemplateParamText_EmptyBecomesSpace(t *testing.T) {
	t.Parallel()

	if got := templateParamText(""); got != " " {
		t.Errorf("templateParamText(\"\") = %q, want a single space", got)
	}
	if got := templateParamText("x"); got != "x" {
		t.Errorf("templateParamText(%q) = %q, want unchanged", "x", got)
	}
}
