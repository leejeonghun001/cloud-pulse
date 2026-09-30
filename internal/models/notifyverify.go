package models

// NotifyVerifyPlatform identifies a notification platform targeted by
// `cloud-pulse-hub notify verify` (SPEC-v0.7 §2).
type NotifyVerifyPlatform string

// Supported notify-verify platforms.
const (
	NotifyVerifyDiscord  NotifyVerifyPlatform = "discord"
	NotifyVerifyTelegram NotifyVerifyPlatform = "telegram"
	NotifyVerifyWhatsApp NotifyVerifyPlatform = "whatsapp"
)

// NotifyVerifyStatus is one platform's outcome from a verify run.
type NotifyVerifyStatus string

// Supported notify-verify statuses (SPEC-v0.7 §2's
// "accepted"/"verified"/"failed" plus "skipped" for a platform with no
// credentials supplied).
const (
	// NotifyVerifyAccepted means the platform's API accepted the send
	// (2xx) but read-back could not be performed or was inconclusive.
	NotifyVerifyAccepted NotifyVerifyStatus = "accepted"
	// NotifyVerifyVerified means the platform accepted the send AND a
	// read-back check confirmed the message/attachment exists.
	NotifyVerifyVerified NotifyVerifyStatus = "verified"
	// NotifyVerifyFailed means the send itself failed, or a read-back
	// check found the message/attachment missing/mismatched.
	NotifyVerifyFailed NotifyVerifyStatus = "failed"
	// NotifyVerifySkipped means no credentials were supplied for this
	// platform, so it was not attempted.
	NotifyVerifySkipped NotifyVerifyStatus = "skipped"
)

// NotifyVerifyResult is one platform's outcome from a
// `cloud-pulse-hub notify verify` / scripts/verify-notify.py run.
// Every field is guaranteed secret-free (see DiagnoseError's
// redaction rule) — safe to print, log, or emit as --json.
type NotifyVerifyResult struct {
	Platform NotifyVerifyPlatform `json:"platform"`
	Status   NotifyVerifyStatus   `json:"status"`
	// MessageID/ChatID/MediaID identify the sent artifact for read-back
	// and --cleanup, when applicable to Platform (e.g. Discord's
	// message_id, Telegram's message_id, WhatsApp's media id).
	MessageID string `json:"message_id,omitempty"`
	ChatID    string `json:"chat_id,omitempty"`
	MediaID   string `json:"media_id,omitempty"`
	// AttachmentVerified reports whether a chart-image attachment was
	// confirmed present on read-back (Discord only; other platforms
	// leave this false/omitted since their APIs don't expose that
	// check per SPEC-v0.7 §2's platform limitations table).
	AttachmentVerified bool `json:"attachment_verified,omitempty"`
	// Cleaned reports whether a --cleanup delete/deleteMessage call
	// succeeded, omitted when --cleanup was not requested.
	Cleaned *bool `json:"cleaned,omitempty"`
	// Diagnosis is populated on Status == failed, using the same
	// secret-free classification as alert delivery (see Diagnosis).
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
	// Detail is a short human-readable summary, never a secret.
	Detail string `json:"detail,omitempty"`
}

// NotifyVerifyReport is the top-level output of a verify run across
// every requested platform (the --json output shape).
type NotifyVerifyReport struct {
	Results []NotifyVerifyResult `json:"results"`
	// OK is true only if every attempted (non-skipped) platform
	// reached verified or accepted status.
	OK bool `json:"ok"`
}
