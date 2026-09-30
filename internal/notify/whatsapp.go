package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// whatsappDefaultAPIBase is the Meta Graph API's official base URL
// (https://developers.facebook.com/docs/whatsapp/cloud-api/reference/media
// and .../messages). A channel-config "api_base" override is only
// honored when the SSRF guard has been relaxed.
const whatsappDefaultAPIBase = "https://graph.facebook.com"

// whatsappDefaultAPIVersion is used when a channel doesn't specify
// "api_version".
const whatsappDefaultAPIVersion = "v21.0"

// whatsappDefaultTemplateLang is used when a channel doesn't specify
// "template_lang".
const whatsappDefaultTemplateLang = "en_US"

// whatsappSender delivers Messages via the WhatsApp Cloud API: upload
// the chart PNG to POST /{phone_number_id}/media, then send either a
// template message (header image + body params, works any time) or a
// plain image message (only works inside the 24-hour customer-service
// window).
type whatsappSender struct {
	apiBase       string
	apiVersion    string
	accessToken   string
	phoneNumberID string
	to            string
	templateName  string
	templateLang  string
	client        *http.Client
}

// validateWhatsAppConfig checks "access_token", "phone_number_id",
// "to"; "template_name"/"template_lang"/"api_version" are optional.
func validateWhatsAppConfig(cfg map[string]string) map[string]string {
	errs := map[string]string{}
	requireNonEmpty(errs, cfg, "access_token")
	requireNonEmpty(errs, cfg, "phone_number_id")
	requireNonEmpty(errs, cfg, "to")
	if to := cfg["to"]; to != "" {
		for _, r := range to {
			if r < '0' || r > '9' {
				errs["to"] = "must be E.164 digits only (no +, spaces, or punctuation)"
				break
			}
		}
	}
	return errs
}

// newWhatsAppSender builds a Sender for ch.
func newWhatsAppSender(ch models.NotifyChannel, client *http.Client, opts clientOptions) (Sender, error) {
	if errs := validateWhatsAppConfig(ch.Config); len(errs) > 0 {
		return nil, fmt.Errorf("notify: whatsapp: invalid config: %v", errs)
	}
	base := whatsappDefaultAPIBase
	if custom := ch.Config["api_base"]; custom != "" {
		if !opts.allowCustomEndpoints {
			return nil, fmt.Errorf("notify: whatsapp: api_base override requires custom endpoints to be enabled")
		}
		u, err := url.Parse(custom)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("notify: whatsapp: invalid api_base %q", custom)
		}
		base = strings.TrimSuffix(custom, "/")
	} else if !opts.allowCustomEndpoints {
		if _, err := validateEndpointHost(whatsappDefaultAPIBase+"/", whatsappAllowedHosts, opts); err != nil {
			return nil, fmt.Errorf("notify: whatsapp: %w", err)
		}
	}

	version := ch.Config["api_version"]
	if version == "" {
		version = whatsappDefaultAPIVersion
	}
	lang := ch.Config["template_lang"]
	if lang == "" {
		lang = whatsappDefaultTemplateLang
	}

	return &whatsappSender{
		apiBase:       base,
		apiVersion:    version,
		accessToken:   ch.Config["access_token"],
		phoneNumberID: ch.Config["phone_number_id"],
		to:            ch.Config["to"],
		templateName:  ch.Config["template_name"],
		templateLang:  lang,
		client:        client,
	}, nil
}

func (s *whatsappSender) endpoint(path string) string {
	return fmt.Sprintf("%s/%s/%s", s.apiBase, s.apiVersion, path)
}

// graphError mirrors the Graph API's standard error envelope, e.g.
// {"error":{"message":"...","type":"...","code":...}}.
type graphError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// Send implements Sender: uploads msg.Image (required — WhatsApp
// messages are always sent with the chart per SPEC-v0.5 §B) then sends
// either a template or plain image message.
func (s *whatsappSender) Send(ctx context.Context, msg Message) error {
	if !msg.HasImage() {
		return fmt.Errorf("notify: whatsapp: message has no image; whatsapp notifications require a chart image")
	}

	mediaID, err := s.uploadMedia(ctx, msg.Image, msg.ImageName)
	if err != nil {
		return err
	}

	if s.templateName != "" {
		return s.sendTemplate(ctx, mediaID, msg)
	}
	return s.sendImageMessage(ctx, mediaID, msg)
}

