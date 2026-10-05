// exam_handler.go — HTTP handlers for the R+ Exam (proctored sitting)
// admin surface per the R+ Stage 3 wave 2 build-out plan.
//
// 3 endpoints registered via handlers.go integration diff:
//
//	GET  /api/v1/exams           — list current-tenant exams
//	POST /api/v1/exams           — create DRAFT (training-admin/instructor)
//	GET  /api/v1/exams/{id}      — fetch one (tenant-scoped 404)
//
// Authorisation (verified inside the handler, NOT at middleware):
//   - tenantRequired enforces X-Tenant-Id presence (400 otherwise).
//   - gcid header is required on writes (401 if missing) — Bucket 4
//     servicemesh propagation guarantees this when the caller's JWT is valid.
//   - POST: caller carries `instructor`, `admin`, or `training-admin` role
//     (403 otherwise). Mirrors the CJ#2 course pattern.
//   - GET list / GET by-id: any in-tenant caller (tenant-scoped read).
//
// Add-only: the existing Deps struct gains one new field `Exams
// *inmem.ExamRepo` via integration diff; nothing else in this file
// touches handlers.go / cmd/server.
//
// Per the R+ Stage 3 wave 2 build-out brief — closes the BFF coverage gap
// for /api/v1/exams that has been holding a hardcoded fixture in
// chora-web exams.service.ts (CSPO / DSA-101 / Advanced SM).
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// -----------------------------------------------------------------------------
// Request DTO
// -----------------------------------------------------------------------------

type createExamReq struct {
	CourseID        string    `json:"course_id"`
	Title           string    `json:"title"`
	ScheduledAt     time.Time `json:"scheduled_at"`
	DurationMinutes int       `json:"duration_minutes"`
	Capacity        int       `json:"capacity"`
	ProctorMethod   string    `json:"proctor_method"`
}

// -----------------------------------------------------------------------------
// Dispatchers — registered by handlers.go integration diff
// -----------------------------------------------------------------------------

