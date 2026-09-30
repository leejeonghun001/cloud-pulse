// Package notify's diagnose.go classifies a failed Sender.Send error
// (network-level or a platform's HTTP response) into a machine-
// readable models.Diagnosis (SPEC-v0.6 §4). Pure and side-effect free:
// no network calls, no logging — callers (the hub's channel-test
// routes, internal/alerting's delivery worker) own presenting or
// persisting the result.
//
// Every Diagnosis produced here is built exclusively from platform-
// controlled response text (Discord's "message", Telegram's
// "description", WhatsApp's "error.message"/"error_data.details") or
// from this file's own static guidance strings — never from a
// channel's configured secret (bot token, webhook token, access
// token, phone number). See redactSecretsInText for the defense-in-
// depth belt-and-suspenders pass applied to every Detail string.
package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// windowsWSAECONNREFUSED is Windows' WSAECONNREFUSED numeric value
// (10061) — distinct from Unix's syscall.ECONNREFUSED, and not
// available as a named syscall constant on non-Windows GOOS builds.
// Comparing the underlying errno's Unix-syscall.Errno-compatible
// integer value against this constant lets diagnose.go recognize a
// Windows connection-refused error without a windows-only build-
// tagged file (both platforms' errno types convert to int cleanly).
const windowsWSAECONNREFUSED = 10061

// DiagnoseError classifies a Sender.Send error into a models.Diagnosis
// (SPEC-v0.6 §4). A nil err is not a valid input and returns a zero
// Diagnosis with DiagnosisPlatformError — callers should only invoke
// this when Send has actually returned a non-nil error.
func DiagnoseError(err error) models.Diagnosis {
	if err == nil {
		return models.Diagnosis{
			Code:   models.DiagnosisPlatformError,
			Title:  "Unknown error",
			Detail: "no error was provided to diagnose",
		}
	}

	var raErr *RetryAfterError
	if errors.As(err, &raErr) {
		return diagnoseRateLimited(raErr)
	}

	var statusErr *statusError
	if errors.As(err, &statusErr) {
		return diagnoseStatus(statusErr)
	}

	return diagnoseNetworkError(err)
}

// diagnoseRateLimited builds the rate_limited Diagnosis for a 429
// response, surfacing the resolved retry delay in Hint without ever
// including the underlying error's text (which may echo a platform
// message but never a secret — still passed through
// redactSecretsInText for defense in depth).
func diagnoseRateLimited(raErr *RetryAfterError) models.Diagnosis {
	detail := "The platform asked the sender to slow down (HTTP 429)."
	if raErr.Err != nil {
		detail = redactSecretsInText(raErr.Err.Error())
	}
	return models.Diagnosis{
		Code:   models.DiagnosisRateLimited,
		Title:  "Rate limited",
		Detail: detail,
		Hint:   fmt.Sprintf("Wait about %s before retrying.", raErr.After.Round(time.Second)),
	}
}

// diagnoseNetworkError classifies a transport-level error (no HTTP
// response was ever received) via errors.As/errors.Is down a fixed
// chain: context deadline, DNS, TLS, then connection-refused vs. a
// generic network-unreachable *net.OpError, falling back to
// network_unreachable for anything else.
func diagnoseNetworkError(err error) models.Diagnosis {
	if errors.Is(err, context.DeadlineExceeded) {
		return models.Diagnosis{
			Code:   models.DiagnosisTimeout,
			Title:  "Timed out",
			Detail: "The request did not complete within the delivery timeout.",
			Hint:   "This is often transient network latency; try again. If it persists, check the hub's outbound connectivity.",
		}
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return models.Diagnosis{
			Code:   models.DiagnosisDNSFailure,
			Title:  "DNS resolution failed",
			Detail: redactSecretsInText(fmt.Sprintf("could not resolve %s", dnsErr.Name)),
			Hint:   "Check the hub's DNS resolver and internet connectivity.",
		}
	}

	if isTLSError(err) {
		return models.Diagnosis{
			Code:   models.DiagnosisTLSError,
			Title:  "TLS/certificate error",
			Detail: redactSecretsInText(err.Error()),
			Hint:   "Check the hub host's system CA certificates and system clock.",
		}
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if isConnectionRefused(opErr) {
			return models.Diagnosis{
				Code:   models.DiagnosisConnectionRefused,
				Title:  "Connection refused",
				Detail: redactSecretsInText(opErr.Error()),
				Hint:   "Check firewall/proxy rules and that outbound access to this host is allowed.",
			}
		}
		return models.Diagnosis{
			Code:   models.DiagnosisNetworkUnreachable,
			Title:  "Network unreachable",
			Detail: redactSecretsInText(opErr.Error()),
			Hint:   "Check firewall/proxy rules and outbound network access from the hub.",
		}
	}

	return models.Diagnosis{
		Code:   models.DiagnosisNetworkUnreachable,
		Title:  "Network error",
		Detail: redactSecretsInText(err.Error()),
		Hint:   "Check firewall/proxy rules and outbound network access from the hub.",
	}
}

