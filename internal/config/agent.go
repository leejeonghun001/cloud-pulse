package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// defaultNetExclude lists interface name globs excluded from network
// accounting by default: loopback, container/VM bridges, and common VPN
// mesh tunnel interfaces.
const defaultNetExclude = "lo,lo0,docker*,veth*,br-*,virbr*,tailscale*,utun*,cni*,flannel*,cali*,kube*,vxlan*,tun*,wg*,zt*"

// Agent holds the configuration for the cloud-pulse agent process.
type Agent struct {
	// HubURL is the base URL of the hub to report to (trailing '/' trimmed).
	HubURL string
	// Token is the bearer token used to authenticate reports to the hub.
	Token string
	// HostID uniquely identifies this host to the hub.
	HostID string
	// Interval is how often the agent collects and reports a sample.
	Interval time.Duration
	// Provider is "auto", "aws", "oci", or "other". "auto" means the
	// agent should detect the provider at runtime (e.g. via DMI on Linux).
	Provider string
	// EgressLimitBytes overrides the provider's default monthly egress
	// limit, in bytes. nil means use the provider default.
	EgressLimitBytes *uint64
	// NetExclude lists glob patterns of network interface names to
	// exclude from network accounting.
	NetExclude []string
	// LogLevel is one of debug|info|warn|error.
	LogLevel string
	// LogFormat is one of text|json.
	LogFormat string
}

// DefaultAgentNetExclude returns the default network interface exclusion
// globs used when CP_NET_EXCLUDE is unset, split into individual patterns.
// Exposed so callers (e.g. cmd/agent's -once mode) can apply the same
// defaults without loading full agent configuration.
func DefaultAgentNetExclude() []string {
	return splitList(defaultNetExclude)
}

// LoadAgent loads and validates the agent configuration from environment
// variables via l, using hostname as the default host ID source.
//
// Recognized variables: CP_HUB_URL (required, http/https), CP_AGENT_TOKEN
// (required, >=16 chars), CP_HOST_ID (default: sanitized hostname),
// CP_INTERVAL (default 15s, min 5s), CP_PROVIDER (auto|aws|oci|other,
// default auto), CP_EGRESS_LIMIT_GB (unset -> provider default), CP_NET_EXCLUDE
// (comma-separated globs), CP_LOG_LEVEL, CP_LOG_FORMAT.
func LoadAgent(l LookupFunc, hostname string) (Agent, error) {
	cfg := Agent{}

	hubURL, err := loadHubURL(l)
	if err != nil {
		return Agent{}, err
	}
	cfg.HubURL = hubURL

	token := getString(l, "CP_AGENT_TOKEN", "")
	if len(token) < 16 {
		return Agent{}, fmt.Errorf("config: CP_AGENT_TOKEN must be at least 16 characters")
	}
	cfg.Token = token

	hostID, err := loadHostID(l, hostname)
	if err != nil {
		return Agent{}, err
	}
	cfg.HostID = hostID

	interval, err := getDuration(l, "CP_INTERVAL", 15*time.Second, 5*time.Second)
	if err != nil {
		return Agent{}, err
	}
	cfg.Interval = interval

	provider, err := loadProvider(l)
	if err != nil {
		return Agent{}, err
	}
	cfg.Provider = provider

	egressLimit, err := loadEgressLimitBytes(l)
	if err != nil {
		return Agent{}, err
	}
	cfg.EgressLimitBytes = egressLimit

	cfg.NetExclude = splitList(getString(l, "CP_NET_EXCLUDE", defaultNetExclude))
	cfg.LogLevel = getString(l, "CP_LOG_LEVEL", "info")
	cfg.LogFormat = getString(l, "CP_LOG_FORMAT", "text")

	return cfg, nil
}

// loadHubURL reads and validates CP_HUB_URL: required, http/https scheme,
// non-empty host, trailing '/' trimmed.
func loadHubURL(l LookupFunc) (string, error) {
	raw := getString(l, "CP_HUB_URL", "")
	if raw == "" {
		return "", fmt.Errorf("config: CP_HUB_URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("config: CP_HUB_URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("config: CP_HUB_URL must use http or https scheme")
	}
	if u.Host == "" {
		return "", fmt.Errorf("config: CP_HUB_URL must include a host")
	}
	return strings.TrimRight(raw, "/"), nil
}

// loadHostID reads CP_HOST_ID, defaulting to a sanitized hostname when
// unset. An explicit value must already be a valid host ID.
func loadHostID(l LookupFunc, hostname string) (string, error) {
	v, ok := l("CP_HOST_ID")
	v = strings.TrimSpace(v)
	if !ok || v == "" {
		return models.SanitizeHostID(hostname), nil
	}
	if !models.ValidHostID(v) {
		return "", fmt.Errorf("config: CP_HOST_ID %q is not a valid host id", v)
	}
	return v, nil
}

// loadProvider reads and validates CP_PROVIDER (auto|aws|oci|other,
// case-insensitive), defaulting to "auto".
func loadProvider(l LookupFunc) (string, error) {
	v := strings.ToLower(getString(l, "CP_PROVIDER", "auto"))
	switch v {
	case "auto", "aws", "oci", "other":
		return v, nil
	default:
		return "", fmt.Errorf("config: CP_PROVIDER must be one of auto|aws|oci|other, got %q", v)
	}
}

// loadEgressLimitBytes reads CP_EGRESS_LIMIT_GB (a float, GiB units) and
// converts it to bytes. Returns nil when unset (caller should use the
// provider default). 0 means unlimited.
func loadEgressLimitBytes(l LookupFunc) (*uint64, error) {
	_, ok := l("CP_EGRESS_LIMIT_GB")
	if !ok {
		return nil, nil
	}
	gb, err := getFloat(l, "CP_EGRESS_LIMIT_GB", 0)
	if err != nil {
		return nil, err
	}
	if gb < 0 {
		return nil, fmt.Errorf("config: CP_EGRESS_LIMIT_GB must be >= 0")
	}
	bytes := uint64(gb * float64(models.GiB))
	return &bytes, nil
}
