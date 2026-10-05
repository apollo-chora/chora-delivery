// exam_sitting_handler.go — HTTP handlers for the W4 Brick-B operational
// exam-sitting surface (ExamSitting FSM; ADR-190 D2 + ADR-191). Companion
// files: exam_invigilator_handler.go + incident_report_handler.go (they share
// ExamSittingDeps + RegisterExamSittingRoutes defined here).
//
// Routes (mounted by RegisterExamSittingRoutes; called from NewServer per the
// WIRING CRIB, guarded on a non-nil *ExamSittingDeps):
//
//	POST   /api/v1/exams/{examID}/sittings                        → create (SCHEDULED)
//	GET    /api/v1/exams/{examID}/sittings                        → list by exam
//	POST   /api/v1/exams/sittings/{sittingID}/{action}            → transition (open|begin|close|cancel)
//	POST   /api/v1/exams/sittings/{sittingID}/invigilators        → assign      (invigilator handler)
//	GET    /api/v1/exams/sittings/{sittingID}/invigilators        → roster      (invigilator handler)
//	DELETE /api/v1/exams/sittings/{sittingID}/invigilators/{id}   → unassign    (invigilator handler)
//	POST   /api/v1/exams/sittings/{sittingID}/incidents           → file        (incident handler)
//	GET    /api/v1/exams/sittings/{sittingID}/incidents           → audit trail (incident handler)
//
// These Go 1.22 method+wildcard patterns are MORE SPECIFIC than the existing
// /api/v1/exams/ subtree catch-all (examsSubHandler), so they win for these
// paths without a ServeMux conflict — the same coexistence the exam-candidate
// routes rely on.
//
// ROUTE-SHAPE NOTE: create+list are exam-nested (`/exams/{examID}/sittings`);
// the sitting-item operations (transition) + invigilators + incidents live
// under the literal `/exams/sittings/{sittingID}/…` prefix (keyed by the
// sitting's own id — the examID is not needed once the sitting exists). A bare
// `/exams/sittings/{sittingID}` route is deliberately NOT registered: Go 1.22's
// ServeMux flags it as a cross-wildcard conflict with `/exams/{examID}/sittings`
// (an {examID} could be the literal "sittings"). GET-one is served client-side
// off the list; the transition response returns the updated sitting. The
// `{action}` wildcard coexists with the sibling `invigilators`/`incidents`
// literals (literal wins per Go's specificity rule) — verified.
//
// Authorisation (mirrors exam_candidate_handler.go): tenantRequired + the
// caller's X-Tenant-Id (400) + gcid header (401) via callerTenantGCID; every
// WRITE additionally requires an instructor/admin/training-admin role via
// hasExamAdminRole (403 otherwise). Reads require tenant + gcid only.
package httpapi

