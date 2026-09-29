package models

import (
	"encoding/json"
	"testing"
)

// TestSettingsDTOs_JSONRoundTrip verifies the JSON tags on the settings
// DTOs match the contract in SPEC-v0.2.md, including that Hosts encodes
// as [] (not null) when empty and that nil limit pointers encode as null.
func TestSettingsDTOs_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("host_limits_view", func(t *testing.T) {
		t.Parallel()
		v := HostLimitsView{
			HostID:                     "h1",
			Hostname:                   "web-1",
			Provider:                   ProviderAWS,
			AgentEgressLimitBytes:      100 * GiB,
			EgressLimitBytes:           nil,
			IngressLimitBytes:          uint64Ptr(5 * GiB),
			EffectiveEgressLimitBytes:  100 * GiB,
			EffectiveIngressLimitBytes: 5 * GiB,
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if m["egress_limit_bytes"] != nil {
			t.Errorf(`"egress_limit_bytes" = %v, want null`, m["egress_limit_bytes"])
		}
		if _, ok := m["ingress_limit_bytes"]; !ok {
			t.Error(`"ingress_limit_bytes" key missing`)
		}
		wantKeys := []string{
			"host_id", "hostname", "provider", "agent_egress_limit_bytes",
			"egress_limit_bytes", "ingress_limit_bytes",
			"effective_egress_limit_bytes", "effective_ingress_limit_bytes",
		}
		for _, k := range wantKeys {
			if _, ok := m[k]; !ok {
				t.Errorf("missing JSON key %q", k)
			}
		}
	})

	t.Run("settings_view_hosts_empty_not_null", func(t *testing.T) {
		t.Parallel()
		v := SettingsView{
			Version:              "0.2.0",
			AllowedCIDRs:         []string{"*"},
			OfflineAfterSeconds:  60,
			CloudIntervalSeconds: 900,
			UIAuthEnabled:        false,
			AgentTokenHint:       "abcd…wxyz",
			AlertWebhookURL:      "",
			AlertWebhookSource:   "none",
			Hosts:                []HostLimitsView{},
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		hosts, ok := m["hosts"].([]any)
		if !ok {
			t.Fatalf(`"hosts" = %#v, want a JSON array`, m["hosts"])
		}
		if len(hosts) != 0 {
			t.Errorf("hosts length = %d, want 0", len(hosts))
		}
	})

	t.Run("agent_token_view", func(t *testing.T) {
		t.Parallel()
		v := AgentTokenView{
			AgentToken:     "not-a-real-secret-value",
			InstallCommand: "curl -fsSL https://example.invalid/install-agent.sh | sudo bash",
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var got AgentTokenView
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got != v {
			t.Errorf("round-trip = %+v, want %+v", got, v)
		}
	})
}
