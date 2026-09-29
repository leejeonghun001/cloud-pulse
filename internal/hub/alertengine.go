package hub

import (
	"context"
	"errors"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// alertEngineAdapter adapts a *alerting.Engine to this package's
// AlertEngine interface. The method sets are already identical
// (Evaluate/Preview with the same signatures), so this is a type
// definition rather than a wrapper with forwarding methods — declared
// here (not as a plain type alias in server.go) because internal/hub
// must not import internal/alerting at package-var/interface-declaration
// scope in server.go itself (see AlertEngine's doc comment); confining
// the import to this one adapter file keeps that boundary intentional
// and easy to audit.
type alertEngineAdapter struct {
	engine *alerting.Engine
}

// newAlertEngineAdapter wraps engine as a hub.AlertEngine.
func newAlertEngineAdapter(engine *alerting.Engine) AlertEngine {
	return alertEngineAdapter{engine: engine}
}

// WrapAlertEngine adapts a *alerting.Engine to this package's
// AlertEngine interface for cmd/hub to pass into Options.Alerting; it is
// the exported form of newAlertEngineAdapter, kept as a thin wrapper so
// this package's own tests can keep using the unexported name.
func WrapAlertEngine(engine *alerting.Engine) AlertEngine {
	return newAlertEngineAdapter(engine)
}

func (a alertEngineAdapter) Evaluate(ctx context.Context, now time.Time, hosts []models.HostSnapshot) error {
	return a.engine.Evaluate(ctx, now, hosts)
}

func (a alertEngineAdapter) Preview(ctx context.Context, now time.Time, rule models.AlertRule, hosts []models.HostSnapshot) (map[string]bool, error) {
	return a.engine.Preview(ctx, now, rule, hosts)
}

// Wait implements alertEngineWaiter (see server.go's Wait), delegating
// to the underlying engine's delivery-queue drain.
func (a alertEngineAdapter) Wait() {
	a.engine.Wait()
}

// notifySenderAdapter adapts an alerting.Sender to this package's
// NotifySender interface (Send(ctx, msg any) error), converting the
// any-boxed msg back to alerting.Message. Used by
// newAlertingNotifyFactory so cmd/hub (and tests in this package) can
// hand the hub package a NotifySenderFactory backed by
// internal/alerting's own Sender type without internal/hub importing
// internal/notify.
type notifySenderAdapter struct {
	sender alerting.Sender
}

func (a notifySenderAdapter) Send(ctx context.Context, msg any) error {
	m, ok := msg.(alerting.Message)
	if !ok {
		return errUnexpectedMessageType
	}
	return a.sender.Send(ctx, m)
}

// errUnexpectedMessageType is returned by notifySenderAdapter.Send when
// msg isn't an alerting.Message, which would indicate a bug in the
// caller (the engine only ever enqueues alerting.Message values) rather
// than a real delivery failure.
var errUnexpectedMessageType = errors.New("hub: notify sender adapter: unexpected message type")

// newAlertingSenderFactory adapts an alerting.SenderFactory to this
// package's NotifySenderFactory, so cmd/hub (and this package's own
// tests) can wire internal/alerting's engine straight through to
// Options.NotifyFactory without a second, separate factory
// implementation.
func newAlertingSenderFactory(factory alerting.SenderFactory) NotifySenderFactory {
	return func(ch models.NotifyChannel) (NotifySender, error) {
		sender, err := factory(ch)
		if err != nil {
			return nil, err
		}
		return notifySenderAdapter{sender: sender}, nil
	}
}

// WrapNotifySenderFactory adapts an alerting.SenderFactory to this
// package's NotifySenderFactory for cmd/hub to pass into
// Options.NotifyFactory; it is the exported form of
// newAlertingSenderFactory, kept as a thin wrapper so this package's own
// tests can keep using the unexported name.
func WrapNotifySenderFactory(factory alerting.SenderFactory) NotifySenderFactory {
	return newAlertingSenderFactory(factory)
}
