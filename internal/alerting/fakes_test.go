package alerting

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// testLogger returns a slog.Logger that discards output, for tests that
// don't care about log content but need a non-nil logger.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// fakeStore is a minimal in-memory Store implementation for engine
// tests, independent of internal/hub's fakeStore (which this package
// must not import, to avoid a test-only dependency on internal/hub).
type fakeStore struct {
	mu sync.Mutex

	rules    map[int64]models.AlertRule
	channels map[int64]models.NotifyChannel
	states   map[string]models.AlertState
	events   map[int64]models.AlertEvent
	nextID   int64

	series map[string]models.Series // hostID -> full series available

	ruleErr   error
	seriesErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		rules:    make(map[int64]models.AlertRule),
		channels: make(map[int64]models.NotifyChannel),
		states:   make(map[string]models.AlertState),
		events:   make(map[int64]models.AlertEvent),
		series:   make(map[string]models.Series),
	}
}

func stateKey(ruleID int64, hostID string) string { return fmt.Sprintf("%d|%s", ruleID, hostID) }

func (f *fakeStore) ListAlertRules(_ context.Context) ([]models.AlertRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ruleErr != nil {
		return nil, f.ruleErr
	}
	out := make([]models.AlertRule, 0, len(f.rules))
	for _, r := range f.rules {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) GetAlertRule(_ context.Context, id int64) (models.AlertRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rules[id]
	if !ok {
		return models.AlertRule{}, models.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) GetNotifyChannel(_ context.Context, id int64) (models.NotifyChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.channels[id]
	if !ok {
		return models.NotifyChannel{}, models.ErrNotFound
	}
	return ch, nil
}

func (f *fakeStore) GetAlertState(_ context.Context, ruleID int64, hostID string) (models.AlertState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.states[stateKey(ruleID, hostID)]
	if !ok {
		return models.AlertState{RuleID: ruleID, HostID: hostID, State: models.AlertStateOK}, nil
	}
	return st, nil
}

func (f *fakeStore) SetAlertState(_ context.Context, st models.AlertState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[stateKey(st.RuleID, st.HostID)] = st
	return nil
}

func (f *fakeStore) DeleteAlertStatesForRule(_ context.Context, ruleID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, st := range f.states {
		if st.RuleID == ruleID {
			delete(f.states, k)
		}
	}
	return nil
}

func (f *fakeStore) CreateAlertEvent(_ context.Context, ev models.AlertEvent) (models.AlertEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	ev.ID = f.nextID
	if ev.Deliveries == nil {
		ev.Deliveries = []models.Delivery{}
	}
	f.events[ev.ID] = ev
	return ev, nil
}

func (f *fakeStore) UpdateAlertEvent(_ context.Context, ev models.AlertEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.events[ev.ID]; !ok {
		return models.ErrNotFound
	}
	if ev.Deliveries == nil {
		ev.Deliveries = []models.Delivery{}
	}
	f.events[ev.ID] = ev
	return nil
}

func (f *fakeStore) GetActiveAlertEvent(_ context.Context, ruleID int64, hostID string) (models.AlertEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best models.AlertEvent
	found := false
	for _, ev := range f.events {
		if ev.RuleID == ruleID && ev.HostID == hostID && ev.State == models.AlertEventFiring {
			if !found || ev.StartedAt > best.StartedAt {
				best = ev
				found = true
			}
		}
	}
	if !found {
		return models.AlertEvent{}, models.ErrNotFound
	}
	return best, nil
}

func (f *fakeStore) GetAlertEvent(_ context.Context, id int64) (models.AlertEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev, ok := f.events[id]
	if !ok {
		return models.AlertEvent{}, models.ErrNotFound
	}
	return ev, nil
}

func (f *fakeStore) QuerySeries(_ context.Context, hostID string, from, to int64) (models.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seriesErr != nil {
		return models.Series{}, f.seriesErr
	}
	full, ok := f.series[hostID]
	if !ok {
		return models.NewSeries(hostID, models.ResolutionRaw, from, to), nil
	}
	out := models.NewSeries(hostID, models.ResolutionRaw, from, to)
	for i, ts := range full.Timestamps {
		if ts < from || ts > to {
			continue
		}
		out.Append(models.SeriesPoint{
			TS: ts, CPU: full.CPU[i], Mem: full.Mem[i], Disk: full.Disk[i], Load1: full.Load1[i],
		})
	}
	return out, nil
}

// setSeries stores a full CPU/Mem/Disk/Load1 series for hostID; points
// are given as (ts, cpu) pairs for brevity in CPU-only tests, with
// Mem/Disk/Load1 mirroring the same value (tests that need distinct
// values per metric use setSeriesFull instead).
func (f *fakeStore) setSeriesCPU(hostID string, points []seriesPoint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := models.NewSeries(hostID, models.ResolutionRaw, 0, 0)
	for _, p := range points {
		s.Append(models.SeriesPoint{TS: p.ts, CPU: p.value, Mem: p.value, Disk: p.value, Load1: p.value})
	}
	f.series[hostID] = s
}

type seriesPoint struct {
	ts    int64
	value float64
}

func (f *fakeStore) setRule(r models.AlertRule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules[r.ID] = r
}

func (f *fakeStore) setChannel(ch models.NotifyChannel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[ch.ID] = ch
}

func (f *fakeStore) getState(ruleID int64, hostID string) models.AlertState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[stateKey(ruleID, hostID)]
}

func (f *fakeStore) listEvents() []models.AlertEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.AlertEvent, 0, len(f.events))
	for _, ev := range f.events {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// fakeSender is a Sender test double recording every call, optionally
// failing a configurable number of times before succeeding (to exercise
// retry behavior).
type fakeSender struct {
	mu        sync.Mutex
	calls     []Message
	failTimes int // number of calls that return an error before succeeding
	err       error
	retryHint time.Duration
}

func (s *fakeSender) Send(_ context.Context, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, msg)
	if len(s.calls) <= s.failTimes {
		if s.retryHint > 0 {
			return &RetryAfterError{Err: s.errOrDefault(), RetryAfter: s.retryHint}
		}
		return s.errOrDefault()
	}
	return nil
}

func (s *fakeSender) errOrDefault() error {
	if s.err != nil {
		return s.err
	}
	return errors.New("fake sender error")
}

func (s *fakeSender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *fakeSender) lastMessage() Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

// fakeClock provides a controllable time.Time for deterministic tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// instantAfter replaces deliveryWorker.afterFunc in tests so retry
// backoff waits resolve immediately rather than sleeping real time.
func instantAfter(_ time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}
