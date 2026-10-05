// offering_curriculum_handler.go — HTTP handler for the W2.D offering-nested
// Curriculum surface: a READ-ONLY view of an Offering's attached courses + each
// course's ordered content outline.
//
// Endpoint (dispatched from offeringsSubHandler in offering_handler.go):
//
//	GET /api/v1/offerings/{id}/curriculum  — this offering's attached-course
//	                                          outline (object-derived tab)
//
// Intra-chora_delivery only (Offering + CJ#2 Course + CourseContent share the DB
// per ddd-enforcement #3) — NO cross-DB query, NO new aggregate, NO migration,
// NO new event. The attached-course titles come from the CJ#2 Course aggregate
// (deps.CourseCJ2.Courses — the same course-id space the offering-create picker
// uses, GET /api/v1/courses?state=PUBLISHED); the per-course content outline
// comes from the existing CourseContent read service (deps.CourseCJ2.Content.Svc,
// CHO-1612). A course with no curriculum yet is an EMPTY outline (200 items:[]),
// not an error — mirroring listContent.
//
// This is deliberately NOT the W7 Module/DAG structure aggregate (that needs an
// ADR + migration); it is a flat read of what is already attached.
//
// Authorisation mirrors the assessments surface: instructor / training-admin
// (hasInstructorRole) + X-Tenant-Id + gcid (callerTenantGCID) — role-driven
// VISIBILITY, R+ (Rhythm+) audience.
package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/apollo-chora/chora-common/tracing"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// handleOfferingGetCurriculum — GET /api/v1/offerings/{id}/curriculum.
//
// Steps:
//
//	(a) verify wiring (offerings repo + CJ#2 course port + content service);
//	(b) enforce tenant (400) + gcid (401) + instructor/admin role (403);
//	(c) load the offering for this tenant (404 if missing / cross-tenant /
//	    soft-deleted), copying the guard from offering_assessments_handler.go;
//	(d) for each o.CourseIDs entry resolve the course title (CJ#2 aggregate) +
//	    its ordered content outline (CourseContent), preserving offering order;
//	(e) write a hand-built snake_case DTO
//	    { "courses": [ { "id", "title", "items":[{item_id,kind,title,position}] } ] }.
func handleOfferingGetCurriculum(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	// (a) Wiring — fail-loud 503 (no stub), per feedback_no_stubs_real_wiring.
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.CourseCJ2 == nil || deps.CourseCJ2.Courses == nil ||
		deps.CourseCJ2.Content == nil || deps.CourseCJ2.Content.Svc == nil {
		writeError(w, http.StatusServiceUnavailable, "course-content service not wired")
		return
	}
	// (b) Identity + RBAC (enforces tenant 400 + gcid 401; then role 403).
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	// (c) Offering must exist for this tenant (same-DB soft FK; no Go FK).
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	// (d) Resolve each attached course's title + content outline (offering order
	//     preserved). Both reads are intra-chora_delivery.
	courses := make([]map[string]interface{}, 0, len(o.CourseIDs))
	for _, courseID := range o.CourseIDs {
		title := ""
		if c, found, err := deps.CourseCJ2.Courses.Get(ctx, tenantID, courseID); err != nil {
			writeError(w, http.StatusInternalServerError, "course lookup failed: "+err.Error())
			return
		} else if found && c != nil {
			title = c.Title
		}
		// A course with no curriculum yet is an EMPTY outline (not an error),
		// mirroring listContent's cc.ErrNotFound handling.
		items := make([]map[string]interface{}, 0)
		content, err := deps.CourseCJ2.Content.Svc.Get(ctx, tenantID, courseID)
		if err != nil && !errors.Is(err, cc.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "curriculum lookup failed: "+err.Error())
			return
		}
		if content != nil {
			for _, it := range content.Items {
				items = append(items, map[string]interface{}{
					"item_id":  it.ItemID,
					"kind":     string(it.Kind),
					"ref":      it.Ref, // included so the S1 editor can render/edit it
					"title":    it.Title,
					"position": it.Position,
				})
			}
		}
		courses = append(courses, map[string]interface{}{
			"id":    courseID,
			"title": title,
			"items": items,
		})
	}
	// (e) Hand-built snake_case wire DTO.
	writeJSON(w, http.StatusOK, map[string]interface{}{"courses": courses})
}

// -----------------------------------------------------------------------------
// R+ Phase-2 S1 — Curriculum AUTHORING (make the read-only outline write-capable)
//
// The offering endpoint is a VALIDATED PROXY: it enforces course_id ∈
// offering.CourseIDs then delegates to the CourseContent service. The Course
// (not the Offering) owns its content; items reference atoms/media by UUID/URL
// (atom-centric — collections query atoms, never own them). Intra-chora_delivery,
// admin-gated (hasOfferingAdminRole), POST-only (edge-safe). Errors map through
// the shared writeContentErr (ErrInvalidArgument/ErrDuplicateItem/ErrCapExceeded
// /ErrDeleted → 400; ErrItemNotFound → 404).
// -----------------------------------------------------------------------------

// addCurriculumItemReq is the POST /curriculum body.
type addCurriculumItemReq struct {
	CourseID string `json:"course_id"`
	Kind     string `json:"kind"`
	Ref      string `json:"ref"`
	Title    string `json:"title"`
}

