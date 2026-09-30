// synthetic.go implements SPEC-v0.8 §3-2's weekly synthetic connectivity
// test: the hub sends a real test message through the exact same send
// path as `cloud-pulse-hub notify verify` (see cmd/hub/notifyverifyrun.go
// and scripts/verify-notify.py, which this deliberately mirrors rather
// than reimplements), attempting a read-back + cleanup delete where the
// platform's API allows it (Discord, Telegram), and reporting
// accepted/verified/failed for platforms that don't (WhatsApp, webhook).
package channelhealth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

// SenderFactory constructs the notify.Sender used for a synthetic test,
// matching cmd/hub's notifySenderFactory shape so callers (the hub) can
// pass notify.New directly (or a fake in tests) without an adapter.
type SenderFactory func(ch models.NotifyChannel, client *http.Client) (notify.Sender, error)

// defaultSenderFactory delegates to notify.New, the same constructor
// production alert delivery and `notify verify` both use — there is no
// parallel sender implementation in this package.
func defaultSenderFactory(ch models.NotifyChannel, client *http.Client) (notify.Sender, error) {
	return notify.New(ch, client)
}

// synthenticHTTPClientTimeout bounds the shared http.Client for a
// synthetic test run, mirroring notifyVerifyHTTPClientTimeout's role in
// cmd/hub/notifyverifyrun.go.
const synthenticHTTPClientTimeout = 30 * time.Second

// defaultAttemptTimeout bounds each individual send/read-back/delete
// call within one synthetic test run.
const defaultAttemptTimeout = 20 * time.Second

// RunOptions configures RunSyntheticTest. A zero-value RunOptions uses
// defaultSenderFactory and defaultAttemptTimeout.
type RunOptions struct {
	SenderFactory SenderFactory
	// Timeout bounds each individual send/read-back/delete call. <= 0
	// uses defaultAttemptTimeout.
	Timeout time.Duration
}

// syntheticMessageTitle/Text are the fixed test message content every
// weekly synthetic test sends — deliberately generic (SPEC-v0.8 §3-2's
// synthetic test carries no real alert data) and distinct from `notify
// verify`'s own message text so an operator can tell which mechanism
// sent a given test message in their Discord/Telegram history.
const (
	syntheticMessageTitle = "cloud-pulse channel health check"
	syntheticMessageText  = "This is an automated weekly connectivity check from cloud-pulse's notification channel health report (SPEC-v0.8 §3-2). Safe to ignore."
)

// RunSyntheticTest sends one synthetic test message to ch, following
// the exact same accepted -> read-back -> cleanup sequence as `notify
// verify` (cmd/hub/notifyverifyrun.go's runOneNotifyVerify): Discord and
// Telegram get a read-back confirmation and a best-effort delete;
// WhatsApp and webhook channels are reported "accepted" once the
// platform's API returns success, with no read-back/delete attempted
// (see internal/notify/verify.go's package doc comment on why those
// platforms have no such capability). The returned models.
// SyntheticResult never carries a secret: Detail is always built from
// notify.DiagnoseError's already-redacted output or this file's own
// static strings.
func RunSyntheticTest(ctx context.Context, ch models.NotifyChannel, opts RunOptions) models.SyntheticResult {
	factory := opts.SenderFactory
	if factory == nil {
		factory = defaultSenderFactory
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultAttemptTimeout
	}
	client := &http.Client{Timeout: synthenticHTTPClientTimeout}

	result := models.SyntheticResult{ChannelID: ch.ID}

	sender, err := factory(ch, client)
	if err != nil {
		result.Outcome = models.SyntheticFailed
		diag := notify.DiagnoseError(err)
		result.Diagnosis = diag.Code
		result.Detail = diag.Title
		return result
	}

	msg := notify.Message{
		Title:    syntheticMessageTitle,
		Text:     syntheticMessageText,
		Severity: notify.SeverityInfo,
	}

	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	refSender, hasRef := sender.(notify.RefSender)
	var ref notify.MessageRef
	if hasRef {
		ref, err = refSender.SendWithRef(sendCtx, msg)
	} else {
		err = sender.Send(sendCtx, msg)
	}
	if err != nil {
		result.Outcome = models.SyntheticFailed
		diag := notify.DiagnoseError(err)
		result.Diagnosis = diag.Code
		result.Detail = diag.Title
		return result
	}

	result.Outcome = models.SyntheticAccepted
	result.Detail = "accepted by platform"

	if reader, ok := sender.(notify.MessageReader); ok {
		readCtx, readCancel := context.WithTimeout(ctx, timeout)
		_, err := reader.ReadBack(readCtx, ref)
		readCancel()
		if err != nil {
			result.Outcome = models.SyntheticFailed
			diag := notify.DiagnoseError(err)
			result.Diagnosis = diag.Code
			result.Detail = "read-back failed: " + diag.Title
			return result
		}
		result.Outcome = models.SyntheticVerified
		result.Detail = "verified via read-back"
	}

	// Cleanup (best-effort delete) is always attempted for every
	// verified/accepted run when the platform supports it — unlike
	// `notify verify`'s opt-in --cleanup flag, the weekly synthetic
	// test always cleans up after itself (SPEC-v0.8 §3-2: "가능하면
	// read-back 후 --cleanup 방식으로 삭제한다"), since there is no
	// interactive operator here to decide otherwise and leaving a
	// week's worth of test messages behind in a real Discord/Telegram
	// channel would be poor behavior for an opt-in automated feature.
	// A cleanup failure does not downgrade an already-successful
	// outcome — the message was sent and (if applicable) verified; a
	// leftover test message is a minor cosmetic issue, not a
	// connectivity failure, so it is folded into Detail rather than
	// flipping Outcome to failed.
	if deleter, ok := sender.(notify.MessageDeleter); ok {
		delCtx, delCancel := context.WithTimeout(ctx, timeout)
		delErr := deleter.Delete(delCtx, ref)
		delCancel()
		if delErr != nil {
			result.Detail += fmt.Sprintf("; cleanup failed: %s", notify.DiagnoseError(delErr).Title)
		} else {
			result.Detail += "; cleaned up"
		}
	}

	return result
}
