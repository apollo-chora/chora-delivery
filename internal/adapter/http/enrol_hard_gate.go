// enrol_hard_gate.go — ADR-226 §4 hard_gate prerequisite enforcement at enrol.
//
// The prerequisite DAG (ADR-226 Sub-phase A) authored + validated + displayed
// course→course edges; §4 deferred the behavioural gate ("locked until you
// finish X"). This is that gate: a learner cannot self-enrol in a course while
// any of its hard_gate prerequisite courses is not yet completed. advisory
// prerequisites never block. Enforced on the learner SELF-enrol paths only —
// admin roster placement (handleOfferingEnroll) keeps its authority.
package httpapi

import (
	"context"
	"net/http"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// unmetEnrolHardGates returns the hard_gate prerequisites of courseID that the
// learner (gcid) has NOT completed — the enrol-blocking set (empty ⇒ allowed).
//
// SAME-TENANT only: the course's prerequisite edges and the learner's
// completion facts must share `tenantID` to compare, so callers gate on
// pc.TenantID == tenantID (cross-tenant public enrol is not gated here — a
// tracked follow-up, not a silent skip). Degrades to "allowed" (nil, nil) when
// the prerequisite/enrolment ports are not wired — enforcement is a config
// capability, never a swallowed failure: a real port error is returned loud.
func unmetEnrolHardGates(deps Deps, ctx context.Context, tenantID, courseID, gcid string) ([]domain.CoursePrerequisite, error) {
	if deps.CourseCJ2 == nil || deps.CourseCJ2.Prerequisites == nil || deps.Enrollments == nil {
		return nil, nil
	}
	enrolments, err := deps.Enrollments.ListByGCID(ctx, tenantID, gcid)
	if err != nil {
		return nil, err
	}
	completed := make(map[string]bool, len(enrolments))
	for _, e := range enrolments {
		if e.Status == domain.EnrollmentStatusCompleted {
			completed[e.CourseID] = true
		}
	}
	return deps.CourseCJ2.Prerequisites.UnmetHardGates(ctx, tenantID, courseID, completed)
}

// writeEnrolHardGateBlocked writes the 422 the learner UI turns into a
// "locked until you finish X" state, carrying the unmet prerequisite course ids.
func writeEnrolHardGateBlocked(w http.ResponseWriter, unmet []domain.CoursePrerequisite) {
	ids := make([]string, 0, len(unmet))
	for _, p := range unmet {
		ids = append(ids, p.PrerequisiteCourseID)
	}
	writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
		"error":                         "prerequisites_not_met",
		"message":                       "enrolment blocked: finish the prerequisite course(s) first",
		"unmet_prerequisite_course_ids": ids,
	})
}
