package hub

import (
	"net/http"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// registerInventoryRoutes registers the inventory API (SPEC-v0.5 §C): a
// single per-host read endpoint.
//
//	GET /api/v1/hosts/{id}/inventory
func (s *Server) registerInventoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/hosts/{id}/inventory", s.requireUser(s.handleGetHostInventory))
}

// handleGetHostInventory responds with a host's most recently reported
// models.Inventory: GET /api/v1/hosts/{id}/inventory. 404 with
// APIError{Code: "no_inventory"} when the host is unknown or has never
// reported one — both cases are indistinguishable to the caller (an
// unknown host also "has no inventory"), keeping this handler a single
// Store call instead of an extra existence check.
func (s *Server) handleGetHostInventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	inv, err := s.store.GetHostInventory(ctx, id)
	if err != nil {
		if isNotFound(err) {
			writeJSON(w, http.StatusNotFound, models.APIError{Error: "no inventory reported for this host", Code: "no_inventory"})
			return
		}
		s.logger.Error("get host inventory failed", "host_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, models.APIError{Error: "internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, inv)
}
