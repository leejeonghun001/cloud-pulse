package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// S3BucketConfig identifies one S3 bucket to collect statistics for.
type S3BucketConfig struct {
	// Name is the S3 bucket name.
	Name string
	// Region is the AWS region the bucket lives in.
	Region string
}

// Hub holds the hub server's configuration, loaded from environment
// variables via LoadHub.
type Hub struct {
	// Listen is the address the HTTP server listens on (e.g. ":8090").
	Listen string
	// DataDir is the directory holding the hub's SQLite database file.
	DataDir string
	// AgentToken authenticates agent report ingestion.
	AgentToken string
	// UIToken authenticates read endpoints when non-empty; empty
	// disables authentication on read endpoints.
	UIToken string
	// AllowedCIDRs restricts which client addresses may reach the hub.
	// A nil slice means allow all.
	AllowedCIDRs []netip.Prefix

	// OfflineAfter is the duration since a host's last-seen time after
	// which it is reported as down.
	OfflineAfter time.Duration
	// CloudInterval is the interval between cloud bucket collections.
	CloudInterval time.Duration

	// AlertWebhookURL is the webhook endpoint egress alerts are posted
	// to; empty disables webhook notifications.
	AlertWebhookURL string

	// LogLevel is one of debug|info|warn|error.
	LogLevel string
	// LogFormat is one of text|json.
	LogFormat string

	// S3Buckets lists the S3 buckets to collect statistics for.
	S3Buckets []S3BucketConfig
	// S3FilterID is the CloudWatch S3 storage lens filter ID used for
	// request metrics (default "EntireBucket").
	S3FilterID string

	// AWSAccessKeyID is the AWS access key used to call CloudWatch/S3
	// APIs.
	AWSAccessKeyID string
	// AWSSecretAccessKey is the AWS secret key used to call
	// CloudWatch/S3 APIs.
	AWSSecretAccessKey string
	// AWSSessionToken is an optional AWS session token for temporary
	// credentials.
	AWSSessionToken string

	// R2AccountID is the Cloudflare account ID used for R2 collection.
	R2AccountID string
	// R2APIToken is the Cloudflare API token used for R2 collection.
	R2APIToken string
	// R2Buckets lists the R2 buckets to collect statistics for; empty
	// means all buckets in the account.
	R2Buckets []string
}

// DBPath returns the path to the hub's SQLite database file, inside
// DataDir.
func (h Hub) DBPath() string {
	return filepath.Join(h.DataDir, "cloud-pulse.db")
}

// S3Enabled reports whether S3 bucket collection is configured: at least
// one bucket is listed and AWS credentials are present.
func (h Hub) S3Enabled() bool {
	return len(h.S3Buckets) > 0 && h.AWSAccessKeyID != "" && h.AWSSecretAccessKey != ""
}

// R2Enabled reports whether R2 bucket collection is configured: an
// account ID and API token are present.
func (h Hub) R2Enabled() bool {
	return h.R2AccountID != "" && h.R2APIToken != ""
}

// defaultAllowedCIDRs is the default CP_ALLOWED_CIDRS value: Tailscale's
// CGNAT range, Tailscale's IPv6 ULA range, IPv4 loopback, and IPv6
// loopback.
const defaultAllowedCIDRs = "100.64.0.0/10,fd7a:115c:a1e0::/48,127.0.0.0/8,::1/128"

