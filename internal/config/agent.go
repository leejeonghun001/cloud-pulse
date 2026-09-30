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
	// TimeSync is "hub" (default) or "local". "hub" synchronizes sample
	// timestamps to the hub's wall clock via HubClock; "local" uses the
	// agent's own clock unmodified.
	TimeSync string
	// SendJitter is the maximum random delay inserted between collecting
	// a sample and sending it, to spread simultaneous sends across a
	// fleet. 0 (default) disables jitter. Always <= Interval/2.
	SendJitter time.Duration
	// Docker is the resolved CP_DOCKER setting: "off", "auto" (probe the
	// platform default Docker socket), or an explicit socket path/URL.
	// See loadDocker for the exact parsing/normalization rule.
	Docker string
	// RemoteUpdate is the resolved CP_REMOTE_UPDATE setting: true opts
	// this agent into the hub's remote batch-update feature (SPEC-v0.6
	// §2). Default false — the hub can never trigger an update on an
	// agent that hasn't explicitly opted in, and the hub itself has no
	// setting that can turn this on remotely.
	RemoteUpdate bool
	// CloudMetadata is "auto" (probe DMI/IMDS/OCI metadata for
	// HostInfo.CloudInstanceID, each with its own short timeout) or
	// "off" (skip detection entirely; CloudInstanceID is always empty).
	// See SPEC-v0.6 §1.
	CloudMetadata string
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
// (comma-separated globs), CP_LOG_LEVEL, CP_LOG_FORMAT, CP_TIME_SYNC
// (hub|local, default hub), CP_SEND_JITTER (default 0, must be
// <= CP_INTERVAL/2), CP_DOCKER (auto|off|<socket path/URL>, default
// auto), CP_REMOTE_UPDATE (off|on, default off), CP_CLOUD_METADATA
// (auto|off, default auto).
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

	timeSync, err := loadTimeSync(l)
	if err != nil {
		return Agent{}, err
	}
	cfg.TimeSync = timeSync

	sendJitter, err := loadSendJitter(l, interval)
	if err != nil {
		return Agent{}, err
	}
	cfg.SendJitter = sendJitter

	cfg.Docker = loadDocker(l)

	remoteUpdate, err := loadRemoteUpdate(l)
	if err != nil {
		return Agent{}, err
	}
	cfg.RemoteUpdate = remoteUpdate

	cloudMetadata, err := loadCloudMetadata(l)
	if err != nil {
		return Agent{}, err
	}
	cfg.CloudMetadata = cloudMetadata

	return cfg, nil
}

// loadRemoteUpdate reads and validates CP_REMOTE_UPDATE (off|on,
// case-insensitive), defaulting to false ("off"). Deliberately a
// restricted off|on vocabulary (not getBool's broader
// true/false/1/0/t/f) to match SPEC-v0.6 §2's exact spelling and read
// unambiguously in an env file next to CP_DOCKER's similar auto|off
// style.
func loadRemoteUpdate(l LookupFunc) (bool, error) {
	v := strings.ToLower(getString(l, "CP_REMOTE_UPDATE", "off"))
	switch v {
	case "off":
		return false, nil
	case "on":
		return true, nil
	default:
		return false, fmt.Errorf("config: CP_REMOTE_UPDATE must be one of off|on, got %q", v)
	}
}

// loadCloudMetadata reads and validates CP_CLOUD_METADATA (auto|off,
// case-insensitive), defaulting to "auto".
func loadCloudMetadata(l LookupFunc) (string, error) {
	v := strings.ToLower(getString(l, "CP_CLOUD_METADATA", "auto"))
	switch v {
	case "auto", "off":
		return v, nil
	default:
		return "", fmt.Errorf("config: CP_CLOUD_METADATA must be one of auto|off, got %q", v)
	}
}

// loadDocker reads CP_DOCKER, defaulting to "auto". Recognized special
// values are "auto" (probe the platform default Docker socket) and
// "off" (disable Docker collection entirely); any other value is
// treated as an explicit socket path or "unix://" URL, passed through
// unmodified for internal/agent's dockerClient to resolve. There is
// deliberately no validation here beyond defaulting — an invalid path
// surfaces as DockerStatusUnavailable/DockerStatusError at collection
// time (see models.DockerInfo), not a config load error, since a
// typo'd or since-removed socket path shouldn't prevent the agent from
// starting and reporting every other metric.
func loadDocker(l LookupFunc) string {
	v := strings.TrimSpace(getString(l, "CP_DOCKER", "auto"))
	if v == "" {
		return "auto"
	}
	return v
}

// loadTimeSync reads and validates CP_TIME_SYNC (hub|local,
// case-insensitive), defaulting to "hub".
func loadTimeSync(l LookupFunc) (string, error) {
	v := strings.ToLower(getString(l, "CP_TIME_SYNC", "hub"))
	switch v {
	case "hub", "local":
		return v, nil
	default:
		return "", fmt.Errorf("config: CP_TIME_SYNC must be one of hub|local, got %q", v)
	}
}

// loadSendJitter reads CP_SEND_JITTER (a time.Duration string), defaulting
// to 0 (disabled). It must be >= 0 and <= interval/2.
func loadSendJitter(l LookupFunc, interval time.Duration) (time.Duration, error) {
	jitter, err := getDuration(l, "CP_SEND_JITTER", 0, 0)
	if err != nil {
		return 0, err
	}
	if max := interval / 2; jitter > max {
		return 0, fmt.Errorf("config: CP_SEND_JITTER (%s) must be <= half the interval (%s)", jitter, max)
	}
	return jitter, nil
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
