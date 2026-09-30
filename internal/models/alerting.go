package models

// AlertMetric identifies the metric an alert rule evaluates.
type AlertMetric string

// Supported alert metrics.
const (
	AlertMetricCPU          AlertMetric = "cpu"
	AlertMetricMemory       AlertMetric = "memory"
	AlertMetricDisk         AlertMetric = "disk"
	AlertMetricLoad1        AlertMetric = "load1"
	AlertMetricEgressOutPct AlertMetric = "egress_out_pct"
	AlertMetricEgressInPct  AlertMetric = "egress_in_pct"
	AlertMetricHostDown     AlertMetric = "host_down"
	// AlertMetricStorageUsagePct is SPEC-v0.7 §3's storage-usage alert
	// metric: a connected Google Drive/Dropbox account's used-quota
	// percentage. Unlike every other metric, this one is
	// account-scoped, not host-scoped — a rule targeting this metric
	// repurposes AlertRule.HostID to hold a storage account's ID
	// (formatted as a decimal string) instead of a host ID; "" still
	// means "every connected storage account," mirroring the
	// all-hosts convention for every other metric. See
	// internal/alerting/storageusage.go for the evaluation logic this
	// repurposing enables without a schema/model change.
	AlertMetricStorageUsagePct AlertMetric = "storage_usage_pct"
)

// AlertOperator is the comparison an alert rule's threshold is evaluated
// with.
type AlertOperator string

// Supported alert operators.
const (
	AlertOperatorGT  AlertOperator = ">"
	AlertOperatorGTE AlertOperator = ">="
)

// AlertRuleState is one host/rule pair's current position in the alert
// state machine (see internal/alerting.Engine).
type AlertRuleState string

// Alert state-machine states, persisted per rule+host in alert_state.
const (
	AlertStateOK      AlertRuleState = "ok"
	AlertStatePending AlertRuleState = "pending"
	AlertStateFiring  AlertRuleState = "firing"
)

// AlertEventState is the lifecycle state of one alert_events row.
type AlertEventState string

// Alert event states.
const (
	AlertEventFiring   AlertEventState = "firing"
	AlertEventResolved AlertEventState = "resolved"
)

// NotifyChannelType identifies the delivery platform a NotifyChannel
// sends to.
type NotifyChannelType string

// Supported notification channel types.
const (
	NotifyChannelDiscord  NotifyChannelType = "discord"
	NotifyChannelTelegram NotifyChannelType = "telegram"
	NotifyChannelWhatsApp NotifyChannelType = "whatsapp"
	NotifyChannelWebhook  NotifyChannelType = "webhook"
)

