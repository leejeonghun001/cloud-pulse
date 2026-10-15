package main

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

func TestBuildAlerting_SafeConstructorErrorPersistsWithoutSecrets(t *testing.T) {
	// The malformed endpoint fails in the constructor: no external request.
	t.Setenv("CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS", "1")
	store, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "notify-display.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})
	var logs bytes.Buffer
	engine, wrapped, _ := buildAlerting(store, slog.New(slog.NewTextHandler(&logs, nil)))
	defer engine.Close()
	ch, err := store.CreateNotifyChannel(t.Context(), models.NotifyChannel{Name: "test", Type: models.NotifyChannelWhatsApp, Enabled: true, Config: map[string]string{"access_token": "xQ", "phone_number_id": "123", "to": "456", "api_base": "https://credentials.example.invalid/xQ/%zz"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateAlertRule(t.Context(), models.AlertRule{Name: "test", Enabled: true, Metric: models.AlertMetricCPU, Operator: models.AlertOperatorGT, Threshold: 0, ChannelIDs: []int64{ch.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Evaluate(t.Context(), time.Now(), []models.HostSnapshot{{HostID: "test-host", Hostname: "test-host", Status: models.HostUp, Latest: &models.Sample{CPUPercent: 99}}}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	events, err := store.ListAlertEvents(t.Context(), "", "", 0, 10)
	if err != nil || len(events) != 1 || len(events[0].Deliveries) != 1 {
		t.Fatalf("missing persisted failure: %v %+v", err, events)
	}
	delivery := events[0].Deliveries[0]
	text := delivery.Error + logs.String()
	if delivery.OK || delivery.Error == "notification delivery failed" || strings.Contains(text, "xQ") || strings.Contains(text, "credentials.example.invalid") || !strings.Contains(text, "invalid api_base") {
		t.Fatalf("production callback not wired or unsafe: %+v logs=%s", delivery, logs.String())
	}
}
