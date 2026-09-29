package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// httpTimeout bounds every individual HTTP call made by a Sender.
const httpTimeout = 20 * time.Second

// maxResponseBodyBytes bounds how much of a platform's response body
// is read before giving up — error messages are always small; a much
// larger body is either not JSON or not from the platform we expect.
const maxResponseBodyBytes = 1 << 20 // 1 MiB

// RetryAfterError is returned by a Sender when the remote platform
// responds 429 (or otherwise asks the caller to back off), so the
// alerting worker can schedule a retry after the indicated delay
// instead of treating the failure as terminal.
type RetryAfterError struct {
	// After is how long the caller should wait before retrying.
	After time.Duration
	// Err is the underlying error describing the rate limit (may be
	// nil).
	Err error
}

// Error implements the error interface.
func (e *RetryAfterError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("notify: rate limited, retry after %s: %v", e.After, e.Err)
	}
	return fmt.Sprintf("notify: rate limited, retry after %s", e.After)
}

// Unwrap allows errors.Is/errors.As to see the underlying error.
func (e *RetryAfterError) Unwrap() error {
	return e.Err
}

// defaultRetryAfter is used when a 429 response carries no parseable
// Retry-After header.
const defaultRetryAfter = 30 * time.Second

// parseRetryAfter parses an HTTP Retry-After header value relative to the
// current wall clock. An empty or unparseable value falls back to
// defaultRetryAfter.
func parseRetryAfter(header string) time.Duration {
	return parseRetryAfterAt(header, time.Now())
}

// parseRetryAfterAt is parseRetryAfter's clock-injected implementation.
// It keeps HTTP-date retry behavior deterministic in tests.
func parseRetryAfterAt(header string, now time.Time) time.Duration {
	if header == "" {
		return defaultRetryAfter
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs < 0 {
			return defaultRetryAfter
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		d := t.Sub(now)
		if d > 0 {
			return d
		}
	}
	return defaultRetryAfter
}

// newRetryAfterError builds a *RetryAfterError from a 429 response's
// Retry-After header (HTTP header spelling) and an underlying error
// describing the failure.
func newRetryAfterError(resp *http.Response, err error) *RetryAfterError {
	return &RetryAfterError{
		After: parseRetryAfter(resp.Header.Get("Retry-After")),
		Err:   err,
	}
}

// httpClientOption customizes the shared HTTP client behavior; used by
// tests to relax the SSRF guard without touching the process
// environment. See allowCustomEndpoints.
type clientOptions struct {
	allowCustomEndpoints bool
}

// Option customizes Sender construction. See WithAllowCustomEndpoints.
type Option func(*clientOptions)

// WithAllowCustomEndpoints relaxes the SSRF guard so a Sender accepts
// a channel-config-supplied API base URL (Telegram's api_base, the
// WhatsApp Graph API base) instead of only the fixed official host,
// and also drops the https-only requirement so httptest's plain-http
// test servers work. Intended for tests; production code should rely
// on the CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS environment variable via
// AllowCustomEndpointsFromEnv instead of calling this directly — an
// operator who sets that variable accepts both risks (arbitrary host,
// plaintext transport) for their own mirror/self-hosted-bot-API setup.
func WithAllowCustomEndpoints(allow bool) Option {
	return func(o *clientOptions) {
		o.allowCustomEndpoints = allow
	}
}

// AllowCustomEndpointsFromEnv reports whether
// CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS=1 is set in the process environment.
// New's caller (the alerting engine's factory) is expected to call
// this once and pass the result via WithAllowCustomEndpoints; Senders
// never read the environment directly, so tests can simulate either
// state without mutating process-wide state.
func AllowCustomEndpointsFromEnv(lookup func(string) (string, bool)) bool {
	v, ok := lookup("CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS")
	return ok && v == "1"
}

// httpClient returns client, or a new client with httpTimeout if nil.
func httpClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: httpTimeout}
}

// readLimitedBody reads up to maxResponseBodyBytes from resp.Body and
// closes it. It never returns an error solely because the body was
// truncated.
func readLimitedBody(resp *http.Response) []byte {
	defer func() {
		_ = resp.Body.Close() // best-effort close; body content already captured or discarded
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return nil
	}
	return body
}

// cancelReadCloser cancels a request-scoped timeout once the response body
// is closed. It preserves the timeout through body reads: canceling directly
// after http.Client.Do returns can truncate a valid streamed response.
type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.cancel)
	return err
}

// doRequest executes req with client, applying the shared timeout via
// context if req's context has no deadline yet. It returns the raw
// *http.Response for the caller to inspect status/headers; callers
// must read/close the body (readLimitedBody does both). When doRequest
// creates the timeout context, its cancellation is tied to Body.Close so
// the response remains readable until the caller has consumed it.
func doRequest(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		ctx, cancel = context.WithTimeout(ctx, httpTimeout)
		req = req.WithContext(ctx)
	}
	resp, err := httpClient(client).Do(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("notify: request failed: %w", err)
	}
	if cancel != nil {
		resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	}
	return resp, nil
}

// errUnexpectedStatus is wrapped into a descriptive error by callers
// that have already parsed a platform-specific error message from the
// response body.
var errUnexpectedStatus = errors.New("unexpected status")
