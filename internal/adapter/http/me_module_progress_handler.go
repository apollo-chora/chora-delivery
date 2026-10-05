// me_module_progress_handler.go — the A+ learner-scoped, course-only read of the
// W7 StudentModuleProgress projection (CHO-2074):
//
//	GET /api/v1/me/module-progress?course_id=X
//
// The A+ learner surface has NO offeringId (its course view is course-keyed), so
// this endpoint resolves the caller's OWN per-module completion by (course, gcid)
// — no offering needed (modules are course-keyed). Enrolment-gated: a learner
// not enrolled in the course gets 403 so they can't enumerate a foreign course's
// structure. Intra-chora_delivery only — course_modules + student_module_progress.
//
// Distinct from the offering-nested instructor cohort read
// (handleOfferingModuleProgress): that one is a VALIDATED PROXY over an offering;
// this one is the learner's self view keyed only on the course they take.
package httpapi

import (
	"net/http"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// handleMeModuleProgress — GET /api/v1/me/module-progress?course_id=X.
func handleMeModuleProgress(deps Deps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if deps.Modules == nil {
		writeError(w, http.StatusServiceUnavailable, "module store not wired")
		return
	}
	if deps.ModuleProgress == nil {
		writeError(w, http.StatusServiceUnavailable, "module progress store not wired")
		return
	}
	if deps.Enrollments == nil {
		writeError(w, http.StatusServiceUnavailable, "enrollment repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	courseID := strings.TrimSpace(r.URL.Query().Get("course_id"))
	if courseID == "" {
		writeError(w, http.StatusBadRequest, "course_id query parameter is required")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)

	// Enrolment gate: a learner only sees progress for a course they take.
	if _, enrolled, err := deps.Enrollments.GetByCourseAndGCID(ctx, tenantID, courseID, gcid); err != nil {
		writeError(w, http.StatusInternalServerError, "enrollment lookup failed: "+err.Error())
		return
	} else if !enrolled {
		writeError(w, http.StatusForbidden, "not enrolled in this course")
		return
	}

	mods, err := deps.Modules.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		writeModuleErr(w, err)
		return
	}
	moduleIDs := make([]string, 0, len(mods))
	for _, m := range mods {
		moduleIDs = append(moduleIDs, m.ID)
	}
	rows, err := deps.ModuleProgress.ListByModuleIDs(ctx, tenantID, moduleIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "progress read failed: "+err.Error())
		return
	}
	// Only this learner's rows, keyed by module.
	own := make(map[string]*moduleprogress.StudentModuleProgress, len(rows))
	for _, p := range rows {
		if p.GCID == gcid {
			own[p.ModuleID] = p
		}
	}

	out := make([]map[string]interface{}, 0, len(mods))
	for _, m := range mods {
		itemSet := make(map[string]bool, len(m.Items))
		for _, id := range m.ContentItemIDs() {
			itemSet[id] = true
		}
		cc, complete := 0, false
		dto := map[string]interface{}{
			"module_id": m.ID,
			"title":     m.Title,
			"position":  m.Position,
			"total":     len(itemSet),
			"requirement": map[string]interface{}{
				"kind":        string(m.Requirement.Kind),
				"threshold_n": m.Requirement.ThresholdN,
			},
		}
		if p := own[m.ID]; p != nil {
			cc = countIn(p.CompletedContentItemIDs, itemSet)
			complete = p.IsComplete
			if p.CompletedAt != nil {
				dto["completed_at"] = p.CompletedAt.UTC()
			}
		}
		dto["completed_count"] = cc
		dto["is_complete"] = complete
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"course_id": courseID,
		"modules":   out,
	})
}
