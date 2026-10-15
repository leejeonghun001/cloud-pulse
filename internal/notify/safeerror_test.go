package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestSafeErrorText_ChannelCredentialsAndEscapedEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ch       models.NotifyChannel
		secret   string
		endpoint string
	}{
		{"telegram", models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "12:t0", "api_base": "https://api.example.invalid"}}, "12:t0", "https://api.example.invalid"},
		{"whatsapp", models.NotifyChannel{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ", "api_base": "https://graph.example.invalid/credential"}}, "xQ", "https://graph.example.invalid/credential"},
		{"discord", models.NotifyChannel{Type: models.NotifyChannelDiscord, Config: map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/xQ"}}, "xQ", "https://discord.com/api/webhooks/1/xQ"},
		{"generic", models.NotifyChannel{Type: models.NotifyChannelWebhook, Config: map[string]string{"url": "https://usr:pwQ@hooks.example.invalid/private?token=qZ"}}, "pwQ", "https://usr:pwQ@hooks.example.invalid/private?token=qZ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quote := strconv.Quote(tc.endpoint)
			encoded, _ := json.Marshal(tc.endpoint)
			text := fmt.Sprintf("send request failed: %s %s %s %s %s %s %s",
				tc.secret, url.PathEscape(tc.secret), tc.endpoint, url.QueryEscape(tc.endpoint), quote[1:len(quote)-1], string(encoded), strings.ReplaceAll(tc.endpoint, "/", `\/`))
			got := SafeErrorText(tc.ch, errors.New(text))
			for _, secret := range []string{tc.secret, url.PathEscape(tc.secret), tc.endpoint, url.QueryEscape(tc.endpoint)} {
				if strings.Contains(got, secret) {
					t.Fatalf("leaked configured credential/URL in %q", got)
				}
			}
			if !strings.Contains(got, "send request failed") {
				t.Fatalf("lost operation wording: %q", got)
			}
		})
	}
	if SafeErrorText(models.NotifyChannel{}, nil) != "" {
		t.Fatal("nil error should have empty display text")
	}
}

func TestDiagnoseChannelError_PreservesTypesAndStaticGuidance(t *testing.T) {
	ch := models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "12:t0"}}
	dns := &net.DNSError{Name: "12:t0", Err: "no such host"}
	for _, tc := range []struct {
		err  error
		code models.DiagnosisCode
	}{
		{fmt.Errorf("send URL https://api.telegram.org/bot12:t0/sendPhoto: %w", dns), models.DiagnosisDNSFailure},
		{fmt.Errorf("send 12:t0: %w", context.DeadlineExceeded), models.DiagnosisTimeout},
		{&RetryAfterError{After: 3 * time.Second, Err: errors.New("send rate limited 12:t0")}, models.DiagnosisRateLimited},
		{newStatusError("telegram", "send", http.StatusBadRequest, nil, "chat not found 12:t0"), models.DiagnosisTelegramChatNotFound},
	} {
		d := DiagnoseChannelError(ch, tc.err)
		if d.Code != tc.code || strings.Contains(d.Detail, "12:t0") {
			t.Fatalf("diagnosis = %+v", d)
		}
		if d.Hint != DiagnoseError(tc.err).Hint || d.Title != DiagnoseError(tc.err).Title {
			t.Fatalf("static guidance changed: %+v", d)
		}
	}
	var original *net.DNSError
	if !errors.As(fmt.Errorf("%w", dns), &original) || original != dns {
		t.Fatal("original DNS type lost")
	}
}

type safeErrorRoundTripper func(*http.Request) (*http.Response, error)

func (f safeErrorRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSafeErrorText_RealSenderTransportAndConstructor(t *testing.T) {
	ch := models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "12:t0", "chat_id": "123"}}
	dns := &net.DNSError{Name: "api.telegram.org", Err: "no such host"}
	client := &http.Client{Transport: safeErrorRoundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, dns
	})}
	sender, err := New(ch, client, WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(t.Context(), Message{Text: "generated test text"})
	var gotDNS *net.DNSError
	if !errors.As(err, &gotDNS) || gotDNS != dns || DiagnoseChannelError(ch, err).Code != models.DiagnosisDNSFailure {
		t.Fatalf("typed transport error lost: %v", err)
	}
	if text := SafeErrorText(ch, err); strings.Contains(text, "12:t0") || !strings.Contains(text, "Post") {
		t.Fatalf("unsafe/unhelpful transport display: %q", text)
	}
	ch.Config["api_base"] = "https://credentials.example.invalid/%bad\n"
	_, err = New(ch, client, WithAllowCustomEndpoints(true))
	if err == nil || strings.Contains(SafeErrorText(ch, err), "credentials.example.invalid") || !strings.Contains(SafeErrorText(ch, err), "api_base") {
		t.Fatalf("unsafe/unhelpful constructor display: %v", err)
	}
}

func TestSafeErrorText_RedirectErrorURLAndEncodedForms(t *testing.T) {
	endpoint := "https://redirect.example.invalid/usr:pwQ/private?token=qZ"
	err := &url.Error{Op: "Post", URL: endpoint, Err: errors.New("request failed: " + url.QueryEscape(endpoint) + " " + strings.ReplaceAll(endpoint, "/", `\/`))}
	ch := models.NotifyChannel{Type: models.NotifyChannelWebhook, Config: map[string]string{"url": "https://configured.example.invalid/hook"}}
	text := SafeErrorText(ch, err)
	d := DiagnoseChannelError(ch, err)
	if strings.Contains(text+d.Detail, "redirect.example.invalid") || strings.Contains(text+d.Detail, "qZ") || !strings.Contains(text, "request failed") {
		t.Fatalf("URL error exposed redirect URL: %s %+v", text, d)
	}
	if _, ok := ch.Config["error_url"]; ok {
		t.Fatal("redaction mutated channel config")
	}
}

func TestConstructorAndConfigValidation_InvalidCredentialURLSafeWithTypedCause(t *testing.T) {
	ch := models.NotifyChannel{Type: models.NotifyChannelDiscord, Config: map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/xQ/%zz"}}
	_, err := New(ch, &http.Client{})
	if err == nil {
		t.Fatal("expected malformed URL error")
	}
	var originalURL *url.Error
	if !errors.As(err, &originalURL) {
		t.Fatal("sanitized constructor error lost typed URL cause")
	}
	validation := ValidateConfig(ch)
	text := err.Error() + fmt.Sprint(validation)
	if strings.Contains(text, "xQ") || strings.Contains(text, "https://discord.com") || !strings.Contains(text, "parse endpoint url") {
		t.Fatalf("unsafe constructor/validation: %s", text)
	}
}
