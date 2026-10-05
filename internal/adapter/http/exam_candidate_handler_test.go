// exam_candidate_handler_test.go — TDD (RED-first) for the R+ Exam Candidate
// admission HTTP surface (ADR-190 D2 identity-verified admission).
//
// Endpoints under test (mounted by RegisterExamCandidateRoutes):
//
//	POST /api/v1/exams/{examID}/candidates                 → allocate (ALLOCATED)
//	GET  /api/v1/exams/{examID}/candidates                 → list (roster)
//	GET  /api/v1/exams/{examID}/candidates/{gcid}          → get one
//	POST /api/v1/exams/{examID}/candidates/{gcid}/verify   → mark-verified (claim)
//	POST /api/v1/exams/{examID}/candidates/{gcid}/admit    → admit (GATE)
//
// The ADMISSION GATE: admit refuses (403) unless the candidate holds a VERIFIED
// verification claim. When NO real Identity claim adapter is wired (Claims ==
// nil) the verify + admit routes are DARK (501).
//
// Coverage target: >=60% adapter (per .claude/rules/development-execution.md).
package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

const (
	candTenantID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	candOtherTenant = "019e2f93-d586-71b5-8c3d-e2b0d0d50199"
	candAdminGCID   = "019e2f93-d586-71b5-8c3d-e2b0d0d50101"
	candLearnerGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d50300"
	candExamPathID  = "019e2f93-d586-71b5-8c3d-e2b0d0d50200"
)

// candServer wires the candidate routes on a fresh mux. When claimsWired is
// true a VerificationClaimReader double is attached (verify/admit live);
// otherwise Claims is nil (DARK). The returned double lets tests seed claims.
func candServer(claimsWired bool) (http.Handler, *inmem.CandidateRepo, *inmem.VerificationClaimReader) {
	repo := inmem.NewCandidateRepo()
	d := &httpapi.ExamCandidateDeps{Candidates: repo}
	var claims *inmem.VerificationClaimReader
	if claimsWired {
		claims = inmem.NewVerificationClaimReader()
		d.Claims = claims
	}
	mux := http.NewServeMux()
	httpapi.RegisterExamCandidateRoutes(mux, d)
	return mux, repo, claims
}

