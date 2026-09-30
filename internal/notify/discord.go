package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// discordSender delivers Messages to a Discord incoming webhook via
// "Execute Webhook" (multipart/form-data with payload_json + files[0]
// when an image is attached, https://discord.com/developers/docs/resources/webhook#execute-webhook).
type discordSender struct {
	webhookURL string
	client     *http.Client
}

// validateDiscordConfig checks the "webhook_url" field against the
// production SSRF policy (fixed official hosts, https only) — used by
// the hub's API handlers to build APIError.Details on save. Sender
// construction (newDiscordSender) re-validates the host under the
// caller-supplied opts instead of calling this, so tests can relax the
// SSRF guard without this function's fixed-policy check getting in
// the way.
func validateDiscordConfig(cfg map[string]string) map[string]string {
	errs := map[string]string{}
	requireNonEmpty(errs, cfg, "webhook_url")
	if cfg["webhook_url"] != "" {
		if _, err := validateEndpointHost(cfg["webhook_url"], discordAllowedHosts, clientOptions{}); err != nil {
			errs["webhook_url"] = err.Error()
		}
	}
	return errs
}

// newDiscordSender builds a Sender for ch, requiring a non-empty
// webhook_url and SSRF-checking it under opts.
func newDiscordSender(ch models.NotifyChannel, client *http.Client, opts clientOptions) (Sender, error) {
	if ch.Config["webhook_url"] == "" {
		return nil, fmt.Errorf("notify: discord: invalid config: %v", map[string]string{"webhook_url": "required"})
	}
	u, err := validateEndpointHost(ch.Config["webhook_url"], discordAllowedHosts, opts)
	if err != nil {
		return nil, fmt.Errorf("notify: discord: %w", err)
	}
	return &discordSender{webhookURL: u.String(), client: client}, nil
}

// discordEmbed mirrors the subset of Discord's embed object this
// sender populates.
type discordEmbed struct {
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	URL         string             `json:"url,omitempty"`
	Color       int                `json:"color,omitempty"`
	Timestamp   string             `json:"timestamp,omitempty"`
	Fields      []discordField     `json:"fields,omitempty"`
	Image       *discordEmbedImage `json:"image,omitempty"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type discordEmbedImage struct {
	URL string `json:"url"`
}

type discordPayload struct {
	Embeds []discordEmbed `json:"embeds,omitempty"`
}

// discordColorFor maps a Severity to Discord's decimal embed color.
func discordColorFor(sev Severity) int {
	switch sev {
	case SeverityCritical:
		return 0xE74C3C // red
	case SeverityWarning:
		return 0xF39C12 // orange
	case SeverityResolved:
		return 0x2ECC71 // green
	default:
		return 0x3498DB // blue
	}
}

// Send implements Sender.
func (s *discordSender) Send(ctx context.Context, msg Message) error {
	_, err := s.sendWithRef(ctx, msg, false)
	return err
}

// SendWithRef implements RefSender: sends msg exactly as Send does, but
// appends ?wait=true to the webhook URL so Discord's Execute Webhook
// endpoint returns the created message body instead of 204 No Content,
// letting the caller learn the message's id and whether it carries an
// attachment (see
// https://discord.com/developers/docs/resources/webhook#execute-webhook).
func (s *discordSender) SendWithRef(ctx context.Context, msg Message) (MessageRef, error) {
	return s.sendWithRef(ctx, msg, true)
}

func (s *discordSender) sendWithRef(ctx context.Context, msg Message, wait bool) (MessageRef, error) {
	embed := discordEmbed{
		Title:       msg.Title,
		Description: msg.Text,
		URL:         msg.URL,
		Color:       discordColorFor(msg.Severity),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	for _, f := range msg.Fields {
		embed.Fields = append(embed.Fields, discordField{Name: f.Name, Value: f.Value, Inline: true})
	}

	imageName := msg.ImageName
	if imageName == "" {
		imageName = "chart.png"
	}
	if msg.HasImage() {
		embed.Image = &discordEmbedImage{URL: "attachment://" + imageName}
	}

	payload := discordPayload{Embeds: []discordEmbed{embed}}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return MessageRef{}, fmt.Errorf("notify: discord: marshal payload: %w", err)
	}

	body, contentType, err := buildDiscordMultipart(payloadJSON, msg, imageName)
	if err != nil {
		return MessageRef{}, err
	}

	url := s.webhookURL
	if wait {
		if strings.Contains(url, "?") {
			url += "&wait=true"
		} else {
			url += "?wait=true"
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return MessageRef{}, fmt.Errorf("notify: discord: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := doRequest(ctx, s.client, req)
	if err != nil {
		return MessageRef{}, fmt.Errorf("notify: discord: %w", err)
	}
	respBody := readLimitedBody(resp)

	if resp.StatusCode == http.StatusTooManyRequests {
		return MessageRef{}, newRetryAfterError(resp, fmt.Errorf("notify: discord: rate limited: %s", string(respBody)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return MessageRef{}, newStatusError("discord", "", resp.StatusCode, respBody, discordErrorMessage(respBody))
	}
	if !wait {
		return MessageRef{}, nil
	}
	var parsed struct {
		ID          string `json:"id"`
		Attachments []struct {
			ID string `json:"id"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return MessageRef{}, fmt.Errorf("notify: discord: parse wait=true response: %w", err)
	}
	return MessageRef{MessageID: parsed.ID, HasAttachment: len(parsed.Attachments) > 0}, nil
}

