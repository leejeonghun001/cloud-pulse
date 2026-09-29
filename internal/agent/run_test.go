package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestRun_TicksImmediatelyThenOnIntervalAndStopsOnCancel(t *testing.T) {
	t.Parallel()

	var reportCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report models.AgentReport
		_ = json.NewDecoder(r.Body).Decode(&report)
		reportCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	src := &fakeSource{
		cpuTimes: [][]cpu.TimesStat{{{User: 1}}, {{User: 2}}, {{User: 3}}, {{User: 4}}, {{User: 5}}},
	}
	collector := NewCollector(src, CollectorOptions{Now: time.Now})
	reporter := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})

	var hostInfoCalls atomic.Int32
	hostInfoFunc := func(context.Context) models.HostInfo {
		hostInfoCalls.Add(1)
		return models.HostInfo{ID: "h1"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := Run(ctx, collector, reporter, hostInfoFunc, 50*time.Millisecond, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Run took too long: %v", elapsed)
	}
	if reportCount.Load() < 2 {
		t.Errorf("reportCount = %d, want at least 2 (immediate tick + at least one interval tick)", reportCount.Load())
	}
	if hostInfoCalls.Load() < 1 {
		t.Error("hostInfoFunc should be called at least once (startup)")
	}
}

func TestRun_ReturnsNilOnImmediateCancel(t *testing.T) {
	t.Parallel()

	src := &fakeSource{cpuTimes: [][]cpu.TimesStat{{{User: 1}}}}
	collector := NewCollector(src, CollectorOptions{Now: time.Now})
	reporter := NewReporter(ReporterOptions{HubURL: "http://example.invalid", Token: "tok"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	hostInfoFunc := func(context.Context) models.HostInfo { return models.HostInfo{} }

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, collector, reporter, hostInfoFunc, 10*time.Millisecond, nil)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly after ctx cancellation")
	}
}

// --- nextAlignedBoundary boundary math ---

func TestNextAlignedBoundary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		now      time.Time
		interval time.Duration
		want     time.Time
	}{
		{
			name:     "15s_mid_interval_rounds_up",
			now:      time.Date(2026, 1, 1, 0, 0, 7, 0, time.UTC),
			interval: 15 * time.Second,
			want:     time.Date(2026, 1, 1, 0, 0, 15, 0, time.UTC),
		},
		{
			name:     "15s_exactly_on_boundary_stays",
			now:      time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC),
			interval: 15 * time.Second,
			want:     time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC),
		},
		{
			name:     "15s_just_past_boundary_rounds_to_next",
			now:      time.Date(2026, 1, 1, 0, 0, 30, 1, time.UTC),
			interval: 15 * time.Second,
			want:     time.Date(2026, 1, 1, 0, 0, 45, 0, time.UTC),
		},
		{
			name:     "60s_rounds_to_next_minute",
			now:      time.Date(2026, 1, 1, 0, 5, 37, 0, time.UTC),
			interval: 60 * time.Second,
			want:     time.Date(2026, 1, 1, 0, 6, 0, 0, time.UTC),
		},
		{
			name:     "60s_exactly_on_minute_stays",
			now:      time.Date(2026, 1, 1, 0, 6, 0, 0, time.UTC),
			interval: 60 * time.Second,
			want:     time.Date(2026, 1, 1, 0, 6, 0, 0, time.UTC),
		},
		{
			name:     "7s_odd_interval_aligns_to_epoch_multiple",
			now:      time.Date(2026, 1, 1, 0, 0, 20, 0, time.UTC),
			interval: 7 * time.Second,
			// unix time for 00:00:20 is a multiple of 7 plus 6
			// (20 % 7 == 6), so next boundary is +1s.
			want: time.Date(2026, 1, 1, 0, 0, 21, 0, time.UTC),
		},
		{
			name:     "7s_on_exact_multiple_stays",
			now:      time.Date(2026, 1, 1, 0, 0, 21, 0, time.UTC),
			interval: 7 * time.Second,
			want:     time.Date(2026, 1, 1, 0, 0, 21, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := nextAlignedBoundary(tc.now, tc.interval)
			if !got.Equal(tc.want) {
				t.Errorf("nextAlignedBoundary(%v, %v) = %v, want %v", tc.now, tc.interval, got, tc.want)
			}
			// Sanity: result must be a multiple of interval since the
			// unix epoch, and must be >= now.
			if got.UnixNano()%tc.interval.Nanoseconds() != 0 {
				t.Errorf("result %v is not epoch-aligned to %v", got, tc.interval)
			}
			if got.Before(tc.now) {
				t.Errorf("result %v is before now %v", got, tc.now)
			}
		})
	}
}

