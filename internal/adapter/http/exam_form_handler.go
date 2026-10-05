// exam_form_handler.go — HTTP handlers for the W4 Brick-1 ExamForm + ExamResult
// surface (ADR-190 D2 Exam bounded context).
//
// Endpoints (nested under the gateway-proxied /api/v1/exams/ subtree; the
// gateway already proxies the whole subtree so NO gateway change is needed):
//
//	POST /api/v1/exams/{examID}/forms                  — create ASSEMBLED form
//	GET  /api/v1/exams/{examID}/forms                  — list (tenant+exam scoped)
//	POST /api/v1/exams/{examID}/forms/{formID}/expose  — ASSEMBLED → EXPOSED
//	POST /api/v1/exams/{examID}/forms/{formID}/retire  — EXPOSED → RETIRED
//	POST /api/v1/exams/{examID}/results                — record result → PASS/FAIL
//
// Wiring (ADD-ONLY, collision-free with the parallel Brick-3 track): the routes
// are registered via RegisterExamFormRoutes using Go 1.22 method+wildcard
// patterns. Those patterns are strictly MORE SPECIFIC than the existing
// /api/v1/exams/ subtree registration (examsSubHandler), so they coexist on one
// mux without a conflict — no edit to exam_handler.go / handlers.go is required
// from this Brick (the parent adds the two Deps fields + the one
// RegisterExamFormRoutes call; see the wiring crib).
//
// Authorisation (mirrors exam_handler.go, verified in-handler NOT at
// middleware): tenantRequired enforces X-Tenant-Id; callerTenantGCID enforces
// gcid on writes (401); hasExamAdminRole gates writes (403). Reads are
// tenant-scoped for any in-tenant caller.
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

// ExamResultEventPublisher is the outcome-event seam (ADR-190 D1): a finalised
// ExamResult publishes chora.delivery.exam_result.released.v1 into the outbox
// for the future outcome-spine / certificate consumer (W6). Narrow port so the
// handler stays decoupled from the events adapter; satisfied by
// *events.ExamResultPublisher (wired in handlers.go / main.go).
type ExamResultEventPublisher interface {
	PublishExamResultReleased(ctx context.Context, actorGCID string, released exam.ExamResultReleased) error
}

// ExamFormDeps is the minimal dependency set for the ExamForm surface. It is
// deliberately separate from the service-wide Deps struct so this Brick's
// handler compiles + tests green without editing the shared registry; the
// parent constructs it from the new Deps fields at wiring time.
type ExamFormDeps struct {
	Forms   exam.ExamFormStore
	Results exam.ExamResultStore

	// Events tees the ExamResultReleased outcome event into the outbox when a
	// result is finalised (ADR-190 D1). Optional: nil ⇒ the durable exam_results
	// row is still written (the source of truth) but the outcome seam stays dark
	// — the owner wires it at integration (see the main.go crib); prod sets it.
	Events ExamResultEventPublisher
}

// RegisterExamFormRoutes mounts the ExamForm + ExamResult routes on mux. Call
// from NewServer after constructing ExamFormDeps from the new Deps fields.
func RegisterExamFormRoutes(mux *http.ServeMux, fd ExamFormDeps) {
	mux.HandleFunc("POST /api/v1/exams/{examID}/forms", logging(tenantRequired(examFormCreateHandler(fd))))
	mux.HandleFunc("GET /api/v1/exams/{examID}/forms", logging(tenantRequired(examFormListHandler(fd))))
	mux.HandleFunc("POST /api/v1/exams/{examID}/forms/{formID}/expose", logging(tenantRequired(examFormTransitionHandler(fd, transitionExpose))))
	mux.HandleFunc("POST /api/v1/exams/{examID}/forms/{formID}/retire", logging(tenantRequired(examFormTransitionHandler(fd, transitionRetire))))
	mux.HandleFunc("POST /api/v1/exams/{examID}/results", logging(tenantRequired(examResultCreateHandler(fd))))
	mux.HandleFunc("GET /api/v1/exams/{examID}/results", logging(tenantRequired(examResultListHandler(fd))))
}

// -----------------------------------------------------------------------------
// Request DTOs
// -----------------------------------------------------------------------------