import (
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// ExamSittingDeps carries the three W4 Brick-B stores. It is nested in the
// shared Deps as `ExamSittingDeps *ExamSittingDeps` (crib) so it composes
// without churning the main struct (mirrors ExamCandidateDeps). A nil store ⇒
// the corresponding routes return 503.
type ExamSittingDeps struct {
	// Sittings persists ExamSitting aggregates. Production wires pg.SittingRepo
	// (chora_delivery.exam_sittings, migration 0048); dev/tests wire
	// inmem.NewSittingRepo().
	Sittings exam.ExamSittingStore
	// Invigilators persists ExamInvigilator assignments (pg.InvigilatorRepo /
	// inmem.NewInvigilatorRepo()).
	Invigilators exam.ExamInvigilatorStore
	// Incidents persists the append-only IncidentReport trail
	// (pg.IncidentRepo / inmem.NewIncidentRepo()).
	Incidents exam.IncidentReportStore
}

// RegisterExamSittingRoutes mounts the Brick-B subtree on mux. Safe no-op when
// d is nil; individual handlers return 503 for a nil backing store.
func RegisterExamSittingRoutes(mux *http.ServeMux, d *ExamSittingDeps) {
	if d == nil {
		return
	}
	mux.HandleFunc("/api/v1/exams/{examID}/sittings", logging(tenantRequired(examSittingsRootHandler(d))))
	// Item ops are nested under {examID} so segment-2 is a distinct literal
	// (sittings vs candidates/forms). The bare /api/v1/exams/sittings/{sittingID}/…
	// shape aliases /api/v1/exams/{examID}/candidates/{gcid} ({examID}=sittings),
	// a Go 1.22 ServeMux registration-time panic. Handlers key off {sittingID};
	// {examID} is path-scoping only (a sitting is globally unique within a tenant).
	mux.HandleFunc("/api/v1/exams/{examID}/sittings/{sittingID}/{action}", logging(tenantRequired(examSittingActionHandler(d))))

	mux.HandleFunc("/api/v1/exams/{examID}/sittings/{sittingID}/invigilators", logging(tenantRequired(examInvigilatorsRootHandler(d))))
	mux.HandleFunc("/api/v1/exams/{examID}/sittings/{sittingID}/invigilators/{invigilatorID}", logging(tenantRequired(examInvigilatorByIDHandler(d))))

	mux.HandleFunc("/api/v1/exams/{examID}/sittings/{sittingID}/incidents", logging(tenantRequired(examIncidentsRootHandler(d))))
}

// -----------------------------------------------------------------------------
// Request DTOs
// -----------------------------------------------------------------------------

type createSittingReq struct {
	ExamFormID string    `json:"exam_form_id"`
	RoomID     string    `json:"room_id"`
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
	Capacity   int       `json:"capacity"`
}

// -----------------------------------------------------------------------------
// Dispatchers
// -----------------------------------------------------------------------------

func examSittingsRootHandler(d *ExamSittingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleSittingList(d, w, r)
		case http.MethodPost:
			handleSittingCreate(d, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func examSittingActionHandler(d *ExamSittingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleSittingTransition(d, w, r)
	}
}

// -----------------------------------------------------------------------------
// Sitting handlers
// -----------------------------------------------------------------------------

func handleSittingCreate(d *ExamSittingDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Sittings == nil {
		writeError(w, http.StatusServiceUnavailable, "sittings repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasExamAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	examID := r.PathValue("examID")
	var req createSittingReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s, err := exam.NewExamSitting(exam.NewExamSittingInput{
		TenantID:   tenantID,
		ExamID:     examID,
		ExamFormID: req.ExamFormID,
		RoomID:     req.RoomID,
		StartsAt:   req.StartsAt,
		EndsAt:     req.EndsAt,
		Capacity:   req.Capacity,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	if err := d.Sittings.Save(ctx, s); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sittingDTO(s))
}

func handleSittingList(d *ExamSittingDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Sittings == nil {
		writeError(w, http.StatusServiceUnavailable, "sittings repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	examID := r.PathValue("examID")
	items, err := d.Sittings.ListByExam(tracing.WithTenantID(r.Context(), tenantID), tenantID, examID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, s := range items {
		out = append(out, sittingDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
}

// resolveSitting loads a tenant-scoped, non-soft-deleted sitting or writes a 404.
func resolveSitting(d *ExamSittingDeps, w http.ResponseWriter, r *http.Request, tenantID string) (*exam.ExamSitting, bool) {
	sittingID := r.PathValue("sittingID")
	s, ok, err := d.Sittings.Get(tracing.WithTenantID(r.Context(), tenantID), sittingID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sitting lookup failed: "+err.Error())
		return nil, false
	}
	if !ok || s == nil || s.TenantID != tenantID || s.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "sitting not found")
		return nil, false
	}
	return s, true
}

func handleSittingTransition(d *ExamSittingDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Sittings == nil {
		writeError(w, http.StatusServiceUnavailable, "sittings repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	// Gated PER ACTION. A PROCTOR opens and closes the sitting it is staffing
	// (ADR-191 exam:sitting_open / exam:sitting_close) but does not `begin`
	// it or `cancel` it: neither is a ratified PROCTOR capability, and the
	// FSM documents cancel as an admin move (sitting.go). Gating the handler
	// instead of the action would hand a proctor both.
	action := r.PathValue("action")
	if !callerMayTransitionSitting(r, action) {
		writeError(w, http.StatusForbidden, "caller lacks a role permitted to drive this sitting transition")
		return
	}
	s, ok := resolveSitting(d, w, r, tenantID)
	if !ok {
		return
	}
	var err error
	switch action {
	case "open":
		err = s.Open()
	case "begin":
		err = s.Begin()
	case "close":
		err = s.Close()
	case "cancel":
		err = s.Cancel()
	default:
		writeError(w, http.StatusBadRequest, "unknown sitting action (want open|begin|close|cancel)")
		return
	}
	if err != nil {
		// Every ExamSitting FSM sentinel maps to 409 (illegal transition).
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	if err := d.Sittings.Save(ctx, s); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sittingDTO(s))
}

// callerMayTransitionSitting reports whether the caller may drive the named
// ExamSitting FSM transition.
//
// open, begin and close are exam admin OR PROCTOR. ADR-191 as amended
// 2026-09-06 grants the proctor exam:sitting_open, exam:sitting_begin and
// exam:sitting_close: it is the person in the room, so opening the check-in
// window, starting the sitting and closing it are all its job. `begin` was
// the sixth capability, added by owner ruling after this gate first shipped
// admitting five.
//
// cancel stays exam admin ONLY. The amendment added one verb, not the set,
// and sitting.go documents cancel as the admin move ("admin cancel before
// opening" / "pre-run"). An unknown action falls here too, so a new FSM verb
// is admin-only until somebody decides otherwise: fail-closed on the
// privilege.
func callerMayTransitionSitting(r *http.Request, action string) bool {
	switch action {
	case "open", "begin", "close":
		return hasSittingOperationsRole(r)
	default:
		return hasExamAdminRole(r)
	}
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

func sittingDTO(s *exam.ExamSitting) map[string]interface{} {
	out := map[string]interface{}{
		"id":         s.ID,
		"tenant_id":  s.TenantID,
		"exam_id":    s.ExamID,
		"starts_at":  s.StartsAt.UTC().Format(time.RFC3339Nano),
		"ends_at":    s.EndsAt.UTC().Format(time.RFC3339Nano),
		"capacity":   s.Capacity,
		"state":      string(s.State),
		"created_at": s.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": s.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if s.ExamFormID != "" {
		out["exam_form_id"] = s.ExamFormID
	}
	if s.RoomID != "" {
		out["room_id"] = s.RoomID
	}
	if s.DeletedAt != nil {
		out["deleted_at"] = s.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
