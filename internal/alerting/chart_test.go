package alerting

import (
	"bytes"
	"context"
	"image/png"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// TestNotify_FiringWithSeries_AttachesChartImage verifies that a firing
// notification for a series-backed metric (cpu/memory/disk/load1)
// carries a valid PNG chart image (SPEC-v0.5 §B: notifications include
// a chart; smoke.sh §E decodes image_png_base64 as PNG magic bytes).
func TestNotify_FiringWithSeries_AttachesChartImage(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC)
	clock := newFakeClock(now)
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU high", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 0, DurationSec: 0, CooldownSec: 3600,
		ChannelIDs: []int64{1},
	})
	store.setSeriesCPU("h1", []seriesPoint{
		{ts: now.Add(-5 * time.Minute).Unix(), value: 91},
		{ts: now.Add(-2 * time.Minute).Unix(), value: 95},
		{ts: now.Unix(), value: 97},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 97})
	if err := e.Evaluate(context.Background(), now, []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
	msg := sender.lastMessage()
	if !msg.HasImage() {
		t.Fatalf("message has no image, want a chart PNG attached")
	}
	if msg.ImageName != chartImageName {
		t.Errorf("ImageName = %q, want %q", msg.ImageName, chartImageName)
	}
	if len(msg.Image) < 8 || !bytes.Equal(msg.Image[:8], []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("image does not start with PNG magic bytes: %x", msg.Image[:min(8, len(msg.Image))])
	}
	if _, err := png.Decode(bytes.NewReader(msg.Image)); err != nil {
		t.Fatalf("image did not decode as PNG: %v", err)
	}
}

// TestNotify_EgressFiring_NoChartImage verifies that egress metrics
// (which have no chartable raw series — they're percentages of a
// monthly counter, not a time series column) do not attach an image,
// and delivery still succeeds without one.
func TestNotify_EgressFiring_NoChartImage(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := newFakeClock(now)
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "Outbound 80%", Enabled: true, Metric: models.AlertMetricEgressOutPct,
		Operator: models.AlertOperatorGTE, Threshold: 80, CooldownSec: 3600,
		ChannelIDs: []int64{1},
	})

	host := models.HostSnapshot{HostID: "h1", Hostname: "h1-host", Status: models.HostUp, Egress: models.EgressUsage{Percent: 85}}
	if err := e.Evaluate(context.Background(), now, []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
	msg := sender.lastMessage()
	if msg.HasImage() {
		t.Fatalf("egress message unexpectedly has an image")
	}
}

// TestNotify_NoSeriesData_StillDeliversWithoutImage verifies that a
// series-backed metric with no queryable history (e.g. a host that just
// started reporting) still delivers a notification, just without a
// chart image, rather than failing or blocking delivery.
func TestNotify_NoSeriesData_StillDeliversWithoutImage(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := newFakeClock(now)
	sender := &fakeSender{}
	senderFactory := func(models.NotifyChannel) (Sender, error) { return sender, nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU high", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 0, DurationSec: 0, CooldownSec: 3600,
		ChannelIDs: []int64{1},
	})
	// No series data set for h1 at all.

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 97})
	if err := e.Evaluate(context.Background(), now, []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	if got := sender.callCount(); got != 1 {
		t.Fatalf("sender calls = %d, want 1", got)
	}
	if sender.lastMessage().HasImage() {
		t.Fatalf("message unexpectedly has an image despite no series data")
	}
}

// TestNotify_MultiChannelFiring_RecordsAllDeliveries verifies that a
// rule with multiple channels records a delivery outcome for every
// channel on the event, not just the last one written — a regression
// test for the stale-snapshot bug where each per-channel deliveryJob
// captured the event's Deliveries slice at enqueue time and each
// full-replace UpdateAlertEvent write clobbered every earlier job's
// append (see notify.go's recordDelivery doc comment).
func TestNotify_MultiChannelFiring_RecordsAllDeliveries(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := newFakeClock(now)
	senders := map[int64]*fakeSender{
		1: {},
		2: {},
		3: {},
	}
	senderFactory := func(ch models.NotifyChannel) (Sender, error) { return senders[ch.ID], nil }
	e := newTestEngine(t, store, clock, senderFactory)

	store.setChannel(models.NotifyChannel{ID: 1, Name: "c1", Type: models.NotifyChannelWebhook, Enabled: true})
	store.setChannel(models.NotifyChannel{ID: 2, Name: "c2", Type: models.NotifyChannelDiscord, Enabled: true})
	store.setChannel(models.NotifyChannel{ID: 3, Name: "c3", Type: models.NotifyChannelTelegram, Enabled: true})
	store.setRule(models.AlertRule{
		ID: 1, Name: "CPU high", Enabled: true, Metric: models.AlertMetricCPU,
		Operator: models.AlertOperatorGT, Threshold: 0, DurationSec: 0, CooldownSec: 3600,
		ChannelIDs: []int64{1, 2, 3},
	})

	host := hostSnapshot("h1", &models.Sample{CPUPercent: 95})
	if err := e.Evaluate(context.Background(), now, []models.HostSnapshot{host}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	e.Close()

	events := store.listEvents()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	deliveries := events[0].Deliveries
	if len(deliveries) != 3 {
		t.Fatalf("deliveries = %d, want 3 (one per channel), got %+v", len(deliveries), deliveries)
	}
	seen := map[int64]bool{}
	for _, d := range deliveries {
		if !d.OK {
			t.Errorf("delivery for channel %d not ok: %+v", d.ChannelID, d)
		}
		seen[d.ChannelID] = true
	}
	for _, id := range []int64{1, 2, 3} {
		if !seen[id] {
			t.Errorf("no delivery recorded for channel %d; deliveries=%+v", id, deliveries)
		}
	}
}
