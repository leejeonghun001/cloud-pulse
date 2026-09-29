package models

// HostLimitsView is the read-model for one host's egress/ingress limit
// configuration on the hub settings page: the agent-reported default, any
// hub override, and the resulting effective values.
type HostLimitsView struct {
	HostID   string   `json:"host_id"`
	Hostname string   `json:"hostname"`
	Provider Provider `json:"provider"`
	// AgentEgressLimitBytes is the outbound limit reported by the agent
	// (CP_EGRESS_LIMIT_GB or the provider default); 0 means unlimited.
	AgentEgressLimitBytes uint64 `json:"agent_egress_limit_bytes"`
	// EgressLimitBytes is the hub override for outbound, or nil if none.
	EgressLimitBytes *uint64 `json:"egress_limit_bytes"`
	// IngressLimitBytes is the hub override for inbound, or nil if none.
	IngressLimitBytes          *uint64 `json:"ingress_limit_bytes"`
	EffectiveEgressLimitBytes  uint64  `json:"effective_egress_limit_bytes"`
	EffectiveIngressLimitBytes uint64  `json:"effective_ingress_limit_bytes"`
}

// SettingsView is the read-model for the hub settings page: version,
// network/access configuration, and per-host limit overrides.
type SettingsView struct {
	Version string `json:"version"`
	// AllowedCIDRs is ["*"] when the allowlist is disabled (allow-all).
	AllowedCIDRs         []string `json:"allowed_cidrs"`
	OfflineAfterSeconds  int64    `json:"offline_after_seconds"`
	CloudIntervalSeconds int64    `json:"cloud_interval_seconds"`
	UIAuthEnabled        bool     `json:"ui_auth_enabled"`
	// AgentTokenHint is the first 4 + "…" + last 4 characters of the
	// agent token only; never the full value.
	AgentTokenHint string `json:"agent_token_hint"`
	// AlertWebhookURL is the effective webhook URL ("" if none
	// configured).
	AlertWebhookURL string `json:"alert_webhook_url"`
	// AlertWebhookSource is "hub" (set via the settings API), "env" (from
	// CP_ALERT_WEBHOOK_URL), or "none".
	AlertWebhookSource string `json:"alert_webhook_source"`
	// Hosts is sorted by hostname; encoded as [] rather than null when
	// empty.
	Hosts []HostLimitsView `json:"hosts"`
}

// AgentTokenView is the response to the admin agent-token reveal
// endpoint: the full agent token and a ready-to-run install command.
type AgentTokenView struct {
	AgentToken string `json:"agent_token"`
	// InstallCommand uses the hub URL the browser reached to
	// reach the hub (request scheme + Host), not a configured value.
	InstallCommand string `json:"install_command"`
}
