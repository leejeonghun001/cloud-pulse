package alerting

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

type displayFailureSender struct{ err error }

func (s displayFailureSender) Send(context.Context, Message) error { return s.err }

func TestDeliveryErrorDisplay_CallbackAndNilFallbackKeepOriginalError(t *testing.T) {
	for _, factoryFailure := range []bool{false, true} {
		for _, callback := range []bool{false, true} {
			store := newFakeStore()
			ev, err := store.CreateAlertEvent(t.Context(), models.AlertEvent{})
			if err != nil {
				t.Fatal(err)
			}
			original := errors.New("send failed token-xQ")
			var logs bytes.Buffer
			opts := deliveryWorkerOptions{Store: store, Logger: slog.New(slog.NewTextHandler(&logs, nil)), Sender: func(models.NotifyChannel) (Sender, error) {
				if factoryFailure {
					return nil, original
				}
				return displayFailureSender{original}, nil
			}, Diagnose: func(err error) *models.DiagnosisCode {
				if !errors.Is(err, original) {
					t.Errorf("original error not passed to diagnosis")
				}
				code := models.DiagnosisDNSFailure
				return &code
			}}
			if callback {
				opts.SafeErrorText = func(ch models.NotifyChannel, err error) string {
					if ch.Config["bot_token"] != "token-xQ" || !errors.Is(err, original) {
						t.Error("callback did not get original channel/error")
					}
					return "send failed (redacted)"
				}
			}
			w := newDeliveryWorker(opts)
			w.afterFunc = func(time.Duration) <-chan time.Time { c := make(chan time.Time, 1); c <- time.Now(); return c }
			w.enqueue(deliveryJob{event: ev, channel: models.NotifyChannel{ID: 1, Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "token-xQ"}}})
			w.drain()
			w.close()
			stored, _ := store.GetAlertEvent(t.Context(), ev.ID)
			if len(stored.Deliveries) != 1 {
				t.Fatal("delivery not persisted")
			}
			d := stored.Deliveries[0]
			if strings.Contains(d.Error+logs.String(), "token-xQ") || d.Diagnosis == nil || *d.Diagnosis != models.DiagnosisDNSFailure {
				t.Fatalf("unsafe/unclassified delivery: %+v logs=%q", d, logs.String())
			}
			if !callback && d.Error != "notification delivery failed" {
				t.Fatalf("nil callback must use fixed fallback: %q", d.Error)
			}
		}
	}
}
