package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// webhookSender delivers Messages as a generic JSON POST, compatible
// with Slack ("text") and Discord ("content") simple-webhook payloads
// — the pre-v0.5 behavior this replaces (see internal/hub's old
// WebhookNotifier).
type webhookSender struct {
	url          string
	includeImage bool
	client       *http.Client
}

// validateWebhookConfig checks "url" against the production policy
// (https required, no fixed-host allowlist — a generic webhook is
// explicitly user-configurable to point anywhere the admin chooses).
// "include_image" is optional (parsed as a truthy string, default
// false).
func validateWebhookConfig(cfg map[string]string) map[string]string {
	errs := map[string]string{}
	requireNonEmpty(errs, cfg, "url")
	if cfg["url"] != "" {
		if err := validateGenericWebhookURL(cfg["url"], clientOptions{}); err != nil {
			errs["url"] = err.Error()
		}
	}
	return errs
}

// validateGenericWebhookURL requires https unless opts relaxes it for
// tests (WithAllowCustomEndpoints) — production code always leaves
// opts at its zero value, so a real deployment can never configure a
// plaintext-http generic webhook.
func validateGenericWebhookURL(rawURL string, opts clientOptions) error {
	if rawURL == "" {
		return fmt.Errorf("required")
	}
	if opts.allowCustomEndpoints {
		return nil
	}
	if len(rawURL) < len("https://") || rawURL[:len("https://")] != "https://" {
		return fmt.Errorf("notify: webhook: url must use https")
	}
	return nil
}

// newWebhookSender builds a Sender for ch.
func newWebhookSender(ch models.NotifyChannel, client *http.Client, opts clientOptions) (Sender, error) {
	if ch.Config["url"] == "" {
		return nil, fmt.Errorf("notify: webhook: invalid config: %v", map[string]string{"url": "required"})
	}
	if err := validateGenericWebhookURL(ch.Config["url"], opts); err != nil {
		return nil, fmt.Errorf("notify: webhook: invalid config: %v", map[string]string{"url": err.Error()})
	}
	return &webhookSender{
		url:          ch.Config["url"],
		includeImage: ch.Config["include_image"] == "true" || ch.Config["include_image"] == "1",
		client:       client,
	}, nil
}

// webhookField mirrors Field for JSON encoding.
type webhookField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// webhookPayload is the JSON body sent to the configured URL.
type webhookPayload struct {
	Text           string         `json:"text"`
	Content        string         `json:"content"`
	Title          string         `json:"title"`
	Severity       string         `json:"severity,omitempty"`
	Fields         []webhookField `json:"fields,omitempty"`
	URL            string         `json:"url,omitempty"`
	ImagePNGBase64 string         `json:"image_png_base64,omitempty"`
}

// Send implements Sender.
func (s *webhookSender) Send(ctx context.Context, msg Message) error {
	payload := webhookPayload{
		Text:     msg.Text,
		Content:  msg.Text,
		Title:    msg.Title,
		Severity: string(msg.Severity),
		URL:      msg.URL,
	}
	for _, f := range msg.Fields {
		payload.Fields = append(payload.Fields, webhookField(f))
	}
	if s.includeImage && msg.HasImage() {
		payload.ImagePNGBase64 = base64.StdEncoding.EncodeToString(msg.Image)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notify: webhook: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := doRequest(ctx, s.client, req)
	if err != nil {
		return fmt.Errorf("notify: webhook: %w", err)
	}
	respBody := readLimitedBody(resp)

	if resp.StatusCode == http.StatusTooManyRequests {
		return newRetryAfterError(resp, fmt.Errorf("notify: webhook: rate limited"))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxLen = 500
		if len(respBody) > maxLen {
			respBody = respBody[:maxLen]
		}
		return newStatusError("webhook", "", resp.StatusCode, respBody, string(respBody))
	}
	return nil
}
