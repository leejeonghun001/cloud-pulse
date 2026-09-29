package hub

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestIngest_ServerTimeMsInResponse(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 3, 1, 8, 30, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	report := models.AgentReport{Host: sampleHostInfo("host-time"), Samples: []models.Sample{{Timestamp: now.Unix()}}}
	body, _ := json.Marshal(report)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/report", "203.0.113.1:1234", opts.AgentToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.IngestResponse](t, rec.Body)
	if got.ServerTimeMs != now.UnixMilli() {
		t.Errorf("ServerTimeMs = %d, want %d", got.ServerTimeMs, now.UnixMilli())
	}
}

func TestAgentTime_Endpoint(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 3, 1, 8, 30, 0, 0, time.UTC)
	opts := testOptions()
	opts.Now = fixedNow(now)
	s := newTestServer(t, opts, store)

	rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/agent/time", "203.0.113.1:1234", opts.AgentToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[models.TimeResponse](t, rec.Body)
	if got.ServerTimeMs != now.UnixMilli() {
		t.Errorf("ServerTimeMs = %d, want %d", got.ServerTimeMs, now.UnixMilli())
	}
}

func TestAgentTime_RequiresAgentToken(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	t.Run("missing_token", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/agent/time", "203.0.113.1:1234", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("wrong_token", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/agent/time", "203.0.113.1:1234", "wrong-token-value", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("correct_token", func(t *testing.T) {
		t.Parallel()
		rec := doRequest(t, s.Handler(), http.MethodGet, "/api/v1/agent/time", "203.0.113.1:1234", s.opts.AgentToken, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
}

func TestAgentTime_WrongMethod405(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	s := newTestServer(t, testOptions(), store)

	rec := doRequest(t, s.Handler(), http.MethodPost, "/api/v1/agent/time", "203.0.113.1:1234", s.opts.AgentToken, nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (body=%s)", rec.Code, rec.Body.String())
	}
}
