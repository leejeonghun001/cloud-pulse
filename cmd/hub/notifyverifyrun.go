// notifyverifyrun.go builds a models.NotifyVerifyReport by sending a real
// test notification (with a chart image, rendered by the same
// internal/alerting/chart.Render production code the alert-delivery
// worker uses) through internal/notify's real senders, one per platform
// with credentials configured. This is the "same send path as
// production" requirement from SPEC-v0.7 §2 — no parallel implementation
// of any platform's wire format exists here.
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/alerting/chart"
	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

// notifyVerifyHTTPClientTimeout bounds the http.Client shared by every
// sender constructed for a verify run — separate from and no shorter
// than notifyVerifyOptions.timeout, which bounds the whole per-platform
// attempt (send + read-back + optional delete).
const notifyVerifyHTTPClientTimeout = 30 * time.Second

// notifySenderFactory constructs the Sender for one verification attempt.
type notifySenderFactory func(models.NotifyChannel, *http.Client) (notify.Sender, error)

// defaultNotifySender preserves the production SSRF policy used by alert
// delivery. Tests provide an option-owned factory for fake endpoints.
func defaultNotifySender(ch models.NotifyChannel, client *http.Client) (notify.Sender, error) {
	return notify.New(ch, client)
}

// runNotifyVerifyReport resolves which platforms have credentials
// configured and runs each, building the final report. Platforms with no
// credentials are reported "skipped", never attempted.
func runNotifyVerifyReport(ctx context.Context, values map[string]string, opts notifyVerifyOptions) models.NotifyVerifyReport {
	client := &http.Client{Timeout: notifyVerifyHTTPClientTimeout}

	var statusListener *notify.StatusWebhookListener
	if opts.statusWebhookListen != "" {
		appSecret := values[envWhatsAppAppSecret]
		statusListener = notify.NewStatusWebhookListener(appSecret)
		if _, err := statusListener.Start(opts.statusWebhookListen); err != nil {
			return models.NotifyVerifyReport{
				OK: false,
				Results: []models.NotifyVerifyResult{{
					Platform: models.NotifyVerifyWhatsApp,
					Status:   models.NotifyVerifyFailed,
					Detail:   fmt.Sprintf("start --status-webhook-listen: %v", err),
				}},
			}
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = statusListener.Close(shutdownCtx) // best-effort shutdown; the process is exiting regardless
		}()
	}

	var results []models.NotifyVerifyResult
	targets := notifyVerifyTargets(opts.platform)
	for _, platform := range targets {
		configured := notifyVerifyChannelFor(platform, values)
		if configured == nil {
			results = append(results, models.NotifyVerifyResult{
				Platform: platform,
				Status:   models.NotifyVerifySkipped,
				Detail:   "no credentials configured",
			})
			continue
		}
		results = append(results, runOneNotifyVerify(ctx, *configured, client, opts, statusListener))
	}

	ok := true
	for _, r := range results {
		if r.Status == models.NotifyVerifyFailed {
			ok = false
		}
	}
	return models.NotifyVerifyReport{Results: results, OK: ok}
}

// notifyVerifyTargets returns the platform list to attempt for a
// --platform value ("all" expands to every supported platform; the
// per-platform configured-credentials check still applies afterward).
func notifyVerifyTargets(platform string) []models.NotifyVerifyPlatform {
	if platform != "all" {
		return []models.NotifyVerifyPlatform{models.NotifyVerifyPlatform(platform)}
	}
	return []models.NotifyVerifyPlatform{
		models.NotifyVerifyDiscord,
		models.NotifyVerifyTelegram,
		models.NotifyVerifyWhatsApp,
	}
}

// notifyVerifyChannelFor builds a models.NotifyChannel for platform from
// values, or nil if the required credentials for that platform are not
// all present.
func notifyVerifyChannelFor(platform models.NotifyVerifyPlatform, values map[string]string) *models.NotifyChannel {
	switch platform {
	case models.NotifyVerifyDiscord:
		webhookURL := values[envDiscordWebhookURL]
		if webhookURL == "" {
			return nil
		}
		return &models.NotifyChannel{
			Type:    models.NotifyChannelDiscord,
			Enabled: true,
			Config:  map[string]string{"webhook_url": webhookURL},
		}
	case models.NotifyVerifyTelegram:
		botToken := values[envTelegramBotToken]
		chatID := values[envTelegramChatID]
		if botToken == "" || chatID == "" {
			return nil
		}
		return &models.NotifyChannel{
			Type:    models.NotifyChannelTelegram,
			Enabled: true,
			Config:  map[string]string{"bot_token": botToken, "chat_id": chatID},
		}
	case models.NotifyVerifyWhatsApp:
		accessToken := values[envWhatsAppAccessToken]
		phoneNumberID := values[envWhatsAppPhoneNumberID]
		to := values[envWhatsAppTo]
		if accessToken == "" || phoneNumberID == "" || to == "" {
			return nil
		}
		cfg := map[string]string{
			"access_token":    accessToken,
			"phone_number_id": phoneNumberID,
			"to":              to,
		}
		if v := values[envWhatsAppTemplateName]; v != "" {
			cfg["template_name"] = v
		}
		if v := values[envWhatsAppTemplateLang]; v != "" {
			cfg["template_lang"] = v
		}
		return &models.NotifyChannel{
			Type:    models.NotifyChannelWhatsApp,
			Enabled: true,
			Config:  cfg,
		}
	default:
		return nil
	}
}

