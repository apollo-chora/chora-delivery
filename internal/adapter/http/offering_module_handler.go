// offering_module_handler.go — HTTP handlers for the R+ Phase-2 W7 (WS-A)
// offering-nested Module course-structure surface: group an attached course's
// flat course_content items into ordered, named Modules, each carrying a
// completion Requirement.
//
// Endpoints (dispatched from offeringsSubHandler in offering_handler.go — all
// under the gateway/mesh/Armor-allowed /api/v1/offerings/* glob; GET + POST
// only, so no gateway/Armor change is needed):
//
//	GET  /api/v1/offerings/{id}/modules?course_id=X       list a course's modules
//	POST /api/v1/offerings/{id}/modules                   create a module {course_id,title}
//	POST /api/v1/offerings/{id}/modules/add-item          {course_id,module_id,content_item_id}
//	POST /api/v1/offerings/{id}/modules/remove-item       {course_id,module_id,item_id}
//	POST /api/v1/offerings/{id}/modules/reorder-items     {course_id,module_id,ordered_item_ids}
//	POST /api/v1/offerings/{id}/modules/set-requirement   {course_id,module_id,kind,threshold_n,required_item_ids}
//	POST /api/v1/offerings/{id}/modules/remove            {course_id,module_id} (soft-delete)
//
// This is a VALIDATED PROXY, mirroring the S1 Curriculum surface: the offering
// endpoint enforces course_id ∈ offering.CourseIDs (you may only structure an
// attached course) then delegates to the Module aggregate (module.ModulePort).
// The Module is its own aggregate keyed by CourseID; a ModuleItem references a
// course_content.ContentItem BY UUID (cross-aggregate, never owns it). Because a
// course's structure is SHARED-canonical across every offering that attaches it,
// module writes honour the SAME edit-lock as curriculum edits (B1.2): a course
// bundled by a LAUNCHED/RUNNING offering is locked (409) so a live cohort's
// structure never shifts under it. Intra-chora_delivery only — no cross-DB
// query, no new event.
//
// Authorisation mirrors the curriculum surface: the GET list uses
// hasInstructorRole; every write uses the stricter hasOfferingAdminRole.
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/module"
)

// -----------------------------------------------------------------------------
// Request bodies (snake_case wire) + write preamble
// -----------------------------------------------------------------------------

type createModuleReq struct {
	CourseID string `json:"course_id"`
	Title    string `json:"title"`
}

type addModuleItemReq struct {
	CourseID      string `json:"course_id"`
	ModuleID      string `json:"module_id"`
	ContentItemID string `json:"content_item_id"`
}

type removeModuleItemReq struct {
	CourseID string `json:"course_id"`
	ModuleID string `json:"module_id"`
	ItemID   string `json:"item_id"`
}

type reorderModuleItemsReq struct {
	CourseID       string   `json:"course_id"`
	ModuleID       string   `json:"module_id"`
	OrderedItemIDs []string `json:"ordered_item_ids"`
}

type setModuleRequirementReq struct {
	CourseID        string   `json:"course_id"`
	ModuleID        string   `json:"module_id"`
	Kind            string   `json:"kind"`
	ThresholdN      int      `json:"threshold_n"`
	RequiredItemIDs []string `json:"required_item_ids"`
}

type removeModuleReq struct {
	CourseID string `json:"course_id"`
	ModuleID string `json:"module_id"`
}

