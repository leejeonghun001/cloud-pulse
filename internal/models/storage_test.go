package models

import (
	"encoding/json"
	"testing"
)

func TestStorageAccount_Redacted(t *testing.T) {
	a := StorageAccount{
		ID:       1,
		Provider: StorageAccountGoogleDrive,
		Name:     "user@example.com",
		Config:   map[string]string{"client_id": "abc123"},
		Secret:   map[string]string{"client_secret": "shh", "refresh_token": "shh2"},
	}
	got := a.Redacted()
	if got.Secret != nil {
		t.Fatalf("Redacted().Secret = %#v, want nil", got.Secret)
	}
	if got.Config["client_id"] != "abc123" {
		t.Errorf("Redacted() must not touch Config, got %#v", got.Config)
	}
	// Original must be unmodified.
	if a.Secret == nil {
		t.Error("Redacted() must not mutate the receiver's Secret map")
	}
}

func TestStorageQuota_UsedPercent(t *testing.T) {
	tests := []struct {
		name string
		q    StorageQuota
		want float64
	}{
		{"unlimited", StorageQuota{UsedBytes: 100, Unlimited: true}, 0},
		{"zero_limit", StorageQuota{UsedBytes: 100, LimitBytes: 0}, 0},
		{"half", StorageQuota{UsedBytes: 50, LimitBytes: 100}, 50},
		{"over", StorageQuota{UsedBytes: 150, LimitBytes: 100}, 150},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.q.UsedPercent(); got != tt.want {
				t.Errorf("UsedPercent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidStorageInterval(t *testing.T) {
	tests := []struct {
		v    StorageInterval
		want bool
	}{
		{StorageInterval15m, true},
		{StorageInterval1h, true},
		{StorageInterval6h, true},
		{StorageInterval24h, true},
		{StorageInterval(""), false},
		{StorageInterval("2h"), false},
	}
	for _, tt := range tests {
		if got := ValidStorageInterval(tt.v); got != tt.want {
			t.Errorf("ValidStorageInterval(%q) = %v, want %v", tt.v, got, tt.want)
		}
	}
}

func TestStorageAccountStale(t *testing.T) {
	const hour = 3600
	tests := []struct {
		name          string
		lastSuccessAt int64
		now           int64
		interval      StorageInterval
		want          bool
	}{
		{"never_succeeded", 0, 10 * hour, StorageInterval1h, false},
		{"fresh", 1000, 1000 + hour, StorageInterval1h, false},
		{"stale", 1000, 1000 + 3*hour, StorageInterval1h, true},
		{"exactly_2x_not_stale", 1000, 1000 + 2*hour, StorageInterval1h, false},
		{"invalid_interval", 1000, 1000 + 100*hour, StorageInterval("bogus"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StorageAccountStale(tt.lastSuccessAt, tt.now, tt.interval); got != tt.want {
				t.Errorf("StorageAccountStale(%d, %d, %q) = %v, want %v", tt.lastSuccessAt, tt.now, tt.interval, got, tt.want)
			}
		})
	}
}

func TestStorageAccountSnapshot_JSONRoundTrip(t *testing.T) {
	snap := StorageAccountSnapshot{
		AccountID:    1,
		Status:       StorageAccountOK,
		AccountEmail: "user@example.com",
		Quota:        StorageQuota{UsedBytes: 100, LimitBytes: 1000},
		CollectedAt:  100,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got StorageAccountSnapshot
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != snap {
		t.Errorf("round-trip mismatch: got %#v, want %#v", got, snap)
	}
}

func TestStorageOAuthStartResponse_JSONRoundTrip(t *testing.T) {
	resp := StorageOAuthStartResponse{
		Flow:            "device",
		VerificationURL: "https://example.invalid/device",
		UserCode:        "ABCD-1234",
		ExpiresInSec:    1800,
		PollIntervalSec: 5,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got StorageOAuthStartResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != resp {
		t.Errorf("round-trip mismatch: got %#v, want %#v", got, resp)
	}
}
