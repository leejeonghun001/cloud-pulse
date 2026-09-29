package notify

import (
	"fmt"
	"net/url"
	"strings"
)

// Official hosts each notifier is allowed to reach by default. A
// channel config value naming any other host is rejected unless the
// SSRF guard has been relaxed for the sender (see
// WithAllowCustomEndpoints) — this prevents a compromised/malicious
// hub admin (or a bug that lets user input reach a URL field) from
// turning an alert channel into an arbitrary internal-network HTTP
// client.
var (
	discordAllowedHosts = map[string]bool{
		"discord.com":    true,
		"discordapp.com": true,
	}
	telegramAllowedHosts = map[string]bool{
		"api.telegram.org": true,
	}
	whatsappAllowedHosts = map[string]bool{
		"graph.facebook.com": true,
	}
)

// validateEndpointHost parses rawURL and checks its host against
// allowed. When opts.allowCustomEndpoints is true, any host is
// accepted regardless of scheme — this mode is only ever reached in
// production via CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS=1, and tests need it
// to accept httptest's plain-http URLs too. Outside that mode, the
// scheme must be https and the host must be in allowed.
func validateEndpointHost(rawURL string, allowed map[string]bool, opts clientOptions) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("notify: parse endpoint url: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, fmt.Errorf("notify: endpoint url has no host")
	}
	if opts.allowCustomEndpoints {
		return u, nil
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("notify: endpoint url must use https, got %q", u.Scheme)
	}
	if !allowed[host] {
		return nil, fmt.Errorf("notify: endpoint host %q is not an official host and custom endpoints are disabled", host)
	}
	return u, nil
}
