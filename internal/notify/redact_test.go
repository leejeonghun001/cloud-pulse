package notify

import "testing"

func TestRedactURL(t *testing.T) {
	t.Parallel()

	if got := redactURL(""); got != "" {
		t.Errorf("redactURL(\"\") = %q, want empty", got)
	}
	if got := redactURL("https://discord.com/api/webhooks/1/super-secret-token"); got == "https://discord.com/api/webhooks/1/super-secret-token" {
		t.Errorf("redactURL() must not return the original URL unchanged")
	}
}

func TestRedactSecret(t *testing.T) {
	t.Parallel()

	if got := redactSecret(""); got != "(not set)" {
		t.Errorf("redactSecret(\"\") = %q, want (not set)", got)
	}
	if got := redactSecret("super-secret-value"); got != "(set)" {
		t.Errorf("redactSecret(non-empty) = %q, want (set)", got)
	}
	if got := redactSecret("x"); got == "x" {
		t.Errorf("redactSecret() must never return the input value itself")
	}
}
