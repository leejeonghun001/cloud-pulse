package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

type verifyDisplaySender struct {
	branch    string
	err       error
	deadlines []time.Time
}

func (s *verifyDisplaySender) mark(ctx context.Context) {
	d, _ := ctx.Deadline()
	s.deadlines = append(s.deadlines, d)
}
func (s *verifyDisplaySender) Send(ctx context.Context, _ notify.Message) error {
	s.mark(ctx)
	if s.branch == "send" {
		return s.err
	}
	return nil
}
func (s *verifyDisplaySender) ReadBack(ctx context.Context, _ notify.MessageRef) (bool, error) {
	s.mark(ctx)
	if s.branch == "read" {
		return false, s.err
	}
	return true, nil
}
func (s *verifyDisplaySender) Delete(ctx context.Context, _ notify.MessageRef) error {
	s.mark(ctx)
	return s.err
}

func TestNotifyVerify_ErrorBranchesTextJSONSafeAndCleanupPreservesSuccess(t *testing.T) {
	// stdout capture is intentionally not parallel; no files or external sends.
	for _, branch := range []string{"constructor", "send", "read", "cleanup"} {
		ch := models.NotifyChannel{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "12:t0", "api_base": "https://api.example.invalid/private"}}
		err := errors.New("send request failed 12:t0 https://api.example.invalid/private")
		sender := &verifyDisplaySender{branch: branch, err: err}
		opts := notifyVerifyOptions{timeout: time.Second, cleanup: true, senderFactory: func(models.NotifyChannel, *http.Client) (notify.Sender, error) {
			if branch == "constructor" {
				return nil, err
			}
			return sender, nil
		}}
		result := runOneNotifyVerify(t.Context(), ch, &http.Client{}, opts, nil)
		report := models.NotifyVerifyReport{Results: []models.NotifyVerifyResult{result}}
		encoded, _ := json.Marshal(report)
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout := os.Stdout
		os.Stdout = write
		printNotifyVerifyText(report)
		os.Stdout = stdout
		_ = write.Close()
		text, _ := io.ReadAll(read)
		_ = read.Close()
		combined := string(encoded) + string(text)
		if strings.Contains(combined, "12:t0") || strings.Contains(combined, "api.example.invalid") {
			t.Fatalf("%s leaked error secret: %s", branch, combined)
		}
		if branch == "cleanup" && (result.Status != models.NotifyVerifyVerified || result.Cleaned == nil || *result.Cleaned || !strings.Contains(result.Detail, "cleanup failed: send request failed")) {
			t.Fatalf("cleanup failure lost success/useful wording: %+v", result)
		}
		for _, d := range sender.deadlines {
			if d.IsZero() {
				t.Fatal("CLI operation was not bounded by a deadline")
			}
		}
	}
}

