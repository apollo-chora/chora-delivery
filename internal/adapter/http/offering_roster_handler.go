// offering_roster_handler.go — HTTP handler for the R+ offering-nested Roster
// surface: a READ-ONLY per-course roster of the learners enrolled across an
// Offering's attached courses.
//
// Endpoint (dispatched from offeringsSubHandler in offering_handler.go):
//
//	GET /api/v1/offerings/{id}/roster  — this offering's attached-course
//	                                     rosters (object-derived tab; graduate + short)
//
// Intra-chora_delivery only (Offering + CJ#2 Course + Enrollment share the DB
// per ddd-enforcement #3) — NO cross-DB query, NO new aggregate, NO migration,
// NO new event. Learners come from the SAME rostering.CourseRosterRepo the
// standalone /api/v1/rosters/{courseId} view uses (a projection over the
// canonical EnrollmentPort); course titles from the SAME deps.CourseCJ2.Courses
// port the Curriculum + Certification surfaces use.
//
// display_name falls back to the GCID and progress_pct to 0 today — the
// upstream identity (display name) + consumption (progress) projections are
// unwired, so the roster fails VISIBLE per feedback_no_stubs_real_wiring rather
// than fabricating a name. Learner name/progress enrichment is a FLAGGED
// Pub/Sub follow-up, NEVER a cross-DB join.
//
// A course with no enrolments yields an EMPTY learners list (200, learners: []),
// not an error. The offering-level distinct_learner_count counts PEOPLE (unique
// GCIDs across all attached courses), so a learner enrolled in two attached
// courses counts once there even though each course's learner_count includes
// them.
//
// Authorisation mirrors the Curriculum + Certification surfaces: instructor /
// training-admin (hasInstructorRole) + X-Tenant-Id (400) + gcid (401) —
// role-driven VISIBILITY, R+ (Rhythm+) audience. Read-only (GET); the
// dispatcher 405s any other method.
package httpapi

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/domain/rostering"
)

// enrollOfferingReq is the POST /roster body (R+ Phase-2 S3): admin-enrol one
// learner (by GCID) into one of the offering's attached courses.
type enrollOfferingReq struct {
	CourseID string `json:"course_id"`
	GCID     string `json:"gcid"`
}

