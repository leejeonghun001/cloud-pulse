package config

import (
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func validAgentEnv() map[string]string {
	return map[string]string{
		"CP_HUB_URL":     "https://hub.example.com",
		"CP_AGENT_TOKEN": "0123456789abcdef",
	}
}

func TestLoadAgent_Valid(t *testing.T) {
	t.Parallel()

	cfg, err := LoadAgent(mapLookup(validAgentEnv()), "my-host")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cfg.HubURL != "https://hub.example.com" {
		t.Errorf("HubURL = %q", cfg.HubURL)
	}
	if cfg.Token != "0123456789abcdef" {
		t.Errorf("Token = %q", cfg.Token)
	}
	if cfg.HostID != models.SanitizeHostID("my-host") {
		t.Errorf("HostID = %q, want %q", cfg.HostID, models.SanitizeHostID("my-host"))
	}
	if cfg.Interval != 15*time.Second {
		t.Errorf("Interval = %v, want 15s", cfg.Interval)
	}
	if cfg.Provider != "auto" {
		t.Errorf("Provider = %q, want auto", cfg.Provider)
	}
	if cfg.EgressLimitBytes != nil {
		t.Errorf("EgressLimitBytes = %v, want nil", cfg.EgressLimitBytes)
	}
	if len(cfg.NetExclude) == 0 {
		t.Error("NetExclude should have default entries")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text", cfg.LogFormat)
	}
}

func TestLoadAgent_HubURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		url     string
		wantErr bool
		wantURL string
	}{
		{"missing", "", true, ""},
		{"http_ok", "http://hub.example.com", false, "http://hub.example.com"},
		{"https_ok", "https://hub.example.com", false, "https://hub.example.com"},
		{"trailing_slash_trimmed", "https://hub.example.com/", false, "https://hub.example.com"},
		{"multiple_trailing_slashes_trimmed", "https://hub.example.com//", false, "https://hub.example.com"},
		{"invalid_scheme", "ftp://hub.example.com", true, ""},
		{"no_host", "https://", true, ""},
		{"unparsable", "http://[::1", true, ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := validAgentEnv()
			if tc.url == "" {
				delete(env, "CP_HUB_URL")
			} else {
				env["CP_HUB_URL"] = tc.url
			}
			cfg, err := LoadAgent(mapLookup(env), "host")
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadAgent err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if !strings.HasPrefix(err.Error(), "config:") {
					t.Errorf("error %q must be prefixed with config:", err.Error())
				}
				return
			}
			if cfg.HubURL != tc.wantURL {
				t.Errorf("HubURL = %q, want %q", cfg.HubURL, tc.wantURL)
			}
		})
	}
}

func TestLoadAgent_Token(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"missing", "", true},
		{"too_short", "short", true},
		{"exactly_15_chars", "abcdefghijklmno", true},
		{"exactly_16_chars", "abcdefghijklmnop", false},
		{"long", "abcdefghijklmnopqrstuvwxyz", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := validAgentEnv()
			if tc.token == "" {
				delete(env, "CP_AGENT_TOKEN")
			} else {
				env["CP_AGENT_TOKEN"] = tc.token
			}
			_, err := LoadAgent(mapLookup(env), "host")
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadAgent err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "config:") {
				t.Errorf("error %q must be prefixed with config:", err.Error())
			}
		})
	}
}

func TestLoadAgent_HostID(t *testing.T) {
	t.Parallel()

	t.Run("default_sanitizes_hostname", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadAgent(mapLookup(validAgentEnv()), "My Host!.local")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		want := models.SanitizeHostID("My Host!.local")
		if cfg.HostID != want {
			t.Errorf("HostID = %q, want %q", cfg.HostID, want)
		}
	})

	t.Run("explicit_valid_id_used_as_is", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_HOST_ID"] = "custom-host.01"
		cfg, err := LoadAgent(mapLookup(env), "ignored")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		if cfg.HostID != "custom-host.01" {
			t.Errorf("HostID = %q, want custom-host.01", cfg.HostID)
		}
	})

	t.Run("explicit_invalid_id_errors", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_HOST_ID"] = "invalid host id!"
		_, err := LoadAgent(mapLookup(env), "host")
		if err == nil {
			t.Fatal("expected error for invalid CP_HOST_ID")
		}
		if !strings.HasPrefix(err.Error(), "config:") {
			t.Errorf("error %q must be prefixed with config:", err.Error())
		}
	})
}

