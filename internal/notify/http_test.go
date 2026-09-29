package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"empty", "", defaultRetryAfter},
		{"seconds", "5", 5 * time.Second},
		{"zero seconds", "0", 0},
		{"negative seconds", "-5", defaultRetryAfter},
		{"garbage", "not-a-number-or-date", defaultRetryAfter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parseRetryAfter(tt.header); got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	header := now.Add(2 * time.Minute).Format(http.TimeFormat)
	if got := parseRetryAfterAt(header, now); got != 2*time.Minute {
		t.Errorf("parseRetryAfterAt(%q, %s) = %v, want 2m", header, now, got)
	}
}

func TestRetryAfterError_ErrorString(t *testing.T) {
	t.Parallel()

	e := &RetryAfterError{After: 30 * time.Second}
	if e.Error() == "" {
		t.Errorf("Error() returned empty string")
	}

	wrapped := &RetryAfterError{After: 30 * time.Second, Err: errUnexpectedStatus}
	if wrapped.Unwrap() != errUnexpectedStatus {
		t.Errorf("Unwrap() did not return the wrapped error")
	}
}

func TestAllowCustomEndpointsFromEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		ok    bool
		want  bool
	}{
		{"unset", "", false, false},
		{"set to 1", "1", true, true},
		{"set to true", "true", true, false},
		{"set to 0", "0", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lookup := func(string) (string, bool) { return tt.value, tt.ok }
			if got := AllowCustomEndpointsFromEnv(lookup); got != tt.want {
				t.Errorf("AllowCustomEndpointsFromEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDoRequest_KeepsTimeoutContextUntilBodyClose(t *testing.T) {
	releaseBody := make(chan struct{})
	defer func() {
		select {
		case <-releaseBody:
		default:
			close(releaseBody)
		}
	}()

	headersSent := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(headersSent)
		<-releaseBody
		_, _ = io.WriteString(w, "response body") // test server response; client assertion owns failures
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := doRequest(context.Background(), srv.Client(), req)
	if err != nil {
		t.Fatalf("doRequest: %v", err)
	}
	<-headersSent
	close(releaseBody)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll response body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
	if string(body) != "response body" {
		t.Errorf("response body = %q, want %q", body, "response body")
	}
}
