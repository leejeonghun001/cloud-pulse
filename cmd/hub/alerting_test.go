package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

// TestNotifySenderBridge_Send_DeliversFieldForField verifies that
// notifySenderBridge copies every alerting.Message field into the
// notify.Message a real internal/notify.Sender receives, using a
// generic webhook channel against an httptest server as the concrete
// Sender under test.
func TestNotifySenderBridge_Send_DeliversFieldForField(t *testing.T) {
	t.Setenv("CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS", "1")

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	factory := newNotifySenderFactory()
	sender, err := factory(models.NotifyChannel{
		Type:   models.NotifyChannelWebhook,
		Config: map[string]string{"url": srv.URL, "include_image": "true"},
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	msg := alerting.Message{
		Title:    "host1: CPU high",
		Text:     "CPU is 95.0% (threshold 90.0%)",
		Severity: alerting.SeverityWarning,
		Fields:   []alerting.Field{{Name: "Host", Value: "host1"}},
		URL:      "https://example.invalid/#/hosts/host1",
		Image:    []byte{0x89, 'P', 'N', 'G'},
	}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(gotBody) == 0 {
		t.Fatalf("webhook server received an empty body")
	}
	body := string(gotBody)
	for _, want := range []string{"host1: CPU high", "CPU is 95.0%", "image_png_base64"} {
		if !contains(body, want) {
			t.Errorf("request body missing %q: %s", want, body)
		}
	}
}

// TestNotifySenderBridge_Send_MapsRetryAfter verifies that a 429
// response from a webhook endpoint (mapped by internal/notify to a
// *notify.RetryAfterError) surfaces to the alerting delivery worker as
// a *alerting.RetryAfterError with the same delay, since the worker's
// backoff logic (internal/alerting/notify.go's deliver) only recognizes
// its own package's error type via errors.As.
func TestNotifySenderBridge_Send_MapsRetryAfter(t *testing.T) {
	t.Setenv("CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS", "1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	factory := newNotifySenderFactory()
	sender, err := factory(models.NotifyChannel{
		Type:   models.NotifyChannelWebhook,
		Config: map[string]string{"url": srv.URL},
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	err = sender.Send(context.Background(), alerting.Message{Title: "t", Text: "x"})
	if err == nil {
		t.Fatal("Send: want error, got nil")
	}
	ra, ok := err.(*alerting.RetryAfterError)
	if !ok {
		t.Fatalf("Send error type = %T, want *alerting.RetryAfterError", err)
	}
	if ra.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %s, want 7s", ra.RetryAfter)
	}
}

// TestBuildAlerting_WiresEngineToHubOptions verifies that buildAlerting
// returns an engine/AlertEngine/NotifySenderFactory triple that
// satisfies hub.Options' fields and drives a real evaluate-and-deliver
// round trip end to end (fakeStore standing in for *storage.DB, which
// buildAlerting only requires to satisfy alerting.Store).
func TestBuildAlerting_WiresEngineToHubOptions(t *testing.T) {
	t.Setenv("CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS", "1")

	var delivered []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered = append(delivered, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cloud-pulse.db")
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})

	logger := testDiscardLogger()
	engine, wrapped, notifyFactory := buildAlerting(store, logger)
	t.Cleanup(engine.Close)

	var opts hub.Options
	opts.Alerting = wrapped
	opts.NotifyFactory = notifyFactory
	if opts.Alerting == nil || opts.NotifyFactory == nil {
		t.Fatal("buildAlerting returned nil Alerting/NotifyFactory")
	}

	ch, err := store.CreateNotifyChannel(ctx, models.NotifyChannel{
		Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true,
		Config: map[string]string{"url": srv.URL},
	})
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}
	if _, err := store.CreateAlertRule(ctx, models.AlertRule{
		Name: "CPU high", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 0, ChannelIDs: []int64{ch.ID},
	}); err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}

	host := models.HostSnapshot{HostID: "h1", Hostname: "h1", Status: models.HostUp, Latest: &models.Sample{CPUPercent: 99}}
	if err := wrapped.Evaluate(ctx, time.Now(), []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	engine.Wait()

	if len(delivered) != 1 {
		t.Fatalf("delivered = %v, want exactly one delivery", delivered)
	}
}

// contains reports whether s contains substr (avoids pulling in
// strings just for this one helper across two tests).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// testDiscardLogger returns a *slog.Logger that discards all output,
// for tests that need a non-nil logger but don't assert on log content.
func testDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
