package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"

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
