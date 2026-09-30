package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// fakeSecretToken/fakeSecretURL are obviously-fake credentials used
// only to assert they never leak into a Diagnosis. They match the
// secret-shaped patterns diagnose.go's redactSecretsInText strips.
const (
	fakeSecretToken = "123456:TEST-TOKEN-abcdefghijklmnop"
	fakeSecretURL   = "https://discord.com/api/webhooks/999999/AAABBBCCCDDDEEEFFFGGGHHH"
)

// assertNoSecret fails t if any of the fake secret material appears
// anywhere in d's Title/Detail/Hint/DocsURL.
func assertNoSecret(t *testing.T, d models.Diagnosis) {
	t.Helper()
	all := d.Title + " " + d.Detail + " " + d.Hint + " " + d.DocsURL
	for _, secret := range []string{fakeSecretToken, fakeSecretURL, "TEST-TOKEN"} {
		if strings.Contains(all, secret) {
			t.Errorf("diagnosis leaked secret %q: %+v", secret, d)
		}
	}
}

// --- Network-error codes ---

func TestDiagnoseError_Timeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the client's context is canceled by its own deadline
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	_, sendErr := srv.Client().Do(req)
	if sendErr == nil {
		t.Fatal("expected a timeout error, got nil")
	}

	d := DiagnoseError(sendErr)
	if d.Code != models.DiagnosisTimeout {
		t.Errorf("Code = %q, want %q (err: %v)", d.Code, models.DiagnosisTimeout, sendErr)
	}
	assertNoSecret(t, d)
}

func TestDiagnoseError_DNSFailure(t *testing.T) {
	t.Parallel()

	// Constructing *net.DNSError directly keeps this deterministic and
	// fast — no real DNS resolution against a nonexistent host, which
	// would depend on network/DNS timeout behavior we don't control.
	dnsErr := &net.DNSError{Err: "no such host", Name: "webhook-host.invalid", IsNotFound: true}
	d := DiagnoseError(dnsErr)
	if d.Code != models.DiagnosisDNSFailure {
		t.Errorf("Code = %q, want %q", d.Code, models.DiagnosisDNSFailure)
	}
	assertNoSecret(t, d)
}

func TestDiagnoseError_ConnectionRefused_Unix(t *testing.T) {
	t.Parallel()

	// A real closed-port dial reproduces this deterministically: bind
	// a listener, close it immediately, then dial the now-unused
	// address — the OS returns ECONNREFUSED (loopback, no firewall
	// involved) rather than a timeout.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if dialErr == nil {
		t.Fatal("expected dial to a closed port to fail")
	}

	d := DiagnoseError(dialErr)
	if d.Code != models.DiagnosisConnectionRefused {
		t.Errorf("Code = %q, want %q (err: %v)", d.Code, models.DiagnosisConnectionRefused, dialErr)
	}
	assertNoSecret(t, d)
}

func TestDiagnoseError_ConnectionRefused_SyntheticWindowsErrno(t *testing.T) {
	t.Parallel()

	// Windows reports connection-refused as WSAECONNREFUSED (10061),
	// a different numeric value than Unix's syscall.ECONNREFUSED. This
	// test constructs the *net.OpError shape directly (rather than
	// requiring a real Windows dial) so the classifier's cross-
	// platform branch is exercised deterministically on any host OS.
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: syscall.Errno(windowsWSAECONNREFUSED),
	}
	d := DiagnoseError(opErr)
	if d.Code != models.DiagnosisConnectionRefused {
		t.Errorf("Code = %q, want %q", d.Code, models.DiagnosisConnectionRefused)
	}
	assertNoSecret(t, d)
}

func TestDiagnoseError_NetworkUnreachable_GenericOpError(t *testing.T) {
	t.Parallel()

	opErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("network is unreachable")}
	d := DiagnoseError(opErr)
	if d.Code != models.DiagnosisNetworkUnreachable {
		t.Errorf("Code = %q, want %q", d.Code, models.DiagnosisNetworkUnreachable)
	}
	assertNoSecret(t, d)
}

func TestDiagnoseError_NetworkUnreachable_GenericFallback(t *testing.T) {
	t.Parallel()

	d := DiagnoseError(errors.New("connection reset by peer"))
	if d.Code != models.DiagnosisNetworkUnreachable {
		t.Errorf("Code = %q, want %q", d.Code, models.DiagnosisNetworkUnreachable)
	}
	assertNoSecret(t, d)
}

