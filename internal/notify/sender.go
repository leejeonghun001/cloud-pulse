package notify

import (
	"context"
)

// Sender delivers a Message to one configured notification channel.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}
