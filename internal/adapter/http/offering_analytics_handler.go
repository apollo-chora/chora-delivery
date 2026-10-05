// offering_analytics_handler.go — HTTP handler for the R+ offering-nested
// Analytics surface: a READ-ONLY delivery-local analytics roll-up for an
// Offering (the async/self-paced tab).
//
// Endpoint (dispatched from offeringsSubHandler in offering_handler.go):
//
//	GET /api/v1/offerings/{id}/analytics  — enrolment + capacity + lifecycle
//	                                        + assessment metrics (object-derived tab)
//
// Every metric is intra-chora_delivery (Offering + CJ#2 Course + the roster
// projection over enrolments + the offering's assessments per ddd-enforcement
// #3) — NO cross-DB query, NO new aggregate, NO migration, NO event. Learner
// COUNTS come from the SAME rostering.CourseRosterRepo the roster tab uses
// (per-course learner_count + a unique-GCID set for the distinct people total);
// titles from deps.CourseCJ2.Courses; assessment count from
// deps.AssessmentDeps.Assessments.ListByOffering.
//
// avg_progress_pct + completion_rate_pct (CHO-1827) roll up the
// CourseLearnerProgress projection (migration 0054) over the offering's courses.
// Learner progress lives in chora_consumption and a cross-DB query is FORBIDDEN,
// so the projection is fed by chora-consumption's
// learning_path.{advanced,completed}.v1 through the course-progress push inboxes:
// the event is the bridge, and this read stays intra-chora_delivery like every
// other metric here. Both are NULL (never 0) when the offering has no enrolments,
// because "nobody has made progress" and "there is nobody" are different facts.
//
// Deferred (flagged in the FE, NOT faked here): pass_rate (needs a graded-outcome
// port on Deps).
//
// Authorisation mirrors the Curriculum / Certification / Roster surfaces:
// instructor / training-admin (hasInstructorRole) + X-Tenant-Id (400) + gcid
// (401). Read-only (GET); the dispatcher 405s any other method.
package httpapi

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	courseprogress "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
)