func TestDiagnoseError_TLSError(t *testing.T) {
	t.Parallel()

	// httptest.NewTLSServer signs its certificate with a fresh, self-
	// signed CA that isn't in the client's trust store — a plain
	// http.Client (no InsecureSkipVerify, no custom pool) fails the
	// handshake with an x509 verification error, giving us the "self-
	// signed / unknown authority" case deterministically without any
	// externally-provisioned certificate.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected a TLS verification error dialing an httptest TLS server with the default client")
	}

	d := DiagnoseError(err)
	if d.Code != models.DiagnosisTLSError {
		t.Errorf("Code = %q, want %q (err: %v)", d.Code, models.DiagnosisTLSError, err)
	}
	assertNoSecret(t, d)
}

func TestDiagnoseError_TLSError_RecordHeader(t *testing.T) {
	t.Parallel()

	// A tls.RecordHeaderError (e.g. dialing a plain-HTTP port with
	// TLS) is a second TLS-shaped error type isTLSError must also
	// recognize.
	err := tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}
	d := DiagnoseError(err)
	if d.Code != models.DiagnosisTLSError {
		t.Errorf("Code = %q, want %q", d.Code, models.DiagnosisTLSError)
	}
	assertNoSecret(t, d)
}

// --- Rate limiting (shared across platforms) ---

func TestDiagnoseError_RateLimited(t *testing.T) {
	t.Parallel()

	raErr := &RetryAfterError{After: 7 * time.Second, Err: errors.New("rate limited")}
	d := DiagnoseError(raErr)
	if d.Code != models.DiagnosisRateLimited {
		t.Errorf("Code = %q, want %q", d.Code, models.DiagnosisRateLimited)
	}
	if !strings.Contains(d.Hint, "7s") {
		t.Errorf("Hint = %q, want it to mention the 7s retry delay", d.Hint)
	}
	assertNoSecret(t, d)
}

// --- Discord ---

func TestDiagnoseError_DiscordWebhookNotFound(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "discord", "", http.StatusNotFound,
		`{"code":10015,"message":"Unknown Webhook"}`,
		models.DiagnosisDiscordWebhookGone)
}

func TestDiagnoseError_DiscordUnauthorized(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "discord", "", http.StatusUnauthorized,
		`{"code":50027,"message":"Invalid Webhook Token"}`,
		models.DiagnosisDiscordUnauthorized)
}

func TestDiagnoseError_DiscordPlatformErrorFallback(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "discord", "", http.StatusInternalServerError,
		`{"code":0,"message":"Internal Server Error"}`,
		models.DiagnosisPlatformError)
}

// --- Telegram ---

func TestDiagnoseError_TelegramUnauthorized(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "telegram", "", http.StatusUnauthorized,
		`Unauthorized`,
		models.DiagnosisTelegramUnauthorized)
}

func TestDiagnoseError_TelegramChatNotFound(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "telegram", "", http.StatusBadRequest,
		`Bad Request: chat not found`,
		models.DiagnosisTelegramChatNotFound)
}

func TestDiagnoseError_TelegramBotBlocked(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "telegram", "", http.StatusForbidden,
		`Forbidden: bot was blocked by the user`,
		models.DiagnosisTelegramBotBlocked)
}

func TestDiagnoseError_TelegramNotMember(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "telegram", "", http.StatusBadRequest,
		`Bad Request: not enough rights to send text messages to the chat`,
		models.DiagnosisTelegramNotMember)
}

func TestDiagnoseError_TelegramThreadNotFound(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "telegram", "", http.StatusBadRequest,
		`Bad Request: message thread not found`,
		models.DiagnosisTelegramThreadMissing)
}

func TestDiagnoseError_TelegramGenericForbiddenFallsBackToBlocked(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "telegram", "", http.StatusForbidden,
		`Forbidden: bot is not a member of the supergroup chat`,
		models.DiagnosisTelegramBotBlocked)
}

func TestDiagnoseError_TelegramPlatformErrorFallback(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "telegram", "", http.StatusBadRequest,
		`Bad Request: something else entirely`,
		models.DiagnosisPlatformError)
}

// --- WhatsApp ---

func TestDiagnoseError_WhatsAppTokenInvalid(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "whatsapp", "message", http.StatusUnauthorized,
		`{"error":{"message":"Error validating access token","type":"OAuthException","code":190,"error_data":{"details":"Session has expired"}}}`,
		models.DiagnosisWhatsAppTokenInvalid)
}

func TestDiagnoseError_WhatsAppPermission_Code10(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "whatsapp", "message", http.StatusForbidden,
		`{"error":{"message":"Permission denied","type":"OAuthException","code":10}}`,
		models.DiagnosisWhatsAppPermission)
}

func TestDiagnoseError_WhatsAppPermission_Code200Range(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "whatsapp", "message", http.StatusForbidden,
		`{"error":{"message":"API Permission","type":"OAuthException","code":200}}`,
		models.DiagnosisWhatsAppPermission)
}

