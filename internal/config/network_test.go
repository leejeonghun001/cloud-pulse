package config

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestParseListen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		listen  string
		mode    string
		addrs   []string
		port    int
		wantErr bool
	}{
		{name: "all_default", listen: ":8090", mode: "all", addrs: nil, port: 8090},
		{name: "custom_ipv4", listen: "127.0.0.1:8090", mode: "custom", addrs: []string{"127.0.0.1"}, port: 8090},
		{name: "custom_ipv6_bracketed", listen: "[fd7a::1]:8090", mode: "custom", addrs: []string{"fd7a::1"}, port: 8090},
		{name: "custom_other_port", listen: "192.168.1.5:9090", mode: "custom", addrs: []string{"192.168.1.5"}, port: 9090},
		{name: "invalid_no_colon", listen: "notanaddr", wantErr: true},
		{name: "invalid_host", listen: "not-an-ip:8090", wantErr: true},
		{name: "invalid_port", listen: "127.0.0.1:notaport", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mode, addrs, port, err := ParseListen(tt.listen)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseListen(%q): expected error, got none", tt.listen)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseListen(%q): %v", tt.listen, err)
			}
			if mode != tt.mode {
				t.Errorf("mode = %q, want %q", mode, tt.mode)
			}
			if !reflect.DeepEqual(addrs, tt.addrs) {
				t.Errorf("addrs = %v, want %v", addrs, tt.addrs)
			}
			if port != tt.port {
				t.Errorf("port = %d, want %d", port, tt.port)
			}
		})
	}
}

func TestFormatListen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mode      string
		addresses []string
		port      int
		want      []string
	}{
		{name: "all", mode: "all", port: 8090, want: []string{"[::]:8090"}},
		{name: "custom_single", mode: "custom", addresses: []string{"127.0.0.1"}, port: 8090, want: []string{"127.0.0.1:8090"}},
		{name: "custom_multi", mode: "custom", addresses: []string{"127.0.0.1", "192.168.1.5"}, port: 8090, want: []string{"127.0.0.1:8090", "192.168.1.5:8090"}},
		{name: "custom_ipv6", mode: "custom", addresses: []string{"fd7a::1"}, port: 8090, want: []string{"[fd7a::1]:8090"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatListen(tt.mode, tt.addresses, tt.port)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FormatListen(%q, %v, %d) = %v, want %v", tt.mode, tt.addresses, tt.port, got, tt.want)
			}
		})
	}
}

func TestParseListen_FormatListen_RoundTrip(t *testing.T) {
	t.Parallel()
	tests := []string{":8090", "127.0.0.1:8090", "[fd7a::1]:8090"}
	for _, listen := range tests {
		mode, addrs, port, err := ParseListen(listen)
		if err != nil {
			t.Fatalf("ParseListen(%q): %v", listen, err)
		}
		got := FormatListen(mode, addrs, port)
		if len(got) != 1 {
			t.Fatalf("FormatListen round-trip = %v, want 1 entry", got)
		}
	}
}

func TestHub_EnvNetworkConfig(t *testing.T) {
	t.Parallel()

	t.Run("all_mode_allow_all_cidrs", func(t *testing.T) {
		t.Parallel()
		h := Hub{Listen: ":8090", AllowedCIDRs: nil}
		cfg, err := h.EnvNetworkConfig()
		if err != nil {
			t.Fatalf("EnvNetworkConfig: %v", err)
		}
		if cfg.Mode != "all" || cfg.Port != 8090 {
			t.Errorf("cfg = %+v, want Mode=all Port=8090", cfg)
		}
		if len(cfg.AllowedCIDRs) != 1 || cfg.AllowedCIDRs[0] != "*" {
			t.Errorf("AllowedCIDRs = %v, want [*]", cfg.AllowedCIDRs)
		}
	})

	t.Run("custom_mode_with_cidrs", func(t *testing.T) {
		t.Parallel()
		h := Hub{
			Listen: "127.0.0.1:8090",
			AllowedCIDRs: []netip.Prefix{
				netip.MustParsePrefix("100.64.0.0/10"),
				netip.MustParsePrefix("127.0.0.0/8"),
			},
		}
		cfg, err := h.EnvNetworkConfig()
		if err != nil {
			t.Fatalf("EnvNetworkConfig: %v", err)
		}
		if cfg.Mode != "custom" || len(cfg.Addresses) != 1 || cfg.Addresses[0] != "127.0.0.1" {
			t.Errorf("cfg = %+v, want Mode=custom Addresses=[127.0.0.1]", cfg)
		}
		want := []string{"100.64.0.0/10", "127.0.0.0/8"}
		if !reflect.DeepEqual(cfg.AllowedCIDRs, want) {
			t.Errorf("AllowedCIDRs = %v, want %v", cfg.AllowedCIDRs, want)
		}
	})

	t.Run("invalid_listen_errors", func(t *testing.T) {
		t.Parallel()
		h := Hub{Listen: "garbage"}
		if _, err := h.EnvNetworkConfig(); err == nil {
			t.Fatal("expected error for invalid Listen")
		}
	})
}

func TestParseAllowedCIDRsList(t *testing.T) {
	t.Parallel()

	t.Run("star_means_allow_all", func(t *testing.T) {
		t.Parallel()
		got, err := ParseAllowedCIDRsList([]string{"*"})
		if err != nil {
			t.Fatalf("ParseAllowedCIDRsList: %v", err)
		}
		if got != nil {
			t.Errorf("got = %v, want nil", got)
		}
	})

	t.Run("valid_entries", func(t *testing.T) {
		t.Parallel()
		got, err := ParseAllowedCIDRsList([]string{"192.168.1.0/24", "10.0.0.5"})
		if err != nil {
			t.Fatalf("ParseAllowedCIDRsList: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got = %v, want 2 entries", got)
		}
	})

	t.Run("invalid_entry_errors", func(t *testing.T) {
		t.Parallel()
		if _, err := ParseAllowedCIDRsList([]string{"not-a-cidr"}); err == nil {
			t.Fatal("expected error for invalid CIDR entry")
		}
	})

	t.Run("empty_list_means_allow_all", func(t *testing.T) {
		t.Parallel()
		got, err := ParseAllowedCIDRsList(nil)
		if err != nil {
			t.Fatalf("ParseAllowedCIDRsList: %v", err)
		}
		if got != nil {
			t.Errorf("got = %v, want nil", got)
		}
	})
}