// handleOfferingEnroll — POST /api/v1/offerings/{id}/roster.
//
// Admin-enrol a learner into one attached course. Steps: verify wiring +
// tenant/gcid/role; load the offering (404); validate course_id ∈ CourseIDs
// (400) + gcid present (400); enforce the offering capacity per attached course
// (409 when a NEW enrolment would exceed a non-zero Capacity — a re-enrol of an
// existing learner is idempotent + never blocked); then Register (idempotent on
// the natural key) → 201. Reuses the canonical EnrollmentPort (same rows the
// per-course roster projects) — the Course still owns enrolment; the offering
// endpoint is a validated proxy. No cross-DB, no migration.
func handleOfferingEnroll(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.Enrollments == nil {
		writeError(w, http.StatusServiceUnavailable, "enrollments repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
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
	var req enrollOfferingReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.GCID) == "" {
		writeError(w, http.StatusBadRequest, "gcid required")
		return
	}
	if !offeringHasCourse(o, req.CourseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return
	}
	// Capacity gate — only a NEW enrolment can exceed the cap (0 = unbounded);
	// a re-enrol of an already-enrolled learner is idempotent + always allowed.
	_, existed, gerr := deps.Enrollments.GetByCourseAndGCID(ctx, tenantID, req.CourseID, req.GCID)
	if gerr != nil {
		writeError(w, http.StatusInternalServerError, "enrolment lookup failed: "+gerr.Error())
		return
	}
	if !existed && o.Capacity > 0 {
		count, cerr := deps.Enrollments.CountByCourse(ctx, tenantID, req.CourseID)
		if cerr != nil {
			writeError(w, http.StatusInternalServerError, "enrolment count failed: "+cerr.Error())
			return
		}
		if count >= o.Capacity {
			writeError(w, http.StatusConflict, "offering is at capacity")
			return
		}
	}
	e, err := deps.Enrollments.Register(ctx, tenantID, req.CourseID, req.GCID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// CHO-2152 — emit enrollment.created ONLY AFTER the row is registered, and
	// only for a genuinely NEW enrolment (`existed` is already computed above
	// for the capacity gate). chora-consumption bootstraps the learner's
	// LearningPath off this topic: without it an admin-enrolled learner can
	// never open the course (A+ spins on "Setting up your learning path…").
	// Mirrors the A+ self-enrol (v1_handlers.go) + the payments subscriber, and
	// closes the asymmetry with handleOfferingRosterRemove below, which has
	// always published its cancellation. A publish failure must NOT fail the
	// request — the enrolment row is already committed, and a client retry
	// would find existed=true and never re-emit, permanently orphaning the
	// event. Loud-log instead (the outbox tee owns retry/dead-letter).
	if !existed && deps.Publisher != nil {
		if _, perr := deps.Publisher.PublishEnrollmentCreated(events.EnrollmentCreated{
			TenantID:     e.TenantID,
			GCID:         e.GCID,
			EnrollmentID: e.ID,
			CourseID:     e.CourseID,
			LearnerGCID:  e.GCID,
			Traceparent:  r.Header.Get("traceparent"),
		}); perr != nil {
			log.Printf("delivery: roster enrol: publish %s failed course=%s gcid=%s err=%v",
				events.TopicEnrollmentCreated, e.CourseID, e.GCID, perr)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"enrollment_id": e.ID,
		"course_id":     e.CourseID,
		"gcid":          e.GCID,
		"status":        string(e.Status),
		"enrolled_at":   e.EnrolledAt.UTC().Format(time.RFC3339),
	})
}

// handleOfferingRosterRemove — POST /api/v1/offerings/{id}/roster/remove.
//
// Admin-unenrol a learner (by GCID) from one of the offering's attached
// courses — the cancel sibling of handleOfferingEnroll. It MIRRORS the enrol
// handler's validation shape: verify wiring + tenant/gcid/role; load the
// offering (404); validate course_id ∈ CourseIDs (400) + gcid present (400);
// then look up the learner's enrolment on that course and soft-delete it via
// the canonical EnrollmentPort.Cancel (the same rows the per-course roster
// projects). A missing enrolment is 404. The Course still owns enrolment; the
// offering endpoint is a validated proxy. No cross-DB, no migration, no new
// event — it emits the SAME chora.delivery.enrollment.cancelled.v1 as the V1
// DELETE cancel path so downstream subscribers act identically. 204 on success.
func handleOfferingRosterRemove(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.Enrollments == nil {
		writeError(w, http.StatusServiceUnavailable, "enrollments repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
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
	var req enrollOfferingReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.GCID) == "" {
		writeError(w, http.StatusBadRequest, "gcid required")
		return
	}
	if !offeringHasCourse(o, req.CourseID) {
		writeError(w, http.StatusBadRequest, "course_id is not attached to this offering")
		return
	}
	// Resolve the learner's enrolment on the attached course (natural key).
	e, existed, gerr := deps.Enrollments.GetByCourseAndGCID(ctx, tenantID, req.CourseID, req.GCID)
	if gerr != nil {
		writeError(w, http.StatusInternalServerError, "enrolment lookup failed: "+gerr.Error())
		return
	}
	if !existed || e == nil {
		writeError(w, http.StatusNotFound, "enrolment not found")
		return
	}
	// Persist the soft-delete via the port (fail loud — never 204 a no-op).
	if cerr := deps.Enrollments.Cancel(ctx, tenantID, e.ID); cerr != nil {
		writeError(w, http.StatusInternalServerError, "enrolment cancel failed: "+cerr.Error())
		return
	}
	// Emit the cancellation event only AFTER the row is soft-deleted.
	if deps.Publisher != nil {
		_, _ = deps.Publisher.PublishEnrollmentCancelled(events.EnrollmentCancelled{
			TenantID:     e.TenantID,
			GCID:         e.GCID,
			EnrollmentID: e.ID,
			CourseID:     e.CourseID,
			LearnerGCID:  e.GCID,
			Reason:       "admin-initiated",
			Traceparent:  r.Header.Get("traceparent"),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleOfferingGetRoster — GET /api/v1/offerings/{id}/roster.
//
// Steps:
//
//	(a) verify wiring (offerings repo + roster repo + CJ#2 course port);
//	(b) enforce tenant (400) + gcid (401) + instructor/admin role (403);
//	(c) load the offering for this tenant (404 if missing / cross-tenant /
//	    soft-deleted), mirroring offering_certification_handler.go;
//	(d) for each o.CourseIDs entry (offering order preserved) resolve the course
//	    title (CJ#2 Course) + materialise its roster (CourseRosterRepo),
//	    accumulating unique GCIDs for the offering-level people count;
//	(e) write a hand-built snake_case DTO
//	    { "courses": [ { "id", "title", "learners":[{gcid,display_name,
//	      progress_pct,enrolled_at}], "learner_count" } ],
//	      "distinct_learner_count" }.
func handleOfferingGetRoster(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
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
	// (d) Resolve each attached course's title + materialise its roster
	//     (offering order preserved). Both reads are intra-chora_delivery.
	//     `distinct` tracks unique learner GCIDs across all courses.
	courses := make([]map[string]interface{}, 0, len(o.CourseIDs))
	distinct := make(map[string]struct{})
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
			// Bubble repo errors as 5xx so the real cause surfaces in logs
			// rather than masquerading as an empty roster.
			writeError(w, http.StatusInternalServerError, "roster lookup failed: "+err.Error())
			return
		}
		courses = append(courses, map[string]interface{}{
			"id":            courseID,
			"title":         title,
			"learners":      offeringRosterLearners(roster, distinct),
			"learner_count": roster.LearnerCount(),
		})
	}
	// (e) Hand-built snake_case wire DTO.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"courses":                courses,
		"distinct_learner_count": len(distinct),
	})
}

// offeringRosterLearners renders a CourseRoster's learner rows as snake_case
// wire maps (mirroring courseRosterDTO's learner block in roster_handler.go)
// and records each GCID in `distinct` for the offering-level people count. The
// slice is always non-nil so an empty roster marshals to `[]`, never `null`
// (FE contract: empty learners array, not a placeholder).
func offeringRosterLearners(roster *rostering.CourseRoster, distinct map[string]struct{}) []map[string]interface{} {
	learners := make([]map[string]interface{}, 0, len(roster.Learners))
	for _, l := range roster.Learners {
		distinct[l.GCID] = struct{}{}
		learners = append(learners, map[string]interface{}{
			"gcid":         l.GCID,
			"display_name": l.DisplayName,
			"progress_pct": l.ProgressPct,
			"enrolled_at":  l.EnrolledAt.UTC().Format(time.RFC3339),
		})
	}
	return learners
}
