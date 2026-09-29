package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAlertRuleJSON(t *testing.T) {
	t.Parallel()

	rule := AlertRule{
		ID:             1,
		Name:           "CPU above 90% for 5 minutes",
		Enabled:        true,
		Metric:         AlertMetricCPU,
		HostID:         "",
		Operator:       AlertOperatorGT,
		Threshold:      90,
		DurationSec:    300,
		CooldownSec:    3600,
		NotifyResolved: true,
		ChannelIDs:     []int64{1, 2},
		CreatedAt:      100,
		UpdatedAt:      200,
	}
	b, err := json.Marshal(rule)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{
		"id", "name", "enabled", "metric", "host_id", "operator", "threshold",
		"duration_sec", "cooldown_sec", "notify_resolved", "channel_ids",
		"created_at", "updated_at",
	} {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing JSON field %q in %s", field, b)
		}
	}

	var decoded AlertRule
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, rule) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, rule)
	}
}

func TestNotifyChannelJSON(t *testing.T) {
	t.Parallel()

	ch := NotifyChannel{
		ID:      5,
		Name:    "ops-discord",
		Type:    NotifyChannelDiscord,
		Enabled: true,
		Config: map[string]string{
			"webhook_url": "https://discord.com/api/webhooks/123/abc",
		},
		CreatedAt: 100,
		UpdatedAt: 200,
	}
	b, err := json.Marshal(ch)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded NotifyChannel
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, ch) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, ch)
	}
}

func TestNotifyChannelType_SecretFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		typ  NotifyChannelType
		want []string
	}{
		{NotifyChannelDiscord, []string{"webhook_url"}},
		{NotifyChannelTelegram, []string{"bot_token"}},
		{NotifyChannelWhatsApp, []string{"access_token"}},
		{NotifyChannelWebhook, nil},
		{NotifyChannelType("unknown"), nil},
	}
	for _, tt := range tests {
		got := tt.typ.SecretFields()
		if len(got) != len(tt.want) {
			t.Errorf("%s.SecretFields() = %v, want %v", tt.typ, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%s.SecretFields() = %v, want %v", tt.typ, got, tt.want)
				break
			}
		}
	}
}

func TestNotifyChannel_Redacted(t *testing.T) {
	t.Parallel()

	t.Run("discord webhook_url redacted", func(t *testing.T) {
		t.Parallel()
		ch := NotifyChannel{
			Type:   NotifyChannelDiscord,
			Config: map[string]string{"webhook_url": "https://discord.com/api/webhooks/123/secret"},
		}
		got := ch.Redacted()
		if got.Config["webhook_url"] != RedactedConfigValue {
			t.Errorf("webhook_url = %q, want %q", got.Config["webhook_url"], RedactedConfigValue)
		}
		if ch.Config["webhook_url"] == RedactedConfigValue {
			t.Error("Redacted must not mutate the original channel's Config")
		}
	})

	t.Run("telegram preserves non-secret fields", func(t *testing.T) {
		t.Parallel()
		ch := NotifyChannel{
			Type: NotifyChannelTelegram,
			Config: map[string]string{
				"bot_token": "123456:TEST-TOKEN",
				"chat_id":   "-100123456",
			},
		}
		got := ch.Redacted()
		if got.Config["bot_token"] != RedactedConfigValue {
			t.Errorf("bot_token = %q, want redacted", got.Config["bot_token"])
		}
		if got.Config["chat_id"] != "-100123456" {
			t.Errorf("chat_id = %q, want unchanged", got.Config["chat_id"])
		}
	})

	t.Run("webhook has no secret fields", func(t *testing.T) {
		t.Parallel()
		ch := NotifyChannel{
			Type:   NotifyChannelWebhook,
			Config: map[string]string{"url": "https://example.com/hook"},
		}
		got := ch.Redacted()
		if got.Config["url"] != "https://example.com/hook" {
			t.Errorf("url = %q, want unchanged", got.Config["url"])
		}
	})

	t.Run("empty config", func(t *testing.T) {
		t.Parallel()
		ch := NotifyChannel{Type: NotifyChannelDiscord}
		got := ch.Redacted()
		if len(got.Config) != 0 {
			t.Errorf("Config = %v, want empty", got.Config)
		}
	})

	t.Run("empty secret value left empty, not redacted", func(t *testing.T) {
		t.Parallel()
		ch := NotifyChannel{
			Type:   NotifyChannelDiscord,
			Config: map[string]string{"webhook_url": ""},
		}
		got := ch.Redacted()
		if got.Config["webhook_url"] != "" {
			t.Errorf("webhook_url = %q, want empty (not redacted)", got.Config["webhook_url"])
		}
	})
}

func TestAlertEventJSON(t *testing.T) {
	t.Parallel()

	ev := AlertEvent{
		ID:         1,
		RuleID:     2,
		RuleName:   "CPU above 90%",
		HostID:     "host1",
		Hostname:   "host1.local",
		Metric:     AlertMetricCPU,
		State:      AlertEventFiring,
		Value:      93.4,
		Threshold:  90,
		StartedAt:  100,
		NotifiedAt: 105,
		ResolvedAt: 0,
		Deliveries: []Delivery{
			{ChannelID: 1, ChannelName: "ops-discord", OK: true, At: 106},
			{ChannelID: 2, ChannelName: "ops-telegram", OK: false, Error: "timeout", At: 106},
		},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded AlertEvent
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, ev) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, ev)
	}
}

func TestAlertEventEmptyDeliveriesEncodedAsArray(t *testing.T) {
	t.Parallel()

	ev := AlertEvent{ID: 1, Deliveries: []Delivery{}}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if string(raw["deliveries"]) != "[]" {
		t.Errorf("deliveries = %s, want []", raw["deliveries"])
	}
}

func TestDeliveryJSON_ErrorOmittedWhenOK(t *testing.T) {
	t.Parallel()

	d := Delivery{ChannelID: 1, ChannelName: "c", OK: true, At: 100}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["error"]; ok {
		t.Errorf("error must be omitted when OK, got %s", b)
	}
}

func TestAlertStateJSON(t *testing.T) {
	t.Parallel()

	st := AlertState{
		RuleID:       1,
		HostID:       "host1",
		State:        AlertStatePending,
		Since:        100,
		LastNotified: 0,
		LastValue:    91.2,
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded AlertState
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, st) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, st)
	}
}

func TestAlertMetricConstants(t *testing.T) {
	t.Parallel()

	want := map[AlertMetric]string{
		AlertMetricCPU:          "cpu",
		AlertMetricMemory:       "memory",
		AlertMetricDisk:         "disk",
		AlertMetricLoad1:        "load1",
		AlertMetricEgressOutPct: "egress_out_pct",
		AlertMetricEgressInPct:  "egress_in_pct",
		AlertMetricHostDown:     "host_down",
	}
	for metric, str := range want {
		if string(metric) != str {
			t.Errorf("%v = %q, want %q", metric, string(metric), str)
		}
	}
}

func TestAlertRuleState_Severity_HostSnapshotZeroValue(t *testing.T) {
	t.Parallel()

	// HostSnapshot is a plain struct with no methods; this test only
	// verifies it constructs and its zero value is well-formed (nil
	// Latest, empty Egress) since the alerting stage will depend on this
	// shape being stable.
	var snap HostSnapshot
	if snap.Latest != nil {
		t.Errorf("zero-value HostSnapshot.Latest = %+v, want nil", snap.Latest)
	}
}
