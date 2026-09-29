// This file wires internal/alerting's engine and internal/notify's
// senders together for the real hub binary: internal/hub deliberately
// has no import-time dependency on either package (see
// internal/hub/server.go's AlertEngine/NotifySenderFactory doc
// comments), so cmd/hub is the one place that imports both and adapts
// between them.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

// notifyHTTPTimeout bounds every outbound HTTP call a notify.Sender
// makes to a Discord/Telegram/WhatsApp/generic-webhook endpoint.
const notifyHTTPTimeout = 20 * time.Second

// newNotifySenderFactory builds an alerting.SenderFactory backed by
// internal/notify.New, sharing one *http.Client across every channel
// and resolving the SSRF-guard relaxation once from the environment
// (per notify.AllowCustomEndpointsFromEnv's doc comment: Senders never
// read the environment directly, so this is the one call site that
// does, for the real binary).
func newNotifySenderFactory() alerting.SenderFactory {
	client := &http.Client{Timeout: notifyHTTPTimeout}
	allowCustom := notify.AllowCustomEndpointsFromEnv(os.LookupEnv)

	return func(ch models.NotifyChannel) (alerting.Sender, error) {
		sender, err := notify.New(ch, client, notify.WithAllowCustomEndpoints(allowCustom))
		if err != nil {
			return nil, err
		}
		return notifySenderBridge{sender: sender}, nil
	}
}

// notifySenderBridge adapts an internal/notify.Sender to
// internal/alerting.Sender. The two packages declare structurally
// identical Message/Field/Severity types on purpose (see
// internal/alerting/notify.go's doc comment), so Send is a
// field-for-field copy, not a semantic translation; it also maps a
// *notify.RetryAfterError to *alerting.RetryAfterError so the alerting
// delivery worker's backoff logic (which only recognizes its own
// package's error type, see internal/alerting/notify.go's deliver)
// still honors a platform's Retry-After hint.
type notifySenderBridge struct {
	sender notify.Sender
}

// Send implements alerting.Sender.
func (b notifySenderBridge) Send(ctx context.Context, msg alerting.Message) error {
	err := b.sender.Send(ctx, toNotifyMessage(msg))
	if err == nil {
		return nil
	}
	if ra := asRetryAfterError(err); ra != nil {
		return &alerting.RetryAfterError{Err: err, RetryAfter: ra.After}
	}
	return err
}

// toNotifyMessage copies m field-for-field into a notify.Message.
func toNotifyMessage(m alerting.Message) notify.Message {
	fields := make([]notify.Field, len(m.Fields))
	for i, f := range m.Fields {
		fields[i] = notify.Field{Name: f.Name, Value: f.Value}
	}
	return notify.Message{
		Title:     m.Title,
		Text:      m.Text,
		Severity:  notify.Severity(m.Severity),
		Fields:    fields,
		URL:       m.URL,
		Image:     m.Image,
		ImageName: m.ImageName,
	}
}

// asRetryAfterError walks err's Unwrap chain looking for a
// *notify.RetryAfterError.
func asRetryAfterError(err error) *notify.RetryAfterError {
	for err != nil {
		if ra, ok := err.(*notify.RetryAfterError); ok {
			return ra
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil
		}
		err = u.Unwrap()
	}
	return nil
}

// Compile-time assertion that notifySenderBridge satisfies
// alerting.Sender.
var _ alerting.Sender = notifySenderBridge{}

// buildAlerting constructs the real internal/alerting.Engine (backed by
// store, which must already satisfy alerting.Store — *storage.DB does)
// and adapts it plus an internal/notify-backed sender factory into
// hub.Options.Alerting/NotifyFactory. logger is used for both the
// engine's own logging and its async delivery worker's. The returned
// *alerting.Engine must have Close called on it during shutdown (it
// owns a background delivery-worker goroutine) — see runHub.
func buildAlerting(store alerting.Store, logger *slog.Logger) (*alerting.Engine, hub.AlertEngine, hub.NotifySenderFactory) {
	senderFactory := newNotifySenderFactory()
	engine := alerting.New(alerting.Options{
		Store:         store,
		SenderFactory: senderFactory,
		Logger:        logger,
	})
	return engine, hub.WrapAlertEngine(engine), hub.WrapNotifySenderFactory(senderFactory)
}
