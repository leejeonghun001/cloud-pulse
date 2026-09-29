package notify

// redactURL returns url unchanged if it is empty, otherwise a fixed
// placeholder — used so a webhook/bot-token-bearing URL is never
// written to a log line. Callers should prefer logging a channel's
// name/type/ID instead of any URL at all; this helper exists for the
// rare case a URL must appear in a log message (e.g. "invalid webhook
// host") without leaking the token embedded in it.
func redactURL(url string) string {
	if url == "" {
		return ""
	}
	return "(redacted)"
}

// redactSecret returns "(set)" if v is non-empty, "(not set)"
// otherwise — the same presence-only redaction convention used
// elsewhere in the hub (see CODING_CONVENTIONS.md "No secrets
// logged").
func redactSecret(v string) string {
	if v == "" {
		return "(not set)"
	}
	return "(set)"
}
