package notify

import (
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// SafeErrorText is for display, logging and persistence only. Keep err itself
// for errors.Is/As, diagnosis and retries; transport errors can contain URLs.
func SafeErrorText(ch models.NotifyChannel, err error) string {
	if err == nil {
		return ""
	}
	return SafeText(channelWithErrorURL(ch, err), err.Error())
}

// url.Error may include a redirect or generated operation URL rather than
// exactly the configured endpoint. Treat it as sensitive display data too.
// Copy config: never mutate the saved channel or the original typed error.
func channelWithErrorURL(ch models.NotifyChannel, err error) models.NotifyChannel {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.URL != "" {
		cfg := make(map[string]string, len(ch.Config)+1)
		for k, v := range ch.Config {
			cfg[k] = v
		}
		cfg["error_url"] = urlErr.URL
		ch.Config = cfg
	}
	return ch
}

// SafeText extends the credential-shape sanitizer with this channel's exact
// secret values (including short tokens) and configured endpoint URLs. It
// recognizes common URL/JSON/Go/HTML escaped representations too. Non-secret
// destinations are not treated as credentials. Do not apply this to static
// guidance: a one-character credential can also occur in ordinary prose.
func SafeText(ch models.NotifyChannel, text string) string {
	values := make(map[string]bool)
	add := func(value string) {
		if value == "" || value == models.RedactedConfigValue {
			return
		}
		values[value] = true
		values[url.QueryEscape(value)] = true
		values[url.PathEscape(value)] = true
		values[html.EscapeString(value)] = true
		values[strings.ReplaceAll(value, "/", `\/`)] = true
		quoted := strconv.Quote(value)
		values[quoted[1:len(quoted)-1]] = true
		encoded, _ := json.Marshal(value)
		values[string(encoded[1:len(encoded)-1])] = true
	}
	for _, field := range ch.Type.SecretFields() {
		add(ch.Config[field])
	}
	for key, value := range ch.Config {
		if key == "url" || key == "webhook_url" || key == "api_base" || strings.HasSuffix(key, "_url") {
			add(strings.TrimRight(value, "/"))
			// URL errors may print userinfo or query credentials separately.
			if u, err := url.Parse(value); err == nil {
				if u.User != nil {
					add(u.User.Username())
					password, _ := u.User.Password()
					add(password)
				}
				for _, vs := range u.Query() {
					for _, v := range vs {
						add(v)
					}
				}
				if ch.Type == models.NotifyChannelDiscord && key == "webhook_url" {
					parts := strings.Split(strings.TrimRight(u.Path, "/"), "/")
					if len(parts) > 0 {
						add(parts[len(parts)-1])
					}
				}
			}
		}
	}
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	// Longest first, one pass: short secrets cannot corrupt the placeholder
	// inserted for a longer URL or another credential.
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) == len(keys[j]) {
			return keys[i] < keys[j]
		}
		return len(keys[i]) > len(keys[j])
	})
	pairs := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		pairs = append(pairs, key, "(redacted)")
	}
	return redactSecretsInText(strings.NewReplacer(pairs...).Replace(text))
}

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
