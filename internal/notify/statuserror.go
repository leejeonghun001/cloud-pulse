package notify

import "fmt"

// statusError wraps a non-2xx HTTP response from a notify platform,
// carrying enough context (platform, an optional delivery stage, the
// HTTP status, and the raw response body) for DiagnoseError to classify
// it without the caller having to re-parse anything. It always wraps
// errUnexpectedStatus so existing errors.Is(err, errUnexpectedStatus)-
// style checks (if any) keep working, and its Error() string matches
// the pre-existing "notify: <platform>: unexpected status: status
// <code>: <message>" shape so log lines and test assertions relying on
// that text are unaffected.
type statusError struct {
	// Platform is one of "discord", "telegram", "whatsapp", "webhook".
	Platform string
	// Stage distinguishes multiple HTTP calls within one Send, e.g.
	// WhatsApp's media upload vs. the message send itself. Empty for
	// senders that make only one call.
	Stage string
	// StatusCode is the response's HTTP status code.
	StatusCode int
	// Body is the raw (already size-limited) response body.
	Body []byte
	// Message is the platform-specific error message already
	// extracted from Body (e.g. Discord's "message", Telegram's
	// "description", WhatsApp's "error.message") — kept alongside Body
	// so Error() and diagnosis text don't need to re-parse JSON.
	Message string
}

// Error implements the error interface.
func (e *statusError) Error() string {
	return fmt.Sprintf("notify: %s: %s: status %d: %s", e.Platform, errUnexpectedStatus, e.StatusCode, e.Message)
}

// Unwrap allows errors.Is(err, errUnexpectedStatus) to keep working
// through a *statusError.
func (e *statusError) Unwrap() error {
	return errUnexpectedStatus
}

// newStatusError builds a *statusError for platform/stage from resp's
// status code, its already-read body, and the platform-specific
// message already extracted from that body.
func newStatusError(platform, stage string, statusCode int, body []byte, message string) *statusError {
	return &statusError{Platform: platform, Stage: stage, StatusCode: statusCode, Body: body, Message: message}
}
