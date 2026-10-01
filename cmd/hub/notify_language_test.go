package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting"
	"github.com/leejeonghun001/cloud-pulse/internal/hub"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/storage"
)

// TestBuildAlerting_NotifyLanguageAppliesToDeliveredText checks the real
// wiring end to end: the hub's notify_language setting selects the text
// of a delivered notification, and a change applies to the next one.
func TestBuildAlerting_NotifyLanguageAppliesToDeliveredText(t *testing.T) {
	t.Setenv("CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS", "1")

	var (
		mu     sync.Mutex
		titles []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		titles = append(titles, payload.Title)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "cloud-pulse.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	engine, wrapped, _ := buildAlerting(store, testDiscardLogger())
	t.Cleanup(engine.Close)
	evaluator, ok := wrapped.(hub.RolloutAlertEvaluator)
	if !ok {
		t.Fatal("wrapped AlertEngine does not implement hub.RolloutAlertEvaluator")
	}

	ch, err := store.CreateNotifyChannel(ctx, models.NotifyChannel{
		Name: "ops-webhook", Type: models.NotifyChannelWebhook, Enabled: true,
		Config: map[string]string{"url": srv.URL},
	})
	if err != nil {
		t.Fatalf("CreateNotifyChannel: %v", err)
	}
	if _, err := store.CreateAlertRule(ctx, models.AlertRule{
		Name: "batch failed", Enabled: true,
		Metric: models.AlertMetricUpdateBatchFailed, ChannelIDs: []int64{ch.ID},
	}); err != nil {
		t.Fatalf("CreateAlertRule: %v", err)
	}

	send := func(batchID string) {
		t.Helper()
		outcome := alerting.UpdateBatchOutcome{
			BatchID: batchID, Target: "latest", Succeeded: 1, Failed: 1,
			FailedHosts: []alerting.FailedUpdateHost{{HostID: "h1", Hostname: "host-one", Reason: models.UpdateReasonTimeout}},
		}
		if err := evaluator.EvaluateUpdateBatch(ctx, time.Now(), outcome); err != nil {
			t.Fatalf("EvaluateUpdateBatch: %v", err)
		}
		engine.Wait()
	}

	if err := store.SetSetting(ctx, hub.SettingNotifyLanguage, "ko"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	send("b-ko")
	if err := store.SetSetting(ctx, hub.SettingNotifyLanguage, "en"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	send("b-en")

	mu.Lock()
	defer mu.Unlock()
	want := []string{"업데이트 배치 b-ko: 1건 실패", "Update batch b-en: 1 failed"}
	if len(titles) != len(want) || titles[0] != want[0] || titles[1] != want[1] {
		t.Fatalf("delivered titles = %q, want %q", titles, want)
	}
}