func TestDiagnoseError_WhatsAppRecipientNotAllowed(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "whatsapp", "message", http.StatusBadRequest,
		`{"error":{"message":"(#131030) Recipient phone number not in allowed list","type":"OAuthException","code":131030}}`,
		models.DiagnosisWhatsAppRecipientDenied)
}

func TestDiagnoseError_WhatsAppWindowClosed(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "whatsapp", "message", http.StatusBadRequest,
		`{"error":{"message":"(#131047) Re-engagement message","type":"OAuthException","code":131047,"error_data":{"details":"Message failed to send because more than 24 hours have passed since the customer last replied to this number."}}}`,
		models.DiagnosisWhatsAppWindowClosed)
}

func TestDiagnoseError_WhatsAppTemplateMissing_ParamMismatch(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "whatsapp", "message", http.StatusBadRequest,
		`{"error":{"message":"(#132000) Number of parameters does not match the expected number of params","type":"OAuthException","code":132000}}`,
		models.DiagnosisWhatsAppTemplateMissing)
}

func TestDiagnoseError_WhatsAppTemplateMissing_DoesNotExist(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "whatsapp", "message", http.StatusNotFound,
		`{"error":{"message":"(#132001) Template name does not exist in the translation","type":"OAuthException","code":132001}}`,
		models.DiagnosisWhatsAppTemplateMissing)
}

func TestDiagnoseError_WhatsAppMediaFailed_ByStage(t *testing.T) {
	t.Parallel()
	// Media-stage failures map to whatsapp_media_failed regardless of
	// the specific Graph code, since the media-upload call is a
	// distinct request from the message send.
	assertDiagnosis(t, "whatsapp", "media_upload", http.StatusBadRequest,
		`{"error":{"message":"(#131053) Media upload error","type":"OAuthException","code":131053}}`,
		models.DiagnosisWhatsAppMediaFailed)
}

func TestDiagnoseError_WhatsAppRateLimited(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "whatsapp", "message", http.StatusTooManyRequests,
		`{"error":{"message":"(#130429) Rate limit hit","type":"OAuthException","code":130429}}`,
		models.DiagnosisRateLimited)
}

func TestDiagnoseError_WhatsAppPlatformErrorFallback(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "whatsapp", "message", http.StatusInternalServerError,
		`{"error":{"message":"Unknown error","type":"OAuthException","code":1}}`,
		models.DiagnosisPlatformError)
}

// --- Generic webhook platform (no special-cased codes) ---

func TestDiagnoseError_GenericWebhookPlatformError(t *testing.T) {
	t.Parallel()
	assertDiagnosis(t, "webhook", "", http.StatusInternalServerError, `internal error`, models.DiagnosisPlatformError)
}

// --- nil input ---

func TestDiagnoseError_NilError(t *testing.T) {
	t.Parallel()
	d := DiagnoseError(nil)
	if d.Code != models.DiagnosisPlatformError {
		t.Errorf("Code = %q, want %q for nil input", d.Code, models.DiagnosisPlatformError)
	}
}

// --- Secret redaction on real sender error paths ---

func TestDiagnoseError_StatusErrorFromRealSenderNeverLeaksSecret(t *testing.T) {
	t.Parallel()

	// Exercises the real discordSender.Send path end to end (not a
	// synthetic statusError) against a channel configured with
	// fakeSecretURL, confirming the resulting error — and its
	// Diagnosis — never contains the webhook's token segment.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":50027,"message":"Invalid Webhook Token"}`))
	}))
	defer srv.Close()

	ch := discordChannel(srv.URL + "/api/webhooks/999999/" + strings.TrimPrefix(fakeSecretURL, "x"))
	sender, err := New(ch, srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sendErr := sender.Send(t.Context(), Message{Title: "t"})
	if sendErr == nil {
		t.Fatal("expected Send to fail")
	}

	d := DiagnoseError(sendErr)
	if d.Code != models.DiagnosisDiscordUnauthorized {
		t.Errorf("Code = %q, want %q", d.Code, models.DiagnosisDiscordUnauthorized)
	}
	assertNoSecret(t, d)
	if strings.Contains(sendErr.Error(), fakeSecretURL) {
		t.Errorf("Send() error text leaked the webhook URL: %v", sendErr)
	}
}

// --- helper ---

// assertDiagnosis builds a *statusError for platform/stage/status/body
// and asserts DiagnoseError maps it to wantCode with no secret
// material anywhere in the result.
func assertDiagnosis(t *testing.T, platform, stage string, status int, body string, wantCode models.DiagnosisCode) {
	t.Helper()
	statusErr := newStatusError(platform, stage, status, []byte(body), body)
	d := DiagnoseError(statusErr)
	if d.Code != wantCode {
		t.Errorf("platform=%s stage=%s status=%d: Code = %q, want %q (body: %s)", platform, stage, status, d.Code, wantCode, body)
	}
	assertNoSecret(t, d)
}
