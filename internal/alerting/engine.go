// Package alerting implements cloud-pulse's alert rule engine
// (SPEC-v0.5 §B): evaluating configured AlertRules against host state,
// driving a per-rule-per-host state machine (ok -> pending -> firing ->
// resolved/ok) with sustained-window semantics, and dispatching
// notifications asynchronously through a bounded worker queue.
//
// Engine depends only on the Store interface defined in this package
// (a subset of hub.Store) and the Sender/Message shapes it declares
// itself (mirroring internal/notify's, so this package has no import on
// internal/notify — see notify.go), avoiding any import cycle with
// internal/hub. cmd/hub wires a *storage.DB and an internal/notify-backed
// factory into a *Engine and adapts it to hub.AlertEngine/
// hub.NotifySenderFactory.
package alerting

import (
	"log/slog"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// defaultCooldownSec is applied by the hub API layer when creating a
// rule with CooldownSec left at its zero value from a client that
// didn't set one explicitly; the engine itself treats 0 literally as
// "never re-notify" per the AlertRule.CooldownSec doc comment, so this
// constant is exported for callers assembling a new rule, not used
// internally by Evaluate.
const DefaultCooldownSec = 3600

// egressRetention is how far back Evaluate needs raw samples for a
// sustained-window check; unused for egress_out_pct/egress_in_pct/
// host_down, which read HostSnapshot fields directly rather than a
// series.
const seriesLookbackTolerance = 30 * time.Second

// Engine evaluates alert rules against host state and manages the
// resulting firing/resolved lifecycle, satisfying hub.AlertEngine.
type Engine struct {
	store  Store
	sender SenderFactory
	clock  func() time.Time
	logger *slog.Logger

	delivery *deliveryWorker
}

// Options configures a new Engine.
type Options struct {
	// Store is the persistence backend rules/state/events are read from
	// and written to. Required.
	Store Store
	// SenderFactory constructs a Sender for a configured channel.
	// Required for any notification to actually be delivered; a nil
	// factory makes Evaluate still run the state machine and record
	// events, but every delivery attempt fails with
	// errNoSenderFactory (recorded in the event, never panics).
	SenderFactory SenderFactory
	// Clock returns the current time; nil defaults to time.Now. Tests
	// inject a fake clock for deterministic sustained-window/cooldown
	// behavior.
	Clock func() time.Time
	// Logger is used for all engine/delivery logging; nil defaults to
	// slog.Default().
	Logger *slog.Logger
	// QueueSize bounds the async delivery worker's queue; <=0 defaults
	// to 100 (SPEC-v0.5 §B).
	QueueSize int
	// DeliveryTimeout bounds a single delivery attempt (across all of
	// its retries combined is NOT what this bounds -- see
	// perAttemptTimeout in notify.go); <=0 defaults to 20s.
	DeliveryTimeout time.Duration
	// Diagnose classifies a failed delivery's error into a
	// models.DiagnosisCode (SPEC-v0.6 §4), recorded on the Delivery.
	// Optional and nil by default: this package deliberately has no
	// import on internal/notify (see notify.go's Message/Sender doc
	// comments) so it cannot classify notify-specific error types
	// itself — cmd/hub wires this to internal/notify.DiagnoseError's
	// Code field. A nil Diagnose (e.g. in tests, or an older wiring)
	// simply leaves Delivery.Diagnosis unset; delivery/error recording
	// otherwise behaves identically.
	Diagnose func(error) *models.DiagnosisCode
}

// New constructs an Engine and starts its background delivery worker.
// Callers must call Close to stop the worker and wait for in-flight
// deliveries to finish (e.g. during hub shutdown).
func New(opts Options) *Engine {
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	e := &Engine{
		store:  opts.Store,
		sender: opts.SenderFactory,
		clock:  clock,
		logger: logger,
	}
	e.delivery = newDeliveryWorker(deliveryWorkerOptions{
		Store:     opts.Store,
		Sender:    opts.SenderFactory,
		Clock:     clock,
		Logger:    logger,
		QueueSize: opts.QueueSize,
		Timeout:   opts.DeliveryTimeout,
		Diagnose:  opts.Diagnose,
	})
	return e
}

// Close stops the delivery worker, waiting for all in-flight and already
// enqueued deliveries to finish. Safe to call once; a second call
// no-ops.
func (e *Engine) Close() {
	e.delivery.close()
}

// Wait blocks until the delivery worker's queue has drained and every
// enqueued delivery as of the call has finished, without stopping the
// worker (unlike Close, Evaluate may still be called afterward). Used by
// tests that need to observe delivery results synchronously; production
// callers use Close during shutdown instead.
func (e *Engine) Wait() {
	e.delivery.drain()
}
