// offering_prerequisites_handler.go — HTTP handlers for the R+ Phase-2
// offering-nested Course Prerequisite DAG surface (ADR-226).
//
// Endpoints (registered in offering_handler.go dispatch):
//
//	GET  /api/v1/offerings/{id}/prerequisites          read edges + notes per attached course
//	POST /api/v1/offerings/{id}/prerequisites          add a course→course edge
//	POST /api/v1/offerings/{id}/prerequisites/remove   drop an edge (soft-delete)
//
// VALIDATED PROXY: the offering surface enforces course_id ∈ offering.CourseIDs
// then delegates to the CoursePrerequisiteService, which owns the graph rules
// (catalogue existence + acyclicity + cap). The Course (not the Offering) owns
// its prerequisites — the edge is intra-chora_delivery (course→course, no FK,
// never cross-DB). Admin-gated, POST-only writes (edge-safe, no DELETE method).
//
// Error mapping: malformed input (missing ids / bad kind) → 400; graph refusals
// (self-edge / cycle / cap / unknown course) → 422 (ADR-226 §2); nil service →
// 503. Enforcement of hard_gate at enrol is DEFERRED (ADR-226 §4).
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// Request DTOs
// -----------------------------------------------------------------------------

type addPrerequisiteReq struct {
	CourseID             string `json:"course_id"`
	PrerequisiteCourseID string `json:"prerequisite_course_id"`
	Kind                 string `json:"kind"`
}

type removePrerequisiteReq struct {
	CourseID             string `json:"course_id"`
	PrerequisiteCourseID string `json:"prerequisite_course_id"`
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

// handleOfferingAddPrerequisite — POST /api/v1/offerings/{id}/prerequisites.
func handleOfferingAddPrerequisite(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, o, ok := offeringForPrerequisiteWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req addPrerequisiteReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !offeringHasCourse(o, req.CourseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return
	}
	prereqs, err := deps.CourseCJ2.Prerequisites.Add(ctx, tenantID, req.CourseID, req.PrerequisiteCourseID, delivery.PrereqKind(req.Kind))
	if err != nil {
		writePrerequisiteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prerequisiteWriteDTO(ctx, deps, tenantID, req.CourseID, prereqs))
}

// handleOfferingRemovePrerequisite — POST /api/v1/offerings/{id}/prerequisites/remove.
func handleOfferingRemovePrerequisite(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, o, ok := offeringForPrerequisiteWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req removePrerequisiteReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !offeringHasCourse(o, req.CourseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return
	}
	prereqs, err := deps.CourseCJ2.Prerequisites.Remove(ctx, tenantID, req.CourseID, req.PrerequisiteCourseID)
	if err != nil {
		writePrerequisiteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prerequisiteWriteDTO(ctx, deps, tenantID, req.CourseID, prereqs))
}

// handleOfferingGetPrerequisites — GET /api/v1/offerings/{id}/prerequisites.
// Read surface: any in-tenant caller (tenant-scoped), like the other offering
// GETs. Returns, per attached course: the structured edges (with resolved target
// titles) + the free-text PrerequisiteNotes.
func handleOfferingGetPrerequisites(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.CourseCJ2 == nil || deps.CourseCJ2.Prerequisites == nil {
		writeError(w, http.StatusServiceUnavailable, "prerequisite service not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	courses := make([]map[string]interface{}, 0, len(o.CourseIDs))
	for _, cid := range o.CourseIDs {
		prereqs, err := deps.CourseCJ2.Prerequisites.List(ctx, tenantID, cid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		title := ""
		var notes []string
		if deps.CourseCJ2.Courses != nil {
			if c, found, _ := deps.CourseCJ2.Courses.Get(ctx, tenantID, cid); found && c != nil {
				title = c.Title
				notes = c.PrerequisiteNotes
			}
		}
		courses = append(courses, map[string]interface{}{
			"course_id":          cid,
			"title":              title,
			"prerequisite_notes": defaultedSlice(notes),
			"prerequisites":      prerequisiteRowDTOs(ctx, deps, tenantID, prereqs),
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"courses": courses})
}

// -----------------------------------------------------------------------------
// Preamble + error mapping + DTO
// -----------------------------------------------------------------------------

// offeringForPrerequisiteWrite runs the shared wiring + RBAC + offering-load
// preamble for the two write endpoints. Writes the error response and returns
// ok=false on any failure. Admin-gated (hasOfferingAdminRole).
func offeringForPrerequisiteWrite(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) (context.Context, string, *delivery.Offering, bool) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return nil, "", nil, false
	}
	if deps.CourseCJ2 == nil || deps.CourseCJ2.Prerequisites == nil {
		writeError(w, http.StatusServiceUnavailable, "prerequisite service not wired")
		return nil, "", nil, false
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return nil, "", nil, false
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return nil, "", nil, false
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return nil, "", nil, false
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return nil, "", nil, false
	}
	return ctx, tenantID, o, true
}

// writePrerequisiteErr maps a CoursePrerequisiteService error to an HTTP status:
// malformed input → 400; graph-integrity refusals → 422 (ADR-226 §2). Unknown
// errors surface as 500.
func writePrerequisiteErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, delivery.ErrPrerequisiteCourseRequired),
		errors.Is(err, delivery.ErrPrerequisiteKindInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, delivery.ErrPrerequisiteSelfEdge),
		errors.Is(err, delivery.ErrPrerequisiteCycle),
		errors.Is(err, delivery.ErrPrerequisiteCapExceeded),
		errors.Is(err, delivery.ErrPrerequisiteUnknownCourse):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// prerequisiteWriteDTO renders a course's freshly-mutated prerequisite list.
func prerequisiteWriteDTO(ctx context.Context, deps Deps, tenantID, courseID string, prereqs []delivery.CoursePrerequisite) map[string]interface{} {
	return map[string]interface{}{
		"course_id":     courseID,
		"prerequisites": prerequisiteRowDTOs(ctx, deps, tenantID, prereqs),
	}
}

// prerequisiteRowDTOs renders each edge with its resolved target-course title
// (best-effort: an unresolved title renders as ""). snake_case wire keys.
func prerequisiteRowDTOs(ctx context.Context, deps Deps, tenantID string, prereqs []delivery.CoursePrerequisite) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(prereqs))
	for _, p := range prereqs {
		title := ""
		if deps.CourseCJ2 != nil && deps.CourseCJ2.Courses != nil {
			if c, found, _ := deps.CourseCJ2.Courses.Get(ctx, tenantID, p.PrerequisiteCourseID); found && c != nil {
				title = c.Title
			}
		}
		out = append(out, map[string]interface{}{
			"prerequisite_course_id":    p.PrerequisiteCourseID,
			"prerequisite_course_title": title,
			"kind":                      string(p.Kind),
		})
	}
	return out
}
