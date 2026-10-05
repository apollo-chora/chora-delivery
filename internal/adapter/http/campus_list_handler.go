// campus_list_handler.go — GET /v1/campus list endpoint for the R+
// /r/campusops route (M15a).
//
// ADD-ONLY supplement to v1_handlers.go: the existing campusHandler
// covers POST /v1/campus (create) and campusByIDHandler covers GET
// /v1/campus/{id} (read-by-id). The bare GET /v1/campus list path was
// missing — this file adds it without touching the existing two
// handlers. A method-dispatch wrapper (campusRootHandler, below) is
// registered in handlers.go in place of campusHandler so POST stays
// 100% behaviour-preserving while GET wires through here.
//
// Tenant scoping: the handler trusts X-Tenant-Id stamped by the
// tenantRequired middleware (validated mesh claims via chora-gateway).
// Cross-tenant rows are never returned.
//
// Response shape: { items: [CampusDTO] } where CampusDTO matches the
// existing campusDTO helper (v1_handlers.go) so the FE service layer
// has a single canonical row type to map.
package httpapi

import (
	"log"
	"net/http"

	"github.com/apollo-chora/chora-common/tracing"
)

// campusListHandler serves GET /v1/campus — returns every non-soft-
// deleted Campus owned by the calling tenant, newest-first.
//
// Empty list (200 + `{"items": []}`) is the success shape when no
// campuses exist for the tenant — the FE renders an empty-state.
func campusListHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tenantID := r.Header.Get("X-Tenant-Id")
		// CHO-2293 fail-loud: a nil store or a store error is a 5xx, NEVER a 200
		// with an empty array. Rendering [] on failure made a dead backend
		// indistinguishable from a tenant that simply has no campuses.
		if deps.CampusOps == nil {
			writeError(w, http.StatusServiceUnavailable, "campus store not wired")
			return
		}
		// The tenant MUST be stamped into the CONTEXT, not just the header:
		// pg.CampusRepo calls rls.ApplySession, which reads it from the context
		// and returns ErrNoTenantContext otherwise, failing before any SQL runs.
		// Same idiom as rooms_handler.go and survey_handler.go.
		ctx := tracing.WithTenantID(r.Context(), tenantID)
		rows, err := deps.CampusOps.ListByTenant(ctx, tenantID)
		if err != nil {
			// Log the CAUSE: a generic 500 with no server-side detail is not
			// fail-loud, it just moves the silence from the client to the operator.
			log.Printf("campus list failed: tenant=%s err=%v", tenantID, err)
			writeError(w, http.StatusInternalServerError, "failed to list campuses")
			return
		}
		items := make([]map[string]interface{}, 0, len(rows))
		for _, c := range rows {
			items = append(items, campusDTO(c))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items": items,
		})
	}
}

// campusRootHandler dispatches /v1/campus by method:
//   - GET  → campusListHandler (this file)
//   - POST → campusHandler (v1_handlers.go, behaviour-preserving)
//
// Registered in place of campusHandler so the existing POST contract
// is untouched while the new GET list shares the same exact-match mux
// entry. Anything other than GET/POST returns 405.
func campusRootHandler(deps Deps) http.HandlerFunc {
	post := campusHandler(deps)
	get := campusListHandler(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			get(w, r)
		case http.MethodPost:
			post(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}