// ReadBack implements MessageReader: GETs the message back from Discord's
// webhook-message endpoint and confirms it still carries an attachment
// (https://discord.com/developers/docs/resources/webhook#get-webhook-message).
func (s *discordSender) ReadBack(ctx context.Context, ref MessageRef) (bool, error) {
	if ref.MessageID == "" {
		return false, fmt.Errorf("notify: discord: read back: missing message id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.messageURL(ref.MessageID), nil)
	if err != nil {
		return false, fmt.Errorf("notify: discord: build read-back request: %w", err)
	}
	resp, err := doRequest(ctx, s.client, req)
	if err != nil {
		return false, fmt.Errorf("notify: discord: read back: %w", err)
	}
	body := readLimitedBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, newStatusError("discord", "read_back", resp.StatusCode, body, discordErrorMessage(body))
	}
	var parsed struct {
		Attachments []struct {
			ID string `json:"id"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Errorf("notify: discord: parse read-back response: %w", err)
	}
	return len(parsed.Attachments) > 0, nil
}

// Delete implements MessageDeleter
// (https://discord.com/developers/docs/resources/webhook#delete-webhook-message).
func (s *discordSender) Delete(ctx context.Context, ref MessageRef) error {
	if ref.MessageID == "" {
		return fmt.Errorf("notify: discord: delete: missing message id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.messageURL(ref.MessageID), nil)
	if err != nil {
		return fmt.Errorf("notify: discord: build delete request: %w", err)
	}
	resp, err := doRequest(ctx, s.client, req)
	if err != nil {
		return fmt.Errorf("notify: discord: delete: %w", err)
	}
	body := readLimitedBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newStatusError("discord", "delete", resp.StatusCode, body, discordErrorMessage(body))
	}
	return nil
}

// messageURL builds the GET/DELETE .../messages/{id} URL from the sender's
// webhook URL (which already has the form
// https://discord.com/api/webhooks/{id}/{token}).
func (s *discordSender) messageURL(messageID string) string {
	base := s.webhookURL
	if idx := strings.Index(base, "?"); idx >= 0 {
		base = base[:idx]
	}
	return strings.TrimSuffix(base, "/") + "/messages/" + messageID
}

// discordErrorMessage extracts Discord's "message" field from an
// error response body, falling back to the raw (truncated) body.
func discordErrorMessage(body []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
		return parsed.Message
	}
	const maxLen = 500
	if len(body) > maxLen {
		body = body[:maxLen]
	}
	return string(body)
}

// buildDiscordMultipart builds the multipart/form-data body Discord's
// Execute Webhook endpoint expects: a "payload_json" field, plus
// "files[0]" when msg carries an image.
func buildDiscordMultipart(payloadJSON []byte, msg Message, imageName string) (io.Reader, string, error) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)

	if err := w.WriteField("payload_json", string(payloadJSON)); err != nil {
		return nil, "", fmt.Errorf("notify: discord: write payload_json field: %w", err)
	}

	if msg.HasImage() {
		part, err := createFormFilePart(w, "files[0]", imageName, "image/png")
		if err != nil {
			return nil, "", fmt.Errorf("notify: discord: create file part: %w", err)
		}
		if _, err := part.Write(msg.Image); err != nil {
			return nil, "", fmt.Errorf("notify: discord: write file part: %w", err)
		}
	}

	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("notify: discord: close multipart writer: %w", err)
	}
	return buf, w.FormDataContentType(), nil
}

// createFormFilePart is like multipart.Writer.CreateFormFile but lets
// the caller set an explicit Content-Type instead of the
// extension-sniffed default.
func createFormFilePart(w *multipart.Writer, fieldName, fileName, contentType string) (io.Writer, error) {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, fileName))
	h.Set("Content-Type", contentType)
	return w.CreatePart(h)
}
