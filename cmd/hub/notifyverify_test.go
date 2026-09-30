package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
	"github.com/leejeonghun001/cloud-pulse/internal/notify"
)

func writeCredentialsFile(t *testing.T, dir, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "creds.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}
	// os.WriteFile's mode argument is subject to the process umask at
	// creation time (e.g. mode 0602 with umask 0022 becomes 0600, never
	// reaching the group/other bits the permission tests below need to
	// exercise) -- chmod explicitly afterward to get the exact mode
	// requested, matching scripts/tests/test_verify_notify.py's own
	// path.chmod(...) pattern.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod credentials file: %v", err)
	}
	return path
}

// --- Credential loading / permissions ---

func TestLoadNotifyVerifyCredentials_FileOverEnvPriority(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeCredentialsFile(t, dir, "CP_VERIFY_DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/1/from-file\n", 0o600)

	lookupEnv := func(key string) (string, bool) {
		if key == envDiscordWebhookURL {
			return "https://discord.com/api/webhooks/1/from-env", true
		}
		return "", false
	}

	values, err := loadNotifyVerifyCredentials(path, lookupEnv)
	if err != nil {
		t.Fatalf("loadNotifyVerifyCredentials() error = %v", err)
	}
	if values[envDiscordWebhookURL] != "https://discord.com/api/webhooks/1/from-file" {
		t.Errorf("value = %q, want the credentials-file value to take priority over env", values[envDiscordWebhookURL])
	}
}

func TestLoadNotifyVerifyCredentials_EnvOnly(t *testing.T) {
	t.Parallel()
	lookupEnv := func(key string) (string, bool) {
		if key == envTelegramBotToken {
			return "123456:ABCDEF", true
		}
		if key == envTelegramChatID {
			return "-1001", true
		}
		return "", false
	}
	values, err := loadNotifyVerifyCredentials("", lookupEnv)
	if err != nil {
		t.Fatalf("loadNotifyVerifyCredentials() error = %v", err)
	}
	if values[envTelegramBotToken] != "123456:ABCDEF" || values[envTelegramChatID] != "-1001" {
		t.Errorf("values = %+v, want telegram env vars resolved", values)
	}
}

func TestLoadNotifyVerifyCredentials_RejectsGroupReadablePermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeCredentialsFile(t, dir, "CP_VERIFY_DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/1/x\n", 0o640)

	_, err := loadNotifyVerifyCredentials(path, func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("loadNotifyVerifyCredentials() error = nil, want rejection of a group-readable file")
	}
}

func TestLoadNotifyVerifyCredentials_RejectsWorldWritablePermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeCredentialsFile(t, dir, "CP_VERIFY_DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/1/x\n", 0o602)

	_, err := loadNotifyVerifyCredentials(path, func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("loadNotifyVerifyCredentials() error = nil, want rejection of a world-writable file")
	}
}

func TestLoadNotifyVerifyCredentials_AcceptsStrict0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeCredentialsFile(t, dir, "CP_VERIFY_DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/1/x\n", 0o600)

	values, err := loadNotifyVerifyCredentials(path, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("loadNotifyVerifyCredentials() error = %v, want nil for mode 0600", err)
	}
	if values[envDiscordWebhookURL] == "" {
		t.Errorf("expected the discord webhook url to be loaded")
	}
}

func TestParseNotifyVerifyCredentialsFile_CommentsAndBlankLinesSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "# comment\n\nCP_VERIFY_TELEGRAM_BOT_TOKEN=123:abc\n  \nCP_VERIFY_TELEGRAM_CHAT_ID=-5\n"
	path := writeCredentialsFile(t, dir, content, 0o600)

	values, err := parseNotifyVerifyCredentialsFile(path)
	if err != nil {
		t.Fatalf("parseNotifyVerifyCredentialsFile() error = %v", err)
	}
	if values["CP_VERIFY_TELEGRAM_BOT_TOKEN"] != "123:abc" {
		t.Errorf("bot token = %q, want 123:abc", values["CP_VERIFY_TELEGRAM_BOT_TOKEN"])
	}
	if values["CP_VERIFY_TELEGRAM_CHAT_ID"] != "-5" {
		t.Errorf("chat id = %q, want -5", values["CP_VERIFY_TELEGRAM_CHAT_ID"])
	}
}

