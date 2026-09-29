package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// newTestServerWithEngine builds a Server wired to a real
// *alerting.Engine (Options.Alerting/NotifyFactory) via senderFactory,
// which the engine's async delivery worker AND the hub's channel-test
// endpoints both resolve channels through. clock (opts.Now) drives both
// the Server and the Engine so ingest-triggered and scheduler-triggered
// evaluation see identical timestamps.
func newTestServerWithEngine(t *testing.T, opts Options, store Store, senderFactory alerting.SenderFactory) (*Server, *alerting.Engine) {
	t.Helper()
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	// hub.Store and alerting.Store are structurally identical subsets;
	// fakeStore (and *storage.DB) already satisfy alerting.Store's
	// smaller method set since it is a strict subset of hub.Store's.
	alertStore, ok := store.(alerting.Store)
	if !ok {
		t.Fatalf("store %T does not implement alerting.Store", store)
	}

	engine := alerting.New(alerting.Options{
		Store:         alertStore,
		SenderFactory: senderFactory,
		Clock:         now,
		Logger:        testLogger(),
	})
	t.Cleanup(engine.Close)

	opts.Alerting = newAlertEngineAdapter(engine)
	opts.NotifyFactory = newAlertingSenderFactory(senderFactory)

	s := New(opts, store, nil, nil, nil, testLogger())
	return s, engine
}

// recordingSender is an alerting.Sender test double that appends every
// message it receives (paired with the channel it was constructed for)
// to a shared, mutex-guarded recorder.
type alertMessageRecorder struct {
	mu    sync.Mutex
	calls []recordedAlertMessage
}

type recordedAlertMessage struct {
	channel models.NotifyChannel
	message alerting.Message
}

func (r *alertMessageRecorder) senderFactory() alerting.SenderFactory {
	return func(ch models.NotifyChannel) (alerting.Sender, error) {
		return recordingSender{ch: ch, rec: r}, nil
	}
}

func (r *alertMessageRecorder) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *alertMessageRecorder) messages() []recordedAlertMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedAlertMessage, len(r.calls))
	copy(out, r.calls)
	return out
}

type recordingSender struct {
	ch  models.NotifyChannel
	rec *alertMessageRecorder
}

func (s recordingSender) Send(_ context.Context, msg alerting.Message) error {
	s.rec.mu.Lock()
	defer s.rec.mu.Unlock()
	s.rec.calls = append(s.rec.calls, recordedAlertMessage{channel: s.ch, message: msg})
	return nil
}
