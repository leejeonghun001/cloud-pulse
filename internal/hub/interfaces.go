package hub

import (
	"net"
	"net/netip"
	"sort"
	"strings"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// tailscaleCGNAT and tailscaleULA are Tailscale's own address ranges,
// used both to classify an interface as "tailscale" by address (in case
// its name doesn't start with "tailscale") and as the SuggestedCIDR for
// any address that falls inside them.
var (
	tailscaleCGNAT = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleULA   = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// virtualNamePrefixes lists interface-name globs (prefix match, "*"
// stripped) classified as "virtual": container/VPN/tunnel interfaces
// that are not the host's "real" physical or Tailscale adapters. Mirrors
// CP_NET_EXCLUDE's default list (minus tailscale*, which gets its own
// "tailscale" kind, and minus lo/lo0, which get "loopback").
var virtualNamePrefixes = []string{
	"docker", "veth", "br-", "virbr", "cni", "flannel", "cali",
	"kube", "vxlan", "wg", "zt", "tun", "utun",
}

// defaultNetInterfaces is the production net.Interfaces call, given a
// name so networkroutes.go's netInterfacesFunc var can reference it
// directly without importing "net" solely for that purpose.
func defaultNetInterfaces() ([]net.Interface, error) {
	return net.Interfaces()
}

// listInterfaces returns every network interface on the host, classified
// per SPEC-v0.4 §2, sorted by name. It never returns a nil slice on
// success (empty input yields an empty, non-nil slice). netInterfaces is
// injectable for tests; production code always passes net.Interfaces.
func listInterfaces(netInterfaces func() ([]net.Interface, error)) ([]models.NetInterface, error) {
	ifaces, err := netInterfaces()
	if err != nil {
		return nil, err
	}

	out := make([]models.NetInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		up := iface.Flags&net.FlagUp != 0
		loopback := iface.Flags&net.FlagLoopback != 0

		addrs, err := iface.Addrs()
		var netAddrs []models.NetAddress
		if err == nil {
			netAddrs = classifyAddresses(addrs, loopback)
		}

		out = append(out, models.NetInterface{
			Name:      iface.Name,
			Index:     iface.Index,
			Up:        up,
			Loopback:  loopback,
			Kind:      classifyInterfaceKind(iface.Name, loopback, netAddrs),
			Addresses: netAddrs,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// classifyAddresses converts iface.Addrs()'s []net.Addr into
// []models.NetAddress, skipping any entry that isn't an *net.IPNet
// (shouldn't occur in practice; net.Interface.Addrs always returns
// *net.IPNet on every supported platform, but this is defensive).
func classifyAddresses(addrs []net.Addr, ifaceLoopback bool) []models.NetAddress {
	out := make([]models.NetAddress, 0, len(addrs))
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		ones, _ := ipNet.Mask.Size()

		family := "ipv4"
		if addr.Is6() {
			family = "ipv6"
		}

		scope := "global"
		switch {
		case ifaceLoopback || addr.IsLoopback():
			scope = "loopback"
		case addr.IsLinkLocalUnicast():
			scope = "link-local"
		}

		network := netip.PrefixFrom(addr, ones).Masked().String()

		out = append(out, models.NetAddress{
			IP:            addr.String(),
			PrefixLen:     ones,
			Family:        family,
			Scope:         scope,
			Network:       network,
			SuggestedCIDR: suggestedCIDR(addr, scope, network),
		})
	}
	return out
}

// suggestedCIDR implements SPEC-v0.4 §2's SuggestedCIDR rule: a broader
// allowlist suggestion appropriate for addr's kind.
func suggestedCIDR(addr netip.Addr, scope, network string) string {
	switch {
	case scope == "loopback":
		if addr.Is4() {
			return "127.0.0.0/8"
		}
		return "::1/128"
	case tailscaleCGNAT.Contains(addr):
		return tailscaleCGNAT.String()
	case tailscaleULA.Contains(addr):
		return tailscaleULA.String()
	default:
		return network
	}
}

// classifyInterfaceKind implements SPEC-v0.4 §2's Kind classification:
// loopback (by flag), tailscale (by name prefix or address range),
// virtual (known container/VPN/tunnel name prefixes), else physical.
func classifyInterfaceKind(name string, loopback bool, addrs []models.NetAddress) string {
	if loopback {
		return "loopback"
	}

	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "tailscale") {
		return "tailscale"
	}
	for _, a := range addrs {
		if a.SuggestedCIDR == tailscaleCGNAT.String() || a.SuggestedCIDR == tailscaleULA.String() {
			return "tailscale"
		}
	}

	for _, prefix := range virtualNamePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return "virtual"
		}
	}

	return "physical"
}
