// offering_module_progress_handler.go — HTTP read for the W7 StudentModuleProgress
// projection (CHO-2074):
//
//	GET /api/v1/offerings/{id}/modules/progress?course_id=X[&gcid=Y]
//
// Role-driven visibility (integrative UI, no toggle):
//   - an instructor/admin sees the whole cohort's per-module completion (each
//     module carries a `learners` array); an optional ?gcid= narrows to one;
//   - a learner sees ONLY their own per-module completion (the gcid param is
//     ignored) and MUST be enrolled in the attached course (403 otherwise) so a
//     non-enrolled learner cannot enumerate a foreign course's structure.
//
// A VALIDATED PROXY like the module-authoring surface: enforces course_id ∈
// offering.CourseIDs, then composes the course's modules (module.ModulePort) with
// the per-learner progress rows (moduleprogress.ProgressPort). completed_count is
// computed against each module's CURRENT items so a completion for a since-removed
// item never inflates the count. Intra-chora_delivery only — no cross-DB query.
package httpapi

import (
	"net/http"
	"sort"
	"strings"

	"github.com/apollo-chora/chora-common/tracing"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// handleOfferingModuleProgress — GET /api/v1/offerings/{id}/modules/progress.
func handleOfferingModuleProgress(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
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

	isInstructor := hasInstructorRole(r)
	// Learner path: own progress only, and must be enrolled in the course.
	if !isInstructor {
		if deps.Enrollments == nil {
			writeError(w, http.StatusServiceUnavailable, "enrollment repo not wired")
			return
		}
		if _, enrolled, err := deps.Enrollments.GetByCourseAndGCID(ctx, tenantID, courseID, gcid); err != nil {
			writeError(w, http.StatusInternalServerError, "enrollment lookup failed: "+err.Error())
			return
		} else if !enrolled {
			writeError(w, http.StatusForbidden, "not enrolled in this course")
			return
		}
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
	byModule := make(map[string][]*moduleprogress.StudentModuleProgress, len(mods))
	for _, p := range rows {
		byModule[p.ModuleID] = append(byModule[p.ModuleID], p)
	}

	// Cohort view (instructor, no gcid filter) vs single-learner view.
	filterGCID := gcid // learner: own
	viewerRole := "learner"
	if isInstructor {
		viewerRole = "instructor"
		filterGCID = strings.TrimSpace(r.URL.Query().Get("gcid")) // optional; "" ⇒ whole cohort
	}

	out := make([]map[string]interface{}, 0, len(mods))
	for _, m := range mods {
		itemSet := make(map[string]bool, len(m.Items))
		for _, id := range m.ContentItemIDs() {
			itemSet[id] = true
		}
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
		if isInstructor && filterGCID == "" {
			learners := make([]map[string]interface{}, 0, len(byModule[m.ID]))
			for _, p := range byModule[m.ID] {
				learners = append(learners, learnerProgressDTO(p, itemSet))
			}
			sort.Slice(learners, func(i, j int) bool {
				return learners[i]["gcid"].(string) < learners[j]["gcid"].(string)
			})
			dto["learners"] = learners
		} else {
			var self *moduleprogress.StudentModuleProgress
			for _, p := range byModule[m.ID] {
				if p.GCID == filterGCID {
					self = p
					break
				}
			}
			cc, complete := 0, false
			if self != nil {
				cc = countIn(self.CompletedContentItemIDs, itemSet)
				complete = self.IsComplete
			}
			dto["gcid"] = filterGCID
			dto["completed_count"] = cc
			dto["is_complete"] = complete
			if self != nil && self.CompletedAt != nil {
				dto["completed_at"] = self.CompletedAt.UTC()
			}
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"course_id":   courseID,
		"viewer_role": viewerRole,
		"modules":     out,
	})
}

// learnerProgressDTO renders one learner's progress for a module (cohort row).
func learnerProgressDTO(p *moduleprogress.StudentModuleProgress, itemSet map[string]bool) map[string]interface{} {
	row := map[string]interface{}{
		"gcid":            p.GCID,
		"completed_count": countIn(p.CompletedContentItemIDs, itemSet),
		"is_complete":     p.IsComplete,
	}
	if p.CompletedAt != nil {
		row["completed_at"] = p.CompletedAt.UTC()
	}
	return row
}

// countIn counts how many of ids are members of the current-item set (so a
// completion for a since-removed item is not counted toward the total).
func countIn(ids []string, itemSet map[string]bool) int {
	n := 0
	for _, id := range ids {
		if itemSet[id] {
			n++
		}
	}
	return n
}