func TestNotifyVerify_TypedTimeoutAndRateLimitClassification(t *testing.T) {
	ch := models.NotifyChannel{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ"}}
	for _, tc := range []struct {
		err  error
		code models.DiagnosisCode
	}{
		{fmt.Errorf("send xQ: %w", context.DeadlineExceeded), models.DiagnosisTimeout},
		{&notify.RetryAfterError{After: time.Second, Err: errors.New("rate limited xQ")}, models.DiagnosisRateLimited},
	} {
		result := runOneNotifyVerify(t.Context(), ch, &http.Client{}, notifyVerifyOptions{timeout: time.Second, senderFactory: func(models.NotifyChannel, *http.Client) (notify.Sender, error) {
			return &verifyDisplaySender{branch: "send", err: tc.err}, nil
		}}, nil)
		encoded, _ := json.Marshal(result)
		if result.Diagnosis == nil || result.Diagnosis.Code != tc.code || strings.Contains(string(encoded), "xQ") {
			t.Fatalf("original error classification or redaction lost: %+v", result)
		}
	}
}

func TestNotifyVerify_AllProviderKnownSecretsConstructorSendReadCleanupJSON(t *testing.T) {
	for _, ch := range []models.NotifyChannel{
		{Type: models.NotifyChannelTelegram, Config: map[string]string{"bot_token": "12:t0"}},
		{Type: models.NotifyChannelWhatsApp, Config: map[string]string{"access_token": "xQ"}},
		{Type: models.NotifyChannelDiscord, Config: map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/xQ"}},
		{Type: models.NotifyChannelWebhook, Config: map[string]string{"url": "https://user:pwQ@hooks.example.invalid/private?token=qZ"}},
	} {
		for _, branch := range []string{"constructor", "send", "read", "cleanup"} {
			errText := "send request failed "
			for _, value := range ch.Config {
				errText += value + " " + url.QueryEscape(value) + " "
			}
			err := errors.New(errText)
			result := runOneNotifyVerify(t.Context(), ch, &http.Client{}, notifyVerifyOptions{timeout: time.Second, cleanup: true, senderFactory: func(models.NotifyChannel, *http.Client) (notify.Sender, error) {
				if branch == "constructor" {
					return nil, err
				}
				return &verifyDisplaySender{branch: branch, err: err}, nil
			}}, nil)
			encoded, _ := json.Marshal(result)
			for _, secret := range []string{"12:t0", "xQ", "pwQ", "qZ", "hooks.example.invalid"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("%s/%s leaked in CLI JSON", ch.Type, branch)
				}
			}
		}
	}
}

type verifyCompatSender struct {
	send   func(context.Context) (notify.MessageRef, error)
	delete func(context.Context) error
}

func (s verifyCompatSender) Send(ctx context.Context, _ notify.Message) error {
	_, err := s.send(ctx)
	return err
}
func (s verifyCompatSender) SendWithRef(ctx context.Context, _ notify.Message) (notify.MessageRef, error) {
	return s.send(ctx)
}
func (s verifyCompatSender) Delete(ctx context.Context, _ notify.MessageRef) error {
	return s.delete(ctx)
}

type verifyCompatReader struct {
	verifyCompatSender
	read func(context.Context) (bool, error)
}

func (s verifyCompatReader) ReadBack(ctx context.Context, _ notify.MessageRef) (bool, error) {
	return s.read(ctx)
}

type verifyCompatWaiter func(context.Context, string) (string, error)

func (f verifyCompatWaiter) WaitFor(ctx context.Context, id string) (string, error) {
	return f(ctx, id)
}

func assertVerifyFreshAllowance(t *testing.T, ctx context.Context, budget time.Duration) time.Time {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || ctx.Err() != nil {
		t.Fatalf("operation started without a live deadline: %v", ctx.Err())
	}
	remaining := time.Until(deadline)
	if remaining <= budget/2 || remaining > budget {
		t.Fatalf("operation allowance = %s, want its own %s budget", remaining, budget)
	}
	return deadline
}

func TestNotifyVerify_PerOperationTimeoutCleanupGetsFreshAllowance(t *testing.T) {
	const budget = 15 * time.Millisecond
	var deadlines []time.Time
	consume := func(ctx context.Context) {
		deadlines = append(deadlines, assertVerifyFreshAllowance(t, ctx, budget))
		// Deliberately report fake success as each allowance expires. This
		// forces prior operations' elapsed time without timing/sleep races.
		<-ctx.Done()
	}
	sender := verifyCompatReader{
		verifyCompatSender: verifyCompatSender{
			send: func(ctx context.Context) (notify.MessageRef, error) {
				consume(ctx)
				return notify.MessageRef{MessageID: "generated-message"}, nil
			},
			delete: func(ctx context.Context) error { consume(ctx); return nil },
		},
		read: func(ctx context.Context) (bool, error) { consume(ctx); return true, nil },
	}
	result := runOneNotifyVerify(t.Context(), models.NotifyChannel{Type: models.NotifyChannelDiscord}, &http.Client{}, notifyVerifyOptions{
		timeout: budget, cleanup: true,
		senderFactory: func(models.NotifyChannel, *http.Client) (notify.Sender, error) { return sender, nil },
	}, nil)
	if result.Status != models.NotifyVerifyVerified || result.Cleaned == nil || !*result.Cleaned || len(deadlines) != 3 {
		t.Fatalf("per-operation run did not complete: %+v deadlines=%v", result, deadlines)
	}
	for i := 1; i < len(deadlines); i++ {
		if deadlines[i].Sub(deadlines[i-1]) < budget/2 {
			t.Fatal("read-back/cleanup reused an earlier operation's deadline")
		}
	}
}

func TestNotifyVerify_StatusWaitSeparateBudgetAndFreshCleanup(t *testing.T) {
	const requestBudget = 10 * time.Millisecond
	for _, defaultWait := range []bool{false, true} {
		t.Run(fmt.Sprintf("default_wait_%t", defaultWait), func(t *testing.T) {
			opts, err := parseNotifyVerifyArgs([]string{"--platform", "whatsapp", "--cleanup", "--timeout", "10ms"})
			if err != nil {
				t.Fatal(err)
			}
			waitBudget := 30 * time.Millisecond
			if defaultWait {
				waitBudget = 60 * time.Second
			} else {
				opts.statusWebhookWait = waitBudget
			}
			var sendDeadline, waitDeadline, cleanupDeadline time.Time
			sender := verifyCompatSender{
				send: func(ctx context.Context) (notify.MessageRef, error) {
					sendDeadline = assertVerifyFreshAllowance(t, ctx, requestBudget)
					<-ctx.Done()
					return notify.MessageRef{MessageID: "generated-wamid"}, nil
				},
				delete: func(ctx context.Context) error {
					cleanupDeadline = assertVerifyFreshAllowance(t, ctx, requestBudget)
					return nil
				},
			}
			opts.senderFactory = func(models.NotifyChannel, *http.Client) (notify.Sender, error) { return sender, nil }
			waiter := verifyCompatWaiter(func(ctx context.Context, id string) (string, error) {
				if id != "generated-wamid" {
					t.Fatalf("waiter received wrong message id: %q", id)
				}
				waitDeadline = assertVerifyFreshAllowance(t, ctx, waitBudget)
				if defaultWait {
					// Inspect the default 60s budget without actually waiting 60s.
					return "delivered", nil
				}
				<-ctx.Done()
				return "", ctx.Err()
			})
			result := runOneNotifyVerify(t.Context(), models.NotifyChannel{Type: models.NotifyChannelWhatsApp}, &http.Client{}, opts, waiter)
			want := models.NotifyVerifyAccepted
			if defaultWait {
				want = models.NotifyVerifyVerified
			}
			if result.Status != want || result.Cleaned == nil || !*result.Cleaned {
				t.Fatalf("independent status/cleanup allowances lost: %+v", result)
			}
			if waitDeadline.Sub(sendDeadline) < waitBudget/2 || !cleanupDeadline.After(sendDeadline) {
				t.Fatal("status wait or cleanup was clipped by the send timeout")
			}
		})
	}
}

func TestNotifyVerify_OperationAndStatusBudgetsRespectShorterParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	assertParent := func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(parentDeadline) {
			t.Fatal("operation escaped the shorter parent deadline")
		}
	}
	sender := verifyCompatSender{
		send: func(ctx context.Context) (notify.MessageRef, error) {
			assertParent(ctx)
			return notify.MessageRef{}, nil
		},
		delete: func(ctx context.Context) error { assertParent(ctx); return nil },
	}
	waiter := verifyCompatWaiter(func(ctx context.Context, _ string) (string, error) { assertParent(ctx); return "delivered", nil })
	result := runOneNotifyVerify(ctx, models.NotifyChannel{Type: models.NotifyChannelWhatsApp}, &http.Client{}, notifyVerifyOptions{
		timeout: 2 * time.Second, statusWebhookWait: 60 * time.Second, cleanup: true,
		senderFactory: func(models.NotifyChannel, *http.Client) (notify.Sender, error) { return sender, nil },
	}, waiter)
	if result.Status != models.NotifyVerifyVerified || result.Cleaned == nil || !*result.Cleaned {
		t.Fatalf("parent-aware run failed: %+v", result)
	}
}
