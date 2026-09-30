package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// telegramDefaultAPIBase is Telegram's official Bot API base URL,
// https://core.telegram.org/bots/api. A channel-config "api_base"
// override is only honored when the SSRF guard has been relaxed (see
// WithAllowCustomEndpoints).
const telegramDefaultAPIBase = "https://api.telegram.org"

// telegramCaptionLimit is sendPhoto's caption character limit (Bot API
// docs: "Photo caption ... 0-1024 characters after entities parsing").
const telegramCaptionLimit = 1024

// telegramSender delivers Messages via the Telegram Bot API's
// sendPhoto (when an image is attached) or sendMessage, both HTML
// parse_mode.
type telegramSender struct {
	apiBase string
	token   string
	chatID  string
	// threadID is optional message_thread_id, "" if unset.
	threadID string
	client   *http.Client
}

// validateTelegramConfig checks "bot_token" and "chat_id".
func validateTelegramConfig(cfg map[string]string) map[string]string {
	errs := map[string]string{}
	requireNonEmpty(errs, cfg, "bot_token")
	requireNonEmpty(errs, cfg, "chat_id")
	if v := cfg["message_thread_id"]; v != "" {
		if _, err := strconv.Atoi(v); err != nil {
			errs["message_thread_id"] = "must be an integer"
		}
	}
	return errs
}

// newTelegramSender builds a Sender for ch.
func newTelegramSender(ch models.NotifyChannel, client *http.Client, opts clientOptions) (Sender, error) {
	if errs := validateTelegramConfig(ch.Config); len(errs) > 0 {
		return nil, fmt.Errorf("notify: telegram: invalid config: %v", errs)
	}
	base := telegramDefaultAPIBase
	if custom := ch.Config["api_base"]; custom != "" {
		if !opts.allowCustomEndpoints {
			return nil, fmt.Errorf("notify: telegram: api_base override requires custom endpoints to be enabled")
		}
		u, err := url.Parse(custom)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("notify: telegram: invalid api_base %q", custom)
		}
		base = strings.TrimSuffix(custom, "/")
	} else if !opts.allowCustomEndpoints {
		// Still run the endpoint through the host allowlist for
		// defense in depth, even though we built it ourselves.
		if _, err := validateEndpointHost(telegramDefaultAPIBase+"/", telegramAllowedHosts, opts); err != nil {
			return nil, fmt.Errorf("notify: telegram: %w", err)
		}
	}
	return &telegramSender{
		apiBase:  base,
		token:    ch.Config["bot_token"],
		chatID:   ch.Config["chat_id"],
		threadID: ch.Config["message_thread_id"],
		client:   client,
	}, nil
}

// escapeHTML escapes the characters Telegram's HTML parse_mode
// requires escaped in plain text: <, >, & (see
// https://core.telegram.org/bots/api#html-style — "All <, > and &
// symbols that are not part of a tag ... must be replaced with the
// corresponding HTML entities").
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramHTML renders msg as an HTML-formatted caption/text body:
// bold title, plain text, then "name: value" field lines and an
// optional link.
func telegramHTML(msg Message) string {
	var b strings.Builder
	if msg.Title != "" {
		b.WriteString("<b>")
		b.WriteString(escapeHTML(msg.Title))
		b.WriteString("</b>")
	}
	if msg.Text != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(escapeHTML(msg.Text))
	}
	for _, f := range msg.Fields {
		b.WriteString("\n")
		b.WriteString(escapeHTML(f.Name))
		b.WriteString(": ")
		b.WriteString(escapeHTML(f.Value))
	}
	if msg.URL != "" {
		b.WriteString("\n")
		b.WriteString(`<a href="`)
		b.WriteString(html.EscapeString(msg.URL))
		b.WriteString(`">Open dashboard</a>`)
	}
	return b.String()
}

// truncateCaption trims s to at most telegramCaptionLimit runes,
// preferring a full rune boundary.
func truncateCaption(s string) string {
	r := []rune(s)
	if len(r) <= telegramCaptionLimit {
		return s
	}
	return string(r[:telegramCaptionLimit])
}

// Send implements Sender.
func (s *telegramSender) Send(ctx context.Context, msg Message) error {
	_, err := s.sendWithRef(ctx, msg)
	return err
}

// SendWithRef implements RefSender: sends msg exactly as Send does,
// additionally parsing the sendPhoto/sendMessage response's
// result.message_id, result.chat.id, and (when sending a photo)
// result.photo to report whether the chart attachment is confirmed present
// directly in the send response — Telegram bots have no API to read a
// message back afterward, so this is the only confirmation available (see
// package doc comment in verify.go).
func (s *telegramSender) SendWithRef(ctx context.Context, msg Message) (MessageRef, error) {
	return s.sendWithRef(ctx, msg)
}

func (s *telegramSender) sendWithRef(ctx context.Context, msg Message) (MessageRef, error) {
	text := telegramHTML(msg)
	if msg.HasImage() {
		return s.sendPhotoWithRef(ctx, text, msg.Image)
	}
	return s.sendMessageWithRef(ctx, text)
}