type pinnedItemReq struct {
	ItemID         string `json:"item_id"`
	AtomRevisionID string `json:"atom_revision_id"`
	Position       int    `json:"position"`
}

type cutScoreReq struct {
	Mode     string  `json:"mode"` // "RAW" | "PERCENT"
	MaxScore int     `json:"max_score"`
	RawMark  int     `json:"raw_mark"`
	Percent  float64 `json:"percent"`
}

type createExamFormReq struct {
	ItemBankID string          `json:"item_bank_id"`
	Items      []pinnedItemReq `json:"items"`
	CutScore   cutScoreReq     `json:"cut_score"`
}

type createExamResultReq struct {
	FormID       string `json:"form_id"`
	CandidateRef string `json:"candidate_ref"`
	RawScore     int    `json:"raw_score"`
}

// -----------------------------------------------------------------------------
// Create form
// -----------------------------------------------------------------------------

func examFormCreateHandler(fd ExamFormDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if fd.Forms == nil {
			writeError(w, http.StatusServiceUnavailable, "exam forms repo not wired")
			return
		}
		tenantID, gcid := callerTenantGCID(w, r)
		if tenantID == "" {
			return
		}
		_ = gcid // captured for future audit / event emission
		if !hasExamAdminRole(r) {
			writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
			return
		}
		examID := r.PathValue("examID")
		var req createExamFormReq
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		f, err := exam.NewExamForm(exam.NewExamFormInput{
			TenantID:   tenantID,
			ExamID:     examID,
			ItemBankID: req.ItemBankID,
		})
		if err != nil {
			writeError(w, examFormStatus(err), err.Error())
			return
		}
		for _, it := range req.Items {
			if err := f.AddItem(exam.PinnedItem{ItemID: it.ItemID, AtomRevisionID: it.AtomRevisionID, Position: it.Position}); err != nil {
				writeError(w, examFormStatus(err), err.Error())
				return
			}
		}
		cut, err := buildCutScore(req.CutScore)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := f.SetCutScore(cut); err != nil {
			writeError(w, examFormStatus(err), err.Error())
			return
		}
		if err := f.Assemble(); err != nil {
			writeError(w, examFormStatus(err), err.Error())
			return
		}
		if err := fd.Forms.Save(tracing.WithTenantID(r.Context(), tenantID), f); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, examFormDTO(f))
	}
}

// buildCutScore constructs a domain CutScore from the request sub-object,
// defaulting to RAW mode when unspecified.
func buildCutScore(in cutScoreReq) (exam.CutScore, error) {
	if in.Mode == string(exam.CutModePercent) {
		return exam.NewPercentCutScore(in.MaxScore, in.Percent)
	}
	return exam.NewRawCutScore(in.MaxScore, in.RawMark)
}

// -----------------------------------------------------------------------------
// List forms
// -----------------------------------------------------------------------------

func examFormListHandler(fd ExamFormDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if fd.Forms == nil {
			writeError(w, http.StatusServiceUnavailable, "exam forms repo not wired")
			return
		}
		tenantID := tenantFromHeader(w, r)
		if tenantID == "" {
			return
		}
		examID := r.PathValue("examID")
		items, err := fd.Forms.ListByExam(tracing.WithTenantID(r.Context(), tenantID), tenantID, examID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]map[string]interface{}, 0, len(items))
		for _, f := range items {
			out = append(out, examFormDTO(f))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
	}
}

// -----------------------------------------------------------------------------
// List results (CHO-2104) — the R+ Results roster
// -----------------------------------------------------------------------------

// examResultListDTO is the roster shape: the stored result fields WITHOUT
// pass_mark. The cut threshold is a grading-event value (released.PassMark) only
// available at record time — it is not persisted on the ExamResult row — so the
// roster surfaces the definitive `outcome` + raw_score/max_score instead.
func examResultListDTO(res *exam.ExamResult) map[string]interface{} {
	return map[string]interface{}{
		"id":            res.ID,
		"tenant_id":     res.TenantID,
		"exam_id":       res.ExamID,
		"exam_form_id":  res.ExamFormID,
		"candidate_ref": res.CandidateRef,
		"raw_score":     res.RawScore,
		"max_score":     res.MaxScore,
		"outcome":       string(res.Outcome),
		"scored_at":     res.ScoredAt.UTC().Format(time.RFC3339Nano),
	}
}