// examsRootHandler dispatches /api/v1/exams (no path suffix).
//
//	GET  → list
//	POST → create DRAFT (RBAC: instructor / admin / training-admin)
func examsRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleExamList(deps, w, r)
		case http.MethodPost:
			handleExamCreate(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// examsSubHandler dispatches /api/v1/exams/{id}.
//
//	GET → fetch one (tenant-scoped 404)
//
// Sub-resources (publish / open / close / grade) are out-of-scope for the
// minimum-aggregate landing — they live on the domain aggregate and will
// surface via follow-on handlers once the FE wires them.
func examsSubHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/exams/")
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" || strings.Contains(rest, "/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		examID := rest
		switch r.Method {
		case http.MethodGet:
			handleExamGet(deps, examID, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleExamCreate(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Exams == nil {
		writeError(w, http.StatusServiceUnavailable, "exams repo not wired")
		return
	}
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid // gcid is captured for future audit / event emission
	if !hasExamAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req createExamReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	e, err := exam.NewExam(exam.NewExamInput{
		TenantID:        tenantID,
		CourseID:        req.CourseID,
		Title:           req.Title,
		ScheduledAt:     req.ScheduledAt,
		DurationMinutes: req.DurationMinutes,
		Capacity:        req.Capacity,
		ProctorMethod:   exam.ProctorMethod(req.ProctorMethod),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// rls.ApplySession (pg adapter) reads the tenant from the context.
	if err := deps.Exams.Save(tracing.WithTenantID(r.Context(), tenantID), e); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, examDTO(e))
}

func handleExamList(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Exams == nil {
		writeError(w, http.StatusServiceUnavailable, "exams repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		// tenantRequired middleware already enforces this, but defensive.
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	items, err := deps.Exams.ListByTenant(ctx, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, e := range items {
		dto := examDTO(e)
		count, err := seatedCandidateCount(ctx, deps, tenantID, e.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		dto["candidate_count"] = count
		if sf, known, err := courseSFEligible(ctx, deps, tenantID, e.CourseID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		} else if known {
			dto["sf_eligible"] = sf
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": out,
	})
}

func handleExamGet(deps Deps, examID string, w http.ResponseWriter, r *http.Request) {
	if deps.Exams == nil {
		writeError(w, http.StatusServiceUnavailable, "exams repo not wired")
		return
	}
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	e, ok, err := deps.Exams.Get(ctx, examID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "exam lookup failed: "+err.Error())
		return
	}
	if !ok || e == nil || e.TenantID != tenantID || e.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "exam not found")
		return
	}
	dto := examDTO(e)
	count, err := seatedCandidateCount(ctx, deps, tenantID, e.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dto["candidate_count"] = count
	writeJSON(w, http.StatusOK, dto)
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

// examDTO renders an Exam for the JSON wire. The FE adapter
// (chora-web exams.service.ts) maps this shape onto ExamSitting.
func examDTO(e *exam.Exam) map[string]interface{} {
	out := map[string]interface{}{
		"id":               e.ID,
		"tenant_id":        e.TenantID,
		"course_id":        e.CourseID,
		"title":            e.Title,
		"scheduled_at":     e.ScheduledAt.UTC().Format(time.RFC3339),
		"duration_minutes": e.DurationMinutes,
		"capacity":         e.Capacity,
		"enrolled_count":   e.EnrolledCount,
		"state":            string(e.State),
		"proctor_method":   string(e.ProctorMethod),
		"created_at":       e.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":       e.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if e.DeletedAt != nil {
		out["deleted_at"] = e.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// seatedCandidateCount derives an exam's candidate-count projection (CHO-2105):
// the number of seat-occupying candidates (active allocations — see
// exam.Candidate.OccupiesSeat) for the exam, in the caller's tenant. Distinct
// from Exam.EnrolledCount (the self-enrolment metric) — the two never overwrite.
//
// Derived at read-time from the same-domain CandidateStore (no cross-DB, no
// migration, no new events). Nil-safe: returns 0 when the candidate store is
// unwired (dev / candidate feature off).
// courseSFEligible resolves an exam's SkillsFuture eligibility from its COURSE
// (R6 D1, the backend half).
//
// The flag is the course's (`delivery.Course.SFEligible`, set at /release) and
// the exam carries only a `course_id`, so the list joins it here rather than
// leaving the frontend to invent one. It previously invented `true`, which
// painted the badge on every sitting in every tenant.
//
// Returns (value, known, error). `known` is FALSE when the course cannot be
// resolved, and the caller then OMITS the field rather than sending `false`:
// absent means "not known", while `false` would assert the course is not
// SkillsFuture funded, which is a different claim and one we cannot make. A
// repo error fails loud, matching seatedCandidateCount; only a clean
// not-found is silent.
func courseSFEligible(ctx context.Context, deps Deps, tenantID, courseID string) (bool, bool, error) {
	if deps.Courses == nil || strings.TrimSpace(courseID) == "" {
		return false, false, nil
	}
	c, ok, err := deps.Courses.Get(ctx, tenantID, courseID)
	if err != nil {
		return false, false, err
	}
	if !ok || c == nil {
		return false, false, nil
	}
	return c.SFEligible, true, nil
}

func seatedCandidateCount(ctx context.Context, deps Deps, tenantID, examID string) (int, error) {
	if deps.ExamCandidateDeps == nil || deps.ExamCandidateDeps.Candidates == nil {
		return 0, nil
	}
	cands, err := deps.ExamCandidateDeps.Candidates.ListByExam(ctx, tenantID, examID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cands {
		if c.OccupiesSeat() {
			n++
		}
	}
	return n, nil
}

// -----------------------------------------------------------------------------
// RBAC helper
// -----------------------------------------------------------------------------

// hasExamAdminRole reports whether the caller carries one of the roles that
// ADMINISTER the exam: authoring forms, scheduling sittings, allocating
// candidates, staffing invigilators. Spelling is normalised in mesh_roles.go.
//
// Mirrors hasInstructorOrAdmin from course_cj2_handler.go but is local to
// the exams surface so the R+ exams track stays self-contained.
//
// ⚠ PROCTOR IS DELIBERATELY ABSENT. This gate guards thirteen call sites, four
// of which are exam-FORM operations: the assembled item set, i.e. the exact
// content ADR-191 D2 embargoes a proctor from. Admitting PROCTOR here to reach
// the four sitting operations it IS entitled to would hand it the other nine.
// Use hasSittingOperationsRole for those instead.
func hasExamAdminRole(r *http.Request) bool {
	return callerHasMeshRole(r, roleInstructor, roleAdmin, roleTrainingAdmin, roleTenantAdmin)
}

// hasSittingOperationsRole reports whether the caller may perform an
// on-the-day sitting OPERATION: check a candidate in, open, begin or close the
// sitting, file an incident. That is the exam admin set PLUS the PROCTOR role
// (ADR-191 D1, "sees the people, not the paper").
//
// The five operations it guards are exactly the five ratified PROCTOR
// capabilities that need a gate change:
//
//	exam:sitting_check_in → handleCandidateVerify + handleCandidateAdmit
//	exam:sitting_open     → handleSittingTransition, action=open
//	exam:sitting_begin    → handleSittingTransition, action=begin
//	exam:sitting_close    → handleSittingTransition, action=close
//	exam:incident_file    → handleIncidentFile
//
// The sixth, exam:roster_view, needs none: the candidate / invigilator list
// reads are gated on tenant + gcid only.
func hasSittingOperationsRole(r *http.Request) bool {
	return hasExamAdminRole(r) || callerHasMeshRole(r, roleProctor)
}

// errExamNotFound — local sentinel for the GET-by-id 404 path. We don't
// surface domain errors directly; the handler maps to standard HTTP codes.
var errExamNotFound = errors.New("exam: not found")
