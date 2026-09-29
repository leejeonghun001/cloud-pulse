package notify

import (
	"fmt"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// ValidateConfig checks ch.Config for the fields required by ch.Type,
// returning a map of field name -> error message for every problem
// found (empty map if ch.Config is valid). The hub's API handlers use
// this to populate APIError.Details on a 400 response when creating
// or updating a notify channel.
func ValidateConfig(ch models.NotifyChannel) map[string]string {
	switch ch.Type {
	case models.NotifyChannelDiscord:
		return validateDiscordConfig(ch.Config)
	case models.NotifyChannelTelegram:
		return validateTelegramConfig(ch.Config)
	case models.NotifyChannelWhatsApp:
		return validateWhatsAppConfig(ch.Config)
	case models.NotifyChannelWebhook:
		return validateWebhookConfig(ch.Config)
	default:
		return map[string]string{"type": fmt.Sprintf("unsupported channel type %q", ch.Type)}
	}
}

// requireNonEmpty adds a "required" error for key to errs when
// cfg[key] is empty.
func requireNonEmpty(errs map[string]string, cfg map[string]string, key string) {
	if cfg[key] == "" {
		errs[key] = "required"
	}
}