func TestLoadAgent_Interval(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		interval string
		want     time.Duration
		wantErr  bool
	}{
		{"default", "", 15 * time.Second, false},
		{"valid_above_min", "30s", 30 * time.Second, false},
		{"exactly_min", "5s", 5 * time.Second, false},
		{"below_min", "4s", 0, true},
		{"unparsable", "banana", 0, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := validAgentEnv()
			if tc.interval != "" {
				env["CP_INTERVAL"] = tc.interval
			}
			cfg, err := LoadAgent(mapLookup(env), "host")
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadAgent err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if cfg.Interval != tc.want {
				t.Errorf("Interval = %v, want %v", cfg.Interval, tc.want)
			}
		})
	}
}

func TestLoadAgent_Provider(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider string
		want     string
		wantErr  bool
	}{
		{"default", "", "auto", false},
		{"auto", "auto", "auto", false},
		{"aws", "aws", "aws", false},
		{"oci", "oci", "oci", false},
		{"other", "other", "other", false},
		{"case_insensitive", "AWS", "aws", false},
		{"invalid", "gcp", "", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := validAgentEnv()
			if tc.provider != "" {
				env["CP_PROVIDER"] = tc.provider
			}
			cfg, err := LoadAgent(mapLookup(env), "host")
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadAgent err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if cfg.Provider != tc.want {
				t.Errorf("Provider = %q, want %q", cfg.Provider, tc.want)
			}
		})
	}
}

func TestLoadAgent_EgressLimit(t *testing.T) {
	t.Parallel()

	t.Run("unset_is_nil", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadAgent(mapLookup(validAgentEnv()), "host")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		if cfg.EgressLimitBytes != nil {
			t.Errorf("EgressLimitBytes = %v, want nil", cfg.EgressLimitBytes)
		}
	})

	t.Run("zero_means_unlimited_but_explicit", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_EGRESS_LIMIT_GB"] = "0"
		cfg, err := LoadAgent(mapLookup(env), "host")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		if cfg.EgressLimitBytes == nil || *cfg.EgressLimitBytes != 0 {
			t.Errorf("EgressLimitBytes = %v, want pointer to 0", cfg.EgressLimitBytes)
		}
	})

	t.Run("converts_gib_to_bytes", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_EGRESS_LIMIT_GB"] = "2"
		cfg, err := LoadAgent(mapLookup(env), "host")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		want := uint64(2 * models.GiB)
		if cfg.EgressLimitBytes == nil || *cfg.EgressLimitBytes != want {
			t.Errorf("EgressLimitBytes = %v, want pointer to %d", cfg.EgressLimitBytes, want)
		}
	})

	t.Run("fractional_gib", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_EGRESS_LIMIT_GB"] = "0.5"
		cfg, err := LoadAgent(mapLookup(env), "host")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		want := uint64(0.5 * models.GiB)
		if cfg.EgressLimitBytes == nil || *cfg.EgressLimitBytes != want {
			t.Errorf("EgressLimitBytes = %v, want pointer to %d", cfg.EgressLimitBytes, want)
		}
	})

	t.Run("negative_errors", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_EGRESS_LIMIT_GB"] = "-1"
		_, err := LoadAgent(mapLookup(env), "host")
		if err == nil {
			t.Fatal("expected error for negative CP_EGRESS_LIMIT_GB")
		}
		if !strings.HasPrefix(err.Error(), "config:") {
			t.Errorf("error %q must be prefixed with config:", err.Error())
		}
	})

	t.Run("unparsable_errors", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_EGRESS_LIMIT_GB"] = "abc"
		_, err := LoadAgent(mapLookup(env), "host")
		if err == nil {
			t.Fatal("expected error for unparsable CP_EGRESS_LIMIT_GB")
		}
	})
}

func TestLoadAgent_NetExclude(t *testing.T) {
	t.Parallel()

	t.Run("default_includes_common_interfaces", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadAgent(mapLookup(validAgentEnv()), "host")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		want := map[string]bool{"lo": true, "docker*": true, "veth*": true}
		got := map[string]bool{}
		for _, g := range cfg.NetExclude {
			got[g] = true
		}
		for w := range want {
			if !got[w] {
				t.Errorf("NetExclude missing default glob %q, got %v", w, cfg.NetExclude)
			}
		}
	})

	t.Run("explicit_overrides_default", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_NET_EXCLUDE"] = "eth1,wlan*"
		cfg, err := LoadAgent(mapLookup(env), "host")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		if len(cfg.NetExclude) != 2 || cfg.NetExclude[0] != "eth1" || cfg.NetExclude[1] != "wlan*" {
			t.Errorf("NetExclude = %v, want [eth1 wlan*]", cfg.NetExclude)
		}
	})
}

