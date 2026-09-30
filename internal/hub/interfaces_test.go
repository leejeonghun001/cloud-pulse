package hub

import (
	"errors"
	"net"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func mustIPNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", cidr, err)
	}
	ipNet.IP = ip
	return ipNet
}

func TestClassifyAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cidr          string
		ifaceLoopback bool
		wantFamily    string
		wantScope     string
		wantSuggested string
	}{
		{name: "ipv4_loopback", cidr: "127.0.0.1/8", ifaceLoopback: true, wantFamily: "ipv4", wantScope: "loopback", wantSuggested: "127.0.0.0/8"},
		{name: "ipv6_loopback", cidr: "::1/128", ifaceLoopback: true, wantFamily: "ipv6", wantScope: "loopback", wantSuggested: "::1/128"},
		{name: "tailscale_cgnat", cidr: "100.64.0.1/32", wantFamily: "ipv4", wantScope: "global", wantSuggested: "100.64.0.0/10"},
		{name: "tailscale_ula", cidr: "fd7a:115c:a1e0::1/128", wantFamily: "ipv6", wantScope: "global", wantSuggested: "fd7a:115c:a1e0::/48"},
		{name: "lan_ipv4", cidr: "198.51.100.2/24", wantFamily: "ipv4", wantScope: "global", wantSuggested: "198.51.100.0/24"},
		{name: "link_local_ipv4", cidr: "169.254.1.5/16", wantFamily: "ipv4", wantScope: "link-local", wantSuggested: "169.254.0.0/16"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyAddresses([]net.Addr{mustIPNet(t, tt.cidr)}, tt.ifaceLoopback)
			if len(got) != 1 {
				t.Fatalf("classifyAddresses(%q) = %v, want 1 entry", tt.cidr, got)
			}
			a := got[0]
			if a.Family != tt.wantFamily {
				t.Errorf("Family = %q, want %q", a.Family, tt.wantFamily)
			}
			if a.Scope != tt.wantScope {
				t.Errorf("Scope = %q, want %q", a.Scope, tt.wantScope)
			}
			if a.SuggestedCIDR != tt.wantSuggested {
				t.Errorf("SuggestedCIDR = %q, want %q", a.SuggestedCIDR, tt.wantSuggested)
			}
		})
	}
}

func TestClassifyInterfaceKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		iface    string
		loopback bool
		addrs    []models.NetAddress
		want     string
	}{
		{name: "loopback_by_flag", iface: "lo", loopback: true, want: "loopback"},
		{name: "tailscale_by_name", iface: "tailscale0", want: "tailscale"},
		{name: "tailscale_by_address", iface: "wg-ts", addrs: []models.NetAddress{{SuggestedCIDR: "100.64.0.0/10"}}, want: "tailscale"},
		{name: "docker_virtual", iface: "docker0", want: "virtual"},
		{name: "veth_virtual", iface: "veth1234abcd", want: "virtual"},
		{name: "wireguard_virtual", iface: "wg0", want: "virtual"},
		{name: "physical_eth", iface: "eth0", want: "physical"},
		{name: "physical_wlan", iface: "wlan0", want: "physical"},
		{name: "physical_unknown", iface: "enp3s0", want: "physical"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyInterfaceKind(tt.iface, tt.loopback, tt.addrs)
			if got != tt.want {
				t.Errorf("classifyInterfaceKind(%q, %v, %v) = %q, want %q", tt.iface, tt.loopback, tt.addrs, got, tt.want)
			}
		})
	}
}

func TestListInterfaces_PropagatesError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	_, err := listInterfaces(func() ([]net.Interface, error) { return nil, wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("listInterfaces error = %v, want %v", err, wantErr)
	}
}

func TestListInterfaces_EmptyIsNonNil(t *testing.T) {
	t.Parallel()
	got, err := listInterfaces(func() ([]net.Interface, error) { return nil, nil })
	if err != nil {
		t.Fatalf("listInterfaces: %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("got = %v, want empty", got)
	}
}

func TestListInterfaces_SortedByName(t *testing.T) {
	t.Parallel()
	got, err := listInterfaces(func() ([]net.Interface, error) {
		return []net.Interface{
			{Name: "wlan0", Index: 2},
			{Name: "eth0", Index: 1},
			{Name: "lo", Index: 0, Flags: net.FlagLoopback},
		}, nil
	})
	if err != nil {
		t.Fatalf("listInterfaces: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	wantOrder := []string{"eth0", "lo", "wlan0"}
	for i, name := range wantOrder {
		if got[i].Name != name {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, name)
		}
	}
	if got[1].Kind != "loopback" {
		t.Errorf("lo Kind = %q, want loopback", got[1].Kind)
	}
}

// TestListInterfaces_RealCall smoke-tests the production path (real
// net.Interfaces) doesn't panic or error on this machine, without
// asserting on the exact adapter set (which varies by CI runner/OS).
func TestListInterfaces_RealCall(t *testing.T) {
	t.Parallel()
	got, err := listInterfaces(net.Interfaces)
	if err != nil {
		t.Fatalf("listInterfaces(net.Interfaces): %v", err)
	}
	if got == nil {
		t.Fatal("got nil, want non-nil slice")
	}
}
