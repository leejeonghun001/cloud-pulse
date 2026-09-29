package notify

import (
	"fmt"
	"net/http"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// New builds a Sender for ch. client is used for every outbound HTTP
// call; if nil, a client with the shared httpTimeout is used. opts
// customizes SSRF-guard behavior — production callers should build
// opts via WithAllowCustomEndpoints(AllowCustomEndpointsFromEnv(...))
// once and reuse it; tests pass WithAllowCustomEndpoints(true)
// directly rather than mutating the process environment.
func New(ch models.NotifyChannel, client *http.Client, opts ...Option) (Sender, error) {
	var o clientOptions
	for _, opt := range opts {
		opt(&o)
	}

	switch ch.Type {
	case models.NotifyChannelDiscord:
		return newDiscordSender(ch, client, o)
	case models.NotifyChannelTelegram:
		return newTelegramSender(ch, client, o)
	case models.NotifyChannelWhatsApp:
		return newWhatsAppSender(ch, client, o)
	case models.NotifyChannelWebhook:
		return newWebhookSender(ch, client, o)
	default:
		return nil, fmt.Errorf("notify: unsupported channel type %q", ch.Type)
	}
}