func TestLoadAgent_TimeSync(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		timeSync string
		want     string
		wantErr  bool
	}{
		{"default", "", "hub", false},
		{"hub", "hub", "hub", false},
		{"local", "local", "local", false},
		{"case_insensitive", "LOCAL", "local", false},
		{"invalid", "ntp", "", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := validAgentEnv()
			if tc.timeSync != "" {
				env["CP_TIME_SYNC"] = tc.timeSync
			}
			cfg, err := LoadAgent(mapLookup(env), "host")
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadAgent err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if !strings.HasPrefix(err.Error(), "config:") {
					t.Errorf("error %q must be prefixed with config:", err.Error())
				}
				return
			}
			if cfg.TimeSync != tc.want {
				t.Errorf("TimeSync = %q, want %q", cfg.TimeSync, tc.want)
			}
		})
	}
}

func TestLoadAgent_SendJitter(t *testing.T) {
	t.Parallel()

	t.Run("default_zero", func(t *testing.T) {
		t.Parallel()
		cfg, err := LoadAgent(mapLookup(validAgentEnv()), "host")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		if cfg.SendJitter != 0 {
			t.Errorf("SendJitter = %v, want 0", cfg.SendJitter)
		}
	})

	t.Run("within_half_interval_ok", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_INTERVAL"] = "30s"
		env["CP_SEND_JITTER"] = "10s"
		cfg, err := LoadAgent(mapLookup(env), "host")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		if cfg.SendJitter != 10*time.Second {
			t.Errorf("SendJitter = %v, want 10s", cfg.SendJitter)
		}
	})

	t.Run("exactly_half_interval_ok", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_INTERVAL"] = "30s"
		env["CP_SEND_JITTER"] = "15s"
		cfg, err := LoadAgent(mapLookup(env), "host")
		if err != nil {
			t.Fatalf("LoadAgent: %v", err)
		}
		if cfg.SendJitter != 15*time.Second {
			t.Errorf("SendJitter = %v, want 15s", cfg.SendJitter)
		}
	})

	t.Run("above_half_interval_errors", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_INTERVAL"] = "30s"
		env["CP_SEND_JITTER"] = "16s"
		_, err := LoadAgent(mapLookup(env), "host")
		if err == nil {
			t.Fatal("expected error for CP_SEND_JITTER above half the interval")
		}
		if !strings.HasPrefix(err.Error(), "config:") {
			t.Errorf("error %q must be prefixed with config:", err.Error())
		}
	})

	t.Run("negative_errors", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_SEND_JITTER"] = "-1s"
		_, err := LoadAgent(mapLookup(env), "host")
		if err == nil {
			t.Fatal("expected error for negative CP_SEND_JITTER")
		}
	})

	t.Run("unparsable_errors", func(t *testing.T) {
		t.Parallel()
		env := validAgentEnv()
		env["CP_SEND_JITTER"] = "banana"
		_, err := LoadAgent(mapLookup(env), "host")
		if err == nil {
			t.Fatal("expected error for unparsable CP_SEND_JITTER")
		}
	})
}

func TestLoadAgent_Docker(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value string // "" means CP_DOCKER unset
		want  string
	}{
		{"unset_defaults_to_auto", "", "auto"},
		{"explicit_auto", "auto", "auto"},
		{"off", "off", "off"},
		{"explicit_socket_path", "/tmp/podman.sock", "/tmp/podman.sock"},
		{"unix_url", "unix:///tmp/podman.sock", "unix:///tmp/podman.sock"},
		{"empty_string_defaults_to_auto", "   ", "auto"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := validAgentEnv()
			if tc.value != "" {
				env["CP_DOCKER"] = tc.value
			}
			cfg, err := LoadAgent(mapLookup(env), "host")
			if err != nil {
				t.Fatalf("LoadAgent: %v", err)
			}
			if cfg.Docker != tc.want {
				t.Errorf("Docker = %q, want %q", cfg.Docker, tc.want)
			}
		})
	}
}

func TestLoadAgent_LogSettings(t *testing.T) {
	t.Parallel()

	env := validAgentEnv()
	env["CP_LOG_LEVEL"] = "debug"
	env["CP_LOG_FORMAT"] = "json"
	cfg, err := LoadAgent(mapLookup(env), "host")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want json", cfg.LogFormat)
	}
}

func TestDefaultAgentNetExclude(t *testing.T) {
	t.Parallel()

	got := DefaultAgentNetExclude()
	if len(got) == 0 {
		t.Fatal("DefaultAgentNetExclude() returned empty slice")
	}
	want := map[string]bool{"lo": true, "docker*": true, "tailscale*": true}
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for w := range want {
		if !set[w] {
			t.Errorf("DefaultAgentNetExclude() missing %q, got %v", w, got)
		}
	}
}
