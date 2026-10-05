// instructor_courses_handler.go — GET /api/v1/instructors/{instructor_gcid}/courses
//
// Closes debt #4 / A6 per docs/m13/handoff-fe-to-be-service-2026-05-14.md:
// the FE instructor-roster surface previously had no by-instructor read; the
// only option was fetching the full tenant catalogue and client-filtering,
// which is both expensive and leaky.
//
// Authorization (verified inside the handler, NOT at middleware):
//
//   - tenantRequired enforces X-Tenant-Id presence (400 if missing).
//   - gcid header is required (401 if missing) — Bucket 4 servicemesh
//     propagation guarantees this when the caller's JWT is valid.
//   - The path's `instructor_gcid` MUST be a valid UUID (422 otherwise).
//   - Caller is authorised when ANY of the following hold:
//     a. caller_gcid == path.instructor_gcid (the instructor themselves), OR
//     b. caller carries the `instructor` typed role in `x-mesh-user-roles`
//     (Bucket 4 servicemesh header — comma-separated lowercase roles), OR
//     c. caller carries the `admin` typed role.
//     Any other role surface (learner / auditor) is 403.
//
// RLS: the handler depends on the CataloguePort, which on production wires
// the pg.CatalogueRepo. CatalogueRepo.ListByInstructor wraps every read in
// rls.ApplySession with the tenant from r.Header.Get("X-Tenant-Id"), so the
// row-level policy on `courses` filters to the caller's tenant — a course
// authored by the SAME instructor in a DIFFERENT tenant cannot leak.
package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// uuidLen is the length of the canonical UUID string form (8-4-4-4-12 + 4 dashes).
const uuidLen = 36

// instructorCoursesHandler serves GET /api/v1/instructors/{instructor_gcid}/courses.
//
// The route is registered under the /api/v1/instructors/ subtree dispatcher
// in NewServer (handlers.go). Only the GET method is allowed; everything else
// returns 405.
func instructorCoursesHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract the trailing /{instructor_gcid}/courses segments. The
		// dispatcher routes /api/v1/instructors/ to this handler; we parse
		// {instructor_gcid} + verify the leaf is `courses`.
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/instructors/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")
		// Expected shape: [<instructor_gcid>, "courses"]. Anything else 404.
		if len(parts) != 2 || parts[1] != "courses" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		instructorGCID := strings.TrimSpace(parts[0])
		if instructorGCID == "" {
			writeError(w, http.StatusNotFound, "instructor_gcid required in path")
			return
		}
		// 422 on malformed UUID — the path param is contract-typed as UUID
		// in the OpenAPI; a non-UUID is a client bug, not a not-found.
		if !looksLikeUUID(instructorGCID) {
			writeError(w, http.StatusUnprocessableEntity,
				"instructor_gcid must be a UUID")
			return
		}

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

		// Authorization: caller is the instructor themselves OR holds
		// instructor/admin role in the active tenant.
		if !authorisedForInstructorRoster(callerGCID, instructorGCID, r) {
			writeError(w, http.StatusForbidden,
				"caller is not the instructor and lacks the instructor/admin role")
			return
		}

		page, per, perr := parseInstructorRosterPagination(r)
		if perr != nil {
			writeError(w, http.StatusUnprocessableEntity, perr.Error())
			return
		}

		tenantID := r.Header.Get("X-Tenant-Id")
		items, total, err := deps.Catalogue.ListByInstructor(r.Context(), tenantID, instructorGCID, page, per)
		if err != nil {
			writeError(w, http.StatusInternalServerError,
				"catalogue read failed: "+err.Error())
			return
		}

		out := make([]map[string]interface{}, 0, len(items))
		for _, pc := range items {
			// Refresh enrolled count from the live registry projection.
			// Tenant must land in ctx for pg.EnrollmentRepo's RLS read.
			countCtx := tracing.WithTenantID(r.Context(), pc.TenantID)
			n, _ := deps.Enrollments.CountByCourse(countCtx, pc.TenantID, pc.ID)
			count := int32(n)
			out = append(out, publicCourseDTO(pc, count))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items": out,
			"total": total,
			"page":  page,
			"per":   per,
		})
	}
}

// looksLikeUUID is a cheap structural check — full strict-UUID validation
// is done downstream by Postgres on the `instructor_gcid uuid` bind. The
// 8-4-4-4-12 dash pattern is sufficient to differentiate "valid UUID shape"
// from "client typo".
//
// Accepts both UUIDv4 and UUIDv7 (the version nibble is not enforced — the
// chora_delivery.courses.instructor_gcid column accepts any version per
// migrations/0001_initial.sql).
func looksLikeUUID(s string) bool {
	if len(s) != uuidLen {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') ||
				(r >= 'a' && r <= 'f') ||
				(r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

// authorisedForInstructorRoster reports whether the caller may read the
// roster of the instructor identified by instructorGCID.
//
//   - Self-read: caller_gcid == instructor_gcid → ALWAYS allowed.
//   - Role-read: caller carries `instructor` OR `admin` in the
//     `x-mesh-user-roles` header (Bucket 4 servicemesh propagation).
//
// The role header is comma-separated lowercase strings. Empty / missing
// header is treated as "no roles" (learner default).
func authorisedForInstructorRoster(callerGCID, instructorGCID string, r *http.Request) bool {
	if callerGCID == instructorGCID {
		return true
	}
	return callerHasMeshRole(r, roleInstructor, roleAdmin)
}

// parseInstructorRosterPagination extracts page + per from the query string
// with the conventional defaults (page=1, per=20) and rejects invalid
// values with a 422-shaped error (caller maps it to the response status).
//
// per cap of 200 mirrors the rest of the delivery service's pagination
// (see PageLimit OpenAPI parameter).
func parseInstructorRosterPagination(r *http.Request) (page, per int, err error) {
	page = 1
	per = 20
	q := r.URL.Query()
	if v := q.Get("page"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 {
			return 0, 0, errPaginationInvalid("page must be a positive integer")
		}
		page = n
	}
	if v := q.Get("per"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 200 {
			return 0, 0, errPaginationInvalid("per must be in [1,200]")
		}
		per = n
	}
	return page, per, nil
}

// paginationError carries a 422-shaped error message back to the handler.
type paginationError struct{ msg string }

func (e *paginationError) Error() string { return e.msg }

func errPaginationInvalid(msg string) error { return &paginationError{msg: msg} }

// Compile-time guard: the in-memory adapter satisfies the new port method
// so unit tests + local dev keep working without the pgx adapter wired.
var _ domain.CataloguePort = (*domain.InMemCatalogue)(nil)
