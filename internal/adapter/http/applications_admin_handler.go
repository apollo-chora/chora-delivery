// applications_admin_handler.go — R+ training-admin Course-Application
// review queue (M14 R+ surface buildout).
//
// Routes registered (orchestrator wires the mux entry in handlers.go;
// this file is ADD-ONLY per the parallel-session contract):
//
//	GET /api/v1/applications          — list ALL applications in the
//	                                    current tenant (optional ?state=
//	                                    filter), newest-first.
//	GET /api/v1/applications/{id}     — detail (admin view; bypasses the
//	                                    owner-or-404 guard the learner
//	                                    /v1/me/applications/{id} surface
//	                                    enforces).
//
// Distinct from applications.go which serves the LEARNER scope
// (/v1/me/applications…). The admin queue surfaces every applicant's
// row across the tenant — gated to training-admins only.
//
// Authorization (verified inside the handler, NOT at middleware) —
// mirrors the pattern in instructor_courses_handler.go:
//
//   - tenantRequired enforces X-Tenant-Id presence (400 if missing).
//   - gcid header is required (401 if missing) — Bucket 4 servicemesh
//     propagation guarantees this when the caller's JWT is valid.
//   - Caller MUST carry the `training-admin` role in the
//     `x-mesh-user-roles` Bucket 4 servicemesh header (comma-separated
//     lowercase). `admin` is also accepted as a superset role.
//     Every other role (learner / instructor-only / auditor / empty)
//     returns 403.
//
// State filter values (wire form: UPPER_SNAKE_CASE per the FE brief):
//
//	SUBMITTED | IN_REVIEW | OFFER_MADE | ACCEPTED | PAID | ENROLLED |
//	REJECTED | WITHDRAWN
//
// The handler also accepts the lowercase domain form (`submitted`,
// `under_review`, …) for parity with the JSON `status` field the
// response body emits. `IN_REVIEW` is a wire-form alias for the
// domain's `under_review` constant — the wire form follows the
// ADR-164 §1 narrative which uses IN_REVIEW; the domain enum
// predates ADR-164 and uses `under_review` (kept for backward
// compatibility — see application.go's `allowedTransitions` map).
//
// RLS: in production the wired pg.ApplicationRepo runs every read
// through rls.ApplySession with the tenant from r.Header.Get
// ("X-Tenant-Id"), so cross-tenant rows never surface. The in-memory
// adapter's ListByTenant filters in-process by TenantID.
//
// Hexagonal: ADAPTER. Domain code never imports this file.
package httpapi

import (
	"log"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

// applicationsAdminHandler serves the two R+ admin Course-Application
// endpoints. The mux entry is wired by the orchestrator in handlers.go
// — this file ONLY declares the handler factory.
//
// Routes:
//
//	GET /api/v1/applications          (collection)
//	GET /api/v1/applications/{id}     (single)
//
// `/api/v1/applications/` (trailing slash) dispatches the single-row
// detail; the bare path serves the collection list.
func applicationsAdminHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		callerGCID := strings.TrimSpace(r.Header.Get("gcid"))
		if callerGCID == "" {
			writeError(w, http.StatusUnauthorized,
				"gcid header required (caller identity)")
			return
		}
		if !authorisedForApplicationsAdmin(r) {
			writeError(w, http.StatusForbidden,
				"caller lacks the training-admin role required to view the applications queue")
			return
		}
		if deps.Applications == nil {
			// Wire safety net — admin queue unmounted in unconfigured
			// dev/test rigs (the orchestrator gates the mux entry on
			// deps.Applications != nil so this branch should never
			// fire in practice, but keeps the handler honest).
			writeError(w, http.StatusServiceUnavailable, "applications repo not configured")
			return
		}

		// Dispatch list (bare path) vs detail (trailing /{id}).
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/applications")
		rest = strings.TrimPrefix(rest, "/")
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" {
			handleApplicationsAdminList(deps, w, r)
			return
		}
		// Single-row detail: /{id}. Reject any deeper segment.
		if strings.Contains(rest, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		handleApplicationsAdminDetail(deps, rest, w, r)
	}
}

