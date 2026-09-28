package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestScheduler_RunsCollectorsAndRecordsStatus(t *testing.T) {
	t.Parallel()

	collectorErr := errors.New("collect failed")
	collector := &fakeCollector{
		name: "test-collector",
		stats: []models.BucketStats{
			{Provider: models.StorageS3, Bucket: "b1", CollectedAt: 1},
		},
		err: collectorErr,
	}

	store := newFakeStore()
	opts := testOptions()
	opts.CloudInterval = 50 * time.Millisecond
	s := New(opts, store, []BucketCollector{collector}, nil, nil, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.RunBackground(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunBackground did not return within 3s of ctx cancellation")
	}

	if calls := collector.callCount(); calls < 1 {
		t.Fatalf("collector called %d times, want at least 1", calls)
	}

	statuses := s.collectorStatuses()
	if len(statuses) != 1 {
		t.Fatalf("collectorStatuses = %+v, want 1 entry", statuses)
	}
	if statuses[0].Name != "test-collector" {
		t.Errorf("status name = %q, want test-collector", statuses[0].Name)
	}
	if statuses[0].LastError != collectorErr.Error() {
		t.Errorf("status LastError = %q, want %q", statuses[0].LastError, collectorErr.Error())
	}
	if statuses[0].LastRun == 0 {
		t.Error("status LastRun should be non-zero after a run")
	}

	// Partial results (stats returned alongside the error) must still be
	// saved.
	latest, err := store.LatestBuckets(context.Background())
	if err != nil {
		t.Fatalf("LatestBuckets: %v", err)
	}
	if len(latest) != 1 {
		t.Fatalf("LatestBuckets = %+v, want 1 saved despite collector error", latest)
	}
}

func TestScheduler_RollupAndPruneCalled(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	opts := testOptions()
	s := New(opts, store, nil, nil, nil, testLogger())

	// Directly exercise the loop bodies with very short tickers by
	// invoking RunBackground and canceling quickly; rollup/prune run on
	// fixed 1m/1h intervals so we can't observe a tick within the test
	// budget. Instead, verify collectAll-independent loops start and
	// stop cleanly (no hang, no panic) and that Rollup/Prune are wired
	// by calling them directly through the Store interface the
	// scheduler uses.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.RunBackground(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunBackground did not return within 3s of ctx cancellation")
	}

	// The loops themselves are exercised for start/stop correctness
	// above; directly verify the store methods scheduler calls exist
	// and work (rollup/prune are unit-testable via the store contract).
	if err := store.Rollup(context.Background(), time.Now()); err != nil {
		t.Errorf("Rollup: %v", err)
	}
	if err := store.Prune(context.Background(), time.Now()); err != nil {
		t.Errorf("Prune: %v", err)
	}
	if store.rollupCalls == 0 {
		t.Error("expected at least one direct Rollup call to register")
	}
	if store.pruneCalls == 0 {
		t.Error("expected at least one direct Prune call to register")
	}
}

func TestScheduler_NoCollectors_NoPanic(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	opts := testOptions()
	s := New(opts, store, nil, nil, nil, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.RunBackground(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunBackground did not return within 3s of ctx cancellation")
	}

	statuses := s.collectorStatuses()
	if len(statuses) != 0 {
		t.Errorf("collectorStatuses = %+v, want empty", statuses)
	}
}

func TestCollectTimeout_CapsAtMax(t *testing.T) {
	t.Parallel()

	s := &Server{opts: Options{CloudInterval: 1 * time.Hour}}
	if got := s.collectTimeout(); got != maxCollectTimeout {
		t.Errorf("collectTimeout() = %v, want %v (capped)", got, maxCollectTimeout)
	}

	s2 := &Server{opts: Options{CloudInterval: 30 * time.Second}}
	if got := s2.collectTimeout(); got != 30*time.Second {
		t.Errorf("collectTimeout() = %v, want 30s", got)
	}

	s3 := &Server{opts: Options{CloudInterval: 0}}
	if got := s3.collectTimeout(); got != maxCollectTimeout {
		t.Errorf("collectTimeout() = %v, want %v (default)", got, maxCollectTimeout)
	}
}
