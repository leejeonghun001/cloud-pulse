package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDiagnosisJSON(t *testing.T) {
	t.Parallel()

	d := Diagnosis{
		Code:    DiagnosisTelegramChatNotFound,
		Title:   "Chat not found",
		Detail:  `Telegram returned 400 "chat not found"`,
		Hint:    "Confirm the chat_id and message the bot first.",
		DocsURL: "https://example.invalid/docs/telegram",
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{"code", "title", "detail", "hint", "docs_url"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing JSON field %q in %s", field, b)
		}
	}

	var decoded Diagnosis
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, d) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, d)
	}
}

func TestTestResultJSON_Success(t *testing.T) {
	t.Parallel()

	tr := TestResult{OK: true, Delivery: "sent sendMessage to chat -1001"}
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["diagnosis"]; ok {
		t.Errorf("expected diagnosis to be omitted on success, got %s", b)
	}

	var decoded TestResult
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, tr) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, tr)
	}
}

func TestTestResultJSON_Failure(t *testing.T) {
	t.Parallel()

	tr := TestResult{
		OK: false,
		Diagnosis: &Diagnosis{
			Code:  DiagnosisConnectionRefused,
			Title: "Connection refused",
		},
	}
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded TestResult
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, tr) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, tr)
	}
}

func TestDiagnosisCodes_NoSecretLikeContentByConstruction(t *testing.T) {
	// Not a runtime check (codes are static string constants) — this
	// test exists to document and enumerate the full supported set so
	// a future addition is deliberate, matching SPEC-v0.6 §4's table.
	t.Parallel()

	codes := []DiagnosisCode{
		DiagnosisDNSFailure, DiagnosisNetworkUnreachable, DiagnosisConnectionRefused,
		DiagnosisTimeout, DiagnosisTLSError, DiagnosisInvalidConfig,
		DiagnosisDiscordWebhookGone, DiagnosisDiscordUnauthorized,
		DiagnosisTelegramUnauthorized, DiagnosisTelegramChatNotFound,
		DiagnosisTelegramBotBlocked, DiagnosisTelegramNotMember, DiagnosisTelegramThreadMissing,
		DiagnosisWhatsAppTokenInvalid, DiagnosisWhatsAppPermission, DiagnosisWhatsAppRecipientDenied,
		DiagnosisWhatsAppWindowClosed, DiagnosisWhatsAppTemplateMissing, DiagnosisWhatsAppMediaFailed,
		DiagnosisRateLimited, DiagnosisPlatformError,
	}
	seen := make(map[DiagnosisCode]bool, len(codes))
	for _, c := range codes {
		if c == "" {
			t.Error("diagnosis code must not be empty")
		}
		if seen[c] {
			t.Errorf("duplicate diagnosis code %q", c)
		}
		seen[c] = true
	}
	if len(codes) != 21 {
		t.Errorf("expected 21 documented diagnosis codes, got %d", len(codes))
	}
}
