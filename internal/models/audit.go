package models

// AuditAction identifies what kind of change an AuditEntry records.
// Free-form but conventionally "<entity>.<verb>" (e.g.
// "pricing_plan.update"); see SPEC-v0.6 §3 개선 c for the exact set of
// tracked actions.
type AuditAction string

// AuditEntry is one row in the audit_log table (SPEC-v0.6 §3 개선 c):
// records who changed what, when, and the before/after values. Never
// contains secret values — callers populating BeforeJSON/AfterJSON are
// responsible for redacting any secret field first.
type AuditEntry struct {
	ID int64 `json:"id"`
	// At is the unix-seconds time of the change.
	At int64 `json:"at"`
	// Actor is "admin" (an authenticated session) or "api_token"
	// (CP_UI_TOKEN), matching the auth method vocabulary used
	// elsewhere (see internal/hub/auth.go).
	Actor string `json:"actor"`
	// Remote is the request's RemoteAddr (host only, no port), for
	// consistency with the rate limiter's client-key rule.
	Remote string      `json:"remote"`
	Action AuditAction `json:"action"`
	// EntityType identifies the kind of thing changed (e.g.
	// "pricing_plan", "host_pricing", "billing_settings").
	EntityType string `json:"entity_type"`
	// EntityID is a free-form identifier for the specific entity (a
	// pricing plan ID, a host ID, or "" for a singleton setting).
	EntityID string `json:"entity_id"`
	// BeforeJSON/AfterJSON are the full before/after values as JSON
	// text, "" when not applicable (e.g. BeforeJSON is "" for a
	// creation, AfterJSON is "" for a deletion).
	BeforeJSON string `json:"before_json,omitempty"`
	AfterJSON  string `json:"after_json,omitempty"`
}

// AuditListView is the response body for
// GET /api/v1/audit?entity_type=&limit=&before=.
type AuditListView struct {
	Entries []AuditEntry `json:"entries"`
	// HasMore reports whether more entries exist before the oldest one
	// returned (for cursor-style pagination on At).
	HasMore bool `json:"has_more"`
}

// AuditRetentionDays is how long audit_log rows are kept before Prune
// removes them (SPEC-v0.6 §3 개선 c: 400 days).
const AuditRetentionDays = 400
