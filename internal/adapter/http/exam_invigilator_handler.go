// exam_invigilator_handler.go — HTTP handlers for per-sitting invigilator
// assignment (ADR-191 D1 rank + O1 single-chief). Shares ExamSittingDeps +
// RegisterExamSittingRoutes (exam_sitting_handler.go).
//
// Routes:
//
//	POST   /api/v1/exams/sittings/{sittingID}/invigilators                 → assign
//	GET    /api/v1/exams/sittings/{sittingID}/invigilators                 → roster
//	DELETE /api/v1/exams/sittings/{sittingID}/invigilators/{invigilatorID} → unassign (soft-delete)
//
// SINGLE-CHIEF GATE (ADR-191 O1): assign lists the sitting's active
// invigilators and calls exam.EnsureSingleChief BEFORE persisting; a second
// chief is refused with 409. The DB partial-unique index (migration 0048) is
// the concurrency backstop.
package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

type assignInvigilatorReq struct {
	InvigilatorGCID string `json:"invigilator_gcid"`
	Rank            string `json:"rank"`
}

// -----------------------------------------------------------------------------
// Dispatchers
// -----------------------------------------------------------------------------

func examInvigilatorsRootHandler(d *ExamSittingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleInvigilatorList(d, w, r)
		case http.MethodPost:
			handleInvigilatorAssign(d, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func examInvigilatorByIDHandler(d *ExamSittingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleInvigilatorUnassign(d, w, r)
	}
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

func handleInvigilatorAssign(d *ExamSittingDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Invigilators == nil {
		writeError(w, http.StatusServiceUnavailable, "invigilators repo not wired")
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
	sittingID := r.PathValue("sittingID")
	var req assignInvigilatorReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	iv, err := exam.NewExamInvigilator(exam.NewExamInvigilatorInput{
		TenantID:        tenantID,
		SittingID:       sittingID,
		InvigilatorGCID: req.InvigilatorGCID,
		Rank:            exam.InvigilatorRank(req.Rank),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	existing, err := d.Invigilators.ListBySitting(ctx, tenantID, sittingID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// THE single-chief gate (ADR-191 O1).
	if err := exam.EnsureSingleChief(existing, iv); err != nil {
		if errors.Is(err, exam.ErrInvigilatorChiefExists) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := d.Invigilators.Save(ctx, iv); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, invigilatorDTO(iv))
}

func handleInvigilatorList(d *ExamSittingDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Invigilators == nil {
		writeError(w, http.StatusServiceUnavailable, "invigilators repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	sittingID := r.PathValue("sittingID")
	items, err := d.Invigilators.ListBySitting(tracing.WithTenantID(r.Context(), tenantID), tenantID, sittingID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, iv := range items {
		out = append(out, invigilatorDTO(iv))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
}

func handleInvigilatorUnassign(d *ExamSittingDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Invigilators == nil {
		writeError(w, http.StatusServiceUnavailable, "invigilators repo not wired")
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
	sittingID := r.PathValue("sittingID")
	invigilatorID := r.PathValue("invigilatorID")
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	iv, ok, err := d.Invigilators.Get(ctx, invigilatorID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invigilator lookup failed: "+err.Error())
		return
	}
	if !ok || iv == nil || iv.TenantID != tenantID || iv.SittingID != sittingID || iv.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "invigilator assignment not found")
		return
	}
	_ = iv.Unassign()
	if err := d.Invigilators.Save(ctx, iv); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, invigilatorDTO(iv))
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

func invigilatorDTO(iv *exam.ExamInvigilator) map[string]interface{} {
	out := map[string]interface{}{
		"id":               iv.ID,
		"tenant_id":        iv.TenantID,
		"sitting_id":       iv.SittingID,
		"invigilator_gcid": iv.InvigilatorGCID,
		"rank":             string(iv.Rank),
		"assigned_at":      iv.AssignedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":       iv.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if iv.DeletedAt != nil {
		out["deleted_at"] = iv.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