// isTLSError reports whether err is (or wraps) one of the standard TLS/
// certificate error types: x509.CertificateInvalidError,
// x509.UnknownAuthorityError, x509.HostnameError, or
// tls.RecordHeaderError.
func isTLSError(err error) bool {
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return true
	}
	var recordHeaderErr tls.RecordHeaderError
	return errors.As(err, &recordHeaderErr)
}

// isConnectionRefused reports whether opErr's underlying cause is a
// connection-refused errno on either Unix (syscall.ECONNREFUSED) or
// Windows (WSAECONNREFUSED, 10061) — both compared as plain integers
// so this file needs no windows-only build-tagged variant.
func isConnectionRefused(opErr *net.OpError) bool {
	var errno syscall.Errno
	if errors.As(opErr, &errno) {
		if errno == syscall.ECONNREFUSED {
			return true
		}
		if int(errno) == windowsWSAECONNREFUSED {
			return true
		}
	}
	return false
}

// diagnoseStatus classifies a *statusError (a non-2xx HTTP response
// from a platform, already parsed into a Message) per platform, using
// SPEC-v0.6 §4's table.
func diagnoseStatus(e *statusError) models.Diagnosis {
	switch e.Platform {
	case "discord":
		return diagnoseDiscordStatus(e)
	case "telegram":
		return diagnoseTelegramStatus(e)
	case "whatsapp":
		return diagnoseWhatsAppStatus(e)
	default:
		return diagnosePlatformError(e)
	}
}

// diagnosePlatformError is the fallback for any status/platform this
// file has no specific mapping for: a generic 4xx/5xx with the
// platform's own (already-extracted) message, secret-stripped.
func diagnosePlatformError(e *statusError) models.Diagnosis {
	return models.Diagnosis{
		Code:   models.DiagnosisPlatformError,
		Title:  fmt.Sprintf("%s error (HTTP %d)", displayPlatform(e.Platform), e.StatusCode),
		Detail: redactSecretsInText(e.Message),
		Hint:   "See the platform's response message above for details.",
	}
}

// displayPlatform title-cases a platform key for display, e.g.
// "discord" -> "Discord".
func displayPlatform(p string) string {
	if p == "" {
		return "Platform"
	}
	return strings.ToUpper(p[:1]) + p[1:]
}

// --- Discord ---

// discordErrorBody mirrors Discord's JSON error envelope
// ({"code": <int>, "message": "<string>", ...}) —
// https://discord.com/developers/docs/reference#error-messages.
type discordErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// discordWebhookNotFoundCode is Discord's "Unknown Webhook" error
// code (10015), returned when a webhook has been deleted or never
// existed.
const discordWebhookNotFoundCode = 10015

// discordInvalidTokenCode is Discord's "Invalid Webhook Token" error
// code (50027), returned when the token half of a webhook URL is
// corrupted or no longer valid.
const discordInvalidTokenCode = 50027

