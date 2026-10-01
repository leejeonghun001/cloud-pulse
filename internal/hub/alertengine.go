package hub

import (
	"context"
	"errors"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// StorageAlertEvaluator is an optional capability an AlertEngine
// implementation may additionally provide: evaluating SPEC-v0.7 §3's
// account-scoped storage_usage_pct rules against connected storage
// accounts. This is intentionally a separate interface, checked via a
// type assertion in storagerun.go, rather than a new method on the
// shared AlertEngine interface in server.go — AlertEngine is a
// multi-stage-owned contract (alerting's own tests, cmd/hub's wiring,
// and this storage stage all touch it), and adding a method there
// would force every other AlertEngine implementation/fake in the
// codebase (including test doubles this stage doesn't own) to grow a
// matching method just to keep compiling, even on hub builds that never
// configure any storage accounts at all.
type StorageAlertEvaluator interface {
	EvaluateStorage(ctx context.Context, now time.Time, accounts []alerting.StorageAccountUsage) error
}

// RolloutAlertEvaluator is an optional capability an AlertEngine
// implementation may additionally provide: evaluating SPEC-v0.8 §3-1's
// agent_outdated (host-scoped, reuses the ok/pending/firing state
// machine) and update_batch_failed (batch-scoped, fire-once) rules.
// Declared as a separate interface and checked via a type assertion in
// fleetalerts.go, for the same reason StorageAlertEvaluator is (see its
// own doc comment): AlertEngine is a multi-stage-owned contract, and a
// hub build with no rollout rules configured should never be forced to
// satisfy a wider interface just to keep compiling.
type RolloutAlertEvaluator interface {
	EvaluateAgentOutdated(ctx context.Context, now time.Time, hosts []alerting.AgentOutdatedInput) error
	EvaluateUpdateBatch(ctx context.Context, now time.Time, outcome alerting.UpdateBatchOutcome) error
}

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

// EvaluateStorage implements hub.StorageAlertEvaluator (an optional
// interface checked via type assertion, not part of the shared
// AlertEngine interface — see server.go's StorageAlertEvaluator doc
// comment for why SPEC-v0.7 §3's storage stage avoids widening a
// shared, multi-stage-owned interface), delegating to the underlying
// engine's own EvaluateStorage.
func (a alertEngineAdapter) EvaluateStorage(ctx context.Context, now time.Time, accounts []alerting.StorageAccountUsage) error {
	return a.engine.EvaluateStorage(ctx, now, accounts)
}

// EvaluateAgentOutdated implements hub.RolloutAlertEvaluator (see
// server.go's RolloutAlertEvaluator doc comment), delegating to the
// underlying engine.
func (a alertEngineAdapter) EvaluateAgentOutdated(ctx context.Context, now time.Time, hosts []alerting.AgentOutdatedInput) error {
	return a.engine.EvaluateAgentOutdated(ctx, now, hosts)
}

// EvaluateUpdateBatch implements hub.RolloutAlertEvaluator, delegating
// to the underlying engine.
func (a alertEngineAdapter) EvaluateUpdateBatch(ctx context.Context, now time.Time, outcome alerting.UpdateBatchOutcome) error {
	return a.engine.EvaluateUpdateBatch(ctx, now, outcome)
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
