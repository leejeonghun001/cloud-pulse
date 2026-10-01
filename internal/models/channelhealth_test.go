package models

import (
	"encoding/json"
	"testing"
)

func TestChannelHealthSettingsJSON(t *testing.T) {
	t.Parallel()

	s := ChannelHealthSettings{
		ChannelID:   3,
		Enabled:     true,
		Weekday:     1,
		MinuteOfDay: 540,
		LastRunAt:   1700000000,
		UpdatedAt:   1700000100,
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ChannelHealthSettings
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded != s {
		t.Errorf("round trip = %+v, want %+v", decoded, s)
	}
}

func TestSyntheticResultJSON_DetailAndDiagnosisOmittedWhenEmpty(t *testing.T) {
	t.Parallel()

	r := SyntheticResult{ID: 1, ChannelID: 2, StartedAt: 100, FinishedAt: 105, Outcome: SyntheticAccepted}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["diagnosis_code"]; ok {
		t.Errorf("diagnosis_code must be omitted when empty, got %s", b)
	}
	if _, ok := raw["detail"]; ok {
		t.Errorf("detail must be omitted when empty, got %s", b)
	}
}

func TestSyntheticResultJSON_FailedRoundTrip(t *testing.T) {
	t.Parallel()

	r := SyntheticResult{
		ID:         1,
		ChannelID:  2,
		StartedAt:  100,
		FinishedAt: 105,
		Outcome:    SyntheticFailed,
		Diagnosis:  DiagnosisTimeout,
		Detail:     "The request did not complete within the delivery timeout.",
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded SyntheticResult
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded != r {
		t.Errorf("round trip = %+v, want %+v", decoded, r)
	}
}

func TestChannelHealthJSON_RecentSyntheticEmptyEncodedAsArray(t *testing.T) {
	t.Parallel()

	ch := ChannelHealth{ChannelID: 1, ChannelName: "c", ChannelType: NotifyChannelDiscord, State: ChannelUnknown}
	b, err := json.Marshal(ch)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if string(raw["recent_synthetic"]) != "null" && string(raw["recent_synthetic"]) != "[]" {
		t.Errorf("recent_synthetic = %s, want null or []", raw["recent_synthetic"])
	}
	if _, ok := raw["settings"]; ok {
		t.Errorf("settings must be omitted when nil, got %s", b)
	}
	if _, ok := raw["last_synthetic"]; ok {
		t.Errorf("last_synthetic must be omitted when nil, got %s", b)
	}
}

func TestChannelHealthReportJSON_BannerChannelNamesOmittedWhenEmpty(t *testing.T) {
	t.Parallel()

	report := ChannelHealthReport{Channels: []ChannelHealth{}, BannerNeeded: false}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["banner_channel_names"]; ok {
		t.Errorf("banner_channel_names must be omitted when empty, got %s", b)
	}
}

func TestDeliveryJSON_DurationMSOmittedWhenZero(t *testing.T) {
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
	if _, ok := raw["duration_ms"]; ok {
		t.Errorf("duration_ms must be omitted when zero, got %s", b)
	}
}

func TestDeliveryJSON_DurationMSRoundTrip(t *testing.T) {
	t.Parallel()

	d := Delivery{ChannelID: 1, ChannelName: "c", OK: true, At: 100, DurationMS: 250}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Delivery
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.DurationMS != 250 {
		t.Errorf("DurationMS = %d, want 250", decoded.DurationMS)
	}
}
