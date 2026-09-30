package storageusage

import (
	"fmt"
	"net/url"
	"strings"
)

// officialHosts lists the only hosts storageusage's HTTP calls are
// permitted to reach by default (SPEC-v0.7 §3's SSRF guard, mirroring
// internal/notify/ssrf.go's pattern) — Google's OAuth/device-flow
// endpoints and Drive API, plus Dropbox's OAuth and API hosts. There is
// no CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS-style production env var override
// here: unlike notify's Telegram/WhatsApp api_base (a legitimate
// self-hosted-relay use case), no storage provider config field is
// meant to carry an arbitrary endpoint URL, so the only way to widen
// this list is the AllowedHosts test-only option below.
var officialHosts = map[string]bool{
	"oauth2.googleapis.com": true,
	"www.googleapis.com":    true,
	"api.dropboxapi.com":    true,
	"www.dropbox.com":       true,
}

// SSRFOptions configures validateURL's allowlist. The zero value is the
// production configuration (officialHosts allowlist, https required).
// AllowedHosts/AllowInsecure exist so both this package's own tests and
// a CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS-enabled hub (test/smoke harness
// only — see AllowCustomEndpointsFromEnv) can point a Provider at an
// httptest/fake local server; production code with no such override
// set should always end up passing the equivalent of SSRFOptions{}.
type SSRFOptions struct {
	// AllowedHosts overrides officialHosts when non-nil.
	AllowedHosts map[string]bool
	// AllowInsecure permits http:// (instead of requiring https://).
	AllowInsecure bool
	// RedirectBase, when non-empty, rewrites every validated URL's
	// scheme+host to this base (path+query preserved) — the actual
	// mechanism a test/smoke harness needs, since a storageusage
	// Client's ssrfChecker return value IS the request URL it issues
	// (see dropbox.Client.doTokenRequest/googledrive.Client's callers):
	// merely widening the allowed-host set doesn't change where a
	// request still constructed against the fixed
	// tokenEndpoint/spaceUsageEndpoint-style constants goes. Must be an
	// "http(s)://host[:port]" with no path.
	RedirectBase string
}

// AllowCustomEndpointsFromEnv reports whether
// CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS=1 is set, mirroring
// internal/notify's AllowCustomEndpointsFromEnv/
// CP_NOTIFY_ALLOW_CUSTOM_ENDPOINTS contract exactly (see
// DECISIONS_LOG.md D-069): a single, hub-wide, test-only escape hatch,
// never read by Provider implementations directly — internal/hub's
// SSRF-checker closures (googleSSRFChecker/dropboxSSRFChecker) call
// this once via cmd/hub's wiring and pass the result down, so tests
// can simulate either state without mutating process-wide state.
func AllowCustomEndpointsFromEnv(lookup func(string) (string, bool)) bool {
	v, ok := lookup("CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS")
	return ok && v == "1"
}

// RedirectBaseFromEnv reads CP_STORAGE_FAKE_BASE_URL, the base URL
// (e.g. "http://127.0.0.1:18500") a test/smoke harness's combined fake
// Google+Dropbox HTTP server listens on. Only consulted when
// AllowCustomEndpointsFromEnv is also true; see
// scripts/smoke.sh's storage section.
func RedirectBaseFromEnv(lookup func(string) (string, bool)) string {
	v, _ := lookup("CP_STORAGE_FAKE_BASE_URL")
	return v
}

// ValidateGoogleURL validates rawURL against the production Google
// OAuth/Drive allowlist (oauth2.googleapis.com, www.googleapis.com),
// exported for internal/hub's OAuth flow handlers to pass as a
// googledrive.Client's ssrfChecker without internal/hub needing to
// depend on this package's unexported validateURL/officialHosts.
func ValidateGoogleURL(rawURL string) (*url.URL, error) {
	return validateURL(rawURL, SSRFOptions{})
}

// ValidateDropboxURL validates rawURL against the production Dropbox
// OAuth/API allowlist (api.dropboxapi.com, www.dropbox.com), exported
// for the same reason as ValidateGoogleURL.
func ValidateDropboxURL(rawURL string) (*url.URL, error) {
	return validateURL(rawURL, SSRFOptions{})
}

// ValidateGoogleURLWithOptions is ValidateGoogleURL, but honoring a
// CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS-sourced override (see
// AllowCustomEndpointsFromEnv/RedirectBaseFromEnv): when
// allowCustomEndpoints is true and redirectBase is non-empty, rawURL's
// path+query is rewritten onto redirectBase instead of the official
// Google host — this is what actually lets a test/smoke harness's fake
// server receive the request, since a storageusage Client always
// issues its request to whatever URL the ssrfChecker returns (see
// dropbox/googledrive Client's doTokenRequest-style callers), not to
// rawURL's own original host. Callers that never set
// CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS get byte-identical behavior to
// ValidateGoogleURL.
func ValidateGoogleURLWithOptions(rawURL string, allowCustomEndpoints bool, redirectBase string) (*url.URL, error) {
	if !allowCustomEndpoints {
		return ValidateGoogleURL(rawURL)
	}
	return validateURL(rawURL, SSRFOptions{AllowInsecure: true, RedirectBase: redirectBase})
}

// ValidateDropboxURLWithOptions is ValidateDropboxURL's
// CP_STORAGE_ALLOW_CUSTOM_ENDPOINTS-aware counterpart, mirroring
// ValidateGoogleURLWithOptions exactly.
func ValidateDropboxURLWithOptions(rawURL string, allowCustomEndpoints bool, redirectBase string) (*url.URL, error) {
	if !allowCustomEndpoints {
		return ValidateDropboxURL(rawURL)
	}
	return validateURL(rawURL, SSRFOptions{AllowInsecure: true, RedirectBase: redirectBase})
}

// validateURL parses rawURL and checks its scheme/host against opts'
// allowlist (officialHosts by default). When opts.RedirectBase is set,
// a successfully validated URL's scheme+host is rewritten onto that
// base (path+query preserved) before being returned — see
// SSRFOptions.RedirectBase's doc comment for why this, not just a wider
// allowlist, is what a test/smoke harness needs.
func validateURL(rawURL string, opts SSRFOptions) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("storageusage: parse url: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, fmt.Errorf("storageusage: url has no host")
	}
	if !opts.AllowInsecure && u.Scheme != "https" {
		return nil, fmt.Errorf("storageusage: url must use https, got %q", u.Scheme)
	}
	if opts.RedirectBase == "" {
		allowed := officialHosts
		if opts.AllowedHosts != nil {
			allowed = opts.AllowedHosts
		}
		if !allowed[host] {
			return nil, fmt.Errorf("storageusage: host %q is not an official host", host)
		}
		return u, nil
	}
	target, err := url.Parse(opts.RedirectBase)
	if err != nil {
		return nil, fmt.Errorf("storageusage: parse redirect base: %w", err)
	}
	target.Path = u.Path
	target.RawQuery = u.RawQuery
	return target, nil
}
