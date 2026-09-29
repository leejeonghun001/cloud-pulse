package models

// NetworkConfig is the hub's listen-address and access-allowlist
// configuration, either persisted in the "network_config" setting (when
// an admin has confirmed a change from the dashboard) or derived from
// CP_LISTEN/CP_ALLOWED_CIDRS.
type NetworkConfig struct {
	// Mode is "all" (listen on every interface, "[::]:Port") or "custom"
	// (listen only on Addresses).
	Mode string `json:"mode"`
	// Addresses is the set of IPs to listen on when Mode is "custom";
	// empty/ignored when Mode is "all".
	Addresses []string `json:"addresses"`
	Port      int      `json:"port"`
	// AllowedCIDRs is the access allowlist; a single-element slice
	// containing "*" means allow all.
	AllowedCIDRs []string `json:"allowed_cidrs"`
}

// PendingNetwork describes an in-progress network configuration change
// that has been applied to the listener but not yet confirmed by the
// admin from a client address still served by the new configuration. It
// auto-reverts to Previous if not confirmed by Deadline.
type PendingNetwork struct {
	Previous NetworkConfig `json:"previous"`
	Deadline int64         `json:"deadline"`
	// URLs is a candidate URL per new listener ("http://<ip>:<port>",
	// bracketed for IPv6), for the admin to open and confirm from.
	URLs []string `json:"urls"`
}

// ClientInfo describes the requesting browser's own connection to the
// hub, shown on the Network settings page so an admin can see which
// address they are currently using.
type ClientInfo struct {
	IP        string `json:"ip"`
	LocalAddr string `json:"local_addr"`
}

// AgentConnection records the local address an agent most recently used
// to reach the hub, for display on the Network settings page (an
// address change may disconnect agents that reach the hub over an
// address about to be removed).
type AgentConnection struct {
	HostID    string `json:"host_id"`
	Hostname  string `json:"hostname"`
	LocalAddr string `json:"local_addr"`
}

// ListenerStatus reports one listen address's current state.
type ListenerStatus struct {
	Addr string `json:"addr"`
	// Status is "listening", "waiting" (address not yet available, e.g.
	// a not-yet-up interface; retried periodically), or "error"
	// (unretryable bind failure such as EADDRINUSE/EACCES, also
	// retried).
	Status string `json:"status"`
	// Error is the last bind error's message, empty when Status is
	// "listening".
	Error string `json:"error"`
	// Since is the unix-seconds timestamp this status was last set.
	Since int64 `json:"since"`
}

// NetworkState is the full read-model for GET /api/v1/settings/network.
type NetworkState struct {
	Config NetworkConfig `json:"config"`
	// Source is "hub" when Config comes from the persisted
	// "network_config" setting, "env" when derived from
	// CP_LISTEN/CP_ALLOWED_CIDRS.
	Source          string           `json:"source"`
	EnvListen       string           `json:"env_listen"`
	EnvAllowedCIDRs []string         `json:"env_allowed_cidrs"`
	Listeners       []ListenerStatus `json:"listeners"`
	// Pending is non-nil while a change is awaiting confirmation.
	Pending *PendingNetwork `json:"pending"`
	Client  ClientInfo      `json:"client"`
	// Agents is sorted by hostname; encoded as [] rather than null when
	// empty.
	Agents []AgentConnection `json:"agents"`
	// Interfaces is the host's network adapters; encoded as [] rather
	// than null when empty or when InterfacesError is set.
	Interfaces []NetInterface `json:"interfaces"`
	// InterfacesError is non-empty when net.Interfaces() failed; the
	// endpoint still returns 200 with Interfaces == [] in that case,
	// never a 500.
	InterfacesError string `json:"interfaces_error,omitempty"`
}

// NetAddress is one address assigned to a network interface.
type NetAddress struct {
	IP        string `json:"ip"`
	PrefixLen int    `json:"prefix_len"`
	// Family is "ipv4" or "ipv6".
	Family string `json:"family"`
	// Scope is "global", "link-local", or "loopback".
	Scope string `json:"scope"`
	// Network is the address's CIDR (IP/PrefixLen normalized to the
	// network address).
	Network string `json:"network"`
	// SuggestedCIDR is a broader allowlist suggestion appropriate for
	// this address's kind: Tailscale's CGNAT range
	// (100.64.0.0/10/fd7a:115c:a1e0::/48), loopback
	// (127.0.0.0/8/::1/128), or otherwise Network itself.
	SuggestedCIDR string `json:"suggested_cidr"`
}

// NetInterface is one network adapter, classified for the Network
// settings page.
type NetInterface struct {
	Name     string `json:"name"`
	Index    int    `json:"index"`
	Up       bool   `json:"up"`
	Loopback bool   `json:"loopback"`
	// Kind is "loopback", "tailscale", "virtual", or "physical".
	Kind      string       `json:"kind"`
	Addresses []NetAddress `json:"addresses"`
}
