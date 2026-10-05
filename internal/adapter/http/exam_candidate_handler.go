// exam_candidate_handler.go — HTTP handlers for the R+ Exam Candidate
// admission surface (ADR-190 D2 identity-verified admission).
//
// Routes (mounted by RegisterExamCandidateRoutes; called from NewServer per the
// WIRING CRIB, guarded on a non-nil *ExamCandidateDeps):
//
//	POST /api/v1/exams/{examID}/candidates                → allocate (ALLOCATED)
//	GET  /api/v1/exams/{examID}/candidates                → list (roster)
//	GET  /api/v1/exams/{examID}/candidates/{gcid}         → get one
//	POST /api/v1/exams/{examID}/candidates/{gcid}/verify  → mark-verified (claim)
//	POST /api/v1/exams/{examID}/candidates/{gcid}/admit   → admit (THE GATE)
//
// These are Go 1.22 wildcard patterns — MORE SPECIFIC than the existing
// /api/v1/exams/ subtree catch-all (examsSubHandler, in exam_handler.go), so
// they win for candidate paths without a ServeMux conflict; exam-by-id still
// reaches the catch-all (proven by TestCandidate_Routes_CoexistWithExamsCatchAll).
//
// Authorisation (verified inside the handler, mirroring exam_handler.go):
//   - tenantRequired middleware + callerTenantGCID enforce X-Tenant-Id (400) +
//     the caller's admin gcid header (401).
//   - writes require an instructor/admin/training-admin role via hasExamAdminRole
//     (403 otherwise).
//
// THE ADMISSION GATE (ADR-190 D2, compliance-grade): a candidate is admitted
// only when it holds a VERIFIED Identity verification claim. The claim is
// resolved through the exam.VerificationClaimReader outbound port:
//   - verify consults the port; if not verified it refuses (403); on upstream
//     error it fails loud (502).
//   - admit re-checks the live claim (defence-in-depth) AND enforces the
//     persisted domain gate (candidate.Admit → 403 if unverified / 409 if not
//     admissible).
//
// DARK ROUTE (fail-loud, NO fake): as of 2026-07-09 chora-identity exposes NO
// service-to-service / by-GCID verification-claim endpoint (its KYC surface,
// GET /v1/me/kyc/status, is self/"me"-scoped). So NO real VerificationClaimReader
// adapter is shipped. When Claims is nil the verify + admit routes return 501
// (DARK) rather than pretend. FOLLOW-UP (ADR-190/ADR-193): add an internal
// chora-identity endpoint (e.g. GET /internal/v1/kyc/status?gcid= or an
// IsVerified gRPC, gated to the delivery service SA), wire a real adapter in
// internal/adapter/clients (mirror question_client.go), and inject it as
// ExamCandidateDeps.Claims. allocate + list + get-one are LIVE regardless.
package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// ExamCandidateDeps carries the candidate-admission ports. It is nested in the
// shared Deps as `ExamCandidateDeps *ExamCandidateDeps` (crib) so it composes
// without churning the main struct (mirrors AssessmentDeps).
type ExamCandidateDeps struct {
	// Candidates is the persistence port. Production wires pg.CandidateRepo
	// (chora_delivery.exam_candidates, migration 0047); dev/tests wire
	// inmem.NewCandidateRepo(). Nil ⇒ routes return 503.
	Candidates exam.CandidateStore
	// Claims is the Identity verification-claim outbound port. Nil ⇒ verify +
	// admit are DARK (501). See the DARK ROUTE note above.
	Claims exam.VerificationClaimReader
}

