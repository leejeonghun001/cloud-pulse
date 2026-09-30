package models

// DiagnosisCode is a machine-readable classification of why a
// notification test send failed (SPEC-v0.6 §4). Produced by
// internal/notify's pure diagnosis mapper from an HTTP status/response
// body or a network error.
type DiagnosisCode string

// Supported diagnosis codes (SPEC-v0.6 §4's table). Kept as an open
// string type (not a restrictive enum check anywhere) since new
// platform-specific codes are expected to be added over time without
// touching this file.
const (
	DiagnosisDNSFailure              DiagnosisCode = "dns_failure"
	DiagnosisNetworkUnreachable      DiagnosisCode = "network_unreachable"
	DiagnosisConnectionRefused       DiagnosisCode = "connection_refused"
	DiagnosisTimeout                 DiagnosisCode = "timeout"
	DiagnosisTLSError                DiagnosisCode = "tls_error"
	DiagnosisInvalidConfig           DiagnosisCode = "invalid_config"
	DiagnosisDiscordWebhookGone      DiagnosisCode = "discord_webhook_not_found"
	DiagnosisDiscordUnauthorized     DiagnosisCode = "discord_unauthorized"
	DiagnosisTelegramUnauthorized    DiagnosisCode = "telegram_unauthorized"
	DiagnosisTelegramChatNotFound    DiagnosisCode = "telegram_chat_not_found"
	DiagnosisTelegramBotBlocked      DiagnosisCode = "telegram_bot_blocked"
	DiagnosisTelegramNotMember       DiagnosisCode = "telegram_not_member"
	DiagnosisTelegramThreadMissing   DiagnosisCode = "telegram_thread_not_found"
	DiagnosisWhatsAppTokenInvalid    DiagnosisCode = "whatsapp_token_invalid"
	DiagnosisWhatsAppPermission      DiagnosisCode = "whatsapp_permission"
	DiagnosisWhatsAppRecipientDenied DiagnosisCode = "whatsapp_recipient_not_allowed"
	DiagnosisWhatsAppWindowClosed    DiagnosisCode = "whatsapp_window_closed"
	DiagnosisWhatsAppTemplateMissing DiagnosisCode = "whatsapp_template_missing"
	DiagnosisWhatsAppMediaFailed     DiagnosisCode = "whatsapp_media_failed"
	DiagnosisRateLimited             DiagnosisCode = "rate_limited"
	DiagnosisPlatformError           DiagnosisCode = "platform_error"
)

// Diagnosis explains why a notification test send failed: a machine
// code plus operator-facing guidance. Never contains a secret value
// (token, webhook URL path, phone number) — producers are responsible
// for redacting Detail/Title before constructing one.
type Diagnosis struct {
	Code   DiagnosisCode `json:"code"`
	Title  string        `json:"title"`
	Detail string        `json:"detail"`
	Hint   string        `json:"hint"`
	// DocsURL links to the relevant setup-guide section, "" if none.
	DocsURL string `json:"docs_url,omitempty"`
}

// TestResult is the response body for
// POST /api/v1/alerts/channels/{id}/test and
// POST /api/v1/alerts/channels/test (SPEC-v0.6 §4).
type TestResult struct {
	OK bool `json:"ok"`
	// Delivery is a short human-readable summary of what was
	// attempted/sent, empty when OK is false and nothing was sent.
	Delivery string `json:"delivery,omitempty"`
	// Diagnosis is present only when OK is false.
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
}
