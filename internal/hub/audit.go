package hub

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Audit actions recorded by this stage's routes (SPEC-v0.6 §3 개선 c).
// Free-form "<entity>.<verb>" strings per models.AuditAction's doc
// comment; other v0.6.0 stages (e.g. billing's interval-change save)
// define their own constants in their own files and call s.recordAudit
// directly — this list only covers network-cost's own entities.
const (
	AuditActionPlanCreate            models.AuditAction = "pricing_plan.create"
	AuditActionPlanUpdate            models.AuditAction = "pricing_plan.update"
	AuditActionPlanDelete            models.AuditAction = "pricing_plan.delete"
	AuditActionHostPricingChange     models.AuditAction = "host_pricing.change"
	AuditActionDisplayCurrencyChange models.AuditAction = "billing_settings.display_currency_change"
)

// auditActorFor returns the AuditEntry.Actor value for the currently
// authenticated request (SPEC-v0.6 §3 개선 c): "admin" for a session
// (the hub's single admin account), "api_token" for CP_UI_TOKEN. Any
// other/unset value (shouldn't occur on an admin-only route, since
// requireAdmin always sets one of the two) falls back to "unknown"
// rather than leaving the field empty.
func auditActorFor(ctx context.Context) string {
	switch authMethodFromContext(ctx) {
	case "session":
		return "admin"
	case "api_token":
		return "api_token"
	default:
		return "unknown"
	}
}

// recordAudit is the shared helper every v0.6.0 stage uses to append an
// AuditEntry (SPEC-v0.6 §3 개선 c): plan create/update/delete, host
// pricing changes, and rate/currency/interval changes (the billing
// stage's interval-change save calls this too — see
// notes/v06-prep.md). before/after are marshaled to JSON; a nil value
// (rather than a zero-value struct) omits that side entirely (empty
// string), matching AuditEntry.BeforeJSON/AfterJSON's "" for
// create/delete doc comment. beforeJSON == afterJSON (byte-identical)
// is treated as a no-op save and skipped entirely, so an unchanged
// settings/plan save never grows the audit log — the "no entry on
// no-op saves" requirement.
//
// Errors are logged but never fail the caller's request: audit logging
// is best-effort observability, not a transactional part of the
// underlying write that already succeeded.
func (s *Server) recordAudit(ctx context.Context, r *http.Request, action models.AuditAction, entityType, entityID string, before, after any) {
	beforeJSON, err := marshalAuditValue(before)
	if err != nil {
		s.logger.Error("audit: marshal before value failed", "action", action, "error", err)
		return
	}
	afterJSON, err := marshalAuditValue(after)
	if err != nil {
		s.logger.Error("audit: marshal after value failed", "action", action, "error", err)
		return
	}

	// No-op save: both sides present and byte-identical. A create
	// (before == "") or delete (after == "") is never a no-op even if
	// both happen to be "" (before and after nil), which cannot occur
	// for a real create/delete call since one side always carries data;
	// this only short-circuits the "update produced the same JSON"
	// case.
	if before != nil && after != nil && beforeJSON == afterJSON {
		return
	}

	entry := models.AuditEntry{
		At:         s.opts.now().Unix(),
		Actor:      auditActorFor(ctx),
		Remote:     clientIP(r),
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		BeforeJSON: beforeJSON,
		AfterJSON:  afterJSON,
	}

	if _, err := s.store.CreateAuditEntry(ctx, entry); err != nil {
		s.logger.Error("audit: create entry failed", "action", action, "error", err)
		return
	}

	s.logger.Info("audit",
		"actor", entry.Actor,
		"remote", entry.Remote,
		"action", string(entry.Action),
		"entity_type", entry.EntityType,
		"entity_id", entry.EntityID,
	)
}

// marshalAuditValue marshals v to a JSON string for AuditEntry's
// before_json/after_json columns, returning "" for a nil v (the
// create/delete "not applicable" case per AuditEntry's doc comment).
func marshalAuditValue(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
