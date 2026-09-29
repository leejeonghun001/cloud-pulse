package config

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// defaultListenPort is used by ParseListen/EnvNetworkConfig when a
// listen address string omits its port (should not occur given
// CP_LISTEN's own default of ":8090", but handled defensively).
const defaultListenPort = 8090

// ParseListen parses a CP_LISTEN-style address string (as accepted by
// net.Listen("tcp", addr): ":8090", "127.0.0.1:8090", or
// "[fd7a::1]:8090") into a models.NetworkConfig's Mode/Addresses/Port
// shape: an empty host (":8090") is Mode "all"; any other host is Mode
// "custom" with that single address. It does not touch AllowedCIDRs.
func ParseListen(listen string) (mode string, addresses []string, port int, err error) {
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", nil, 0, fmt.Errorf("config: parse listen address %q: %w", listen, err)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil {
		return "", nil, 0, fmt.Errorf("config: parse listen address %q: invalid port: %w", listen, err)
	}
	if host == "" {
		return "all", nil, port, nil
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "", nil, 0, fmt.Errorf("config: parse listen address %q: invalid host %q: %w", listen, host, err)
	}
	return "custom", []string{addr.String()}, port, nil
}

// FormatListen is ParseListen's inverse: it renders a Mode/Addresses/Port
// triple back into net.Listen-style address strings, one per listener
// Apply should open. Mode "all" yields a single "[::]:port" dual-stack
// entry; Mode "custom" yields one "ip:port" (bracketed for IPv6) entry
// per address.
func FormatListen(mode string, addresses []string, port int) []string {
	if mode == "all" {
		return []string{fmt.Sprintf("[::]:%d", port)}
	}
	out := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		out = append(out, net.JoinHostPort(addr, strconv.Itoa(port)))
	}
	return out
}

// EnvNetworkConfig derives a models.NetworkConfig from this Hub config's
// Listen/AllowedCIDRs fields (the "env" source in SPEC-v0.4 §2's
// GET /api/v1/settings/network), for use when no "network_config"
// setting is stored yet.
func (h Hub) EnvNetworkConfig() (models.NetworkConfig, error) {
	mode, addresses, port, err := ParseListen(h.Listen)
	if err != nil {
		return models.NetworkConfig{}, err
	}
	return models.NetworkConfig{
		Mode:         mode,
		Addresses:    addresses,
		Port:         port,
		AllowedCIDRs: formatAllowedCIDRsEnv(h.AllowedCIDRs),
	}, nil
}

// formatAllowedCIDRsEnv renders cidrs (nil = allow all) as the
// AllowedCIDRs field of an env-sourced models.NetworkConfig: ["*"] for
// nil, otherwise each prefix's string form.
func formatAllowedCIDRsEnv(cidrs []netip.Prefix) []string {
	if cidrs == nil {
		return []string{"*"}
	}
	out := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, c.String())
	}
	return out
}

// ParseAllowedCIDRsList is the exported form of parseAllowedCIDRs, for
// callers outside this package (the hub's network settings handlers)
// that need to validate/parse a CIDR list supplied via the API using
// the exact same rules CP_ALLOWED_CIDRS itself uses ("*" alone means
// allow all, entries may be a bare IP or a CIDR).
func ParseAllowedCIDRsList(entries []string) ([]netip.Prefix, error) {
	return parseAllowedCIDRs(strings.Join(entries, ","))
}