func diagnoseDiscordStatus(e *statusError) models.Diagnosis {
	var body discordErrorBody
	_ = json.Unmarshal(e.Body, &body) // best-effort; falls back to e.Message below

	switch {
	case e.StatusCode == http.StatusNotFound || body.Code == discordWebhookNotFoundCode:
		return models.Diagnosis{
			Code:    models.DiagnosisDiscordWebhookGone,
			Title:   "Webhook not found",
			Detail:  "Discord reports this webhook no longer exists (deleted or never created).",
			Hint:    "The webhook was likely deleted or regenerated — create a new one and update this channel.",
			DocsURL: "https://discord.com/developers/docs/resources/webhook",
		}
	case e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden || body.Code == discordInvalidTokenCode:
		return models.Diagnosis{
			Code:    models.DiagnosisDiscordUnauthorized,
			Title:   "Webhook unauthorized",
			Detail:  "Discord rejected the webhook's token.",
			Hint:    "The token portion of the webhook URL is invalid or was revoked — copy a fresh webhook URL.",
			DocsURL: "https://discord.com/developers/docs/resources/webhook",
		}
	default:
		return diagnosePlatformError(e)
	}
}

// --- Telegram ---

// telegramDescriptionPatterns maps a (lower-cased) substring of
// Telegram's "description" field to a diagnosis code, checked in
// order — Telegram does not publish a stable numeric error table for
// these (the Bot API docs note error_code values are subject to
// change), so classification keys on description text per SPEC-v0.6
// §4.
var telegramDescriptionPatterns = []struct {
	substr string
	code   models.DiagnosisCode
	title  string
	hint   string
}{
	{"chat not found", models.DiagnosisTelegramChatNotFound, "Chat not found",
		"Double-check the chat_id, and make sure someone has messaged the bot at least once first."},
	{"message thread not found", models.DiagnosisTelegramThreadMissing, "Message thread not found",
		"Confirm the topic/thread ID in a forum-style group."},
	{"bot was blocked", models.DiagnosisTelegramBotBlocked, "Bot was blocked",
		"The recipient blocked the bot — ask them to unblock it in Telegram."},
	{"not enough rights", models.DiagnosisTelegramNotMember, "Bot lacks permission",
		"Invite the bot to the group, or promote it to admin in the channel."},
	{"kicked", models.DiagnosisTelegramNotMember, "Bot removed from chat",
		"Re-invite the bot to the group or re-add it as a channel admin."},
	{"have no rights", models.DiagnosisTelegramNotMember, "Bot lacks permission",
		"Invite the bot to the group, or promote it to admin in the channel."},
}

func diagnoseTelegramStatus(e *statusError) models.Diagnosis {
	desc := strings.ToLower(e.Message)

	if e.StatusCode == http.StatusUnauthorized || strings.Contains(desc, "unauthorized") {
		return models.Diagnosis{
			Code:    models.DiagnosisTelegramUnauthorized,
			Title:   "Bot token invalid",
			Detail:  "Telegram rejected the bot token.",
			Hint:    "The bot token is wrong or was revoked — generate a fresh one via @BotFather.",
			DocsURL: "https://core.telegram.org/bots/api#authorizing-your-bot",
		}
	}
	for _, p := range telegramDescriptionPatterns {
		if strings.Contains(desc, p.substr) {
			return models.Diagnosis{Code: p.code, Title: p.title, Detail: redactSecretsInText(e.Message), Hint: p.hint}
		}
	}
	// A generic 403 with no matched phrase is still most likely a
	// blocked bot or missing membership — SPEC-v0.4 groups both under
	// the same operator action.
	if e.StatusCode == http.StatusForbidden {
		return models.Diagnosis{
			Code:   models.DiagnosisTelegramBotBlocked,
			Title:  "Bot was blocked or lacks access",
			Detail: redactSecretsInText(e.Message),
			Hint:   "Ask the recipient to unblock the bot, or invite/promote it in the target chat.",
		}
	}
	return diagnosePlatformError(e)
}

// --- WhatsApp ---

// whatsappGraphErrorBody mirrors the Graph API's error envelope,
// {"error": {"message", "type", "code", "error_data": {"details"}}}.
type whatsappGraphErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
		Data    struct {
			Details string `json:"details"`
		} `json:"error_data"`
	} `json:"error"`
}