// reorderCurriculumReq is the POST /curriculum/reorder body (full permutation).
type reorderCurriculumReq struct {
	CourseID       string   `json:"course_id"`
	OrderedItemIDs []string `json:"ordered_item_ids"`
}

// removeCurriculumItemReq is the POST /curriculum/remove body.
type removeCurriculumItemReq struct {
	CourseID string `json:"course_id"`
	ItemID   string `json:"item_id"`
}

// handleOfferingAddCurriculumItem — POST /api/v1/offerings/{id}/curriculum.
func handleOfferingAddCurriculumItem(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, gcid, o, ok := offeringForCurriculumWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req addCurriculumItemReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !offeringHasCourse(o, req.CourseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return
	}
	if curriculumEditLocked(ctx, deps, tenantID, req.CourseID, w) {
		return
	}
	updated, err := deps.CourseCJ2.Content.Svc.AddItem(ctx, tenantID, req.CourseID, gcid, cc.AddItemParams{
		Kind: cc.Kind(req.Kind), Ref: req.Ref, Title: req.Title,
	})
	if err != nil {
		writeContentErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, offeringCurriculumWriteDTO(updated))
}

// handleOfferingReorderCurriculum — POST /api/v1/offerings/{id}/curriculum/reorder.
func handleOfferingReorderCurriculum(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, gcid, o, ok := offeringForCurriculumWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req reorderCurriculumReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !offeringHasCourse(o, req.CourseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return
	}
	if curriculumEditLocked(ctx, deps, tenantID, req.CourseID, w) {
		return
	}
	updated, err := deps.CourseCJ2.Content.Svc.Reorder(ctx, tenantID, req.CourseID, gcid, req.OrderedItemIDs)
	if err != nil {
		writeContentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, offeringCurriculumWriteDTO(updated))
}

// handleOfferingRemoveCurriculumItem — POST /api/v1/offerings/{id}/curriculum/remove.
func handleOfferingRemoveCurriculumItem(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, gcid, o, ok := offeringForCurriculumWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req removeCurriculumItemReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !offeringHasCourse(o, req.CourseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return
	}
	if curriculumEditLocked(ctx, deps, tenantID, req.CourseID, w) {
		return
	}
	updated, err := deps.CourseCJ2.Content.Svc.RemoveItem(ctx, tenantID, req.CourseID, gcid, req.ItemID)
	if err != nil {
		writeContentErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, offeringCurriculumWriteDTO(updated))
}

// offeringForCurriculumWrite runs the shared wiring + RBAC + offering-load
// preamble for the three authoring endpoints. It writes the error response and
// returns ok=false on any failure; on success it returns the live offering, the
// resolved tenant, and the actor gcid. Admin-gated (hasOfferingAdminRole — the
// stricter WRITE gate, matching the other offering write surfaces; the read
// GET uses hasInstructorRole).
func offeringForCurriculumWrite(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) (context.Context, string, string, *delivery.Offering, bool) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return nil, "", "", nil, false
	}
	if deps.CourseCJ2 == nil || deps.CourseCJ2.Content == nil || deps.CourseCJ2.Content.Svc == nil {
		writeError(w, http.StatusServiceUnavailable, "course-content service not wired")
		return nil, "", "", nil, false
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return nil, "", "", nil, false
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return nil, "", "", nil, false
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return nil, "", "", nil, false
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return nil, "", "", nil, false
	}
	return ctx, tenantID, gcid, o, true
}

// curriculumEditLocked writes a 409 and returns true when courseID is attached
// to a LAUNCHED or RUNNING offering (B1.2 — the shared-canonical course guardrail:
// a live cohort must not see its curriculum shift under it). Because a course is
// shared across offerings, the lock considers EVERY offering that attaches it,
// not just the one being edited through. When the offerings port is unwired the
// check is a no-op (returns false).
func curriculumEditLocked(ctx context.Context, deps Deps, tenantID, courseID string, w http.ResponseWriter) bool {
	if deps.Offerings == nil {
		return false
	}
	offs, err := deps.Offerings.ListByTenant(ctx, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return true
	}
	for _, o := range offs {
		if o == nil || o.DeletedAt != nil {
			continue
		}
		if o.State != delivery.OfferingStateLaunched && o.State != delivery.OfferingStateRunning {
			continue
		}
		if offeringHasCourse(o, courseID) {
			writeError(w, http.StatusConflict,
				"course is attached to a live offering (LAUNCHED/RUNNING) — its curriculum is locked; conclude or archive the offering before editing")
			return true
		}
	}
	return false
}

// offeringHasCourse reports whether courseID is one of the offering's attached
// courses (the write guard — you may only author content on an attached course).
func offeringHasCourse(o *delivery.Offering, courseID string) bool {
	for _, cid := range o.CourseIDs {
		if cid == courseID {
			return true
		}
	}
	return false
}

// offeringCurriculumWriteDTO renders a course's freshly-mutated outline for the
// authoring endpoints: the course_id plus every item (snake_case, incl. `ref`
// so the editor can re-render without a follow-up GET).
func offeringCurriculumWriteDTO(c *cc.CourseContent) map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(c.Items))
	for _, it := range c.Items {
		items = append(items, map[string]interface{}{
			"item_id":  it.ItemID,
			"kind":     string(it.Kind),
			"ref":      it.Ref,
			"title":    it.Title,
			"position": it.Position,
		})
	}
	return map[string]interface{}{"course_id": c.CourseID, "items": items}
}
