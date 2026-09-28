package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/version"
)

// ErrUnauthorized is returned by Flush when the hub rejects the request
// with 401 or 403. Buffered samples are retained so they can be retried
// once credentials are fixed.
var ErrUnauthorized = errors.New("agent: unauthorized")

// defaultBufferSize is the ring buffer capacity used when
// ReporterOptions.BufferSize is 0.
const defaultBufferSize = 240

// defaultBatchSize is the max samples per report request used when
// ReporterOptions.BatchSize is 0.
const defaultBatchSize = 100

// requestTimeout bounds each report HTTP request.
const requestTimeout = 10 * time.Second

// reportPath is the hub endpoint samples are POSTed to, relative to
// ReporterOptions.HubURL.
const reportPath = "/api/v1/agent/report"

// ReporterOptions configures a Reporter.
type ReporterOptions struct {
	// HubURL is the base URL of the hub (no trailing slash).
	HubURL string
	// Token is the bearer token sent with each request.
	Token string
	// Client is the HTTP client used for requests. If nil, a client with
	// requestTimeout is constructed.
	Client *http.Client
	// Logger receives warn/debug logs. If nil, a discard logger is used.
	Logger *slog.Logger
	// BufferSize is the ring buffer capacity (default 240).
	BufferSize int
	// BatchSize is the max number of samples sent per HTTP request
	// (default 100).
	BatchSize int
}

// Reporter buffers collected samples in memory and periodically flushes
// them to the hub over HTTP, in batches, retrying on transient failures.
type Reporter struct {
	hubURL     string
	token      string
	client     *http.Client
	logger     *slog.Logger
	bufferSize int
	batchSize  int

	mu      sync.Mutex
	samples []models.Sample
}

// NewReporter constructs a Reporter from opts.
func NewReporter(opts ReporterOptions) *Reporter {
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	bufferSize := opts.BufferSize
	if bufferSize <= 0 {
		bufferSize = defaultBufferSize
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	return &Reporter{
		hubURL:     opts.HubURL,
		token:      opts.Token,
		client:     client,
		logger:     logger,
		bufferSize: bufferSize,
		batchSize:  batchSize,
	}
}

// Enqueue adds s to the ring buffer, dropping the oldest sample if the
// buffer is full.
func (r *Reporter) Enqueue(s models.Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.samples) >= r.bufferSize {
		drop := len(r.samples) - r.bufferSize + 1
		r.samples = r.samples[drop:]
	}
	r.samples = append(r.samples, s)
}

// Buffered returns the number of samples currently buffered, for tests and
// diagnostics.
func (r *Reporter) Buffered() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.samples)
}

// Flush sends all buffered samples to the hub in batches of at most
// BatchSize, along with host. Samples are removed from the buffer only
// after a 2xx response for their batch.
//
// On 401/403 the remaining samples (including the failed batch) are kept
// and ErrUnauthorized is returned. On other non-2xx 4xx responses (except
// 408/429, which are treated as transient) the batch is dropped as bad
// data and a warning is logged; Flush continues with the next batch. On
// transient failures (network errors, 408, 429, 5xx) the batch and all
// later batches are kept for the next Flush call and Flush returns the
// error.
func (r *Reporter) Flush(ctx context.Context, host models.HostInfo) error {
	for {
		batch := r.peekBatch()
		if len(batch) == 0 {
			return nil
		}

		status, err := r.postBatch(ctx, host, batch)
		if err != nil {
			return fmt.Errorf("agent: flush: %w", err)
		}

		switch {
		case status >= 200 && status < 300:
			r.dropBatch(len(batch))
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return ErrUnauthorized
		case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests:
			return fmt.Errorf("agent: flush: hub returned status %d", status)
		case status >= 500:
			return fmt.Errorf("agent: flush: hub returned status %d", status)
		case status >= 400:
			r.logger.WarnContext(ctx, "dropping batch rejected by hub", "status", status, "sample_count", len(batch))
			r.dropBatch(len(batch))
		default:
			return fmt.Errorf("agent: flush: hub returned unexpected status %d", status)
		}
	}
}

// peekBatch returns a copy of up to batchSize samples from the front of
// the buffer without removing them.
func (r *Reporter) peekBatch() []models.Sample {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(r.samples)
	if n > r.batchSize {
		n = r.batchSize
	}
	batch := make([]models.Sample, n)
	copy(batch, r.samples[:n])
	return batch
}

// dropBatch removes the first n samples from the front of the buffer.
func (r *Reporter) dropBatch(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if n > len(r.samples) {
		n = len(r.samples)
	}
	r.samples = r.samples[n:]
}

// postBatch sends a single AgentReport batch and returns the HTTP status
// code. A non-nil error indicates the request could not be completed
// (network error, non-HTTP failure); it does not indicate an HTTP error
// status.
func (r *Reporter) postBatch(ctx context.Context, host models.HostInfo, batch []models.Sample) (int, error) {
	report := models.AgentReport{Host: host, Samples: batch}
	body, err := json.Marshal(report)
	if err != nil {
		return 0, fmt.Errorf("agent: marshal report: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, r.hubURL+reportPath, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("agent: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("User-Agent", "cloud-pulse-agent/"+version.Version)

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("agent: post report: %w", err)
	}
	defer func() {
		_ = resp.Body.Close() // response fully drained below; close error is not actionable
	}()
	_, _ = io.Copy(io.Discard, resp.Body) // drain body so the connection can be reused

	return resp.StatusCode, nil
}