func doCand(t *testing.T, h http.Handler, method, path, body, tenantID, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tenantID != "" {
		req.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		req.Header.Set("gcid", gcid)
	}
	if roles != "" {
		req.Header.Set("x-mesh-user-roles", roles)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func allocPath() string  { return "/api/v1/exams/" + candExamPathID + "/candidates" }
func verifyPath() string { return allocPath() + "/" + candLearnerGCID + "/verify" }
func admitPath() string  { return allocPath() + "/" + candLearnerGCID + "/admit" }

func allocBody() string { return `{"gcid":"` + candLearnerGCID + `"}` }

// seedAllocated POSTs an ALLOCATED candidate and asserts 201.
func seedAllocated(t *testing.T, h http.Handler) {
	t.Helper()
	rec := doCand(t, h, "POST", allocPath(), allocBody(), candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed allocate: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Allocate
// -----------------------------------------------------------------------------

func TestCandidate_Allocate_AsAdmin_201(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, "POST", allocPath(), allocBody(), candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "ALLOCATED" {
		t.Errorf("state=%v want ALLOCATED", got["state"])
	}
	if got["verification_status"] != "UNVERIFIED" {
		t.Errorf("verification_status=%v want UNVERIFIED", got["verification_status"])
	}
	if got["gcid"] != candLearnerGCID {
		t.Errorf("gcid=%v want learner gcid", got["gcid"])
	}
	if got["exam_id"] != candExamPathID {
		t.Errorf("exam_id=%v want %s", got["exam_id"], candExamPathID)
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Error("id missing")
	}
}

func TestCandidate_Allocate_AsLearner_403(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, "POST", allocPath(), allocBody(), candTenantID, candAdminGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCandidate_Allocate_NoTenant_400(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, "POST", allocPath(), allocBody(), "", candAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestCandidate_Allocate_NoCallerGCID_401(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, "POST", allocPath(), allocBody(), candTenantID, "", "training-admin")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestCandidate_Allocate_EmptyLearnerGCID_400(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, "POST", allocPath(), `{"gcid":""}`, candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCandidate_Allocate_Duplicate_409(t *testing.T) {
	srv, _, _ := candServer(true)
	seedAllocated(t, srv)
	rec := doCand(t, srv, "POST", allocPath(), allocBody(), candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 (already allocated) body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Verify (mark-verified from the claim)
// -----------------------------------------------------------------------------

func TestCandidate_Verify_ClaimVerified_200(t *testing.T) {
	srv, _, claims := candServer(true)
	seedAllocated(t, srv)
	claims.MarkVerified(candTenantID, candLearnerGCID) // Identity resolved the claim
	rec := doCand(t, srv, "POST", verifyPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "ID_VERIFIED" || got["verification_status"] != "VERIFIED" {
		t.Errorf("state=%v status=%v want ID_VERIFIED/VERIFIED", got["state"], got["verification_status"])
	}
}

func TestCandidate_Verify_ClaimNotVerified_403(t *testing.T) {
	srv, _, _ := candServer(true)
	seedAllocated(t, srv)
	// claim NOT seeded → Identity says not verified → refuse.
	rec := doCand(t, srv, "POST", verifyPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 (claim not verified) body=%s", rec.Code, rec.Body.String())
	}
}

func TestCandidate_Verify_ClaimLookupError_502(t *testing.T) {
	srv, _, claims := candServer(true)
	seedAllocated(t, srv)
	claims.SetError(errors.New("identity upstream 503"))
	rec := doCand(t, srv, "POST", verifyPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want 502 (fail-loud upstream) body=%s", rec.Code, rec.Body.String())
	}
}

func TestCandidate_Verify_CandidateNotFound_404(t *testing.T) {
	srv, _, claims := candServer(true)
	claims.MarkVerified(candTenantID, candLearnerGCID)
	rec := doCand(t, srv, "POST", verifyPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestCandidate_Verify_DarkWhenNoClaimAdapter_501(t *testing.T) {
	srv, _, _ := candServer(false) // Claims nil → DARK
	seedAllocated(t, srv)
	rec := doCand(t, srv, "POST", verifyPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d want 501 (DARK — no real Identity claim adapter) body=%s", rec.Code, rec.Body.String())
	}
}

func TestCandidate_Verify_AsLearner_403(t *testing.T) {
	srv, _, claims := candServer(true)
	seedAllocated(t, srv)
	claims.MarkVerified(candTenantID, candLearnerGCID)
	rec := doCand(t, srv, "POST", verifyPath(), "", candTenantID, candAdminGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 (RBAC)", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Admit (the gate)
// -----------------------------------------------------------------------------

func TestCandidate_Admit_AfterVerify_200(t *testing.T) {
	srv, _, claims := candServer(true)
	seedAllocated(t, srv)
	claims.MarkVerified(candTenantID, candLearnerGCID)
	if rec := doCand(t, srv, "POST", verifyPath(), "", candTenantID, candAdminGCID, "training-admin"); rec.Code != http.StatusOK {
		t.Fatalf("verify setup: %d %s", rec.Code, rec.Body.String())
	}
	rec := doCand(t, srv, "POST", admitPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "ADMITTED" {
		t.Errorf("state=%v want ADMITTED", got["state"])
	}
}

func TestCandidate_Admit_LiveClaimNotVerified_403(t *testing.T) {
	srv, _, _ := candServer(true)
	seedAllocated(t, srv)
	// Never verified; live claim not seeded → admit refused.
	rec := doCand(t, srv, "POST", admitPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestCandidate_Admit_PersistedGate_403(t *testing.T) {
	srv, _, claims := candServer(true)
	seedAllocated(t, srv)
	// Live claim VERIFIED, but the candidate was never marked verified via the
	// verify route → the persisted domain gate still refuses admission.
	claims.MarkVerified(candTenantID, candLearnerGCID)
	rec := doCand(t, srv, "POST", admitPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 (persisted admission gate) body=%s", rec.Code, rec.Body.String())
	}
}

func TestCandidate_Admit_DarkWhenNoClaimAdapter_501(t *testing.T) {
	srv, _, _ := candServer(false) // Claims nil → DARK
	seedAllocated(t, srv)
	rec := doCand(t, srv, "POST", admitPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d want 501 (DARK) body=%s", rec.Code, rec.Body.String())
	}
}

func TestCandidate_Admit_NotFound_404(t *testing.T) {
	srv, _, claims := candServer(true)
	claims.MarkVerified(candTenantID, candLearnerGCID)
	rec := doCand(t, srv, "POST", admitPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// List + get one + isolation
// -----------------------------------------------------------------------------

func TestCandidate_List_ReturnsRoster(t *testing.T) {
	srv, _, _ := candServer(true)
	seedAllocated(t, srv)
	rec := doCand(t, srv, "GET", allocPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, ok := got["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items=%v want 1", got["items"])
	}
}

func TestCandidate_GetOne_OK_And_CrossTenant_404(t *testing.T) {
	srv, _, _ := candServer(true)
	seedAllocated(t, srv)
	getPath := allocPath() + "/" + candLearnerGCID
	if rec := doCand(t, srv, "GET", getPath, "", candTenantID, candAdminGCID, "training-admin"); rec.Code != http.StatusOK {
		t.Fatalf("get one: status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	// Cross-tenant caller must NOT see it.
	if rec := doCand(t, srv, "GET", getPath, "", candOtherTenant, candAdminGCID, "training-admin"); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get: status=%d want 404", rec.Code)
	}
}

func TestCandidate_Root_MethodNotAllowed_405(t *testing.T) {
	srv, _, _ := candServer(true)
	rec := doCand(t, srv, "DELETE", allocPath(), "", candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Route coexistence with the existing /api/v1/exams/ catch-all (crib de-risk)
// -----------------------------------------------------------------------------

// TestCandidate_Routes_CoexistWithExamsCatchAll proves the Go 1.22 wildcard
// candidate patterns register without conflict alongside the existing
// /api/v1/exams/ subtree catch-all (examsSubHandler, owned by exam_handler.go),
// and that the more-specific candidate pattern wins for candidate paths while
// the catch-all still owns /api/v1/exams/{id}. This validates the WIRING CRIB.
func TestCandidate_Routes_CoexistWithExamsCatchAll(t *testing.T) {
	repo := inmem.NewCandidateRepo()
	claims := inmem.NewVerificationClaimReader()
	mux := http.NewServeMux()

	// Stand-in for the real examsSubHandler catch-all (must NOT panic when the
	// candidate wildcard patterns are also registered on the same mux).
	catchAllHit := false
	mux.HandleFunc("/api/v1/exams/", func(w http.ResponseWriter, r *http.Request) {
		catchAllHit = true
		w.WriteHeader(http.StatusTeapot) // distinctive marker
	})

	// Must not panic.
	httpapi.RegisterExamCandidateRoutes(mux, &httpapi.ExamCandidateDeps{Candidates: repo, Claims: claims})

	// Candidate path → candidate handler (201), NOT the catch-all.
	rec := doCand(t, mux, "POST", allocPath(), allocBody(), candTenantID, candAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("candidate route lost to catch-all: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if catchAllHit {
		t.Fatal("candidate path incorrectly reached the /api/v1/exams/ catch-all")
	}

	// A plain /api/v1/exams/{id} still reaches the catch-all (teapot marker).
	rec2 := doCand(t, mux, "GET", "/api/v1/exams/"+candExamPathID, "", candTenantID, candAdminGCID, "training-admin")
	if rec2.Code != http.StatusTeapot || !catchAllHit {
		t.Fatalf("exam-by-id must reach catch-all; status=%d catchAllHit=%v", rec2.Code, catchAllHit)
	}
}
