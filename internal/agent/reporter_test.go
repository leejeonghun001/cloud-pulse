package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestReporter_FlushEmptyBufferNoRequest(t *testing.T) {
	t.Parallel()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	if err := r.Flush(t.Context(), models.HostInfo{}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if called {
		t.Error("server should not be called for an empty buffer")
	}
}

func TestReporter_AuthHeaderAndUserAgent(t *testing.T) {
	t.Parallel()

	var gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		var report models.AgentReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "super-secret-token"})
	r.Enqueue(models.Sample{Timestamp: 1})
	if err := r.Flush(t.Context(), models.HostInfo{ID: "h1"}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if gotAuth != "Bearer super-secret-token" {
		t.Errorf("Authorization = %q, want Bearer super-secret-token", gotAuth)
	}
	if gotUA == "" || gotUA[:18] != "cloud-pulse-agent/" {
		t.Errorf("User-Agent = %q, want prefix cloud-pulse-agent/", gotUA)
	}
	if r.Buffered() != 0 {
		t.Errorf("Buffered = %d, want 0 after successful flush", r.Buffered())
	}
}

func TestReporter_BatchingOf250Into100_100_50(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var batchSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report models.AgentReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Errorf("decode: %v", err)
		}
		mu.Lock()
		batchSizes = append(batchSizes, len(report.Samples))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok", BatchSize: 100, BufferSize: 300})
	for i := 0; i < 250; i++ {
		r.Enqueue(models.Sample{Timestamp: int64(i)})
	}
	if err := r.Flush(t.Context(), models.HostInfo{}); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batchSizes) != 3 {
		t.Fatalf("batch count = %d, want 3, got sizes %v", len(batchSizes), batchSizes)
	}
	if batchSizes[0] != 100 || batchSizes[1] != 100 || batchSizes[2] != 50 {
		t.Errorf("batch sizes = %v, want [100 100 50]", batchSizes)
	}
	if r.Buffered() != 0 {
		t.Errorf("Buffered = %d, want 0", r.Buffered())
	}
}

func TestReporter_RetainOn500(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	r.Enqueue(models.Sample{Timestamp: 1})
	r.Enqueue(models.Sample{Timestamp: 2})

	err := r.Flush(t.Context(), models.HostInfo{})
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
	if r.Buffered() != 2 {
		t.Errorf("Buffered = %d, want 2 (retained on 500)", r.Buffered())
	}
}

func TestReporter_DropOldestWhenBufferFull(t *testing.T) {
	t.Parallel()

	r := NewReporter(ReporterOptions{HubURL: "http://example.invalid", Token: "tok", BufferSize: 3})
	for i := 0; i < 5; i++ {
		r.Enqueue(models.Sample{Timestamp: int64(i)})
	}
	if r.Buffered() != 3 {
		t.Fatalf("Buffered = %d, want 3", r.Buffered())
	}
	// Verify the oldest entries (0, 1) were dropped by checking via Flush
	// batch contents against a test server.
	var gotTimestamps []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var report models.AgentReport
		_ = json.NewDecoder(req.Body).Decode(&report)
		for _, s := range report.Samples {
			gotTimestamps = append(gotTimestamps, s.Timestamp)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r2 := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok", BufferSize: 3})
	for i := 0; i < 5; i++ {
		r2.Enqueue(models.Sample{Timestamp: int64(i)})
	}
	if err := r2.Flush(t.Context(), models.HostInfo{}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(gotTimestamps) != 3 || gotTimestamps[0] != 2 || gotTimestamps[1] != 3 || gotTimestamps[2] != 4 {
		t.Errorf("timestamps = %v, want [2 3 4] (oldest 2 dropped)", gotTimestamps)
	}
}

func TestReporter_ErrUnauthorizedOn401(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "bad-token"})
	r.Enqueue(models.Sample{Timestamp: 1})

	err := r.Flush(t.Context(), models.HostInfo{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Flush err = %v, want ErrUnauthorized", err)
	}
	if r.Buffered() != 1 {
		t.Errorf("Buffered = %d, want 1 (samples kept on 401)", r.Buffered())
	}
}

func TestReporter_ErrUnauthorizedOn403(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "bad-token"})
	r.Enqueue(models.Sample{Timestamp: 1})

	err := r.Flush(t.Context(), models.HostInfo{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Flush err = %v, want ErrUnauthorized", err)
	}
}

func TestReporter_DropBadDataOn4xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	r.Enqueue(models.Sample{Timestamp: 1})

	if err := r.Flush(t.Context(), models.HostInfo{}); err != nil {
		t.Fatalf("Flush should not error on 400 (batch dropped as bad data): %v", err)
	}
	if r.Buffered() != 0 {
		t.Errorf("Buffered = %d, want 0 (bad batch dropped)", r.Buffered())
	}
}

func TestReporter_RetainOn429(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	r.Enqueue(models.Sample{Timestamp: 1})

	if err := r.Flush(t.Context(), models.HostInfo{}); err == nil {
		t.Fatal("expected error on 429 (treated as transient)")
	}
	if r.Buffered() != 1 {
		t.Errorf("Buffered = %d, want 1 (retained on 429)", r.Buffered())
	}
}
