package alerting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Field is one label/value pair attached to a Message (e.g. "Host" /
// "web-1"), rendered by each Sender in whatever way suits its platform.
type Field struct {
	Name  string
	Value string
}

// Message is the platform-agnostic notification payload the engine hands
// to a Sender. It mirrors internal/notify.Message exactly (see
// notes/v05-prep.md and SPEC-v0.5 §B "Notifiers"); the two types are
// kept structurally identical on purpose so cmd/hub's adapter between
// this package and internal/notify is a straight field-for-field copy,
// not a semantic translation.
type Message struct {
	Title    string
	Text     string
	Severity Severity
	Fields   []Field
	// URL is a dashboard link relevant to the message (e.g. the firing
	// host's detail page), optional.
	URL string
	// Image is an optional PNG-encoded chart image.
	Image []byte
	// ImageName is the filename Image should be attached/referenced as
	// (e.g. "chart.png"), ignored when Image is empty.
	ImageName string
}

// HasImage reports whether m carries chart image bytes, mirroring
// internal/notify.Message.HasImage (the two types are kept field-for-
// field identical; see Message's doc comment).
func (m Message) HasImage() bool {
	return len(m.Image) > 0
}

// Severity classifies a Message for a Sender's color/icon choice.
type Severity string

// Supported severities.
const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
	SeverityResolved Severity = "resolved"
)

// Sender delivers one notification message to a channel. Implementations
// live in internal/notify; this interface is redeclared here (rather
// than importing internal/notify.Sender directly) so internal/alerting
// has no import-time dependency on internal/notify, matching the same
// deliberate decoupling hub.NotifySender uses (see
// internal/hub/server.go's doc comment on that type). cmd/hub adapts a
// concrete internal/notify.Sender to this interface (a trivial wrapper,
// since Message/Field/Severity here are field-for-field identical to
// internal/notify's).
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// SenderFactory constructs a Sender for a configured channel. Returning
// an error (e.g. an unrecognized channel Type or invalid Config) must
// not panic.
type SenderFactory func(ch models.NotifyChannel) (Sender, error)

// errNoSenderFactory is recorded as a delivery's error when no
// SenderFactory was configured (e.g. the notify stage hasn't been wired
// in yet, or a test intentionally omits it).
var errNoSenderFactory = errors.New("alerting: no notify sender factory configured")

// RetryAfterError, when returned by a Sender, tells the delivery worker
// how long to wait before its next retry attempt (e.g. from a platform's
// 429 Retry-After header), overriding the default backoff for that one
// retry. Senders that don't have a Retry-After hint should return a
// plain error instead.
type RetryAfterError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *RetryAfterError) Error() string { return e.Err.Error() }
func (e *RetryAfterError) Unwrap() error { return e.Err }

// deliveryJob is one channel's pending notification for one alert event.
// A job with a non-nil barrier is a synchronization marker used by
// deliveryWorker.drain rather than a real delivery (event/channel/
// message are zero-valued and never sent).
type deliveryJob struct {
	event   models.AlertEvent
	channel models.NotifyChannel
	message Message
	barrier chan struct{}
}

// maxDeliveryAttempts is the total number of send attempts per job
// (1 initial + up to 2 retries = 3, per SPEC-v0.5 §B "3 retries").
const maxDeliveryAttempts = 3

// defaultQueueSize is the delivery worker's default bounded queue depth.
const defaultQueueSize = 100

// defaultDeliveryTimeout bounds a single delivery attempt.
const defaultDeliveryTimeout = 20 * time.Second

// baseRetryBackoff is the delay before the first retry; each subsequent
// retry doubles it, unless a RetryAfterError overrides it.
const baseRetryBackoff = 2 * time.Second

// deliveryWorkerOptions configures a deliveryWorker.
type deliveryWorkerOptions struct {
	Store     Store
	Sender    SenderFactory
	Clock     func() time.Time
	Logger    *slog.Logger
	QueueSize int
	Timeout   time.Duration
	// Diagnose optionally classifies a failed delivery's error into a
	// models.DiagnosisCode; see Options.Diagnose's doc comment.
	Diagnose func(error) *models.DiagnosisCode
}

// deliveryWorker asynchronously sends notifications queued by the
// engine, retrying transient failures with backoff and recording every
// attempt's outcome onto the originating AlertEvent.
type deliveryWorker struct {
	store    Store
	sender   SenderFactory
	clock    func() time.Time
	logger   *slog.Logger
	timeout  time.Duration
	diagnose func(error) *models.DiagnosisCode

	queue chan deliveryJob
	wg    sync.WaitGroup

	closeOnce sync.Once
	closed    chan struct{}

	// afterFunc is used for retry backoff sleeps; overridden by tests to
	// avoid real time.Sleep waits (see engine_test.go's fakeAfter).
	afterFunc func(d time.Duration) <-chan time.Time
}

func newDeliveryWorker(opts deliveryWorkerOptions) *deliveryWorker {
	queueSize := opts.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultDeliveryTimeout
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	w := &deliveryWorker{
		store:    opts.Store,
		sender:   opts.Sender,
		clock:    clock,
		logger:   logger,
		timeout:  timeout,
		diagnose: opts.Diagnose,
		queue:    make(chan deliveryJob, queueSize),
		closed:   make(chan struct{}),
		afterFunc: func(d time.Duration) <-chan time.Time {
			return time.After(d)
		},
	}

	w.wg.Add(1)
	go w.run()
	return w
}

