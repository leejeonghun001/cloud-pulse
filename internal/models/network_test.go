package models

import (
	"encoding/json"
	"testing"
)

func TestNetworkConfigJSON(t *testing.T) {
	t.Parallel()

	cfg := NetworkConfig{
		Mode:         "custom",
		Addresses:    []string{"198.51.100.2", "100.64.0.1"},
		Port:         8090,
		AllowedCIDRs: []string{"100.64.0.0/10", "127.0.0.0/8"},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded NetworkConfig
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Mode != cfg.Mode || decoded.Port != cfg.Port ||
		len(decoded.Addresses) != len(cfg.Addresses) || len(decoded.AllowedCIDRs) != len(cfg.AllowedCIDRs) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, cfg)
	}
}

func TestNetworkStateJSON(t *testing.T) {
	t.Parallel()

	state := NetworkState{
		Config:          NetworkConfig{Mode: "all", Port: 8090, AllowedCIDRs: []string{"*"}},
		Source:          "env",
		EnvListen:       ":8090",
		EnvAllowedCIDRs: []string{"*"},
		Listeners: []ListenerStatus{
			{Addr: ":8090", Status: "listening", Since: 100},
		},
		Pending: &PendingNetwork{
			Previous: NetworkConfig{Mode: "all", Port: 8090},
			Deadline: 200,
			URLs:     []string{"http://198.51.100.2:8090"},
		},
		Client:          ClientInfo{IP: "198.51.100.5", LocalAddr: "198.51.100.2:8090"},
		Agents:          []AgentConnection{{HostID: "h1", Hostname: "host1", LocalAddr: "198.51.100.2:8090"}},
		Interfaces:      []NetInterface{},
		InterfacesError: "",
	}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{
		"config", "source", "env_listen", "env_allowed_cidrs", "listeners",
		"pending", "client", "agents", "interfaces",
	} {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing JSON field %q", field)
		}
	}
	if _, ok := raw["interfaces_error"]; ok {
		t.Errorf("interfaces_error must be omitted when empty, got %s", b)
	}

	var decoded NetworkState
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Pending == nil || decoded.Pending.Deadline != 200 {
		t.Errorf("Pending round-trip mismatch: %+v", decoded.Pending)
	}
}

func TestNetworkStateNilPendingJSON(t *testing.T) {
	t.Parallel()

	state := NetworkState{Config: NetworkConfig{Mode: "all", Port: 8090}}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if v, ok := raw["pending"]; !ok || string(v) != "null" {
		t.Errorf(`expected "pending":null, got %s`, raw["pending"])
	}
}

func TestListenerStatusJSON(t *testing.T) {
	t.Parallel()

	ls := ListenerStatus{Addr: "127.0.0.1:8090", Status: "error", Error: "bind: address in use", Since: 42}
	b, err := json.Marshal(ls)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ListenerStatus
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded != ls {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, ls)
	}
}

func TestNetInterfaceJSON(t *testing.T) {
	t.Parallel()

	iface := NetInterface{
		Name:     "tailscale0",
		Index:    3,
		Up:       true,
		Loopback: false,
		Kind:     "tailscale",
		Addresses: []NetAddress{
			{
				IP:            "100.64.0.1",
				PrefixLen:     32,
				Family:        "ipv4",
				Scope:         "global",
				Network:       "100.64.0.1/32",
				SuggestedCIDR: "100.64.0.0/10",
			},
		},
	}
	b, err := json.Marshal(iface)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded NetInterface
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Name != iface.Name || decoded.Kind != iface.Kind || len(decoded.Addresses) != 1 {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, iface)
	}
	if decoded.Addresses[0] != iface.Addresses[0] {
		t.Errorf("address round-trip mismatch: got %+v, want %+v", decoded.Addresses[0], iface.Addresses[0])
	}
}

func TestClientInfoAndAgentConnectionJSON(t *testing.T) {
	t.Parallel()

	c := ClientInfo{IP: "203.0.113.1", LocalAddr: "198.51.100.2:8090"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal ClientInfo: %v", err)
	}
	var decodedC ClientInfo
	if err := json.Unmarshal(b, &decodedC); err != nil {
		t.Fatalf("Unmarshal ClientInfo: %v", err)
	}
	if decodedC != c {
		t.Errorf("ClientInfo round-trip mismatch: got %+v, want %+v", decodedC, c)
	}

	ac := AgentConnection{HostID: "h1", Hostname: "host1", LocalAddr: "198.51.100.2:8090"}
	b2, err := json.Marshal(ac)
	if err != nil {
		t.Fatalf("Marshal AgentConnection: %v", err)
	}
	var decodedAC AgentConnection
	if err := json.Unmarshal(b2, &decodedAC); err != nil {
		t.Fatalf("Unmarshal AgentConnection: %v", err)
	}
	if decodedAC != ac {
		t.Errorf("AgentConnection round-trip mismatch: got %+v, want %+v", decodedAC, ac)
	}
}

func TestPendingNetworkJSON(t *testing.T) {
	t.Parallel()

	p := PendingNetwork{
		Previous: NetworkConfig{Mode: "custom", Addresses: []string{"127.0.0.1"}, Port: 8090, AllowedCIDRs: []string{"127.0.0.0/8"}},
		Deadline: 1234,
		URLs:     []string{"http://127.0.0.1:8090", "http://[fd7a:115c:a1e0::1]:8090"},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded PendingNetwork
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Deadline != p.Deadline || len(decoded.URLs) != len(p.URLs) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, p)
	}
}
