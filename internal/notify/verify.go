// verify.go defines the optional interfaces a Sender may implement to
// support `cloud-pulse-hub notify verify` (SPEC-v0.7 §2) and
// scripts/verify-notify.py's Go-parity read-back/cleanup checks. These
// extend the production senders in place — there is no parallel
// implementation of any platform's wire format here. A platform that
// cannot support read-back or cleanup (Telegram has no "get message" API;
// WhatsApp has no synchronous read-back or delete endpoint) simply does not
// implement the corresponding interface, and callers detect that via a type
// assertion rather than a capability flag.
package notify

import "context"

// MessageRef identifies whatever a platform's SendWithRef call produced,
// carrying only the identifiers that specific platform's read-back/cleanup
// calls need. Fields irrelevant to a platform are left zero.
type MessageRef struct {
	// MessageID is the platform's message identifier (Discord message
	// snowflake, Telegram message_id, WhatsApp WAMID).
	MessageID string
	// ChatID is the destination chat/channel identifier, when the
	// platform's read-back/cleanup call needs it separately from
	// MessageID (Telegram's chat.id; unused by Discord/WhatsApp).
	ChatID string
	// MediaID is a platform-specific media/attachment identifier
	// produced during Send, when applicable (WhatsApp's uploaded media
	// ID).
	MediaID string
	// HasAttachment reports whether the send included a chart image,
	// as observed directly in the send response (Telegram's
	// result.photo array, WhatsApp's media upload step succeeding).
	// Verify callers use this for platforms with no separate
	// attachment read-back call.
	HasAttachment bool
}

// RefSender is implemented by any Sender that can report identifiers for
// the message it just sent, in addition to the plain Sender.Send used by
// the alerting engine. Optional — SendWithRef is used by `notify verify`
// and scripts/verify-notify.py's Go-parity checks only, never by the
// alerting delivery worker.
type RefSender interface {
	Sender
	// SendWithRef sends msg exactly as Send would, additionally
	// returning whatever identifiers the platform's response exposed.
	SendWithRef(ctx context.Context, msg Message) (MessageRef, error)
}

// MessageReader is implemented by a Sender that can independently confirm
// a previously sent message (and, where applicable, its attachment) still
// exists — a true read-back rather than trusting the original Send
// response alone. Discord implements this (GET .../messages/{id}); Telegram
// and WhatsApp do not (see package doc comment above) — callers must type-
// assert rather than assume every RefSender also satisfies this.
type MessageReader interface {
	// ReadBack confirms ref's message exists and, when applicable,
	// carries an attachment. attachmentConfirmed is false (never an
	// error) for a platform/message shape with nothing to check.
	ReadBack(ctx context.Context, ref MessageRef) (attachmentConfirmed bool, err error)
}

// MessageDeleter is implemented by a Sender that can delete a previously
// sent message for `--cleanup`. Discord and Telegram implement this;
// WhatsApp does not (the Cloud API has no delete-message endpoint) — see
// package doc comment above.
type MessageDeleter interface {
	Delete(ctx context.Context, ref MessageRef) error
}