// examResultListHandler serves GET /api/v1/exams/{examID}/results — the tenant's
// results across all forms of an exam, most-recent first. Grades are sensitive:
// tenant + gcid + exam-admin role, RLS-scoped (mirrors the record-result write).
func examResultListHandler(fd ExamFormDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if fd.Results == nil {
			writeError(w, http.StatusServiceUnavailable, "exam results repo not wired")
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
		items, err := fd.Results.ListByExam(tracing.WithTenantID(r.Context(), tenantID), tenantID, examID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]map[string]interface{}, 0, len(items))
		for _, res := range items {
			out = append(out, examResultListDTO(res))
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
	}
}

// -----------------------------------------------------------------------------
// Transitions (expose / retire)
// -----------------------------------------------------------------------------

type formTransition int

const (
	transitionExpose formTransition = iota
	transitionRetire
)

func examFormTransitionHandler(fd ExamFormDeps, tr formTransition) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if fd.Forms == nil {
			writeError(w, http.StatusServiceUnavailable, "exam forms repo not wired")
			return
		}
		tenantID, gcid := callerTenantGCID(w, r)
		if tenantID == "" {
			return
		}
		_ = gcid
		if !hasExamAdminRole(r) {
			writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
			return
		}
		ctx := tracing.WithTenantID(r.Context(), tenantID)
		f, ok, err := loadExamForm(fd, ctx, tenantID, r.PathValue("examID"), r.PathValue("formID"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "exam form lookup failed: "+err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "exam form not found")
			return
		}
		switch tr {
		case transitionExpose:
			err = f.Expose()
		case transitionRetire:
			err = f.Retire()
		}
		if err != nil {
			writeError(w, examFormStatus(err), err.Error())
			return
		}
		if err := fd.Forms.Save(ctx, f); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, examFormDTO(f))
	}
}

// -----------------------------------------------------------------------------
// Record result — the cut-score → PASS/FAIL path
// -----------------------------------------------------------------------------

func examResultCreateHandler(fd ExamFormDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if fd.Forms == nil || fd.Results == nil {
			writeError(w, http.StatusServiceUnavailable, "exam forms/results repo not wired")
			return
		}
		tenantID, gcid := callerTenantGCID(w, r)
		if tenantID == "" {
			return
		}
		if !hasExamAdminRole(r) {
			writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
			return
		}
		var req createExamResultReq
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx := tracing.WithTenantID(r.Context(), tenantID)
		f, ok, err := loadExamForm(fd, ctx, tenantID, r.PathValue("examID"), req.FormID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "exam form lookup failed: "+err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "exam form not found")
			return
		}
		// Grade computes the compliance verdict + the ExamResultReleased event
		// value. The durable ExamResult row is the source of truth; the outcome
		// event (chora.delivery.exam_result.released.v1) is teed into the outbox
		// below for the future outcome-spine / cert consumer (ADR-190 D1).
		res, released, err := f.Grade(req.CandidateRef, req.RawScore)
		if err != nil {
			writeError(w, examFormStatus(err), err.Error())
			return
		}
		if err := fd.Results.Save(ctx, res); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Outcome-event seam: the durable row is committed above (source of
		// truth); tee the finalise event into the outbox. A publish failure is
		// surfaced (fail-loud) — the result is saved but the seam did not durably
		// enqueue, which the caller / ops must see. Prod always wires Events; a
		// nil publisher (minimal wiring) leaves the seam dark by design.
		if fd.Events != nil {
			if perr := fd.Events.PublishExamResultReleased(ctx, gcid, released); perr != nil {
				writeError(w, http.StatusInternalServerError, "exam result recorded but outcome-event publish failed: "+perr.Error())
				return
			}
		}
		writeJSON(w, http.StatusCreated, examResultDTO(res, released.PassMark))
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// loadExamForm resolves a form by id and enforces tenant + exam scoping +
// soft-delete. Returns ok=false (⇒ 404) on any miss so cross-tenant / cross-exam
// probes cannot distinguish "wrong tenant" from "not found".
//
// The error is returned separately (CHO-2184): a lookup that FAILED is not a
// form that is ABSENT, and the caller must 500 rather than 404 on it.
func loadExamForm(fd ExamFormDeps, ctx context.Context, tenantID, examID, formID string) (*exam.ExamForm, bool, error) {
	f, ok, err := fd.Forms.Get(ctx, formID)
	if err != nil {
		return nil, false, err
	}
	if !ok || f == nil || f.TenantID != tenantID || f.ExamID != examID || f.DeletedAt != nil {
		return nil, false, nil
	}
	return f, true, nil
}

// tenantFromHeader mirrors the exam list handler's defensive X-Tenant-Id read
// (tenantRequired already guarantees presence).
func tenantFromHeader(w http.ResponseWriter, r *http.Request) string {
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id required")
		return ""
	}
	return tenantID
}

