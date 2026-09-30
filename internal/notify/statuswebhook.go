// statuswebhook.go implements an optional local HTTP listener that
// verifies WhatsApp Cloud API delivery-status webhook deliveries for
// `cloud-pulse-hub notify verify --status-webhook-listen` (SPEC-v0.7 §2).
// This is only useful when the operator has already pointed a real Meta
// app's webhook configuration at this listener (reachable from the
// internet, e.g. via a tunnel) — the listener itself never registers
// anything with Meta, it only waits for and verifies a delivery already
// configured to arrive.
//
// Payload shape (SPEC-v0.7 §2 / Meta's "Status and Pricing Notifications"
// docs): entry[].changes[].value.statuses[] — each status object carries
// "id" (the WAMID matching the send response's messages[0].id) and
// "status" (sent/delivered/read/failed).
package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxStatusWebhookBodyBytes bounds how much of an incoming webhook POST
// body is read before rejecting it — Meta's status payloads are small
// (well under 64 KiB even with several statuses batched).
const maxStatusWebhookBodyBytes = 1 << 20 // 1 MiB

// StatusWebhookListener is a local HTTP server that verifies incoming
// WhatsApp Cloud API webhook deliveries' X-Hub-Signature-256 header
// against appSecret (HMAC-SHA256 over the raw request body, hex-encoded,
// prefixed "sha256="), and lets a caller wait for a delivery-status
// event matching a specific WAMID.
type StatusWebhookListener struct {
	appSecret string

	mu       sync.Mutex
	waiters  map[string]chan whatsappDeliveryStatus
	server   *http.Server
	listener net.Listener
}

// whatsappDeliveryStatus is the subset of a WhatsApp status webhook
// event's fields this package needs.
type whatsappDeliveryStatus struct {
	ID     string
	Status string
}

// NewStatusWebhookListener builds a listener bound to addr (e.g.
// "127.0.0.1:0" for an ephemeral port, or a specific address/port the
// operator has tunneled/forwarded), verifying incoming deliveries against
// appSecret. It does not start serving until Start is called.
func NewStatusWebhookListener(appSecret string) *StatusWebhookListener {
	return &StatusWebhookListener{
		appSecret: appSecret,
		waiters:   make(map[string]chan whatsappDeliveryStatus),
	}
}

// Start binds addr and begins serving in a background goroutine. Returns
// the bound address (useful when addr's port is "0"). Callers must call
// Close when done.
func (l *StatusWebhookListener) Start(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("notify: status webhook listener: listen: %w", err)
	}
	l.listener = ln
	l.server = &http.Server{
		Handler:           http.HandlerFunc(l.handle),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = l.server.Serve(ln) // Close() causes a benign http.ErrServerClosed
	}()
	return ln.Addr().String(), nil
}

// Close shuts the listener down.
func (l *StatusWebhookListener) Close(ctx context.Context) error {
	if l.server == nil {
		return nil
	}
	if err := l.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("notify: status webhook listener: shutdown: %w", err)
	}
	return nil
}

// WaitFor blocks until a verified status webhook delivery reports a
// status object whose id matches wamid, or ctx is done first. Returns the
// observed status string ("sent"/"delivered"/"read"/"failed") on success.
func (l *StatusWebhookListener) WaitFor(ctx context.Context, wamid string) (string, error) {
	ch := make(chan whatsappDeliveryStatus, 1)
	l.mu.Lock()
	l.waiters[wamid] = ch
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.waiters, wamid)
		l.mu.Unlock()
	}()

	select {
	case ev := <-ch:
		return ev.Status, nil
	case <-ctx.Done():
		return "", fmt.Errorf("notify: status webhook listener: timed out waiting for delivery status of %s: %w", wamid, ctx.Err())
	}
}

func (l *StatusWebhookListener) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxStatusWebhookBodyBytes+1))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(body) > maxStatusWebhookBodyBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if !verifyMetaSignature(l.appSecret, r.Header.Get("X-Hub-Signature-256"), body) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	for _, ev := range parseWhatsAppStatusEvents(body) {
		l.mu.Lock()
		ch, ok := l.waiters[ev.ID]
		l.mu.Unlock()
		if ok {
			select {
			case ch <- ev:
			default:
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

// verifyMetaSignature reports whether header (the X-Hub-Signature-256
// header value, expected form "sha256=<hex>") is a valid HMAC-SHA256 of
// body keyed by appSecret — the standard Meta Graph API webhook signing
// scheme shared by WhatsApp/Facebook/Instagram webhooks. An empty
// appSecret or header is never valid (fails closed).
func verifyMetaSignature(appSecret, header string, body []byte) bool {
	if appSecret == "" || header == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	want := mac.Sum(nil)
	return hmac.Equal(got, want)
}

// whatsappStatusWebhookPayload mirrors the subset of Meta's webhook
// payload shape this package needs:
// entry[].changes[].value.statuses[].{id,status}.
type whatsappStatusWebhookPayload struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Statuses []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"statuses"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// parseWhatsAppStatusEvents extracts every status event from body,
// returning an empty slice (never an error) for a payload with no
// statuses — a webhook delivery about an incoming message rather than a
// status update is a normal, non-error occurrence.
func parseWhatsAppStatusEvents(body []byte) []whatsappDeliveryStatus {
	var parsed whatsappStatusWebhookPayload
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	var events []whatsappDeliveryStatus
	for _, entry := range parsed.Entry {
		for _, change := range entry.Changes {
			for _, st := range change.Value.Statuses {
				events = append(events, whatsappDeliveryStatus{ID: st.ID, Status: st.Status})
			}
		}
	}
	return events
}
