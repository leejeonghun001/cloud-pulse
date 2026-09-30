package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestWhatsAppSender_SendWithRef_ImageMessage confirms SendWithRef
// captures the uploaded media id and the sent message's WAMID from
// messages[0].id
// (https://developers.facebook.com/docs/whatsapp/cloud-api/reference/messages).
func TestWhatsAppSender_SendWithRef_ImageMessage(t *testing.T) {
	t.Parallel()

	srv := whatsappTestServer(t, nil)
	defer srv.Close()

	sender, err := New(whatsappChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	refSender, ok := sender.(RefSender)
	if !ok {
		t.Fatal("whatsappSender does not implement RefSender")
	}

	ref, err := refSender.SendWithRef(t.Context(), Message{Title: "t", Image: []byte{1, 2, 3}, ImageName: "chart.png"})
	if err != nil {
		t.Fatalf("SendWithRef() error = %v", err)
	}
	if ref.MessageID != "wamid.1" {
		t.Errorf("MessageID = %q, want wamid.1", ref.MessageID)
	}
	if ref.MediaID != "MEDIA123" {
		t.Errorf("MediaID = %q, want MEDIA123", ref.MediaID)
	}
	if !ref.HasAttachment {
		t.Errorf("HasAttachment = false, want true")
	}
}

// TestWhatsAppSender_SendWithRef_MissingMessagesArray confirms a 2xx
// response with no messages[0].id (an unexpected/malformed response
// shape) surfaces as an error rather than a silently empty MessageRef.
func TestWhatsAppSender_SendWithRef_MissingMessagesArray(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case len(r.URL.Path) > 0 && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path[len(r.URL.Path)-6:] == "/media" {
				_, _ = w.Write([]byte(`{"id":"MEDIA123"}`))
				return
			}
			_, _ = w.Write([]byte(`{"messaging_product":"whatsapp"}`)) // no "messages" field
		}
	}))
	defer srv.Close()

	sender, err := New(whatsappChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	refSender := sender.(RefSender)
	_, err = refSender.SendWithRef(t.Context(), Message{Title: "t", Image: []byte{1, 2, 3}})
	if err == nil {
		t.Fatal("SendWithRef() error = nil, want error for a response missing messages[0].id")
	}
}

// TestWhatsAppSender_DoesNotImplementMessageReaderOrDeleter documents
// that WhatsApp's Cloud API has no synchronous read-back or
// delete-message endpoint (see verify.go's package doc comment) —
// whatsappSender intentionally implements neither MessageReader nor
// MessageDeleter.
func TestWhatsAppSender_DoesNotImplementMessageReaderOrDeleter(t *testing.T) {
	t.Parallel()

	sender, err := New(whatsappChannel("https://graph.facebook.com", nil), nil, WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := sender.(MessageReader); ok {
		t.Errorf("whatsappSender unexpectedly implements MessageReader")
	}
	if _, ok := sender.(MessageDeleter); ok {
		t.Errorf("whatsappSender unexpectedly implements MessageDeleter")
	}
}

// --- StatusWebhookListener tests ---

func signMetaBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestStatusWebhookListener_WaitFor_Success confirms a correctly signed
// delivery-status payload unblocks WaitFor with the matching WAMID's
// status.
func TestStatusWebhookListener_WaitFor_Success(t *testing.T) {
	t.Parallel()

	l := NewStatusWebhookListener("test-app-secret")
	addr, err := l.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = l.Close(context.Background()) }()

	body := []byte(`{"entry":[{"changes":[{"value":{"statuses":[{"id":"wamid.1","status":"delivered","recipient_id":"15551234567"}]}}]}]}`)

	go func() {
		// Deliver the webhook POST shortly after WaitFor has started
		// listening for it, avoiding a race between the two.
		time.Sleep(20 * time.Millisecond)
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", signMetaBody("test-app-secret", body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("POST webhook: %v", err)
			return
		}
		_ = resp.Body.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := l.WaitFor(ctx, "wamid.1")
	if err != nil {
		t.Fatalf("WaitFor() error = %v", err)
	}
	if status != "delivered" {
		t.Errorf("status = %q, want delivered", status)
	}
}

// TestStatusWebhookListener_WaitFor_Timeout confirms WaitFor returns an
// error once ctx is done without a matching delivery.
func TestStatusWebhookListener_WaitFor_Timeout(t *testing.T) {
	t.Parallel()

	l := NewStatusWebhookListener("secret")
	if _, err := l.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = l.Close(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := l.WaitFor(ctx, "wamid.nonexistent"); err == nil {
		t.Fatal("WaitFor() error = nil, want timeout error")
	}
}

// TestVerifyMetaSignature_RejectsBadSignature confirms an incorrect or
// missing X-Hub-Signature-256 is rejected — this is the exact check a
// real CVE (a competing project's webhook handler omitting it) showed is
// security-critical, see notes/v07-notify-e2e.md.
func TestVerifyMetaSignature_RejectsBadSignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"entry":[]}`)
	cases := []struct {
		name   string
		secret string
		header string
		want   bool
	}{
		{"correct", "s3cr3t", signMetaBody("s3cr3t", body), true},
		{"wrong secret", "s3cr3t", signMetaBody("other", body), false},
		{"missing header", "s3cr3t", "", false},
		{"missing prefix", "s3cr3t", "deadbeef", false},
		{"malformed hex", "s3cr3t", "sha256=not-hex!!", false},
		{"empty secret", "", signMetaBody("", body), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := verifyMetaSignature(c.secret, c.header, body)
			if got != c.want {
				t.Errorf("verifyMetaSignature() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStatusWebhookListener_RejectsBadSignature confirms the HTTP
// handler itself (not just the pure verifyMetaSignature function)
// responds 401 to a badly signed delivery and never unblocks a waiter.
func TestStatusWebhookListener_RejectsBadSignature(t *testing.T) {
	t.Parallel()

	l := NewStatusWebhookListener("real-secret")
	addr, err := l.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = l.Close(context.Background()) }()

	body := []byte(`{"entry":[{"changes":[{"value":{"statuses":[{"id":"wamid.evil","status":"delivered"}]}}]}]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signMetaBody("wrong-secret", body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := l.WaitFor(ctx, "wamid.evil"); err == nil {
		t.Fatal("WaitFor() error = nil, want timeout — a badly signed delivery must never satisfy a waiter")
	}
}

// TestParseWhatsAppStatusEvents_MultipleStatuses confirms multiple
// batched status objects across nested entry/changes arrays are all
// extracted.
func TestParseWhatsAppStatusEvents_MultipleStatuses(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"entry": [
			{"changes": [{"value": {"statuses": [
				{"id": "wamid.1", "status": "sent"},
				{"id": "wamid.2", "status": "delivered"}
			]}}]},
			{"changes": [{"value": {"statuses": [
				{"id": "wamid.3", "status": "read"}
			]}}]}
		]
	}`)
	events := parseWhatsAppStatusEvents(body)
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}
}

// TestParseWhatsAppStatusEvents_NoStatuses confirms a payload with no
// statuses (e.g. an incoming-message webhook) returns an empty slice,
// never an error panic or nil-dereference.
func TestParseWhatsAppStatusEvents_NoStatuses(t *testing.T) {
	t.Parallel()

	events := parseWhatsAppStatusEvents([]byte(`{"entry":[{"changes":[{"value":{"messages":[{"id":"abc"}]}}]}]}`))
	if len(events) != 0 {
		t.Errorf("len(events) = %d, want 0", len(events))
	}
}