func (s *telegramSender) methodURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", s.apiBase, s.token, method)
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
	Result *telegramResultMessage `json:"result"`
}

// telegramResultMessage mirrors the subset of Telegram's Message object
// this package needs from a sendMessage/sendPhoto response's "result"
// field.
type telegramResultMessage struct {
	MessageID int `json:"message_id"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Photo []struct {
		FileID string `json:"file_id"`
	} `json:"photo"`
}

func (s *telegramSender) sendMessageWithRef(ctx context.Context, htmlText string) (MessageRef, error) {
	form := url.Values{}
	form.Set("chat_id", s.chatID)
	form.Set("text", htmlText)
	form.Set("parse_mode", "HTML")
	if s.threadID != "" {
		form.Set("message_thread_id", s.threadID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.methodURL("sendMessage"), strings.NewReader(form.Encode()))
	if err != nil {
		return MessageRef{}, fmt.Errorf("notify: telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.doWithRef(ctx, req)
}

func (s *telegramSender) sendPhotoWithRef(ctx context.Context, htmlCaption string, image []byte) (MessageRef, error) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)

	if err := w.WriteField("chat_id", s.chatID); err != nil {
		return MessageRef{}, fmt.Errorf("notify: telegram: write chat_id field: %w", err)
	}
	if err := w.WriteField("caption", truncateCaption(htmlCaption)); err != nil {
		return MessageRef{}, fmt.Errorf("notify: telegram: write caption field: %w", err)
	}
	if err := w.WriteField("parse_mode", "HTML"); err != nil {
		return MessageRef{}, fmt.Errorf("notify: telegram: write parse_mode field: %w", err)
	}
	if s.threadID != "" {
		if err := w.WriteField("message_thread_id", s.threadID); err != nil {
			return MessageRef{}, fmt.Errorf("notify: telegram: write message_thread_id field: %w", err)
		}
	}
	part, err := createFormFilePart(w, "photo", "chart.png", "image/png")
	if err != nil {
		return MessageRef{}, fmt.Errorf("notify: telegram: create photo part: %w", err)
	}
	if _, err := part.Write(image); err != nil {
		return MessageRef{}, fmt.Errorf("notify: telegram: write photo part: %w", err)
	}
	if err := w.Close(); err != nil {
		return MessageRef{}, fmt.Errorf("notify: telegram: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.methodURL("sendPhoto"), buf)
	if err != nil {
		return MessageRef{}, fmt.Errorf("notify: telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return s.doWithRef(ctx, req)
}

func (s *telegramSender) do(ctx context.Context, req *http.Request) error {
	_, err := s.doWithRef(ctx, req)
	return err
}

func (s *telegramSender) doWithRef(ctx context.Context, req *http.Request) (MessageRef, error) {
	resp, err := doRequest(ctx, s.client, req)
	if err != nil {
		return MessageRef{}, fmt.Errorf("notify: telegram: %w", err)
	}
	body := readLimitedBody(resp)

	var parsed telegramResponse
	_ = json.Unmarshal(body, &parsed) // best-effort parse; fall back to raw body/status below

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := defaultRetryAfter
		switch {
		case parsed.Parameters != nil && parsed.Parameters.RetryAfter > 0:
			retryAfter = time.Duration(parsed.Parameters.RetryAfter) * time.Second
		case resp.Header.Get("Retry-After") != "":
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}
		return MessageRef{}, &RetryAfterError{After: retryAfter, Err: fmt.Errorf("notify: telegram: rate limited: %s", parsed.Description)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !parsed.OK {
		msg := parsed.Description
		if msg == "" {
			const maxLen = 500
			if len(body) > maxLen {
				body = body[:maxLen]
			}
			msg = string(body)
		}
		return MessageRef{}, newStatusError("telegram", "", resp.StatusCode, body, msg)
	}
	ref := MessageRef{ChatID: s.chatID}
	if parsed.Result != nil {
		ref.MessageID = strconv.Itoa(parsed.Result.MessageID)
		if parsed.Result.Chat.ID != 0 {
			ref.ChatID = strconv.FormatInt(parsed.Result.Chat.ID, 10)
		}
		ref.HasAttachment = len(parsed.Result.Photo) > 0
	}
	return ref, nil
}

// Delete implements MessageDeleter. Telegram only allows deleting a
// message sent less than 48 hours ago
// (https://core.telegram.org/bots/api#deletemessage) — a delete attempted
// well past that window fails with a platform error, which callers
// surface as-is rather than special-casing.
func (s *telegramSender) Delete(ctx context.Context, ref MessageRef) error {
	if ref.MessageID == "" {
		return fmt.Errorf("notify: telegram: delete: missing message id")
	}
	chatID := ref.ChatID
	if chatID == "" {
		chatID = s.chatID
	}
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("message_id", ref.MessageID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.methodURL("deleteMessage"), strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("notify: telegram: build delete request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(ctx, req)
}
