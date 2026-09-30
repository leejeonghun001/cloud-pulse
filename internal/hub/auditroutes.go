package hub

import (
	"net/http"
	"strconv"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// maxAuditPageLimit is the largest "limit" GET /api/v1/audit accepts; a
// request for more is silently capped rather than rejected, matching
// the existing maxEventsPageLimit convention in alertroutes.go.
const maxAuditPageLimit = 200

// defaultAuditPageLimit is used when "limit" is omitted or invalid.
const defaultAuditPageLimit = 50

// registerAuditRoutes registers the audit log read API (SPEC-v0.6 §3
// 개선 c): GET /api/v1/audit.
func (s *Server) registerAuditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/audit", s.requireAdmin(s.handleListAuditEntries))
}

// handleListAuditEntries responds with a page of audit log entries,
// newest first: GET /api/v1/audit?entity_type=&limit=&before= (admin).
// before is a unix-seconds cursor (exclusive upper bound on At) for
// paging further back; omitted/0 starts from the newest entry.
func (s *Server) handleListAuditEntries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	entityType := q.Get("entity_type")

	limit := defaultAuditPageLimit
	if raw := q.Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > maxAuditPageLimit {
		limit = maxAuditPageLimit
	}

	var before int64
	if raw := q.Get("before"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			before = v
		}
	}

	// Fetch one extra row to determine HasMore without a separate COUNT
	// query.
	entries, err := s.store.ListAuditEntries(ctx, entityType, before, limit+1)
	if err != nil {
		s.logger.Error("list audit entries failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}

	writeJSON(w, http.StatusOK, models.AuditListView{
		Entries: entries,
		HasMore: hasMore,
	})
}
