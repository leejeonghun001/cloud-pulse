package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// reporterFakeInventoryProvider is a minimal InventoryProvider for
// Reporter-level tests, returning a fixed sequence of snapshots.
type reporterFakeInventoryProvider struct {
	invs []models.Inventory
	idx  int
}

func (f *reporterFakeInventoryProvider) Collect(context.Context, time.Time) (models.Inventory, error) {
	i := f.idx
	if i >= len(f.invs) {
		i = len(f.invs) - 1
	}
	f.idx++
	return f.invs[i], nil
}

func TestReporter_Flush_AttachesInventoryOnFirstReport(t *testing.T) {
	t.Parallel()

	var gotReport models.AgentReport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReport); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	inv := models.Inventory{CollectedAt: 100, Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok", Inventory: &reporterFakeInventoryProvider{invs: []models.Inventory{inv}}})
	r.Enqueue(models.Sample{Timestamp: 1})

	if err := r.Flush(t.Context(), models.HostInfo{ID: "h1"}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if gotReport.Inventory == nil {
		t.Fatal("first report should carry an Inventory")
	}
	if len(gotReport.Inventory.Ports) != 1 || gotReport.Inventory.Ports[0].Port != 80 {
		t.Errorf("got inventory %+v, want the scripted snapshot", gotReport.Inventory)
	}
}

func TestReporter_Flush_OmitsInventoryWhenUnchangedOnSecondReport(t *testing.T) {
	t.Parallel()

	var reports []models.AgentReport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rep models.AgentReport
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			t.Errorf("decode body: %v", err)
		}
		reports = append(reports, rep)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	inv := models.Inventory{Ports: []models.ListeningPort{{Proto: "tcp", Port: 80}}}
	provider := &reporterFakeInventoryProvider{invs: []models.Inventory{inv, inv}}
	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok", Inventory: provider})

	r.Enqueue(models.Sample{Timestamp: 1})
	if err := r.Flush(t.Context(), models.HostInfo{ID: "h1"}); err != nil {
		t.Fatalf("Flush #1: %v", err)
	}

	r.Enqueue(models.Sample{Timestamp: 2})
	if err := r.Flush(t.Context(), models.HostInfo{ID: "h1"}); err != nil {
		t.Fatalf("Flush #2: %v", err)
	}

	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2", len(reports))
	}
	if reports[0].Inventory == nil {
		t.Error("first report should carry an Inventory")
	}
	if reports[1].Inventory != nil {
		t.Error("second report should omit Inventory (unchanged, not yet stale)")
	}
}

func TestReporter_Flush_NoInventoryProviderOmitsField(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok"})
	r.Enqueue(models.Sample{Timestamp: 1})
	if err := r.Flush(t.Context(), models.HostInfo{ID: "h1"}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if strings.Contains(string(gotBody), "\"inventory\"") {
		t.Errorf("body should omit the inventory field entirely when no provider is configured: %s", gotBody)
	}
}

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

func TestReporter_LogsUpdateNoticeWhenHubReportsNewerVersion(t *testing.T) {
	withTestVersion(t, "v0.3.0")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(models.IngestResponse{Accepted: 1, LatestVersion: "v0.3.1"})
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok", Logger: logger})
	r.Enqueue(models.Sample{Timestamp: 1})
	if err := r.Flush(t.Context(), models.HostInfo{}); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if !strings.Contains(buf.String(), "a newer cloud-pulse-agent is available") {
		t.Errorf("expected update notice in log output, got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "latest=v0.3.1") {
		t.Errorf("expected latest=v0.3.1 in log output, got: %q", buf.String())
	}
}

func TestReporter_NoUpdateNoticeWhenLatestVersionAbsent(t *testing.T) {
	withTestVersion(t, "v0.3.0")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(models.IngestResponse{Accepted: 1})
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	r := NewReporter(ReporterOptions{HubURL: srv.URL, Token: "tok", Logger: logger})
	r.Enqueue(models.Sample{Timestamp: 1})
	if err := r.Flush(t.Context(), models.HostInfo{}); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if strings.Contains(buf.String(), "a newer cloud-pulse-agent is available") {
		t.Errorf("expected no update notice when latest_version is absent, got: %q", buf.String())
	}
}