// RegisterExamCandidateRoutes mounts the candidate subtree on mux with the
// standard logging + tenantRequired middleware. Safe no-op when d is nil.
func RegisterExamCandidateRoutes(mux *http.ServeMux, d *ExamCandidateDeps) {
	if d == nil {
		return
	}
	mux.HandleFunc("/api/v1/exams/{examID}/candidates", logging(tenantRequired(examCandidatesRootHandler(d))))
	mux.HandleFunc("/api/v1/exams/{examID}/candidates/{gcid}", logging(tenantRequired(examCandidateGetHandler(d))))
	mux.HandleFunc("/api/v1/exams/{examID}/candidates/{gcid}/verify", logging(tenantRequired(examCandidateVerifyHandler(d))))
	mux.HandleFunc("/api/v1/exams/{examID}/candidates/{gcid}/admit", logging(tenantRequired(examCandidateAdmitHandler(d))))
}

// -----------------------------------------------------------------------------
// Request DTO
// -----------------------------------------------------------------------------

type allocateCandidateReq struct {
	// GCID is the REAL learner GCID being allocated as a candidate (resolved by
	// the caller — resolve-or-register of a NEW user is an Identity-side
	// follow-up, ADR-193). Opaque UUID; no FK.
	GCID string `json:"gcid"`
}

// -----------------------------------------------------------------------------
// Dispatchers
// -----------------------------------------------------------------------------

func examCandidatesRootHandler(d *ExamCandidateDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleCandidateList(d, w, r)
		case http.MethodPost:
			handleCandidateAllocate(d, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func examCandidateGetHandler(d *ExamCandidateDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleCandidateGet(d, w, r)
	}
}

func examCandidateVerifyHandler(d *ExamCandidateDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleCandidateVerify(d, w, r)
	}
}

func examCandidateAdmitHandler(d *ExamCandidateDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handleCandidateAdmit(d, w, r)
	}
}

// -----------------------------------------------------------------------------
// Per-endpoint handlers
// -----------------------------------------------------------------------------

