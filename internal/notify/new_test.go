package notify

import (
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestNew_UnsupportedType(t *testing.T) {
	t.Parallel()

	_, err := New(models.NotifyChannel{Type: "carrier-pigeon"}, nil)
	if err == nil {
		t.Fatal("New() error = nil, want error for an unsupported channel type")
	}
}

func TestNew_DispatchesToEachType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ch   models.NotifyChannel
	}{
		{"discord", discordChannel("https://discord.com/api/webhooks/1/abc")},
		{
			"telegram",
			models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "123456:TEST-TOKEN", "chat_id": "1"}},
		},
		{
			"whatsapp",
			models.NotifyChannel{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "t", "phone_number_id": "1", "to": "15551234567"}},
		},
		{"webhook", webhookChannel("https://example.com/hook", nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sender, err := New(tt.ch, nil)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if sender == nil {
				t.Fatal("New() returned a nil Sender with a nil error")
			}
		})
	}
}