// verifyChartTitle is the fixed title baked into the sample chart every
// verify run renders — deliberately generic (no real hostname/metric
// data exists for a standalone CLI run), matching
// scripts/verify-notify.py's "cloud-pulse verify-notify.py test message"
// framing but for the Go CLI's richer (chart-attached) send path.
const verifyChartTitle = "cloud-pulse notify verify · sample chart"

// buildVerifyMessage constructs the test Message every platform
// receives, rendering a real chart via internal/alerting/chart.Render
// (the same renderer internal/alerting's delivery worker uses) so this
// command exercises the real image-generation and image-attachment code
// paths, not a synthetic placeholder image.
func buildVerifyMessage() (notify.Message, error) {
	now := time.Now().UTC()
	points := make([]chart.Point, 0, 12)
	for i := 0; i < 12; i++ {
		points = append(points, chart.Point{
			Time:  now.Add(-time.Duration(11-i) * 5 * time.Minute),
			Value: 40 + float64(i%5)*3,
		})
	}
	png, err := chart.Render(chart.Params{
		Title:     verifyChartTitle,
		Points:    points,
		Threshold: 90,
		Unit:      "%",
	})
	if err != nil {
		return notify.Message{}, fmt.Errorf("render sample chart: %w", err)
	}
	return notify.Message{
		Title:     "cloud-pulse notify verify",
		Text:      "This is a real test notification sent by `cloud-pulse-hub notify verify` (SPEC-v0.7 §2). Safe to ignore.",
		Severity:  notify.SeverityInfo,
		Image:     png,
		ImageName: "chart.png",
	}, nil
}

// runOneNotifyVerify sends the test message through platform's real
// production sender, attempts a read-back, and (with --cleanup) a
// delete, building one models.NotifyVerifyResult.
func runOneNotifyVerify(ctx context.Context, ch models.NotifyChannel, client *http.Client, opts notifyVerifyOptions, statusListener *notify.StatusWebhookListener) models.NotifyVerifyResult {
	platform := models.NotifyVerifyPlatform(ch.Type)
	result := models.NotifyVerifyResult{Platform: platform}

	factory := opts.senderFactory
	if factory == nil {
		factory = defaultNotifySender
	}
	sender, err := factory(ch, client)
	if err != nil {
		result.Status = models.NotifyVerifyFailed
		result.Detail = err.Error()
		return result
	}

	msg, err := buildVerifyMessage()
	if err != nil {
		result.Status = models.NotifyVerifyFailed
		result.Detail = err.Error()
		return result
	}

	sendCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	refSender, hasRef := sender.(notify.RefSender)
	var ref notify.MessageRef
	if hasRef {
		ref, err = refSender.SendWithRef(sendCtx, msg)
	} else {
		err = sender.Send(sendCtx, msg)
	}
	if err != nil {
		result.Status = models.NotifyVerifyFailed
		diag := notify.DiagnoseError(err)
		result.Diagnosis = &diag
		result.Detail = diag.Title
		return result
	}

	result.MessageID = ref.MessageID
	result.ChatID = ref.ChatID
	result.MediaID = ref.MediaID
	result.AttachmentVerified = ref.HasAttachment
	result.Status = models.NotifyVerifyAccepted
	result.Detail = "accepted by platform"

	if reader, ok := sender.(notify.MessageReader); ok {
		readCtx, readCancel := context.WithTimeout(ctx, opts.timeout)
		confirmed, err := reader.ReadBack(readCtx, ref)
		readCancel()
		if err != nil {
			result.Status = models.NotifyVerifyFailed
			diag := notify.DiagnoseError(err)
			result.Diagnosis = &diag
			result.Detail = "read-back failed: " + diag.Title
			return result
		}
		result.AttachmentVerified = confirmed
		result.Status = models.NotifyVerifyVerified
		result.Detail = "verified via read-back"
	} else if platform == models.NotifyVerifyWhatsApp && statusListener != nil {
		waitCtx, waitCancel := context.WithTimeout(ctx, opts.statusWebhookWait)
		status, err := statusListener.WaitFor(waitCtx, ref.MessageID)
		waitCancel()
		if err != nil {
			result.Detail = "accepted by platform; status webhook did not confirm delivery in time: " + err.Error()
		} else {
			result.Status = models.NotifyVerifyVerified
			result.Detail = "verified via delivery-status webhook (" + status + ")"
		}
	}

	if opts.cleanup {
		cleaned := false
		if deleter, ok := sender.(notify.MessageDeleter); ok {
			delCtx, delCancel := context.WithTimeout(ctx, opts.timeout)
			delErr := deleter.Delete(delCtx, ref)
			delCancel()
			if delErr == nil {
				cleaned = true
			} else {
				result.Detail += "; cleanup failed: " + delErr.Error()
			}
		} else {
			result.Detail += "; cleanup not supported for this platform"
		}
		result.Cleaned = &cleaned
	}

	return result
}
