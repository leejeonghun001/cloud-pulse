package channelhealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

type syntheticRoundTripper func(*http.Request) (*http.Response, error)

func (f syntheticRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSyntheticWhatsApp_RealSenderGeneratedPNGAndMediaIDAccepted(t *testing.T) {
	var calls int
	var deadline time.Time
	var uploaded []byte
	factory := func(ch models.NotifyChannel, client *http.Client) (notify.Sender, error) {
		client.Transport = syntheticRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			d, ok := r.Context().Deadline()
			if !ok {
				t.Fatal("missing shared run deadline")
			}
			if deadline.IsZero() {
				deadline = d
			} else if !deadline.Equal(d) {
				t.Error("WhatsApp upload and send must share deadline")
			}
			body := `{"id":"generated-media-id"}`
			if strings.HasSuffix(r.URL.Path, "/media") {
				_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil {
					t.Fatal(err)
				}
				reader := multipart.NewReader(r.Body, params["boundary"])
				for {
					part, err := reader.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if part.FormName() == "file" {
						uploaded, _ = io.ReadAll(part)
						if part.FileName() != "connectivity-demo.png" {
							t.Error("unexpected file name")
						}
						if _, err := png.Decode(bytes.NewReader(uploaded)); err != nil {
							t.Fatalf("not a generated PNG: %v", err)
						}
					}
				}
			} else if strings.HasSuffix(r.URL.Path, "/messages") {
				var msg struct {
					Type  string `json:"type"`
					Image struct {
						ID string `json:"id"`
					} `json:"image"`
				}
				if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
					t.Fatal(err)
				}
				if msg.Type != "image" || msg.Image.ID != "generated-media-id" {
					t.Fatalf("wrong media reference: %+v", msg)
				}
				body = `{"messages":[{"id":"accepted-message-id"}]}`
			} else {
				t.Fatalf("unexpected endpoint: %s", r.URL.Path)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		return notify.New(ch, client, notify.WithAllowCustomEndpoints(true))
	}
	ch := models.NotifyChannel{ID: 1, Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ", "phone_number_id": "123", "to": "456"}}
	result := RunSyntheticTest(t.Context(), ch, RunOptions{SenderFactory: factory})
	expected, err := notify.GeneratedTestChartPNG()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(uploaded, expected) || calls != 2 || result.Outcome != models.SyntheticAccepted || strings.Contains(result.Detail, "verified") || strings.Contains(result.Detail, "cleaned up") {
		t.Fatalf("incorrect generated-chart acceptance: calls=%d result=%+v", calls, result)
	}
}

type boundedSyntheticSender struct {
	deadlines []time.Time
	readErr   error
	deleteErr error
	blockRead bool
}

func (s *boundedSyntheticSender) mark(ctx context.Context) {
	d, _ := ctx.Deadline()
	s.deadlines = append(s.deadlines, d)
}
func (s *boundedSyntheticSender) Send(ctx context.Context, _ notify.Message) error {
	s.mark(ctx)
	return nil
}
func (s *boundedSyntheticSender) ReadBack(ctx context.Context, _ notify.MessageRef) (bool, error) {
	s.mark(ctx)
	if s.blockRead {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return true, s.readErr
}
func (s *boundedSyntheticSender) Delete(ctx context.Context, _ notify.MessageRef) error {
	s.mark(ctx)
	return s.deleteErr
}

func TestSyntheticRun_SharedDeadlineCleanupFailureAndReadFailure(t *testing.T) {
	for _, blockRead := range []bool{false, true} {
		sender := &boundedSyntheticSender{blockRead: blockRead, deleteErr: errors.New("delete failed xQ https://hooks.example.invalid/private")}
		ch := models.NotifyChannel{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ", "api_base": "https://hooks.example.invalid/private"}}
		start := time.Now()
		result := RunSyntheticTest(t.Context(), ch, RunOptions{RunTimeout: 35 * time.Millisecond, Timeout: time.Second, SenderFactory: func(models.NotifyChannel, *http.Client) (notify.Sender, error) { return sender, nil }})
		if time.Since(start) > time.Second || len(sender.deadlines) != 3 {
			t.Fatalf("run not bounded / cleanup skipped: %+v", result)
		}
		for _, d := range sender.deadlines {
			if !d.Equal(sender.deadlines[0]) {
				t.Error("send/read/delete did not inherit shared deadline")
			}
		}
		if strings.Contains(result.Detail, "xQ") || strings.Contains(result.Detail, "hooks.example.invalid") || !strings.Contains(result.Detail, "cleanup failed: delete failed") {
			t.Fatalf("unsafe cleanup detail: %+v", result)
		}
		if !blockRead && result.Outcome != models.SyntheticVerified {
			t.Fatalf("cleanup failure downgraded send success: %+v", result)
		}
		if blockRead && (result.Outcome != models.SyntheticFailed || result.Diagnosis != models.DiagnosisTimeout) {
			t.Fatalf("read timeout classification lost: %+v", result)
		}
	}
}