func TestParseNotifyVerifyCredentialsFile_MissingEqualsRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeCredentialsFile(t, dir, "this line has no equals sign\n", 0o600)

	if _, err := parseNotifyVerifyCredentialsFile(path); err == nil {
		t.Fatal("parseNotifyVerifyCredentialsFile() error = nil, want a KEY=VALUE parse error")
	}
}

// --- Argument parsing / argv secret rejection ---

func TestParseNotifyVerifyArgs_Help(t *testing.T) {
	t.Parallel()
	_, err := parseNotifyVerifyArgs([]string{"--help"})
	if err != errHelpRequested {
		t.Errorf("err = %v, want errHelpRequested", err)
	}
}

func TestParseNotifyVerifyArgs_ValidFlags(t *testing.T) {
	t.Parallel()
	opts, err := parseNotifyVerifyArgs([]string{
		"--credentials-file", "/tmp/creds.env",
		"--platform", "discord",
		"--cleanup",
		"--timeout", "5s",
		"--json",
	})
	if err != nil {
		t.Fatalf("parseNotifyVerifyArgs() error = %v", err)
	}
	if opts.credentialsFile != "/tmp/creds.env" || opts.platform != "discord" || !opts.cleanup || opts.timeout != 5*time.Second || !opts.jsonOutput {
		t.Errorf("opts = %+v, unexpected values", opts)
	}
}

func TestParseNotifyVerifyArgs_InvalidPlatform(t *testing.T) {
	t.Parallel()
	if _, err := parseNotifyVerifyArgs([]string{"--platform", "myspace"}); err == nil {
		t.Fatal("parseNotifyVerifyArgs() error = nil, want rejection of an invalid --platform value")
	}
}

// TestParseNotifyVerifyArgs_RejectsUnknownArgumentWithoutEchoingSecret
// confirms that even though this CLI has no flag that accepts a secret
// value, an unrecognized bare argument that merely looks like a
// credential is never echoed back verbatim in the resulting error (the
// argv-secret-rejection rule shared with scripts/verify-notify.py).
func TestParseNotifyVerifyArgs_RejectsUnknownArgumentWithoutEchoingSecret(t *testing.T) {
	t.Parallel()
	secretLooking := "https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOpQrStUvWxYz0123456789AbCdEf"
	_, err := parseNotifyVerifyArgs([]string{secretLooking})
	if err == nil {
		t.Fatal("parseNotifyVerifyArgs() error = nil, want rejection of an unrecognized argument")
	}
	if strings.Contains(err.Error(), secretLooking) {
		t.Errorf("error message echoed the secret-looking value verbatim: %v", err)
	}
}

func TestParseNotifyVerifyArgs_RejectsBotTokenShapedArgument(t *testing.T) {
	t.Parallel()
	tokenLike := "123456789:AAABBBCCCDDDEEEFFFGGGHHHIIIJJJKKKLLL"
	_, err := parseNotifyVerifyArgs([]string{tokenLike})
	if err == nil {
		t.Fatal("parseNotifyVerifyArgs() error = nil, want rejection")
	}
	if strings.Contains(err.Error(), tokenLike) {
		t.Errorf("error message echoed the bot-token-shaped value verbatim: %v", err)
	}
}

// --- End-to-end report building against fake platform servers ---

