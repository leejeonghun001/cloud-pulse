package models

import (
	"fmt"
	"strings"
)

// Provider identifies the cloud provider a host runs on, used to select a
// default egress limit and display metadata.
type Provider string

// Supported providers.
const (
	ProviderAWS   Provider = "aws"
	ProviderOCI   Provider = "oci"
	ProviderOther Provider = "other"
)

// Byte size constants used across the domain (binary/IEC units).
const (
	GiB = 1 << 30
	TiB = 1 << 40
)

// Free egress allowances used to derive DefaultEgressLimit.
const (
	AWSFreeEgressBytes = 100 * GiB
	OCIFreeEgressBytes = 10 * TiB
)

// DefaultEgressLimit returns the free-tier egress allowance in bytes for the
// given provider: 100 GiB for AWS, 10 TiB for OCI, and 0 (unlimited) for any
// other provider.
func DefaultEgressLimit(p Provider) uint64 {
	switch p {
	case ProviderAWS:
		return AWSFreeEgressBytes
	case ProviderOCI:
		return OCIFreeEgressBytes
	default:
		return 0
	}
}

// ParseProvider parses s (case-insensitive, surrounding whitespace trimmed)
// into a Provider. Recognized values are "aws", "oci", and "other".
func ParseProvider(s string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "aws":
		return ProviderAWS, nil
	case "oci":
		return ProviderOCI, nil
	case "other":
		return ProviderOther, nil
	default:
		return "", fmt.Errorf("models: invalid provider %q", s)
	}
}

// ValidHostID reports whether id is a valid host identifier: 1 to 128
// characters drawn from [A-Za-z0-9._-].
func ValidHostID(id string) bool {
	n := len(id)
	if n < 1 || n > 128 {
		return false
	}
	for _, r := range id {
		if !isHostIDRune(r) {
			return false
		}
	}
	return true
}

func isHostIDRune(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z':
		return true
	case r >= 'a' && r <= 'z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '.' || r == '_' || r == '-':
		return true
	default:
		return false
	}
}

// SanitizeHostID converts s into a valid host ID: every rune outside
// [A-Za-z0-9._-] is replaced with '-', runs of repeated '-' are collapsed to
// one, leading/trailing '-' and '.' are trimmed, and the result is truncated
// to 128 characters. If the result is empty, "host" is returned.
func SanitizeHostID(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	prevDash := false
	for _, r := range s {
		if isHostIDRune(r) {
			b.WriteRune(r)
			prevDash = r == '-'
			continue
		}
		if prevDash {
			continue
		}
		b.WriteByte('-')
		prevDash = true
	}

	out := strings.Trim(b.String(), "-.")
	if len(out) > 128 {
		out = out[:128]
		out = strings.Trim(out, "-.")
	}
	if out == "" {
		return "host"
	}
	return out
}

// HostInfo describes a monitored host's static/slow-changing identity and
// metadata.
type HostInfo struct {
	ID              string   `json:"id"`
	Hostname        string   `json:"hostname"`
	OS              string   `json:"os"`
	Platform        string   `json:"platform"`
	PlatformVersion string   `json:"platform_version"`
	KernelVersion   string   `json:"kernel_version"`
	Arch            string   `json:"arch"`
	CPUModel        string   `json:"cpu_model"`
	CPUCores        int      `json:"cpu_cores"`
	BootTime        int64    `json:"boot_time"`
	Provider        Provider `json:"provider"`
	// EgressLimitBytes is the monthly egress limit in bytes; 0 means
	// unlimited.
	EgressLimitBytes uint64 `json:"egress_limit_bytes"`
	AgentVersion     string `json:"agent_version"`
}

// HostStatus is the liveness state of a host as seen by the hub.
type HostStatus string

// Host liveness states.
const (
	HostUp   HostStatus = "up"
	HostDown HostStatus = "down"
)

// HostRecord is a host's stored identity plus its last-seen time and latest
// sample.
type HostRecord struct {
	Info     HostInfo `json:"info"`
	LastSeen int64    `json:"last_seen"`
	Latest   *Sample  `json:"latest,omitempty"`
}

// HostSummary is the read-model returned by the hub's host listing/detail
// endpoints: identity, liveness, latest sample, and current egress usage.
type HostSummary struct {
	Host     HostInfo    `json:"host"`
	Status   HostStatus  `json:"status"`
	LastSeen int64       `json:"last_seen"`
	Latest   *Sample     `json:"latest,omitempty"`
	Egress   EgressUsage `json:"egress"`
	// Update describes an available agent update for this host, or nil
	// when no update is known or the host's agent version doesn't
	// parse.
	Update *AgentUpdate `json:"update,omitempty"`
}
