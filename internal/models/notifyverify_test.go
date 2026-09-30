package models

import (
	"encoding/json"
	"testing"
)

func TestNotifyVerifyResult_JSONRoundTrip(t *testing.T) {
	cleaned := true
	code := DiagnosisRateLimited
	result := NotifyVerifyResult{
		Platform:           NotifyVerifyDiscord,
		Status:             NotifyVerifyVerified,
		MessageID:          "123456789",
		AttachmentVerified: true,
		Cleaned:            &cleaned,
		Diagnosis:          &Diagnosis{Code: code, Title: "Rate limited"},
		Detail:             "ok",
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got NotifyVerifyResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Platform != result.Platform || got.Status != result.Status || got.MessageID != result.MessageID {
		t.Errorf("round-trip mismatch: got %#v, want %#v", got, result)
	}
	if got.Cleaned == nil || *got.Cleaned != true {
		t.Errorf("Cleaned round-trip failed: got %v", got.Cleaned)
	}
	if got.Diagnosis == nil || got.Diagnosis.Code != code {
		t.Errorf("Diagnosis round-trip failed: got %#v", got.Diagnosis)
	}
}

func TestNotifyVerifyReport_JSONRoundTrip(t *testing.T) {
	report := NotifyVerifyReport{
		Results: []NotifyVerifyResult{
			{Platform: NotifyVerifyDiscord, Status: NotifyVerifyVerified},
			{Platform: NotifyVerifyTelegram, Status: NotifyVerifySkipped},
		},
		OK: true,
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got NotifyVerifyReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Results) != 2 || got.OK != true {
		t.Errorf("round-trip mismatch: got %#v", got)
	}
}

func TestNotifyVerifyStatus_Values(t *testing.T) {
	// Guard against accidental renames of these string constants, since
	// scripts/verify-notify.py and cmd/hub's `notify verify` CLI must
	// agree on the exact spelling (see SPEC-v0.7 §2).
	tests := map[NotifyVerifyStatus]string{
		NotifyVerifyAccepted: "accepted",
		NotifyVerifyVerified: "verified",
		NotifyVerifyFailed:   "failed",
		NotifyVerifySkipped:  "skipped",
	}
	for constVal, want := range tests {
		if string(constVal) != want {
			t.Errorf("constant = %q, want %q", constVal, want)
		}
	}
}