func TestRunNotifyVerifyReport_AllSkippedWithNoCredentials(t *testing.T) {
	t.Parallel()
	report := runNotifyVerifyReport(context.Background(), map[string]string{}, notifyVerifyOptions{
		platform: "all",
		timeout:  time.Second,
	})
	if !report.OK {
		t.Errorf("report.OK = false, want true when every platform is skipped")
	}
	if len(report.Results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(report.Results))
	}
	for _, r := range report.Results {
		if r.Status != "skipped" {
			t.Errorf("platform %s status = %s, want skipped", r.Platform, r.Status)
		}
	}
}

func TestRunNotifyVerifyReport_DiscordVerified(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"999","attachments":[{"id":"1"}]}`))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"999","attachments":[{"id":"1"}]}`))
		}
	}))
	defer srv.Close()

	fakeSenderFactory := notifySenderFactory(func(ch models.NotifyChannel, client *http.Client) (notify.Sender, error) {
		return notify.New(ch, client, notify.WithAllowCustomEndpoints(true))
	})

	report := runNotifyVerifyReport(context.Background(), map[string]string{
		envDiscordWebhookURL: srv.URL,
	}, notifyVerifyOptions{platform: "discord", timeout: 5 * time.Second, cleanup: true, senderFactory: fakeSenderFactory})

	if !report.OK {
		t.Fatalf("report.OK = false, want true: %+v", report.Results)
	}
	if len(report.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(report.Results))
	}
	r := report.Results[0]
	if r.Status != "verified" {
		t.Errorf("status = %s, want verified", r.Status)
	}
	if !r.AttachmentVerified {
		t.Errorf("AttachmentVerified = false, want true")
	}
	if r.Cleaned == nil || !*r.Cleaned {
		t.Errorf("Cleaned = %v, want true (--cleanup requested and Discord supports delete)", r.Cleaned)
	}
}

func TestRunNotifyVerifyReport_DiscordSendFailureIsFailedWithDiagnosis(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Unknown Webhook","code":10015}`))
	}))
	defer srv.Close()

	fakeSenderFactory := notifySenderFactory(func(ch models.NotifyChannel, client *http.Client) (notify.Sender, error) {
		return notify.New(ch, client, notify.WithAllowCustomEndpoints(true))
	})

	report := runNotifyVerifyReport(context.Background(), map[string]string{
		envDiscordWebhookURL: srv.URL,
	}, notifyVerifyOptions{platform: "discord", timeout: 5 * time.Second, senderFactory: fakeSenderFactory})

	if report.OK {
		t.Fatal("report.OK = true, want false for a failed send")
	}
	r := report.Results[0]
	if r.Status != "failed" {
		t.Errorf("status = %s, want failed", r.Status)
	}
	if r.Diagnosis == nil {
		t.Fatal("Diagnosis = nil, want a populated diagnosis for the failed send")
	}
	if r.Diagnosis.Code != "discord_webhook_not_found" {
		t.Errorf("diagnosis code = %s, want discord_webhook_not_found", r.Diagnosis.Code)
	}
}

func TestRedactIfSecretLooking_URLIsRedacted(t *testing.T) {
	t.Parallel()
	got := redactIfSecretLooking("https://discord.com/api/webhooks/1/verysecret")
	if strings.Contains(got, "verysecret") {
		t.Errorf("redactIfSecretLooking() = %q, still contains the secret", got)
	}
}

func TestRedactIfSecretLooking_BotTokenIsRedacted(t *testing.T) {
	t.Parallel()
	got := redactIfSecretLooking("123456789:AAABBBCCCDDDEEEFFFGGGHHHIIIJJJKKKLLL")
	if strings.Contains(got, "AAABBBCCC") {
		t.Errorf("redactIfSecretLooking() = %q, still contains the token", got)
	}
}

func TestRedactIfSecretLooking_OrdinaryValuePassesThrough(t *testing.T) {
	t.Parallel()
	got := redactIfSecretLooking("discord")
	if got != "discord" {
		t.Errorf("redactIfSecretLooking(%q) = %q, want unchanged", "discord", got)
	}
}
