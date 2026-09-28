package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WebhookNotifier delivers notifications by POSTing a JSON payload to a
// webhook URL. The payload includes "text", "content", and "title"
// fields so a single webhook works for both Slack ("text") and Discord
// ("content").
type WebhookNotifier struct {
	// URL is the webhook endpoint to POST to.
	URL string
	// Client is the HTTP client used to deliver notifications. If nil, a
	// client with a 10s timeout is used.
	Client *http.Client
}

// webhookPayload is the JSON body sent to the webhook URL.
type webhookPayload struct {
	Text    string `json:"text"`
	Content string `json:"content"`
	Title   string `json:"title"`
}

// Notify sends title and message to the configured webhook URL as a
// JSON POST. A non-2xx response is treated as an error.
func (n WebhookNotifier) Notify(ctx context.Context, title, message string) error {
	body, err := json.Marshal(webhookPayload{Text: message, Content: message, Title: title})
	if err != nil {
		return fmt.Errorf("hub: marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("hub: build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("hub: send webhook notification: %w", err)
	}
	defer func() {
		_ = resp.Body.Close() // best-effort close; response body content is discarded either way
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("hub: webhook notification failed: status %d", resp.StatusCode)
	}
	return nil
}
