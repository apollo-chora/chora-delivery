// offering_attendance_handler.go — HTTP handlers for the R+ offering-nested
// Attendance surface (four-mode short delivery_type):
//
//	GET  /api/v1/offerings/{id}/attendance/{sid}  — list a session's marks
//	POST /api/v1/offerings/{id}/attendance        — record/correct a mark
//
// CHO-2186 — the read takes the session id as a PATH SEGMENT. It may NEVER be a
// query param: OWASP CRS 943110 ("Session Fixation: SessionID Parameter Name
// with Off-Domain Referrer") denies any request whose query/body ARGS carry a
// name containing session_id when the Referer is off-domain — and every SPA→API
// call in this platform is cross-subdomain by design (rplus.chora.site →
// api.chora.site), so BOTH halves of that signature hold permanently. Renaming
// the param (offering_session_id, …) does NOT escape it — CRS matches the
// substring. This is a durable platform-wide constraint, not a one-off: a query
// parameter named session_id is unusable anywhere in this API.
//
// The POST is unaffected — it carries session_id in the JSON BODY, which rule
// 1008 does not inspect. That asymmetry is exactly why attendance marks were
// durably WRITTEN and permanently UNREADABLE until this fix.
//
// A session-scoped attendance Record (offering_attendance domain,
// chora_delivery.attendance_records — mig 0037) is a learner's present/absent/
// late/excused mark for one OfferingSession. Upsert is idempotent on
// (tenant,session,gcid) — a re-mark corrects the status. Intra-delivery, cross-
// aggregate by UUID (session_id, gcid), NO cross-DB query, NO event.
//
// Authorisation mirrors the Schedule surface: instructor / admin / training-admin
// (hasOfferingAdminRole) + X-Tenant-Id + gcid (callerTenantGCID). GET/POST only.
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/attendance"
	offeringattendance "github.com/apollo-chora/chora-delivery/internal/domain/offering_attendance"
)

// markAttendanceReq is the POST body (snake_case wire). session_id + gcid +
// status are required; source defaults to manual when blank.
type markAttendanceReq struct {
	SessionID string `json:"session_id"`
	GCID      string `json:"gcid"`
	Status    string `json:"status"`
	Source    string `json:"source"`
}

// handleOfferingListAttendance — GET /api/v1/offerings/{id}/attendance/{sid}.
//
// sessionID arrives from the PATH (router parts[2]), never from the query string
// — see the CHO-2186 note in the file header. A blank sessionID (the bare
// /attendance GET) keeps the pre-existing 400.
func handleOfferingListAttendance(deps Deps, offeringID, sessionID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.OfferingAttendance == nil {
		writeError(w, http.StatusServiceUnavailable, "attendance repo not wired")
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
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id path segment required")
		return
	}
	recs, err := deps.OfferingAttendance.ListBySession(ctx, tenantID, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "attendance list failed: "+err.Error())
		return
	}
	dtos := make([]map[string]interface{}, 0, len(recs))
	for _, rec := range recs {
		dtos = append(dtos, offeringAttendanceDTO(rec))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"records": dtos})
}

// handleOfferingMarkAttendance — POST /api/v1/offerings/{id}/attendance.
func handleOfferingMarkAttendance(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.OfferingAttendance == nil {
		writeError(w, http.StatusServiceUnavailable, "attendance repo not wired")
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
	var req markAttendanceReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Source: blank → NewRecord defaults to manual; a non-blank invalid value 400s.
	var src attendance.Source
	if strings.TrimSpace(req.Source) != "" {
		parsed, perr := attendance.ParseSource(req.Source)
		if perr != nil {
			writeError(w, http.StatusBadRequest, perr.Error())
			return
		}
		src = parsed
	}
	rec, err := offeringattendance.NewRecord(offeringattendance.NewRecordInput{
		TenantID:  tenantID,
		SessionID: req.SessionID,
		GCID:      req.GCID,
		Status:    attendance.Status(req.Status),
		Source:    src,
		Now:       time.Now(),
	})
	if err != nil {
		// Domain guard (blank session/gcid, invalid status) → bad request.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := deps.OfferingAttendance.Upsert(ctx, rec); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, offeringAttendanceDTO(rec))
}

// offeringAttendanceDTO renders a Record for the JSON wire (snake_case).
func offeringAttendanceDTO(rec *offeringattendance.Record) map[string]interface{} {
	return map[string]interface{}{
		"id":          rec.ID,
		"session_id":  rec.SessionID,
		"gcid":        rec.GCID,
		"status":      string(rec.Status),
		"source":      string(rec.Source),
		"recorded_at": rec.RecordedAt.UTC().Format(time.RFC3339),
	}
}