// AlertRule is a user-configured condition that, when satisfied for
// DurationSec continuously, fires an alert to ChannelIDs. HostID == ""
// applies the rule to every host. See internal/alerting.Engine for
// evaluation semantics.
type AlertRule struct {
	ID      int64       `json:"id"`
	Name    string      `json:"name"`
	Enabled bool        `json:"enabled"`
	Metric  AlertMetric `json:"metric"`
	// HostID scopes the rule to one host; "" applies it to all hosts.
	// For Metric == AlertMetricStorageUsagePct specifically, this field
	// is repurposed to hold a storage account's ID (formatted as a
	// decimal string) instead of a host ID — "" still means "every
	// connected storage account." See AlertMetricStorageUsagePct's doc
	// comment for the rationale (SPEC-v0.7 §3's account-scoped metric
	// reusing the existing host_id column rather than adding a new
	// scoping concept/schema column).
	HostID   string        `json:"host_id"`
	Operator AlertOperator `json:"operator"`
	// Threshold is compared against the metric's value in the metric's
	// own unit (percent for cpu/memory/disk/egress_out_pct/
	// egress_in_pct, a load average for load1; ignored for host_down).
	Threshold float64 `json:"threshold"`
	// DurationSec is the sustained window the condition must hold for,
	// in seconds. 0 means fire immediately on the first breaching
	// sample. For host_down, the effective offline threshold is
	// OfflineAfter + DurationSec.
	DurationSec int `json:"duration_sec"`
	// CooldownSec is the minimum time between re-notifications while a
	// rule keeps firing on the same host ("still firing" reminders). 0
	// means never re-notify after the initial firing notification.
	// Default 3600 (applied by callers/migration, not this type).
	CooldownSec int `json:"cooldown_sec"`
	// NotifyResolved controls whether a resolved transition also sends
	// a notification.
	NotifyResolved bool    `json:"notify_resolved"`
	ChannelIDs     []int64 `json:"channel_ids"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
}

// NotifyChannel is a configured destination alerts can be delivered to.
// Config holds per-type fields (see internal/notify); secret fields
// (e.g. "bot_token", "access_token") are redacted to "***" by the hub's
// API responses and preserved on update when the field is omitted or
// still "***".
type NotifyChannel struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Type    NotifyChannelType `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// notifyChannelSecretFields lists the Config keys, per channel Type,
// that must be redacted in API responses and preserved-on-omit during
// an update. Shared by the hub and notify packages.
var notifyChannelSecretFields = map[NotifyChannelType][]string{
	NotifyChannelDiscord:  {"webhook_url"},
	NotifyChannelTelegram: {"bot_token"},
	NotifyChannelWhatsApp: {"access_token"},
	NotifyChannelWebhook:  {},
}

// SecretFields returns the Config keys that hold secret values for
// channel type t (e.g. "bot_token" for telegram). The returned slice
// must not be modified by callers.
func (t NotifyChannelType) SecretFields() []string {
	return notifyChannelSecretFields[t]
}

// RedactedConfigValue is the placeholder API responses use in place of a
// secret Config value; PUT requests that send this value back keep the
// previously stored secret unchanged.
const RedactedConfigValue = "***"

// Redacted returns a copy of ch with every secret Config field (per
// ch.Type.SecretFields()) replaced by RedactedConfigValue. Non-secret
// fields are left as-is.
func (ch NotifyChannel) Redacted() NotifyChannel {
	out := ch
	if len(ch.Config) == 0 {
		return out
	}
	out.Config = make(map[string]string, len(ch.Config))
	secret := make(map[string]bool, len(ch.Type.SecretFields()))
	for _, f := range ch.Type.SecretFields() {
		secret[f] = true
	}
	for k, v := range ch.Config {
		if secret[k] && v != "" {
			out.Config[k] = RedactedConfigValue
			continue
		}
		out.Config[k] = v
	}
	return out
}

// Delivery records the outcome of sending one AlertEvent notification to
// one channel.
type Delivery struct {
	ChannelID   int64  `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	OK          bool   `json:"ok"`
	// Error is the delivery failure's message, empty when OK is true.
	Error string `json:"error,omitempty"`
	// Diagnosis is a machine-readable classification of Error (SPEC-
	// v0.6 §4's DiagnosisCode table), nil when OK is true or when no
	// classifier was configured (e.g. an older build). Producers must
	// never populate this from anything but a secret-free classifier
	// (see internal/notify.DiagnoseError) — like Error, it is persisted
	// and returned via GET /api/v1/alerts/events.
	Diagnosis *DiagnosisCode `json:"diagnosis,omitempty"`
	At        int64          `json:"at"`
}

// AlertEvent is one firing/resolved occurrence of an AlertRule against a
// specific host, together with every delivery attempt made for it.
type AlertEvent struct {
	ID        int64           `json:"id"`
	RuleID    int64           `json:"rule_id"`
	RuleName  string          `json:"rule_name"`
	HostID    string          `json:"host_id"`
	Hostname  string          `json:"hostname"`
	Metric    AlertMetric     `json:"metric"`
	State     AlertEventState `json:"state"`
	Value     float64         `json:"value"`
	Threshold float64         `json:"threshold"`

	StartedAt  int64 `json:"started_at"`
	NotifiedAt int64 `json:"notified_at"`
	// ResolvedAt is 0 while State is "firing".
	ResolvedAt int64 `json:"resolved_at"`

	// Deliveries is encoded as [] rather than null when empty.
	Deliveries []Delivery `json:"deliveries"`
}

// AlertState is one rule+host pair's current position in the sustained-
// condition state machine, persisted so the engine survives hub
// restarts without re-firing or losing an in-progress "pending" window.
type AlertState struct {
	RuleID int64          `json:"rule_id"`
	HostID string         `json:"host_id"`
	State  AlertRuleState `json:"state"`
	// Since is the unix-seconds time State was last entered.
	Since int64 `json:"since"`
	// LastNotified is the unix-seconds time of the most recent
	// notification sent for this rule+host (initial firing or a
	// cooldown-expiry re-notification); 0 if never notified.
	LastNotified int64   `json:"last_notified"`
	LastValue    float64 `json:"last_value"`
}

// HostSnapshot is the per-host input the alerting engine evaluates rules
// against: identity, liveness, and the metric values sustained-window
// evaluation reads from Store.QuerySeries.
type HostSnapshot struct {
	HostID   string
	Hostname string
	Status   HostStatus
	// LastSeen is unix seconds, used for host_down duration checks.
	LastSeen int64
	// Latest is the host's most recent sample, used for immediate
	// (DurationSec == 0) evaluation of cpu/memory/disk/load1.
	Latest *Sample
	Egress EgressUsage
}
