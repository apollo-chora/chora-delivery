// exam_candidate_count_test.go — TDD RED for the derived candidate_count on the
// exam read (CHO-2105). candidate_count reflects seat-occupying candidates
// (ALLOCATED/ID_VERIFIED/ADMITTED), distinct from enrolled_count (self-enrolment).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// newExamServerWithCandidates wires the exam routes PLUS the candidate store so
// the derived candidate_count can be computed.
func newExamServerWithCandidates() (http.Handler, *inmem.CandidateRepo) {
	candRepo := inmem.NewCandidateRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:           inmem.NewCourseRepo(),
		Bookings:          inmem.NewBookingRepo(),
		Exams:             inmem.NewExamRepo(),
		ExamCandidateDeps: &httpapi.ExamCandidateDeps{Candidates: candRepo},
	})
	return srv, candRepo
}

// seedExamCandidate saves a candidate in the requested state via the real FSM.
func seedExamCandidate(t *testing.T, repo *inmem.CandidateRepo, examID, gcid string, state exam.CandidateState) {
	t.Helper()
	c, err := exam.NewCandidate(exam.NewCandidateInput{TenantID: examTestTenantID, ExamID: examID, GCID: gcid})
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	switch state {
	case exam.CandidateStateIDVerified:
		_ = c.MarkVerified()
	case exam.CandidateStateAdmitted:
		_ = c.MarkVerified()
		_ = c.Admit()
	case exam.CandidateStateRejected:
		_ = c.Reject()
	case exam.CandidateStateWithdrawn:
		_ = c.Withdraw()
	case exam.CandidateStateAllocated:
		// default state
	}
	if err := repo.Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func createExamReturningID(t *testing.T, srv http.Handler) string {
	t.Helper()
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup POST exam: %d body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	return created["id"].(string)
}

// AC Projection + No conflation: candidate_count reflects seat-occupying
// candidates; enrolled_count stays the (distinct) self-enrolment figure.
func TestExams_GetByID_CandidateCount_Derived(t *testing.T) {
	srv, candRepo := newExamServerWithCandidates()
	id := createExamReturningID(t, srv)

	// 4 seat-occupying + 2 released.
	seedExamCandidate(t, candRepo, id, "gcid-a", exam.CandidateStateAllocated)
	seedExamCandidate(t, candRepo, id, "gcid-b", exam.CandidateStateAllocated)
	seedExamCandidate(t, candRepo, id, "gcid-c", exam.CandidateStateIDVerified)
	seedExamCandidate(t, candRepo, id, "gcid-d", exam.CandidateStateAdmitted)
	seedExamCandidate(t, candRepo, id, "gcid-e", exam.CandidateStateWithdrawn)
	seedExamCandidate(t, candRepo, id, "gcid-f", exam.CandidateStateRejected)

	rec := doExam(t, srv, "GET", "/api/v1/exams/"+id, "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	if cc, ok := got["candidate_count"].(float64); !ok || int(cc) != 4 {
		t.Errorf("candidate_count=%v want 4 (seat-occupying only)", got["candidate_count"])
	}
	// enrolled_count is the DISTINCT self-enrolment metric — untouched (0 here).
	if ec, ok := got["enrolled_count"].(float64); !ok || int(ec) != 0 {
		t.Errorf("enrolled_count=%v want 0 (distinct; no overwrite)", got["enrolled_count"])
	}
}

// AC Surfaces agree: the list surface carries the same derived candidate_count.
func TestExams_List_CandidateCount_Derived(t *testing.T) {
	srv, candRepo := newExamServerWithCandidates()
	id := createExamReturningID(t, srv)
	seedExamCandidate(t, candRepo, id, "gcid-a", exam.CandidateStateAllocated)
	seedExamCandidate(t, candRepo, id, "gcid-b", exam.CandidateStateAdmitted)

	rec := doExam(t, srv, "GET", "/api/v1/exams", "", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("LIST status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("items=%d want 1", len(items))
	}
	item := items[0].(map[string]interface{})
	if cc, ok := item["candidate_count"].(float64); !ok || int(cc) != 2 {
		t.Errorf("list candidate_count=%v want 2", item["candidate_count"])
	}
}

// Nil-safe: without candidate deps wired, candidate_count defaults to 0.
func TestExams_GetByID_CandidateCount_NilDeps_Zero(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	var created map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	getRec := doExam(t, srv, "GET", "/api/v1/exams/"+id, "", examTestTenantID, examTestAdminGCID, "training-admin")
	var got map[string]interface{}
	_ = json.Unmarshal(getRec.Body.Bytes(), &got)
	if cc, ok := got["candidate_count"].(float64); !ok || int(cc) != 0 {
		t.Errorf("candidate_count=%v want 0 (nil-safe when candidate deps unwired)", got["candidate_count"])
	}
}