// handleApplicationsAdminList serves GET /api/v1/applications with
// an optional ?state=<wire-form> filter.
func handleApplicationsAdminList(deps Deps, w http.ResponseWriter, r *http.Request) {
	stateRaw := strings.TrimSpace(r.URL.Query().Get("state"))
	var statusFilter application.Status
	if stateRaw != "" {
		parsed, err := parseApplicationStatusFilter(stateRaw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		statusFilter = parsed
	}

	tenantID := r.Header.Get("X-Tenant-Id")
	offset, limit := paging(r)
	// Admin queue: tenant-scoped ctx ONLY (no gcid) so the pg adapter's
	// user_isolation SELECT policy stays permissive and the training-admin
	// sees every applicant's row in the tenant (per migration 0002 comment:
	// "admin (R+) flows do NOT set chora.user_gcid so they see all").
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	items, total, err := deps.Applications.ListByTenant(ctx, application.ListByTenantInput{
		TenantID: tenantID,
		Status:   statusFilter,
		Offset:   offset,
		Limit:    limit,
	})
	if err != nil {
		// Fail loud: the admin queue 500 was previously swallowed (only the
		// request line recorded status=500, never the cause), which made an
		// intermittent failure undiagnosable. Record the wrapped cause plus
		// enough context to triage (path, tenant, gcid, state filter).
		log.Printf("delivery: applications admin list 500 path=%s tenant=%s gcid=%s state=%q err=%v",
			r.URL.Path, tenantID, r.Header.Get("gcid"), stateRaw, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, app := range items {
		out = append(out, applicationDTO(app))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
		"total": total,
	})
}

// handleApplicationsAdminDetail serves GET /api/v1/applications/{id}.
// Returns the admin-view detail (no owner-or-404 gate — that's a
// learner-surface invariant; the admin surface IS allowed to inspect
// every row in their tenant).
func handleApplicationsAdminDetail(deps Deps, appID string, w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Tenant-Id")
	// Admin detail: tenant-scoped ctx ONLY (no gcid) so the admin can inspect
	// any applicant's row in the tenant (the owner-or-404 gate is a
	// learner-surface invariant, not an admin one).
	app, ok, err := deps.Applications.Get(tracing.WithTenantID(r.Context(), tenantID), tenantID, appID)
	if err != nil {
		// Fail loud (same rationale as the list path): record the wrapped
		// cause + triage context before returning 500.
		log.Printf("delivery: applications admin detail 500 path=%s tenant=%s gcid=%s application_id=%s err=%v",
			r.URL.Path, tenantID, r.Header.Get("gcid"), appID, err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	writeJSON(w, http.StatusOK, applicationDetailDTO(app))
}

// authorisedForApplicationsAdmin reports whether the caller may read
// the admin-scope applications queue. The `training-admin` role is
// the canonical gate per the brief; `admin` is accepted as a superset.
//
// Reads the `x-mesh-user-roles` Bucket 4 servicemesh header
// (comma-separated lowercase). Empty / missing == "no roles" ==
// 403.
func authorisedForApplicationsAdmin(r *http.Request) bool {
	return callerHasMeshRole(r, roleTrainingAdmin, roleAdmin)
}

// parseApplicationStatusFilter normalises the wire-form ?state= value
// to the domain's Status constant. Accepts both the FE brief's
// UPPER_SNAKE_CASE form (SUBMITTED / IN_REVIEW / ...) AND the lowercase
// snake_case domain form (submitted / under_review / ...) — both must
// resolve so the FE can use either form without forcing a contract
// migration.
//
// `IN_REVIEW` is a wire-form alias for the domain's `under_review`
// constant — see file-level comment for the historical reason
// (ADR-164 §1 narrative vs the pre-ADR domain enum).
func parseApplicationStatusFilter(raw string) (application.Status, error) {
	// Normalise: trim + lowercase.
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "submitted":
		return application.StatusSubmitted, nil
	case "in_review", "under_review":
		return application.StatusUnderReview, nil
	case "offer_made":
		return application.StatusOfferMade, nil
	case "accepted":
		return application.StatusAccepted, nil
	case "paid":
		return application.StatusPaid, nil
	case "enrolled":
		return application.StatusEnrolled, nil
	case "rejected":
		return application.StatusRejected, nil
	case "withdrawn":
		return application.StatusWithdrawn, nil
	case "draft":
		return application.StatusDraft, nil
	}
	return "", errInvalidStateFilter(raw)
}

// errInvalidStateFilter builds a 400-shaped error for an unrecognised
// ?state= value. Lists the accepted wire-form values so the FE can
// surface a useful validation message.
type stateFilterError struct{ raw string }

func (e *stateFilterError) Error() string {
	return "invalid state filter " + jsonQuote(e.raw) +
		" — allowed: SUBMITTED | IN_REVIEW | OFFER_MADE | ACCEPTED | PAID | ENROLLED | REJECTED | WITHDRAWN | DRAFT"
}

func errInvalidStateFilter(raw string) error { return &stateFilterError{raw: raw} }

// jsonQuote wraps a value in double-quotes for the error message
// without pulling encoding/json into the hot path. Pure cosmetic —
// keeps the message readable when the raw value contains characters
// that look like delimiters.
func jsonQuote(s string) string { return `"` + s + `"` }
