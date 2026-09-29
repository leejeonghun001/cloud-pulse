package notify

import (
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestValidateConfig_DispatchesPerType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ch         models.NotifyChannel
		wantFields []string
	}{
		{"discord missing webhook_url", models.NotifyChannel{Type: models.NotifyChannelDiscord, Config: map[string]string{}}, []string{"webhook_url"}},
		{"telegram missing both", models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{}}, []string{"bot_token", "chat_id"}},
		{"whatsapp missing all three", models.NotifyChannel{Type: models.NotifyChannelWhatsApp, Config: map[string]string{}}, []string{"access_token", "phone_number_id", "to"}},
		{"webhook missing url", models.NotifyChannel{Type: models.NotifyChannelWebhook, Config: map[string]string{}}, []string{"url"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			errs := ValidateConfig(tt.ch)
			for _, f := range tt.wantFields {
				if _, ok := errs[f]; !ok {
					t.Errorf("ValidateConfig() missing expected field error %q, got %v", f, errs)
				}
			}
		})
	}
}

func TestValidateConfig_ValidConfigsReturnEmpty(t *testing.T) {
	t.Parallel()

	tests := []models.NotifyChannel{
		{Type: models.NotifyChannelDiscord, Config: map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/abc"}},
		{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "123456:TEST-TOKEN", "chat_id": "1"}},
		{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "t", "phone_number_id": "1", "to": "15551234567"}},
		{Type: models.NotifyChannelWebhook, Config: map[string]string{"url": "https://example.com/hook"}},
	}
	for _, ch := range tests {
		if errs := ValidateConfig(ch); len(errs) != 0 {
			t.Errorf("ValidateConfig(%s) = %v, want empty", ch.Type, errs)
		}
	}
}

func TestValidateConfig_UnsupportedType(t *testing.T) {
	t.Parallel()

	errs := ValidateConfig(models.NotifyChannel{Type: "carrier-pigeon"})
	if _, ok := errs["type"]; !ok {
		t.Errorf("ValidateConfig() for unsupported type missing a \"type\" field error, got %v", errs)
	}
}
