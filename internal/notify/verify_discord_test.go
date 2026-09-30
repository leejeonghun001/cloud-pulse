package notify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDiscordSender_SendWithRef_WaitTrue confirms SendWithRef appends
// ?wait=true and parses the returned message id + attachment presence
// from Discord's Execute Webhook response
// (https://discord.com/developers/docs/resources/webhook#execute-webhook).
func TestDiscordSender_SendWithRef_WaitTrue(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"111222333","attachments":[{"id":"1","filename":"chart.png"}]}`))
	}))
	defer srv.Close()

	sender, err := New(discordChannel(srv.URL), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	refSender, ok := sender.(RefSender)
	if !ok {
		t.Fatal("discordSender does not implement RefSender")
	}

	ref, err := refSender.SendWithRef(t.Context(), Message{Title: "t", Image: []byte{1, 2, 3}, ImageName: "chart.png"})
	if err != nil {
		t.Fatalf("SendWithRef() error = %v", err)
	}
	if gotQuery != "wait=true" {
		t.Errorf("query = %q, want wait=true", gotQuery)
	}
	if ref.MessageID != "111222333" {
		t.Errorf("MessageID = %q, want 111222333", ref.MessageID)
	}
	if !ref.HasAttachment {
		t.Errorf("HasAttachment = false, want true")
	}
}

// TestDiscordSender_ReadBack_Success confirms ReadBack reports
// attachmentConfirmed=true when GET .../messages/{id} returns a message
// with a non-empty attachments array.
func TestDiscordSender_ReadBack_Success(t *testing.T) {
	t.Parallel()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42","attachments":[{"id":"9","filename":"chart.png","content_type":"image/png"}]}`))
	}))
	defer srv.Close()

	sender := mustNewDiscordSender(t, srv)
	reader, ok := sender.(MessageReader)
	if !ok {
		t.Fatal("discordSender does not implement MessageReader")
	}

	ok2, err := reader.ReadBack(t.Context(), MessageRef{MessageID: "42"})
	if err != nil {
		t.Fatalf("ReadBack() error = %v", err)
	}
	if !ok2 {
		t.Errorf("ReadBack() attachmentConfirmed = false, want true")
	}
	if !strings.HasSuffix(gotPath, "/messages/42") {
		t.Errorf("request path = %q, want suffix /messages/42", gotPath)
	}
}

// TestDiscordSender_ReadBack_MessageMissing confirms a 404 from Discord's
// GET endpoint (message deleted or never existed) surfaces as an error,
// not a silent false.
func TestDiscordSender_ReadBack_MessageMissing(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Unknown Message","code":10008}`))
	}))
	defer srv.Close()

	sender := mustNewDiscordSender(t, srv)
	reader := sender.(MessageReader)
	_, err := reader.ReadBack(t.Context(), MessageRef{MessageID: "42"})
	if err == nil {
		t.Fatal("ReadBack() error = nil, want error for missing message")
	}
}

// TestDiscordSender_ReadBack_AttachmentMissing confirms a message that
// exists but carries no attachments reports attachmentConfirmed=false,
// not an error.
func TestDiscordSender_ReadBack_AttachmentMissing(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"42","attachments":[]}`))
	}))
	defer srv.Close()

	sender := mustNewDiscordSender(t, srv)
	reader := sender.(MessageReader)
	confirmed, err := reader.ReadBack(t.Context(), MessageRef{MessageID: "42"})
	if err != nil {
		t.Fatalf("ReadBack() error = %v", err)
	}
	if confirmed {
		t.Errorf("attachmentConfirmed = true, want false for a message with no attachments")
	}
}

// TestDiscordSender_Delete_Success confirms Delete issues DELETE
// .../messages/{id} and treats 2xx as success.
func TestDiscordSender_Delete_Success(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	sender := mustNewDiscordSender(t, srv)
	deleter := sender.(MessageDeleter)
	if err := deleter.Delete(t.Context(), MessageRef{MessageID: "42"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/messages/42") {
		t.Errorf("path = %q, want suffix /messages/42", gotPath)
	}
}

func mustNewDiscordSender(t *testing.T, srv *httptest.Server) Sender {
	t.Helper()
	sender, err := New(discordChannel(srv.URL), srv.Client(), WithAllowCustomEndpoints(true))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return sender
}