func diagnoseWhatsAppStatus(e *statusError) models.Diagnosis {
	if e.Stage == "media_upload" {
		return models.Diagnosis{
			Code:    models.DiagnosisWhatsAppMediaFailed,
			Title:   "Chart image upload failed",
			Detail:  redactSecretsInText(e.Message),
			Hint:    "The image may be too large or an unsupported format for WhatsApp's media API.",
			DocsURL: "https://developers.facebook.com/docs/whatsapp/cloud-api/reference/media",
		}
	}

	var body whatsappGraphErrorBody
	_ = json.Unmarshal(e.Body, &body) // best-effort; falls back to e.Message below
	code := body.Error.Code
	detail := body.Error.Data.Details
	if detail == "" {
		detail = body.Error.Message
	}
	if detail == "" {
		detail = e.Message
	}
	detail = redactSecretsInText(detail)

	switch {
	case code == 190:
		return models.Diagnosis{
			Code:    models.DiagnosisWhatsAppTokenInvalid,
			Title:   "Access token expired",
			Detail:  detail,
			Hint:    "Temporary tokens expire after 24 hours — generate a permanent token from a System User for production use.",
			DocsURL: "https://developers.facebook.com/docs/whatsapp/cloud-api/get-started",
		}
	case code == 10 || code == 100 || (code >= 200 && code <= 299):
		return models.Diagnosis{
			Code:    models.DiagnosisWhatsAppPermission,
			Title:   "Permission denied",
			Detail:  detail,
			Hint:    "Grant the app the whatsapp_business_messaging permission for this WhatsApp Business Account.",
			DocsURL: "https://developers.facebook.com/docs/whatsapp/cloud-api/get-started",
		}
	case code == 131030:
		return models.Diagnosis{
			Code:    models.DiagnosisWhatsAppRecipientDenied,
			Title:   "Recipient not allowed",
			Detail:  detail,
			Hint:    "In development mode, add the recipient's number to the test-number allow list (API Setup → \"To\").",
			DocsURL: "https://developers.facebook.com/docs/whatsapp/cloud-api/get-started",
		}
	case code == 131047:
		return models.Diagnosis{
			Code:    models.DiagnosisWhatsAppWindowClosed,
			Title:   "24-hour window closed",
			Detail:  detail,
			Hint:    "More than 24 hours have passed since the recipient last messaged this number — configure and use an approved template message instead.",
			DocsURL: "https://developers.facebook.com/docs/whatsapp/cloud-api/guides/send-message-templates",
		}
	case code == 132000 || code == 132001:
		return models.Diagnosis{
			Code:    models.DiagnosisWhatsAppTemplateMissing,
			Title:   "Template problem",
			Detail:  detail,
			Hint:    "Check the template name, language code, and that it has been approved in Meta's template manager.",
			DocsURL: "https://developers.facebook.com/docs/whatsapp/cloud-api/guides/send-message-templates",
		}
	case e.StatusCode == http.StatusTooManyRequests:
		return models.Diagnosis{
			Code:   models.DiagnosisRateLimited,
			Title:  "Rate limited",
			Detail: detail,
			Hint:   "Wait and retry; consider spacing out sends.",
		}
	default:
		return diagnosePlatformError(e)
	}
}

// --- Secret-free detail text ---

// secretLikePatterns matches substrings that could leak a credential
// if a platform ever echoed back part of the request (none of
// Discord/Telegram/Bot-API/Graph currently do — this is a defense-in-
// depth pass, not the primary guarantee, which comes from these
// platforms' own generic error phrasing never including the secret
// itself):
//   - a bot-token shape, "<digits>:<20+ token chars>" (Telegram)
//   - "Bearer <token>" (Authorization-header style)
//   - a discord webhook URL's id/token path segment
var secretLikePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b\d{6,}:[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._-]{10,}`),
	regexp.MustCompile(`(?i)https?://(discord(app)?\.com)/api/webhooks/\d+/[A-Za-z0-9_-]+`),
}

// redactSecretsInText strips any secret-shaped substring from s,
// replacing it with "(redacted)". Applied to every Diagnosis
// Title/Detail/Hint string built from platform or network error text
// before it is returned.
func redactSecretsInText(s string) string {
	for _, re := range secretLikePatterns {
		s = re.ReplaceAllString(s, "(redacted)")
	}
	return s
}