// enqueue submits job for asynchronous delivery. If the queue is full,
// the job is dropped and logged at Error level (SPEC-v0.5 §B specifies a
// bounded queue; a persistently full queue means delivery is falling
// behind, which is an operational problem to surface, not to block the
// evaluation path over).
func (w *deliveryWorker) enqueue(job deliveryJob) {
	select {
	case w.queue <- job:
	default:
		w.logger.Error("alerting: delivery queue full, dropping notification",
			"event_id", job.event.ID, "channel_id", job.channel.ID, "channel_name", job.channel.Name)
	}
}

// close stops accepting new work implicitly (no more enqueue calls are
// expected after this) and blocks until every already-queued/in-flight
// delivery has finished.
func (w *deliveryWorker) close() {
	w.closeOnce.Do(func() {
		close(w.queue)
	})
	w.wg.Wait()
}

// drain blocks until every job enqueued before this call has finished
// processing, without stopping the worker. It works by enqueuing a
// barrier job (empty channel) and waiting for the single worker
// goroutine to reach it, which — since jobs are processed strictly in
// FIFO order by exactly one goroutine — guarantees every prior job has
// already been fully processed (including its retries) by the time the
// barrier itself is dequeued.
func (w *deliveryWorker) drain() {
	barrier := make(chan struct{})
	select {
	case w.queue <- deliveryJob{barrier: barrier}:
	case <-w.closed:
		return
	}
	<-barrier
}

func (w *deliveryWorker) run() {
	defer w.wg.Done()
	for job := range w.queue {
		if job.barrier != nil {
			close(job.barrier)
			continue
		}
		w.deliver(job)
	}
}

// deliver sends job, retrying on failure up to maxDeliveryAttempts times
// with backoff, then records the final outcome. recordDelivery re-fetches
// the event before its full-row update so independently queued channels
// cannot overwrite one another's earlier delivery result.
func (w *deliveryWorker) deliver(job deliveryJob) {
	sender, err := w.resolveSender(job.channel)
	if err != nil {
		w.recordDelivery(job, err)
		return
	}

	backoff := baseRetryBackoff
	var lastErr error
	for attempt := 1; attempt <= maxDeliveryAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
		err := sender.Send(ctx, job.message)
		cancel()
		if err == nil {
			w.recordDelivery(job, nil)
			return
		}
		lastErr = err

		if attempt == maxDeliveryAttempts {
			break
		}

		wait := backoff
		var raErr *RetryAfterError
		if errors.As(err, &raErr) {
			wait = raErr.RetryAfter
		}
		select {
		case <-w.afterFunc(wait):
		case <-w.closed:
			// Engine is shutting down; abandon remaining retries rather
			// than blocking Close indefinitely.
			w.recordDelivery(job, lastErr)
			return
		}
		backoff *= 2
	}

	w.recordDelivery(job, lastErr)
}

// resolveSender constructs a Sender for ch via w.sender, mapping a nil
// factory to errNoSenderFactory rather than a nil-pointer call.
func (w *deliveryWorker) resolveSender(ch models.NotifyChannel) (Sender, error) {
	if w.sender == nil {
		return nil, errNoSenderFactory
	}
	sender, err := w.sender(ch)
	if err != nil {
		return nil, fmt.Errorf("alerting: build sender for channel %d (%s): %w", ch.ID, ch.Type, err)
	}
	if sender == nil {
		return nil, fmt.Errorf("alerting: sender factory returned nil for channel %d (%s)", ch.ID, ch.Type)
	}
	return sender, nil
}

// recordDelivery appends job's outcome to job.event.Deliveries and
// persists the updated event. A nil sendErr records a successful
// delivery; a non-nil sendErr's message is recorded on Delivery.Error
// and, when w.diagnose is configured, classified into
// Delivery.Diagnosis (see Options.Diagnose). Errors from the store are
// logged (there is nothing further to do: this already ran on the
// async delivery path, no request is waiting on it).
func (w *deliveryWorker) recordDelivery(job deliveryJob, sendErr error) {
	ok := sendErr == nil
	d := models.Delivery{
		ChannelID:   job.channel.ID,
		ChannelName: job.channel.Name,
		OK:          ok,
		At:          w.clock().Unix(),
	}
	if !ok {
		d.Error = sendErr.Error()
		if w.diagnose != nil {
			d.Diagnosis = w.diagnose(sendErr)
		}
		w.logger.Error("alerting: delivery failed",
			"event_id", job.event.ID, "channel_id", job.channel.ID, "channel_name", job.channel.Name, "error", d.Error)
	} else {
		w.logger.Info("alerting: delivery ok",
			"event_id", job.event.ID, "channel_id", job.channel.ID, "channel_name", job.channel.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Re-fetch the event's current row before appending: notifyEvent
	// enqueues one deliveryJob per channel from a single shared event
	// snapshot (job.event), so two jobs for the same multi-channel
	// firing would otherwise both append to that same stale
	// Deliveries slice and each write back a full replace — the second
	// write silently discards the first job's delivery. Since this
	// worker is the only writer and processes jobs strictly FIFO on one
	// goroutine (see run()), a re-fetch immediately before the write
	// always observes every delivery recorded by an earlier job for the
	// same event, making the read-append-write below safe without
	// further locking.
	current, err := w.store.GetAlertEvent(ctx, job.event.ID)
	if err != nil {
		w.logger.Error("alerting: re-fetch event before recording delivery failed; appending to stale snapshot", "event_id", job.event.ID, "error", err)
		current = job.event
	}
	current.Deliveries = append(append([]models.Delivery{}, current.Deliveries...), d)
	if err := w.store.UpdateAlertEvent(ctx, current); err != nil {
		w.logger.Error("alerting: record delivery result failed", "event_id", job.event.ID, "error", err)
	}
}