// offeringForModuleWrite runs the shared wiring + RBAC + offering-load preamble
// for the module write endpoints (mirrors offeringForCurriculumWrite but gates
// on the Modules port). Writes the error response + returns ok=false on failure;
// on success returns the live offering, resolved tenant, and actor gcid.
// Admin-gated (hasOfferingAdminRole — the WRITE gate).
func offeringForModuleWrite(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) (context.Context, string, string, *delivery.Offering, bool) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return nil, "", "", nil, false
	}
	if deps.Modules == nil {
		writeError(w, http.StatusServiceUnavailable, "module store not wired")
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

// moduleForCourseInOffering resolves an existing module for a module-scoped
// write: it enforces course_id ∈ offering.CourseIDs (400), loads the module
// (404 miss), and verifies the module belongs to the SAME tenant + claimed
// course (404 — a module of another course cannot be reached through this
// attached-course proxy). Writes the error + returns ok=false on any failure.
func moduleForCourseInOffering(ctx context.Context, deps Deps, tenantID, courseID, moduleID string, o *delivery.Offering, w http.ResponseWriter) (*module.Module, bool) {
	if strings.TrimSpace(moduleID) == "" {
		writeError(w, http.StatusBadRequest, "module_id is required")
		return nil, false
	}
	if !offeringHasCourse(o, courseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return nil, false
	}
	m, found, err := deps.Modules.Get(ctx, moduleID)
	if err != nil {
		writeModuleErr(w, err)
		return nil, false
	}
	if !found || m == nil || m.TenantID != tenantID || m.CourseID != courseID {
		writeError(w, http.StatusNotFound, "module not found for this course")
		return nil, false
	}
	return m, true
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

// handleOfferingListModules — GET /api/v1/offerings/{id}/modules?course_id=X.
// Read surface (instructor/admin visibility). Returns the attached course's
// active modules in structure order, each with its ordered items + requirement.
func handleOfferingListModules(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.Modules == nil {
		writeError(w, http.StatusServiceUnavailable, "module store not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasInstructorRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/training-admin role")
		return
	}
	courseID := strings.TrimSpace(r.URL.Query().Get("course_id"))
	if courseID == "" {
		writeError(w, http.StatusBadRequest, "course_id query parameter is required")
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
	if !offeringHasCourse(o, courseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return
	}
	mods, err := deps.Modules.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		writeModuleErr(w, err)
		return
	}
	out := make([]map[string]interface{}, 0, len(mods))
	for _, m := range mods {
		out = append(out, moduleDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"course_id": courseID, "modules": out})
}

// handleOfferingCreateModule — POST /api/v1/offerings/{id}/modules {course_id,title}.
func handleOfferingCreateModule(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, _, o, ok := offeringForModuleWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req createModuleReq
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
	m, err := module.New(module.NewParams{TenantID: tenantID, CourseID: req.CourseID, Title: req.Title})
	if err != nil {
		writeModuleErr(w, err)
		return
	}
	created, err := deps.Modules.Create(ctx, m)
	if err != nil {
		writeModuleErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, moduleDTO(created))
}

// handleOfferingAddModuleItem — POST /api/v1/offerings/{id}/modules/add-item.
func handleOfferingAddModuleItem(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, _, o, ok := offeringForModuleWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req addModuleItemReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := moduleForCourseInOffering(ctx, deps, tenantID, req.CourseID, req.ModuleID, o, w); !ok {
		return
	}
	if curriculumEditLocked(ctx, deps, tenantID, req.CourseID, w) {
		return
	}
	if _, err := deps.Modules.AddItem(ctx, tenantID, req.ModuleID, req.ContentItemID); err != nil {
		writeModuleErr(w, err)
		return
	}
	writeReloadedModule(ctx, deps, req.ModuleID, w)
}

// handleOfferingRemoveModuleItem — POST /api/v1/offerings/{id}/modules/remove-item.
func handleOfferingRemoveModuleItem(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, _, o, ok := offeringForModuleWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req removeModuleItemReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := moduleForCourseInOffering(ctx, deps, tenantID, req.CourseID, req.ModuleID, o, w); !ok {
		return
	}
	if curriculumEditLocked(ctx, deps, tenantID, req.CourseID, w) {
		return
	}
	if err := deps.Modules.RemoveItem(ctx, tenantID, req.ModuleID, req.ItemID); err != nil {
		writeModuleErr(w, err)
		return
	}
	writeReloadedModule(ctx, deps, req.ModuleID, w)
}

// handleOfferingReorderModuleItems — POST /api/v1/offerings/{id}/modules/reorder-items.
func handleOfferingReorderModuleItems(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, _, o, ok := offeringForModuleWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req reorderModuleItemsReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := moduleForCourseInOffering(ctx, deps, tenantID, req.CourseID, req.ModuleID, o, w); !ok {
		return
	}
	if curriculumEditLocked(ctx, deps, tenantID, req.CourseID, w) {
		return
	}
	if err := deps.Modules.Reorder(ctx, tenantID, req.ModuleID, req.OrderedItemIDs); err != nil {
		writeModuleErr(w, err)
		return
	}
	writeReloadedModule(ctx, deps, req.ModuleID, w)
}

// handleOfferingSetModuleRequirement — POST /api/v1/offerings/{id}/modules/set-requirement.
func handleOfferingSetModuleRequirement(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, _, o, ok := offeringForModuleWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req setModuleRequirementReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := moduleForCourseInOffering(ctx, deps, tenantID, req.CourseID, req.ModuleID, o, w); !ok {
		return
	}
	if curriculumEditLocked(ctx, deps, tenantID, req.CourseID, w) {
		return
	}
	reqVO := module.ModuleRequirement{
		Kind:            module.RequirementKind(req.Kind),
		ThresholdN:      req.ThresholdN,
		RequiredItemIDs: req.RequiredItemIDs,
	}
	if err := deps.Modules.SetRequirement(ctx, tenantID, req.ModuleID, reqVO); err != nil {
		writeModuleErr(w, err)
		return
	}
	writeReloadedModule(ctx, deps, req.ModuleID, w)
}

// handleOfferingRemoveModule — POST /api/v1/offerings/{id}/modules/remove.
func handleOfferingRemoveModule(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	ctx, tenantID, _, o, ok := offeringForModuleWrite(deps, offeringID, w, r)
	if !ok {
		return
	}
	var req removeModuleReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := moduleForCourseInOffering(ctx, deps, tenantID, req.CourseID, req.ModuleID, o, w); !ok {
		return
	}
	if curriculumEditLocked(ctx, deps, tenantID, req.CourseID, w) {
		return
	}
	if err := deps.Modules.SoftDelete(ctx, tenantID, req.ModuleID); err != nil {
		writeModuleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"module_id": req.ModuleID, "deleted": true})
}

// -----------------------------------------------------------------------------
// Shared helpers
// -----------------------------------------------------------------------------

// writeReloadedModule re-reads the mutated module + its items and writes the
// fresh DTO (200), so the caller re-renders without a follow-up GET. A load miss
// after a successful mutation is a fail-loud 500 (should never happen).
func writeReloadedModule(ctx context.Context, deps Deps, moduleID string, w http.ResponseWriter) {
	m, found, err := deps.Modules.Get(ctx, moduleID)
	if err != nil {
		writeModuleErr(w, err)
		return
	}
	if !found || m == nil {
		writeError(w, http.StatusInternalServerError, "module vanished after mutation")
		return
	}
	writeJSON(w, http.StatusOK, moduleDTO(m))
}

// moduleDTO renders a Module as the snake_case wire object: id/course_id/title/
// position, the inline requirement, and the ordered items (content_item_id + the
// membership item_id + position).
func moduleDTO(m *module.Module) map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(m.Items))
	for _, it := range m.Items {
		items = append(items, map[string]interface{}{
			"item_id":         it.ID,
			"content_item_id": it.ContentItemID,
			"position":        it.Position,
		})
	}
	required := m.Requirement.RequiredItemIDs
	if required == nil {
		required = []string{}
	}
	return map[string]interface{}{
		"id":        m.ID,
		"course_id": m.CourseID,
		"title":     m.Title,
		"position":  m.Position,
		"requirement": map[string]interface{}{
			"kind":              string(m.Requirement.Kind),
			"threshold_n":       m.Requirement.ThresholdN,
			"required_item_ids": required,
		},
		"items": items,
	}
}

// writeModuleErr maps a Module domain/adapter error to an HTTP status (fail-loud,
// never a masked 200). Guard-clause failures → 400; conflicts (duplicate item,
// mutating a soft-deleted module) → 409; rule/cap refusals → 422; missing
// module/item → 404; anything else → 500.
func writeModuleErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, module.ErrInvalidArgument), errors.Is(err, module.ErrInvalidReorder):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, module.ErrDuplicateItem), errors.Is(err, module.ErrDeleted):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, module.ErrCapExceeded),
		errors.Is(err, module.ErrInvalidRequirement),
		errors.Is(err, module.ErrRequirementViolation):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, module.ErrNotFound), errors.Is(err, module.ErrItemNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "module operation failed: "+err.Error())
	}
}
