// incident_report_handler.go — HTTP handlers for the append-only exam-sitting
// incident trail (ADR-191; design doc §4.7). Shares ExamSittingDeps +
// RegisterExamSittingRoutes (exam_sitting_handler.go).
//
// Routes:
//
//	POST /api/v1/exams/sittings/{sittingID}/incidents → file (append-only)
//	GET  /api/v1/exams/sittings/{sittingID}/incidents → audit trail
//
// APPEND-ONLY: there is deliberately NO update/delete route — a record is
// immutable once filed (corrections are new records). The reporter defaults to
// the authenticated caller's gcid when the body omits reported_by_gcid.
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

type fileIncidentReq struct {
	ReportedByGCID string    `json:"reported_by_gcid"`
	CandidateRef   string    `json:"candidate_ref"`
	Kind           string    `json:"kind"`
	Narrative      string    `json:"narrative"`
	OccurredAt     time.Time `json:"occurred_at"`
}

func examIncidentsRootHandler(d *ExamSittingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleIncidentList(d, w, r)
		case http.MethodPost:
			handleIncidentFile(d, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleIncidentFile(d *ExamSittingDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Incidents == nil {
		writeError(w, http.StatusServiceUnavailable, "incidents repo not wired")
		return
	}
	tenantID, callerGCID := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	// ADR-191 exam:incident_file: filing an incident is a proctor's job.
	if !hasSittingOperationsRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks a role permitted to file a sitting incident")
		return
	}
	sittingID := r.PathValue("sittingID")
	var req fileIncidentReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The invigilator filing the report is the caller by default.
	reporter := strings.TrimSpace(req.ReportedByGCID)
	if reporter == "" {
		reporter = callerGCID
	}
	ir, err := exam.NewIncidentReport(exam.NewIncidentReportInput{
		TenantID:       tenantID,
		SittingID:      sittingID,
		ReportedByGCID: reporter,
		CandidateRef:   req.CandidateRef,
		Kind:           exam.IncidentKind(req.Kind),
		Narrative:      req.Narrative,
		OccurredAt:     req.OccurredAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	if err := d.Incidents.Append(ctx, ir); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, incidentDTO(ir))
}

func handleIncidentList(d *ExamSittingDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Incidents == nil {
		writeError(w, http.StatusServiceUnavailable, "incidents repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	sittingID := r.PathValue("sittingID")
	items, err := d.Incidents.ListBySitting(tracing.WithTenantID(r.Context(), tenantID), tenantID, sittingID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, ir := range items {
		out = append(out, incidentDTO(ir))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

func incidentDTO(ir *exam.IncidentReport) map[string]interface{} {
	out := map[string]interface{}{
		"id":               ir.ID,
		"tenant_id":        ir.TenantID,
		"sitting_id":       ir.SittingID,
		"reported_by_gcid": ir.ReportedByGCID,
		"kind":             string(ir.Kind),
		"narrative":        ir.Narrative,
		"occurred_at":      ir.OccurredAt.UTC().Format(time.RFC3339Nano),
		"created_at":       ir.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if ir.CandidateRef != "" {
		out["candidate_ref"] = ir.CandidateRef
	}
	return out
}