func handleCandidateAllocate(d *ExamCandidateDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Candidates == nil {
		writeError(w, http.StatusServiceUnavailable, "candidates repo not wired")
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
	var req allocateCandidateReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := exam.NewCandidate(exam.NewCandidateInput{
		TenantID: tenantID,
		ExamID:   examID,
		GCID:     req.GCID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	// A duplicate-allocation guard whose read FAILED has not cleared anything.
	// Falling through on error would skip the check entirely and allocate the
	// candidate twice — the guard must fail closed, not open (CHO-2184).
	existing, ok, err := d.Candidates.GetByExamAndGCID(ctx, tenantID, examID, c.GCID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "candidate lookup failed: "+err.Error())
		return
	}
	if ok && existing != nil {
		writeError(w, http.StatusConflict, "candidate already allocated for this exam")
		return
	}
	if err := d.Candidates.Save(ctx, c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, candidateDTO(c))
}

func handleCandidateList(d *ExamCandidateDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Candidates == nil {
		writeError(w, http.StatusServiceUnavailable, "candidates repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	examID := r.PathValue("examID")
	items, err := d.Candidates.ListByExam(tracing.WithTenantID(r.Context(), tenantID), tenantID, examID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, c := range items {
		out = append(out, candidateDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
}

func handleCandidateGet(d *ExamCandidateDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Candidates == nil {
		writeError(w, http.StatusServiceUnavailable, "candidates repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	examID := r.PathValue("examID")
	gcid := r.PathValue("gcid")
	c, ok, err := d.Candidates.GetByExamAndGCID(tracing.WithTenantID(r.Context(), tenantID), tenantID, examID, gcid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "candidate lookup failed: "+err.Error())
		return
	}
	if !ok || c == nil || c.TenantID != tenantID || c.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "candidate not found")
		return
	}
	writeJSON(w, http.StatusOK, candidateDTO(c))
}

func handleCandidateVerify(d *ExamCandidateDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Candidates == nil {
		writeError(w, http.StatusServiceUnavailable, "candidates repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	// ADR-191 exam:sitting_check_in: candidate check-in is a proctor's job.
	if !hasSittingOperationsRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks a role permitted to check a candidate in")
		return
	}
	if d.Claims == nil {
		writeError(w, http.StatusNotImplemented,
			"candidate ID-verification is DARK — no real Identity verification-claim adapter wired (ADR-190 follow-up)")
		return
	}
	examID := r.PathValue("examID")
	gcid := r.PathValue("gcid")
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	c, ok, err := d.Candidates.GetByExamAndGCID(ctx, tenantID, examID, gcid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "candidate lookup failed: "+err.Error())
		return
	}
	if !ok || c == nil {
		writeError(w, http.StatusNotFound, "candidate not found")
		return
	}
	verified, err := d.Claims.IsVerified(ctx, tenantID, gcid)
	if err != nil {
		writeError(w, http.StatusBadGateway, "verification claim lookup failed: "+err.Error())
		return
	}
	if !verified {
		writeError(w, http.StatusForbidden, "identity verification claim not VERIFIED for candidate")
		return
	}
	if err := c.MarkVerified(); err != nil {
		writeCandidateStateError(w, err)
		return
	}
	if err := d.Candidates.Save(ctx, c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, candidateDTO(c))
}

func handleCandidateAdmit(d *ExamCandidateDeps, w http.ResponseWriter, r *http.Request) {
	if d == nil || d.Candidates == nil {
		writeError(w, http.StatusServiceUnavailable, "candidates repo not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	// ADR-191 exam:sitting_check_in: candidate check-in is a proctor's job.
	if !hasSittingOperationsRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks a role permitted to check a candidate in")
		return
	}
	if d.Claims == nil {
		writeError(w, http.StatusNotImplemented,
			"candidate admission gate is DARK — no real Identity verification-claim adapter wired (ADR-190 follow-up)")
		return
	}
	examID := r.PathValue("examID")
	gcid := r.PathValue("gcid")
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	c, ok, err := d.Candidates.GetByExamAndGCID(ctx, tenantID, examID, gcid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "candidate lookup failed: "+err.Error())
		return
	}
	if !ok || c == nil {
		writeError(w, http.StatusNotFound, "candidate not found")
		return
	}
	// Defence-in-depth: re-resolve the LIVE claim at admission time (claims can
	// expire/revoke between verify and admit).
	verified, err := d.Claims.IsVerified(ctx, tenantID, gcid)
	if err != nil {
		writeError(w, http.StatusBadGateway, "verification claim lookup failed: "+err.Error())
		return
	}
	if !verified {
		writeError(w, http.StatusForbidden, "live identity verification claim not VERIFIED — admission refused")
		return
	}
	// Enforce the persisted domain gate.
	if err := c.Admit(); err != nil {
		writeCandidateStateError(w, err)
		return
	}
	if err := d.Candidates.Save(ctx, c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, candidateDTO(c))
}

// -----------------------------------------------------------------------------
// Error mapping
// -----------------------------------------------------------------------------

// writeCandidateStateError maps Candidate FSM sentinels to HTTP codes:
//   - ErrCandidateNotVerified   → 403 (THE admission gate)
//   - everything else (state)   → 409 (illegal transition / not admissible)
func writeCandidateStateError(w http.ResponseWriter, err error) {
	if errors.Is(err, exam.ErrCandidateNotVerified) {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeError(w, http.StatusConflict, err.Error())
}

// -----------------------------------------------------------------------------
// DTO
// -----------------------------------------------------------------------------

// candidateDTO renders a Candidate for the JSON wire.
func candidateDTO(c *exam.Candidate) map[string]interface{} {
	out := map[string]interface{}{
		"id":                  c.ID,
		"tenant_id":           c.TenantID,
		"exam_id":             c.ExamID,
		"gcid":                c.GCID,
		"state":               string(c.State),
		"verification_status": string(c.VerificationStatus),
		"created_at":          c.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":          c.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if c.DeletedAt != nil {
		out["deleted_at"] = c.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