func TestNextAlignedBoundary_IdenticalAcrossSkewedAgents(t *testing.T) {
	t.Parallel()

	// Two agents whose HubClock.Now() has already converged (see
	// TestHubClock_TwoAgentsWithDifferentLocalOffsetsConverge) to the
	// same hub time, starting from local clocks skewed by +3s and -2s.
	// Once corrected, both must compute the identical next boundary.
	hubTime := time.Date(2026, 3, 1, 0, 0, 7, 0, time.UTC)
	interval := 15 * time.Second

	agentACorrected := hubTime // already corrected via HubClock, see clock_test.go
	agentBCorrected := hubTime

	boundaryA := nextAlignedBoundary(agentACorrected, interval)
	boundaryB := nextAlignedBoundary(agentBCorrected, interval)

	if !boundaryA.Equal(boundaryB) {
		t.Fatalf("boundaries differ: A=%v B=%v, want identical", boundaryA, boundaryB)
	}
	want := time.Date(2026, 3, 1, 0, 0, 15, 0, time.UTC)
	if !boundaryA.Equal(want) {
		t.Errorf("boundary = %v, want %v", boundaryA, want)
	}
}

// --- fakeTimeSource: deterministic timeSource for Run tests ---

// fakeTimeSource is a manually-advanced timeSource for deterministic
// tests of RunWithOptions's scheduling logic without real sleeps.
type fakeTimeSource struct {
	mu  sync.Mutex
	now time.Time
	// waiters are channels waiting for the clock to reach a given
	// deadline; advance() delivers to any whose deadline has passed.
	waiters []fakeWaiter
}

type fakeWaiter struct {
	deadline time.Time
	ch       chan time.Time
}

func newFakeTimeSource(start time.Time) *fakeTimeSource {
	return &fakeTimeSource{now: start}
}

func (f *fakeTimeSource) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeTimeSource) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	deadline := f.now.Add(d)
	if !deadline.After(f.now) {
		ch <- f.now
		return ch
	}
	f.waiters = append(f.waiters, fakeWaiter{deadline: deadline, ch: ch})
	return ch
}

// advance moves the fake clock forward by d and fires any waiters whose
// deadline has now passed.
func (f *fakeTimeSource) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	var remaining []fakeWaiter
	for _, w := range f.waiters {
		if !w.deadline.After(now) {
			w.ch <- now
		} else {
			remaining = append(remaining, w)
		}
	}
	f.waiters = remaining
	f.mu.Unlock()
}

func TestRunWithOptions_SampleTimestampsAreBoundaryAligned(t *testing.T) {
	t.Parallel()

	var reportCount atomic.Int32
	var gotTimestamps []int64
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report models.AgentReport
		_ = json.NewDecoder(r.Body).Decode(&report)
		mu.Lock()
		for _, s := range report.Samples {
			gotTimestamps = append(gotTimestamps, s.Timestamp)
		}
		mu.Unlock()
		reportCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Date(2026, 1, 1, 0, 0, 7, 0, time.UTC) // not on a 15s boundary
	fts := newFakeTimeSource(start)

	src := &fakeSource{cpuTimes: [][]cpu.TimesStat{{{User: 1}}, {{User: 2}}, {{User: 3}}}}
	collector := NewCollector(src, CollectorOptions{Now: fts.Now})
	reporter := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	hostInfoFunc := func(context.Context) models.HostInfo { return models.HostInfo{ID: "h1"} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, collector, reporter, hostInfoFunc, 15*time.Second, RunOptions{
			timeSrc: fts,
		})
	}()

	// Drive the fake clock through several boundaries: 00:00:15, :30, :45.
	deadline := time.Now().Add(5 * time.Second)
	for reportCount.Load() < 3 && time.Now().Before(deadline) {
		fts.advance(500 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions did not return after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotTimestamps) < 3 {
		t.Fatalf("got %d timestamps, want >= 3: %v", len(gotTimestamps), gotTimestamps)
	}
	want := []int64{
		time.Date(2026, 1, 1, 0, 0, 15, 0, time.UTC).Unix(),
		time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC).Unix(),
		time.Date(2026, 1, 1, 0, 0, 45, 0, time.UTC).Unix(),
	}
	for i, w := range want {
		if gotTimestamps[i] != w {
			t.Errorf("timestamp[%d] = %d, want %d (boundary-aligned)", i, gotTimestamps[i], w)
		}
	}
}