// examFormStatus maps domain sentinels to HTTP status codes. Validation /
// input errors → 400; state-machine + exposure-lock violations → 409.
func examFormStatus(err error) int {
	switch {
	case errors.Is(err, exam.ErrExamFormTenantRequired),
		errors.Is(err, exam.ErrExamFormExamRequired),
		errors.Is(err, exam.ErrExamFormItemBankRequired),
		errors.Is(err, exam.ErrExamFormItemIDRequired),
		errors.Is(err, exam.ErrExamFormItemRevisionRequired),
		errors.Is(err, exam.ErrExamFormEmpty),
		errors.Is(err, exam.ErrExamFormNoCutScore),
		errors.Is(err, exam.ErrCutScoreMaxInvalid),
		errors.Is(err, exam.ErrCutScoreRawOutOfRange),
		errors.Is(err, exam.ErrCutScorePercentOutOfRange),
		errors.Is(err, exam.ErrCutScoreModeInvalid),
		errors.Is(err, exam.ErrExamResultRawInvalid),
		errors.Is(err, exam.ErrExamResultTenantRequired),
		errors.Is(err, exam.ErrExamResultExamRequired),
		errors.Is(err, exam.ErrExamResultFormRequired),
		errors.Is(err, exam.ErrExamResultCandidateRequired):
		return http.StatusBadRequest
	case errors.Is(err, exam.ErrExamFormExposureLocked),
		errors.Is(err, exam.ErrExamFormNotDraft),
		errors.Is(err, exam.ErrExamFormNotAssembled),
		errors.Is(err, exam.ErrExamFormNotExposed),
		errors.Is(err, exam.ErrExamFormAlreadyExposed),
		errors.Is(err, exam.ErrExamFormRetired):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// -----------------------------------------------------------------------------
// DTOs
// -----------------------------------------------------------------------------

func examFormDTO(f *exam.ExamForm) map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(f.Items))
	for _, it := range f.Items {
		items = append(items, map[string]interface{}{
			"item_id":          it.ItemID,
			"atom_revision_id": it.AtomRevisionID,
			"position":         it.Position,
		})
	}
	out := map[string]interface{}{
		"id":           f.ID,
		"tenant_id":    f.TenantID,
		"exam_id":      f.ExamID,
		"item_bank_id": f.ItemBankID,
		"state":        string(f.State),
		"items":        items,
		"created_at":   f.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":   f.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if f.Cut != nil {
		out["cut_score"] = map[string]interface{}{
			"mode":      string(f.Cut.Mode),
			"max_score": f.Cut.MaxScore,
			"raw_mark":  f.Cut.RawMark,
			"percent":   f.Cut.Percent,
			"pass_mark": f.Cut.PassMark(),
		}
	}
	if f.ExposedAt != nil {
		out["exposed_at"] = f.ExposedAt.UTC().Format(time.RFC3339Nano)
	}
	if f.RetiredAt != nil {
		out["retired_at"] = f.RetiredAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func examResultDTO(res *exam.ExamResult, passMark int) map[string]interface{} {
	return map[string]interface{}{
		"id":            res.ID,
		"tenant_id":     res.TenantID,
		"exam_id":       res.ExamID,
		"exam_form_id":  res.ExamFormID,
		"candidate_ref": res.CandidateRef,
		"raw_score":     res.RawScore,
		"max_score":     res.MaxScore,
		"pass_mark":     passMark,
		"outcome":       string(res.Outcome),
		"scored_at":     res.ScoredAt.UTC().Format(time.RFC3339Nano),
	}
}
