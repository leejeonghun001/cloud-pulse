package notify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTelegramSender_SendWithRef_Photo confirms SendWithRef parses
// result.message_id, result.chat.id, and result.photo from a sendPhoto
// response (https://core.telegram.org/bots/api#sendphoto,
// https://core.telegram.org/bots/api#message).
func TestTelegramSender_SendWithRef_Photo(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":555,"chat":{"id":-100123456},"photo":[{"file_id":"abc","width":800,"height":400}]}}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	refSender, ok := sender.(RefSender)
	if !ok {
		t.Fatal("telegramSender does not implement RefSender")
	}

	ref, err := refSender.SendWithRef(t.Context(), Message{Title: "t", Image: []byte{1, 2, 3}})
	if err != nil {
		t.Fatalf("SendWithRef() error = %v", err)
	}
	if ref.MessageID != "555" {
		t.Errorf("MessageID = %q, want 555", ref.MessageID)
	}
	if ref.ChatID != "-100123456" {
		t.Errorf("ChatID = %q, want -100123456", ref.ChatID)
	}
	if !ref.HasAttachment {
		t.Errorf("HasAttachment = false, want true (result.photo non-empty)")
	}
}

// TestTelegramSender_SendWithRef_TextOnly_NoAttachment confirms a
// text-only sendMessage response (no photo field) reports
// HasAttachment=false.
func TestTelegramSender_SendWithRef_TextOnly_NoAttachment(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":556,"chat":{"id":-100123456}}}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	refSender := sender.(RefSender)
	ref, err := refSender.SendWithRef(t.Context(), Message{Title: "t"})
	if err != nil {
		t.Fatalf("SendWithRef() error = %v", err)
	}
	if ref.HasAttachment {
		t.Errorf("HasAttachment = true, want false for a text-only message")
	}
}

// TestTelegramSender_ChatIDMismatch_StillReturnsResponseChatID confirms
// SendWithRef always reports the chat id Telegram's own response carries
// (used by callers to detect a chat-id mismatch against what was
// configured, per SPEC-v0.7 §2's read-back test-plan item).
func TestTelegramSender_ChatIDMismatch_StillReturnsResponseChatID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Response reports a different chat id than the channel's configured chat_id.
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":557,"chat":{"id":-999999}}}`))
	}))
	defer srv.Close()

	ch := telegramChannel(srv.URL, nil)
	sender, err := New(ch, srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	refSender := sender.(RefSender)
	ref, err := refSender.SendWithRef(t.Context(), Message{Title: "t"})
	if err != nil {
		t.Fatalf("SendWithRef() error = %v", err)
	}
	if ref.ChatID != "-999999" {
		t.Errorf("ChatID = %q, want -999999 (the response's own chat id)", ref.ChatID)
	}
	if ref.ChatID == ch.Config["chat_id"] {
		t.Errorf("test setup bug: response chat id should differ from configured chat_id")
	}
}

// TestTelegramSender_Delete_Success confirms Delete calls deleteMessage
// with the ref's message id and chat id.
func TestTelegramSender_Delete_Success(t *testing.T) {
	t.Parallel()

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "deleteMessage") {
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			gotBody = string(body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	deleter, ok := sender.(MessageDeleter)
	if !ok {
		t.Fatal("telegramSender does not implement MessageDeleter")
	}
	if err := deleter.Delete(t.Context(), MessageRef{MessageID: "555", ChatID: "-100123456"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !strings.Contains(gotBody, "message_id=555") {
		t.Errorf("request body = %q, want it to contain message_id=555", gotBody)
	}
}

// TestTelegramSender_Delete_TooOld confirms a platform error (e.g. past
// the 48-hour deleteMessage window, per
// https://core.telegram.org/bots/api#deletemessage) surfaces as an error
// rather than a silent success.
func TestTelegramSender_Delete_TooOld(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: message can't be deleted"}`))
	}))
	defer srv.Close()

	sender, err := New(telegramChannel(srv.URL, nil), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	deleter := sender.(MessageDeleter)
	err = deleter.Delete(t.Context(), MessageRef{MessageID: "1", ChatID: "-100123456"})
	if err == nil {
		t.Fatal("Delete() error = nil, want error when Telegram rejects the delete")
	}
}

// TestTelegramSender_DoesNotImplementMessageReader documents (via a
// compile-time-checkable runtime assertion) that Telegram has no
// read-back API — bots cannot fetch a message they already sent — so
// telegramSender intentionally does not implement MessageReader; verify
// callers must cap Telegram's result at "accepted", never "verified" via
// a second read call.
func TestTelegramSender_DoesNotImplementMessageReader(t *testing.T) {
	t.Parallel()

	sender, err := New(telegramChannel("https://api.telegram.org", nil), nil, WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := sender.(MessageReader); ok {
		t.Errorf("telegramSender unexpectedly implements MessageReader; Telegram has no read-back API")
	}
}