// handleOfferingGetAnalytics — GET /api/v1/offerings/{id}/analytics.
func handleOfferingGetAnalytics(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	// (a) Wiring — fail-loud 503 (no stub).
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.Rosters == nil {
		writeError(w, http.StatusServiceUnavailable, "rosters repo not wired")
		return
	}
	if deps.CourseCJ2 == nil || deps.CourseCJ2.Courses == nil {
		writeError(w, http.StatusServiceUnavailable, "course port not wired")
		return
	}
	if deps.AssessmentDeps == nil || deps.AssessmentDeps.Assessments == nil {
		writeError(w, http.StatusServiceUnavailable, "assessment repo not wired")
		return
	}
	if deps.CourseProgress == nil {
		writeError(w, http.StatusServiceUnavailable, "course-progress projection not wired")
		return
	}
	// (b) Identity + RBAC (tenant 400 + gcid 401; then role 403).
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
	// (d) Progress projection for every course in the offering, in ONE read.
	//     Fed by chora-consumption via Pub/Sub; read here intra-delivery.
	progressRows, err := deps.CourseProgress.ListByCourseIDs(ctx, tenantID, o.CourseIDs)
	if err != nil {
		// Fail loud. A dead progress read must never degrade into "0% progress",
		// which is indistinguishable from a cohort that has not started.
		writeError(w, http.StatusInternalServerError, "course progress lookup failed: "+err.Error())
		return
	}
	rowsByCourse := make(map[string][]*courseprogress.CourseLearnerProgress, len(o.CourseIDs))
	for _, p := range progressRows {
		rowsByCourse[p.CourseID] = append(rowsByCourse[p.CourseID], p)
	}

	// (e) Per-course roll-up (offering order preserved). Learner counts +
	//     distinct people come from the roster projection; both intra-delivery.
	//     The roster is also the progress DENOMINATOR: an enrolled learner with
	//     no projection row has genuinely made 0% progress (advanced.v1 only
	//     fires once an atom is completed), so averaging over rows-that-exist
	//     would measure only the learners already doing well.
	courses := make([]map[string]interface{}, 0, len(o.CourseIDs))
	distinct := make(map[string]struct{})
	totalEnrollments := 0
	var offeringStats courseprogress.Stats
	for _, courseID := range o.CourseIDs {
		title := ""
		c, found, err := deps.CourseCJ2.Courses.Get(ctx, tenantID, courseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "course lookup failed: "+err.Error())
			return
		}
		if found && c != nil {
			title = c.Title
		}
		roster, err := deps.Rosters.ListByCourse(ctx, tenantID, courseID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "roster lookup failed: "+err.Error())
			return
		}
		n := roster.LearnerCount()
		totalEnrollments += n
		enrolledGCIDs := make([]string, 0, n)
		for _, l := range roster.Learners {
			distinct[l.GCID] = struct{}{}
			enrolledGCIDs = append(enrolledGCIDs, l.GCID)
		}
		stats := courseprogress.RollCourse(enrolledGCIDs, rowsByCourse[courseID])
		offeringStats = offeringStats.Add(stats)
		courses = append(courses, map[string]interface{}{
			"id":                  courseID,
			"title":               title,
			"enrollments":         n,
			"avg_progress_pct":    stats.AvgProgressPct(),    // nil → null when nobody is enrolled
			"completion_rate_pct": stats.CompletionRatePct(), // nil → null when nobody is enrolled
			"completed_learners":  stats.Completed,
		})
	}
	// (f) Assessment count (paginated; bounded loop).
	assessmentCount, err := countOfferingAssessments(ctx, deps, tenantID, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "assessment count failed: "+err.Error())
		return
	}
	// (g) Capacity utilisation: distinct people / seat budget. Capacity 0 =
	//     unbounded (async) → utilisation is N/A (null), not 0.
	distinctN := len(distinct)
	unbounded := o.Capacity == 0
	var utilisation *int
	if !unbounded {
		pct := int(math.Round(float64(distinctN) / float64(o.Capacity) * 100))
		utilisation = &pct
	}
	// (h) Hand-built snake_case DTO. Lifecycle timestamps are null until set.
	//     avg_progress_pct / completion_rate_pct are null (NOT 0) when the
	//     offering has no enrolments: undefined, not zero.
	dto := map[string]interface{}{
		"state":                    string(o.State),
		"capacity":                 o.Capacity,
		"capacity_unbounded":       unbounded,
		"capacity_utilisation_pct": utilisation, // nil → JSON null when unbounded
		"total_enrollments":        totalEnrollments,
		"distinct_learners":        distinctN,
		"assessment_count":         assessmentCount,
		"avg_progress_pct":         offeringStats.AvgProgressPct(),
		"completion_rate_pct":      offeringStats.CompletionRatePct(),
		"completed_enrollments":    offeringStats.Completed,
		"launched_at":              rfc3339OrNil(o.LaunchedAt),
		"concluded_at":             rfc3339OrNil(o.ConcludedAt),
		"courses":                  courses,
	}
	writeJSON(w, http.StatusOK, dto)
}

// countOfferingAssessments returns the number of assessments attached to the
// offering, paginating ListByOffering (bounded loop) so the count is exact
// beyond a single page. Assessments per offering are few; the cap is a
// runaway backstop, not an expected bound.
func countOfferingAssessments(ctx context.Context, deps Deps, tenantID, offeringID string) (int, error) {
	const pageSize = 100
	count := 0
	token := ""
	for i := 0; i < 100; i++ {
		items, next, err := deps.AssessmentDeps.Assessments.ListByOffering(ctx, tenantID, offeringID, pageSize, token)
		if err != nil {
			return 0, err
		}
		count += len(items)
		if next == "" {
			break
		}
		token = next
	}
	return count, nil
}

// rfc3339OrNil renders a *time.Time as a UTC RFC3339 string, or nil (→ JSON
// null) when the pointer is nil — so an unset lifecycle timestamp is an honest
// null on the wire, not a zero-time string.
func rfc3339OrNil(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
