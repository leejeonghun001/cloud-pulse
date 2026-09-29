package models

// HostLimits is a hub-stored per-host override of the agent-reported
// outbound egress limit and the (otherwise unlimited) inbound egress
// limit. A nil pointer means "no override configured"; a non-nil pointer
// to 0 means "explicitly unlimited".
type HostLimits struct {
	HostID string `json:"host_id"`
	// EgressLimitBytes overrides the agent-reported outbound limit when
	// non-nil. nil means use the agent-reported value.
	EgressLimitBytes *uint64 `json:"egress_limit_bytes"`
	// IngressLimitBytes overrides the default-unlimited inbound limit
	// when non-nil. nil means unlimited.
	IngressLimitBytes *uint64 `json:"ingress_limit_bytes"`
	UpdatedAt         int64   `json:"updated_at"`
}

// EffectiveLimits resolves the effective outbound (tx) and inbound (rx)
// egress limits for a host given the agent-reported outbound limit and
// any hub-side overrides in l.
//
// Precedence for tx: l.EgressLimitBytes if non-nil (source "hub"),
// otherwise agentLimit (source "agent"). Precedence for rx:
// l.IngressLimitBytes if non-nil (source "hub"), otherwise 0/unlimited
// (source "none").
func EffectiveLimits(agentLimit uint64, l HostLimits) (tx, rx uint64, txSource, rxSource string) {
	tx, txSource = agentLimit, "agent"
	if l.EgressLimitBytes != nil {
		tx, txSource = *l.EgressLimitBytes, "hub"
	}

	rx, rxSource = 0, "none"
	if l.IngressLimitBytes != nil {
		rx, rxSource = *l.IngressLimitBytes, "hub"
	}

	return tx, rx, txSource, rxSource
}
