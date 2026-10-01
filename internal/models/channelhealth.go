package models

// ChannelHealthSettings is one channel's weekly synthetic-test
// schedule (SPEC-v0.8 §3-2). Absent (no row) means synthetic testing
// is disabled for that channel — the default per SPEC-v0.8 §3-2's
// explicit opt-in requirement. Weekday/MinuteOfDay are always UTC (see
// internal/storage/migrations/0007_v08.sql's 3-2 section) so the
// scheduler never has to reason about a channel's local time zone;
// display-side conversion to the browser's local time is a web-layer
// concern (core/i18n.js's Intl helpers), not stored here.
type ChannelHealthSettings struct {
	ChannelID int64 `json:"channel_id"`
	Enabled   bool  `json:"enabled"`
	// Weekday is 0=Sunday..6=Saturday, UTC.
	Weekday int `json:"weekday"`
	// MinuteOfDay is 0-1439, UTC.
	MinuteOfDay int `json:"minute_of_day"`
	// LastRunAt is the unix-seconds time this channel's synthetic test
	// last actually ran, 0 if never — independent of outcome.
	LastRunAt int64 `json:"last_run_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// ValidWeekday reports whether w is a valid ChannelHealthSettings.Weekday
// value (0-6).
func ValidWeekday(w int) bool {
	return w >= 0 && w <= 6
}

// ValidMinuteOfDay reports whether m is a valid
// ChannelHealthSettings.MinuteOfDay value (0-1439).
func ValidMinuteOfDay(m int) bool {
	return m >= 0 && m <= 1439
}

// SyntheticOutcome is one synthetic test run's result classification
// (SPEC-v0.8 §3-2), mirroring NotifyVerifyStatus's accepted/verified/
// failed vocabulary (excluding "skipped", which has no meaning for a
// scheduled per-channel test — a channel with synthetic testing
// enabled always has credentials by construction).
type SyntheticOutcome string

// Supported synthetic test outcomes.
const (
	// SyntheticAccepted means the platform accepted the send (2xx) but
	// no read-back/cleanup was attempted or applicable (WhatsApp,
	// webhook).
	SyntheticAccepted SyntheticOutcome = "accepted"
	// SyntheticVerified means the platform accepted the send AND a
	// read-back check confirmed the message exists, with cleanup
	// (delete) succeeding where the platform supports it (Discord,
	// Telegram).
	SyntheticVerified SyntheticOutcome = "verified"
	// SyntheticFailed means the send itself failed, or a read-back
	// check found the message missing/mismatched.
	SyntheticFailed SyntheticOutcome = "failed"
)

// SyntheticResult is one channel_synthetic_results row: the outcome of
// one weekly (or manually triggered) synthetic connectivity test.
// Detail is always pre-redacted (see internal/notify.DiagnoseError's
// redaction pass) and DiagnosisCode is empty unless Outcome is
// SyntheticFailed — neither field may ever hold a channel secret.
type SyntheticResult struct {
	ID         int64            `json:"id"`
	ChannelID  int64            `json:"channel_id"`
	StartedAt  int64            `json:"started_at"`
	FinishedAt int64            `json:"finished_at"`
	Outcome    SyntheticOutcome `json:"outcome"`
	Diagnosis  DiagnosisCode    `json:"diagnosis_code,omitempty"`
	Detail     string           `json:"detail,omitempty"`
}

// ChannelHealthState is a channel's overall health classification,
// combining its synthetic-test history and delivery statistics (SPEC-
// v0.8 §3-2). See ComputeChannelHealthState in channelhealthstate.go
// for the pure function that derives this and the thresholds it
// documents.
type ChannelHealthState string

// Supported channel health states.
const (
	// ChannelHealthy means recent synthetic tests and deliveries are
	// succeeding within the documented thresholds.
	ChannelHealthy ChannelHealthState = "healthy"
	// ChannelDegraded means some recent failures were observed but not
	// enough to classify the channel as failing outright.
	ChannelDegraded ChannelHealthState = "degraded"
	// ChannelFailing means the channel's most recent synthetic test
	// failed, or delivery success rate has dropped below the failing
	// threshold.
	ChannelFailing ChannelHealthState = "failing"
	// ChannelUnknown means there is not enough data (no synthetic
	// tests and no deliveries in the lookback window) to classify the
	// channel either way.
	ChannelUnknown ChannelHealthState = "unknown"
)

// ChannelDeliveryStats summarizes a channel's delivery outcomes over a
// lookback window (SPEC-v0.8 §3-2's "채널별 7일·30일 성공률"),
// computed from alert_events delivery records by
// internal/alerting's (or the hub's) pure aggregator — this type
// itself has no storage/query logic, only the shape of the result.
type ChannelDeliveryStats struct {
	// Total is the number of delivery attempts observed in the window.
	Total int `json:"total"`
	// Success is the number of those attempts with OK == true.
	Success int `json:"success"`
	// LastSuccessAt is the unix-seconds time of the most recent
	// successful delivery in the window, 0 if none.
	LastSuccessAt int64 `json:"last_success_at"`
	// LastFailureAt is the unix-seconds time of the most recent failed
	// delivery in the window, 0 if none.
	LastFailureAt int64 `json:"last_failure_at"`
	// LastFailureDiagnosis is the DiagnosisCode of the most recent
	// failed delivery in the window, "" if none or if no classifier
	// was configured for that delivery.
	LastFailureDiagnosis DiagnosisCode `json:"last_failure_diagnosis,omitempty"`
	// AvgLatencyMS/MaxLatencyMS are computed only from deliveries
	// carrying a non-zero DurationMS (see models.Delivery's doc
	// comment on pre-v0.8 deliveries never populating it); 0 if no
	// delivery in the window has latency data.
	AvgLatencyMS int64 `json:"avg_latency_ms"`
	MaxLatencyMS int64 `json:"max_latency_ms"`
}

// SuccessRate returns s.Success / s.Total as a fraction in [0, 1], or
// -1 if Total is 0 (no data — callers must check this before treating
// the result as a real rate).
func (s ChannelDeliveryStats) SuccessRate() float64 {
	if s.Total == 0 {
		return -1
	}
	return float64(s.Success) / float64(s.Total)
}

// ChannelHealth is the full per-channel health report row (SPEC-v0.8
// §3-2's health tab channel card / GET
// /api/v1/alerts/channels/health), combining identity, current state,
// 7-day/30-day delivery stats, the most recent synthetic result (if
// any), and a short strip of recent synthetic results for the UI's
// 12-week display.
type ChannelHealth struct {
	ChannelID   int64              `json:"channel_id"`
	ChannelName string             `json:"channel_name"`
	ChannelType NotifyChannelType  `json:"channel_type"`
	Enabled     bool               `json:"enabled"`
	State       ChannelHealthState `json:"state"`

	Stats7d  ChannelDeliveryStats `json:"stats_7d"`
	Stats30d ChannelDeliveryStats `json:"stats_30d"`

	// Settings is nil if the channel has never had synthetic testing
	// configured (no channel_health_settings row).
	Settings *ChannelHealthSettings `json:"settings,omitempty"`
	// LastSynthetic is the most recent completed synthetic test result
	// for this channel, nil if none has ever run.
	LastSynthetic *SyntheticResult `json:"last_synthetic,omitempty"`
	// RecentSynthetic lists up to the last 12 completed synthetic test
	// results, newest first (SPEC-v0.8 §3-2's "12주 스트립"); may be
	// shorter than 12 (or empty) if fewer have run.
	RecentSynthetic []SyntheticResult `json:"recent_synthetic"`
}

// ChannelHealthReport is the full response body for GET
// /api/v1/alerts/channels/health: every channel's health row plus
// whether a dashboard banner should show because a failing channel had
// no healthy fallback available (SPEC-v0.8 §3-2).
type ChannelHealthReport struct {
	Channels []ChannelHealth `json:"channels"`
	// BannerNeeded is true if at least one channel is currently
	// ChannelFailing/degraded-with-recent-incident and no other
	// healthy enabled channel existed to receive the "channel X is
	// failing" fallback notification at the time of the most recent
	// such incident — see internal/alerting's fallback selection logic
	// (SPEC-v0.8 §3-2's "정상 채널이 없으면 대시보드 배너로 표시").
	BannerNeeded bool `json:"banner_needed"`
	// BannerChannelNames lists the channel name(s) the banner should
	// mention, empty when BannerNeeded is false.
	BannerChannelNames []string `json:"banner_channel_names,omitempty"`
}

// ChannelFailureIncident tracks one channel's currently-open (or most
// recently closed) "failing" incident for the channel-failure fallback
// notification's once-per-incident de-duplication (SPEC-v0.8 §3-2).
// See internal/storage/migrations/0007_v08.sql's channel_failure_
// incidents table (PK channel_id, one row per channel at a time) for
// the persisted shape this mirrors.
type ChannelFailureIncident struct {
	ChannelID int64 `json:"channel_id"`
	OpenedAt  int64 `json:"opened_at"`
	// NotifiedAt is 0 until the fallback notification has actually
	// been sent for this incident (e.g. because no healthy fallback
	// channel existed yet — see FallbackChannelID).
	NotifiedAt int64 `json:"notified_at"`
	// FallbackChannelID is the channel that carried the "channel X is
	// failing" notification, 0 if NotifiedAt is still 0.
	FallbackChannelID int64 `json:"fallback_channel_id,omitempty"`
	// RecoveredAt is 0 while the incident is still open.
	RecoveredAt int64 `json:"recovered_at"`
}

// IsOpen reports whether the incident is still open (has not yet
// recovered).
func (i ChannelFailureIncident) IsOpen() bool {
	return i.RecoveredAt == 0
}