// LoadHub loads and validates the hub configuration from environment
// variables via l. See SPEC.md's Config section for the full list of
// variables and defaults.
func LoadHub(l LookupFunc) (Hub, error) {
	h := Hub{}

	h.Listen = getString(l, "CP_LISTEN", ":8090")
	h.DataDir = getString(l, "CP_DATA_DIR", "./data")

	h.AgentToken = getString(l, "CP_AGENT_TOKEN", "")
	if len(h.AgentToken) < 16 {
		return Hub{}, fmt.Errorf("config: CP_AGENT_TOKEN is required and must be at least 16 characters")
	}

	h.UIToken = getString(l, "CP_UI_TOKEN", "")
	if h.UIToken != "" && len(h.UIToken) < 8 {
		return Hub{}, fmt.Errorf("config: CP_UI_TOKEN must be at least 8 characters")
	}

	cidrs, err := parseAllowedCIDRs(getString(l, "CP_ALLOWED_CIDRS", defaultAllowedCIDRs))
	if err != nil {
		return Hub{}, err
	}
	h.AllowedCIDRs = cidrs

	h.OfflineAfter, err = getDuration(l, "CP_OFFLINE_AFTER", 60*time.Second, 1*time.Second)
	if err != nil {
		return Hub{}, err
	}

	h.CloudInterval, err = getDuration(l, "CP_CLOUD_INTERVAL", 15*time.Minute, 1*time.Minute)
	if err != nil {
		return Hub{}, err
	}

	h.AlertWebhookURL = getString(l, "CP_ALERT_WEBHOOK_URL", "")
	if h.AlertWebhookURL != "" {
		if err := validateWebhookURL(h.AlertWebhookURL); err != nil {
			return Hub{}, err
		}
	}

	h.LogLevel = getString(l, "CP_LOG_LEVEL", "info")
	h.LogFormat = getString(l, "CP_LOG_FORMAT", "text")

	region := getString(l, "CP_S3_REGION", "")
	if region == "" {
		region = getString(l, "AWS_REGION", "us-east-1")
	}
	buckets, err := parseS3Buckets(getString(l, "CP_S3_BUCKETS", ""), region)
	if err != nil {
		return Hub{}, err
	}
	h.S3Buckets = buckets
	h.S3FilterID = getString(l, "CP_S3_FILTER_ID", "EntireBucket")

	h.AWSAccessKeyID = getString(l, "AWS_ACCESS_KEY_ID", "")
	h.AWSSecretAccessKey = getString(l, "AWS_SECRET_ACCESS_KEY", "")
	h.AWSSessionToken = getString(l, "AWS_SESSION_TOKEN", "")

	h.R2AccountID = getString(l, "CP_R2_ACCOUNT_ID", "")
	h.R2APIToken = getString(l, "CP_R2_API_TOKEN", "")
	h.R2Buckets = splitList(getString(l, "CP_R2_BUCKETS", ""))

	return h, nil
}

// parseAllowedCIDRs parses a comma-separated list of CIDRs or bare IPs.
// A bare IP is treated as a /32 (IPv4) or /128 (IPv6) prefix. The literal
// value "*" (alone) means allow all, returned as a nil slice. An invalid
// entry is an error.
func parseAllowedCIDRs(raw string) ([]netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "*" {
		return nil, nil
	}

	entries := splitList(raw)
	if len(entries) == 0 {
		return nil, nil
	}

	out := make([]netip.Prefix, 0, len(entries))
	for _, entry := range entries {
		if entry == "*" {
			return nil, nil
		}
		prefix, err := parseCIDROrIP(entry)
		if err != nil {
			return nil, fmt.Errorf("config: CP_ALLOWED_CIDRS: invalid entry %q: %w", entry, err)
		}
		out = append(out, prefix)
	}
	return out, nil
}

// parseCIDROrIP parses s as a CIDR (e.g. "10.0.0.0/8") or a bare IP
// address (e.g. "10.0.0.1"), returning a /32 or /128 prefix for the
// latter.
func parseCIDROrIP(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		return netip.ParsePrefix(s)
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	bits := 32
	if addr.Is6() && !addr.Is4In6() {
		bits = 128
	}
	return netip.PrefixFrom(addr, bits), nil
}

// parseS3Buckets parses a comma-separated "name[:region]" list, using
// defaultRegion when a bucket entry omits its region.
func parseS3Buckets(raw, defaultRegion string) ([]S3BucketConfig, error) {
	entries := splitList(raw)
	if len(entries) == 0 {
		return nil, nil
	}

	out := make([]S3BucketConfig, 0, len(entries))
	for _, entry := range entries {
		name, region, found := strings.Cut(entry, ":")
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("config: CP_S3_BUCKETS: invalid entry %q: empty bucket name", entry)
		}
		region = strings.TrimSpace(region)
		if !found || region == "" {
			region = defaultRegion
		}
		out = append(out, S3BucketConfig{Name: name, Region: region})
	}
	return out, nil
}

// validateWebhookURL reports an error if raw is not a valid http(s) URL.
func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("config: CP_ALERT_WEBHOOK_URL: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("config: CP_ALERT_WEBHOOK_URL: must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("config: CP_ALERT_WEBHOOK_URL: missing host")
	}
	return nil
}