// uploadMedia uploads image to POST /{phone_number_id}/media
// (multipart, messaging_product=whatsapp, file, type) and returns the
// resulting media id. See
// https://developers.facebook.com/docs/whatsapp/cloud-api/reference/media#upload-media.
func (s *whatsappSender) uploadMedia(ctx context.Context, image []byte, imageName string) (string, error) {
	if imageName == "" {
		imageName = "chart.png"
	}
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	if err := w.WriteField("messaging_product", "whatsapp"); err != nil {
		return "", fmt.Errorf("notify: whatsapp: write messaging_product field: %w", err)
	}
	part, err := createFormFilePart(w, "file", imageName, "image/png")
	if err != nil {
		return "", fmt.Errorf("notify: whatsapp: create file part: %w", err)
	}
	if _, err := part.Write(image); err != nil {
		return "", fmt.Errorf("notify: whatsapp: write file part: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("notify: whatsapp: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint(s.phoneNumberID+"/media"), buf)
	if err != nil {
		return "", fmt.Errorf("notify: whatsapp: build media request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+s.accessToken)

	resp, err := doRequest(ctx, s.client, req)
	if err != nil {
		return "", fmt.Errorf("notify: whatsapp: %w", err)
	}
	body := readLimitedBody(resp)

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", newRetryAfterError(resp, fmt.Errorf("notify: whatsapp: media upload rate limited: %s", graphErrorMessage(body)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newStatusError("whatsapp", "media_upload", resp.StatusCode, body, graphErrorMessage(body))
	}

	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.ID == "" {
		return "", fmt.Errorf("notify: whatsapp: media upload response missing id: %s", string(body))
	}
	return parsed.ID, nil
}

// sendTemplate sends an approved message template with a header image
// (the uploaded media) and body parameters [title, text], via POST
// /{phone_number_id}/messages. Works outside the 24-hour window (see
// https://developers.facebook.com/docs/whatsapp/cloud-api/guides/send-message-templates).
func (s *whatsappSender) sendTemplate(ctx context.Context, mediaID string, msg Message) error {
	body := map[string]any{
		"messaging_product": "whatsapp",
		"to":                s.to,
		"type":              "template",
		"template": map[string]any{
			"name": s.templateName,
			"language": map[string]string{
				"code": s.templateLang,
			},
			"components": []map[string]any{
				{
					"type": "header",
					"parameters": []map[string]any{
						{
							"type": "image",
							"image": map[string]string{
								"id": mediaID,
							},
						},
					},
				},
				{
					"type": "body",
					"parameters": []map[string]any{
						{"type": "text", "text": templateParamText(msg.Title)},
						{"type": "text", "text": templateParamText(msg.Text)},
					},
				},
			},
		},
	}
	return s.postMessage(ctx, body)
}

// templateParamText returns s, or a single space if empty — WhatsApp
// template body parameters must be non-empty.
func templateParamText(s string) string {
	if s == "" {
		return " "
	}
	return s
}

// sendImageMessage sends a plain type:image message with a caption,
// via POST /{phone_number_id}/messages. Only deliverable inside the
// 24-hour customer-service window (see
// https://developers.facebook.com/docs/whatsapp/cloud-api/messages/image-messages) —
// callers configuring a channel without template_name should be told
// this in the Settings UI's step-by-step guide.
func (s *whatsappSender) sendImageMessage(ctx context.Context, mediaID string, msg Message) error {
	body := map[string]any{
		"messaging_product": "whatsapp",
		"to":                s.to,
		"type":              "image",
		"image": map[string]string{
			"id":      mediaID,
			"caption": whatsappCaption(msg),
		},
	}
	return s.postMessage(ctx, body)
}

// whatsappCaption renders a plain-text caption (WhatsApp image
// captions are not HTML) from msg.
func whatsappCaption(msg Message) string {
	var b strings.Builder
	if msg.Title != "" {
		b.WriteString(msg.Title)
	}
	if msg.Text != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(msg.Text)
	}
	for _, f := range msg.Fields {
		b.WriteString("\n")
		b.WriteString(f.Name)
		b.WriteString(": ")
		b.WriteString(f.Value)
	}
	if msg.URL != "" {
		b.WriteString("\n")
		b.WriteString(msg.URL)
	}
	return b.String()
}

func (s *whatsappSender) postMessage(ctx context.Context, body map[string]any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("notify: whatsapp: marshal message body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint(s.phoneNumberID+"/messages"), bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("notify: whatsapp: build message request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.accessToken)

	resp, err := doRequest(ctx, s.client, req)
	if err != nil {
		return fmt.Errorf("notify: whatsapp: %w", err)
	}
	respBody := readLimitedBody(resp)

	if resp.StatusCode == http.StatusTooManyRequests {
		return newRetryAfterError(resp, fmt.Errorf("notify: whatsapp: rate limited: %s", graphErrorMessage(respBody)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newStatusError("whatsapp", "message", resp.StatusCode, respBody, graphErrorMessage(respBody))
	}
	return nil
}

// graphErrorMessage extracts the Graph API's error.message field,
// falling back to the raw (truncated) body.
func graphErrorMessage(body []byte) string {
	var parsed graphError
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	const maxLen = 500
	if len(body) > maxLen {
		body = body[:maxLen]
	}
	return string(body)
}