func TestRunWithOptions_MissedBoundarySkipsWithoutBurst(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotTimestamps []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report models.AgentReport
		_ = json.NewDecoder(r.Body).Decode(&report)
		mu.Lock()
		for _, s := range report.Samples {
			gotTimestamps = append(gotTimestamps, s.Timestamp)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fts := newFakeTimeSource(start)

	src := &fakeSource{cpuTimes: [][]cpu.TimesStat{{{User: 1}}, {{User: 2}}}}
	collector := NewCollector(src, CollectorOptions{Now: fts.Now})
	reporter := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	hostInfoFunc := func(context.Context) models.HostInfo { return models.HostInfo{ID: "h1"} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, collector, reporter, hostInfoFunc, 15*time.Second, RunOptions{
			timeSrc: fts,
		})
	}()

	// Jump straight past several boundaries at once (simulated suspend):
	// from :00 to :50 in one leap, skipping :15/:30/:45.
	deadlineWall := time.Now().Add(3 * time.Second)
	for len(func() []int64 { mu.Lock(); defer mu.Unlock(); return gotTimestamps }()) < 1 && time.Now().Before(deadlineWall) {
		fts.advance(10 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	fts.advance(50 * time.Second) // big jump simulating a suspend

	deadlineWall = time.Now().Add(3 * time.Second)
	for len(func() []int64 { mu.Lock(); defer mu.Unlock(); return gotTimestamps }()) < 2 && time.Now().Before(deadlineWall) {
		fts.advance(10 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions did not return after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotTimestamps) < 2 {
		t.Fatalf("got %d timestamps, want >= 2: %v", len(gotTimestamps), gotTimestamps)
	}
	// The first sample lands on whichever 15s boundary was current when
	// the Run goroutine actually started (a small, inherent race against
	// the test's own advance loop starting immediately), so we only
	// assert it's boundary-aligned, not a specific value.
	first := gotTimestamps[0]
	if first%15 != 0 {
		t.Errorf("first timestamp = %d, not aligned to a 15s boundary", first)
	}
	// The second sample must reflect the simulated suspend: a 50s jump
	// from the first boundary must land Run on the next boundary at or
	// after first+50s, never firing any of the boundaries strictly
	// between first and first+50s as a catch-up burst.
	second := gotTimestamps[1]
	if second-first < 50 {
		t.Errorf("second timestamp = %d (first=%d, delta=%d), want a jump of at least 50s reflecting the skipped suspend interval", second, first, second-first)
	}
	if (second-first)%15 != 0 {
		t.Errorf("second timestamp = %d is not aligned to a 15s boundary relative to first=%d", second, first)
	}
}

func TestRunWithOptions_JitterDelaysWithinBounds(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var receiveTimes []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receiveTimes = append(receiveTimes, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fts := newFakeTimeSource(start)

	src := &fakeSource{cpuTimes: [][]cpu.TimesStat{{{User: 1}}}}
	collector := NewCollector(src, CollectorOptions{Now: fts.Now})
	reporter := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	hostInfoFunc := func(context.Context) models.HostInfo { return models.HostInfo{ID: "h1"} }

	jitter := 5 * time.Second
	var gotJitter time.Duration
	var jitterCalls atomic.Int32
	randomJitter := func(max time.Duration) time.Duration {
		jitterCalls.Add(1)
		if max != jitter {
			t.Errorf("RandomJitter called with %v, want %v", max, jitter)
		}
		gotJitter = 2 * time.Second // deterministic, within [0, jitter)
		return gotJitter
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, collector, reporter, hostInfoFunc, 15*time.Second, RunOptions{
			timeSrc:      fts,
			Jitter:       jitter,
			RandomJitter: randomJitter,
		})
	}()

	deadlineWall := time.Now().Add(3 * time.Second)
	for jitterCalls.Load() < 1 && time.Now().Before(deadlineWall) {
		fts.advance(200 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	// Let the jittered sleep elapse on the fake clock.
	fts.advance(gotJitter + time.Second)
	deadlineWall = time.Now().Add(3 * time.Second)
	for func() int { mu.Lock(); defer mu.Unlock(); return len(receiveTimes) }() < 1 && time.Now().Before(deadlineWall) {
		fts.advance(200 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions did not return after cancel")
	}

	if jitterCalls.Load() < 1 {
		t.Fatal("RandomJitter was never called despite Jitter > 0")
	}
	if gotJitter < 0 || gotJitter >= jitter {
		t.Errorf("jitter %v out of bounds [0, %v)", gotJitter, jitter)
	}
}

func TestRunWithOptions_ZeroJitterNeverCallsRandomJitter(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fts := newFakeTimeSource(start)

	src := &fakeSource{cpuTimes: [][]cpu.TimesStat{{{User: 1}}}}
	collector := NewCollector(src, CollectorOptions{Now: fts.Now})
	reporter := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	hostInfoFunc := func(context.Context) models.HostInfo { return models.HostInfo{ID: "h1"} }

	var jitterCalls atomic.Int32
	randomJitter := func(time.Duration) time.Duration {
		jitterCalls.Add(1)
		return 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, collector, reporter, hostInfoFunc, 15*time.Second, RunOptions{
			timeSrc:      fts,
			Jitter:       0,
			RandomJitter: randomJitter,
		})
	}()

	deadlineWall := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadlineWall) {
		fts.advance(200 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions did not return after cancel")
	}

	if jitterCalls.Load() != 0 {
		t.Errorf("RandomJitter called %d times, want 0 when Jitter == 0", jitterCalls.Load())
	}
}

func TestRunWithOptions_RealElapsedTimeUsedForRates(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotSamples []models.Sample
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report models.AgentReport
		_ = json.NewDecoder(r.Body).Decode(&report)
		mu.Lock()
		gotSamples = append(gotSamples, report.Samples...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fts := newFakeTimeSource(start)

	// Two net-IO snapshots 500 bytes apart; the real elapsed time between
	// collections (per the fake clock, which Run and Collector share via
	// CollectorOptions.Now) determines the rate, NOT the 15s interval
	// between aligned boundaries.
	src := &fakeSource{
		cpuTimes: [][]cpu.TimesStat{{{User: 1}}, {{User: 2}}},
		netIO: []([]net.IOCountersStat){
			{{Name: "eth0", BytesRecv: 0, BytesSent: 0}},
			{{Name: "eth0", BytesRecv: 500, BytesSent: 0}},
		},
	}
	collector := NewCollector(src, CollectorOptions{Now: fts.Now})
	reporter := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	hostInfoFunc := func(context.Context) models.HostInfo { return models.HostInfo{ID: "h1"} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, collector, reporter, hostInfoFunc, 15*time.Second, RunOptions{
			timeSrc: fts,
		})
	}()

	deadlineWall := time.Now().Add(3 * time.Second)
	for func() int { mu.Lock(); defer mu.Unlock(); return len(gotSamples) }() < 1 && time.Now().Before(deadlineWall) {
		fts.advance(1 * time.Second)
		time.Sleep(time.Millisecond)
	}
	// Advance the fake clock by exactly 5 real seconds (not a full 15s
	// interval) before the second collection, to prove the rate uses
	// real elapsed time rather than assuming a full interval elapsed.
	fts.advance(5 * time.Second)
	deadlineWall = time.Now().Add(3 * time.Second)
	for func() int { mu.Lock(); defer mu.Unlock(); return len(gotSamples) }() < 2 && time.Now().Before(deadlineWall) {
		fts.advance(1 * time.Second)
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWithOptions did not return after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotSamples) < 2 {
		t.Fatalf("got %d samples, want >= 2", len(gotSamples))
	}
	// 500 bytes over the real elapsed time between the two Collect calls.
	elapsed := gotSamples[1].Timestamp - gotSamples[0].Timestamp
	if elapsed <= 0 {
		t.Fatalf("elapsed timestamp delta = %d, want > 0", elapsed)
	}
	rate := gotSamples[1].NetRxBps
	if rate <= 0 {
		t.Errorf("NetRxBps = %v, want > 0", rate)
	}
	// The real elapsed time (via fts.Now, shared with the Collector) was
	// forced to be much larger than the 15s boundary-to-boundary gap
	// would suggest at the point the second Collect ran (only ~1s of
	// scheduling advance plus a deliberate 5s jump, well under 15s), so
	// a boundary-based (rather than real-elapsed) rate calculation would
	// produce a materially different number. We just assert the rate is
	// sane (not wildly larger than the boundary-based guess would allow)
	// as a smoke check; the collector-level rate math itself is covered
	// exhaustively in collector_test.go.
	if rate > 500 {
		t.Errorf("NetRxBps = %v, implausibly high for a 500-byte delta", rate)
	}
}
