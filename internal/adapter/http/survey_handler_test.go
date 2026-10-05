// survey_handler_test.go — table-driven tests for the Survey + SurveyResponse
// HTTP surface (Wave-5 R+ /r/surveys build-out).
//
// Coverage target: ≥60% adapter (per .claude/rules/development-execution.md).
//
// TDD: written FIRST then drove the handler in survey_handler.go.
//
// Test matrix:
//
//	POST   /api/v1/surveys                   — create DRAFT
//	GET    /api/v1/surveys[?state=]          — list (tenant-scoped, optional state)
//	GET    /api/v1/surveys/{id}              — single fetch
//	POST   /api/v1/surveys/{id}/publish      — DRAFT → DISTRIBUTED
//	POST   /api/v1/surveys/{id}/close        — DISTRIBUTED → CLOSED
//	POST   /api/v1/surveys/{id}/responses    — learner submission
//	GET    /api/v1/surveys/{id}/responses    — admin lists responses
//
// The domain FSM name is `DISTRIBUTED` (per services/chora-delivery/internal/
// domain/survey/survey.go); the URL action `/publish` maps onto Distribute()
// for instructor-facing terminology. The wire `state` field reflects the
// canonical domain value (DRAFT / DISTRIBUTED / CLOSED).
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	svTestTenantHTTPID    = "019e3000-0000-7000-8000-000000000001"
	svTestCourseHTTPID    = "019e3000-0000-7000-8000-000000000002"
	svTestAuthorHTTPGCID  = "019e3000-0000-7000-9000-000000000001"
	svTestLearner1ID      = "019e3000-0000-7000-9000-000000000002"
	svTestLearner2ID      = "019e3000-0000-7000-9000-000000000003"
	svTestLearnerHTTPGCID = "019e3000-0000-7000-9000-000000000005"
)

// newSurveyServer wires the Survey routes against fresh in-mem adapters.
func newSurveyServer() (http.Handler, *inmem.SurveyRepo) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	repo := inmem.NewSurveyRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      pub,
		Surveys:        repo,
	})
	return srv, repo
}

// doSV issues a request with the standard survey headers.
func doSV(t *testing.T, h http.Handler, method, path, body, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("X-Tenant-Id", svTestTenantHTTPID)
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

// createDraftSurvey is a helper that POSTs a DRAFT survey with one LIKERT
// question + one TEXT question and returns the (id, firstQuestionID,
// secondQuestionID) triple for follow-on tests.
func createDraftSurvey(t *testing.T, srv http.Handler) (string, string, string) {
	t.Helper()
	body := `{
		"course_id": "` + svTestCourseHTTPID + `",
		"title": "Mid-course pulse",
		"questions": [
			{"prompt": "How clear were the slides?", "type": "LIKERT"},
			{"prompt": "What could we improve?", "type": "TEXT"}
		]
	}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys", body, svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("id missing in create response: %s", rec.Body.String())
	}
	qs, _ := got["questions"].([]interface{})
	if len(qs) != 2 {
		t.Fatalf("questions len=%d want 2 body=%s", len(qs), rec.Body.String())
	}
	q1, _ := qs[0].(map[string]interface{})
	q2, _ := qs[1].(map[string]interface{})
	qid1, _ := q1["question_id"].(string)
	qid2, _ := q2["question_id"].(string)
	if qid1 == "" || qid2 == "" {
		t.Fatalf("question_id missing on returned questions: %s", rec.Body.String())
	}
	return id, qid1, qid2
}

// -----------------------------------------------------------------------------
// POST /api/v1/surveys (create DRAFT)
// -----------------------------------------------------------------------------

func TestSV_PostSurvey_AsInstructor_201(t *testing.T) {
	srv, _ := newSurveyServer()
	body := `{
		"course_id": "` + svTestCourseHTTPID + `",
		"title": "Mid-course pulse",
		"questions": [
			{"prompt": "How clear were the slides?", "type": "LIKERT"}
		]
	}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys", body, svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "DRAFT" {
		t.Errorf("state=%v want DRAFT", got["state"])
	}
	if got["course_id"] != svTestCourseHTTPID {
		t.Errorf("course_id=%v want %s", got["course_id"], svTestCourseHTTPID)
	}
	if got["title"] != "Mid-course pulse" {
		t.Errorf("title=%v want 'Mid-course pulse'", got["title"])
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Errorf("id missing")
	}
	if got["response_count"].(float64) != 0 {
		t.Errorf("response_count=%v want 0", got["response_count"])
	}
}

func TestSV_PostSurvey_NoTenantHeader_400(t *testing.T) {
	srv, _ := newSurveyServer()
	req := httptest.NewRequest("POST", "/api/v1/surveys", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("gcid", svTestAuthorHTTPGCID)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestSV_PostSurvey_NoGCIDHeader_401(t *testing.T) {
	srv, _ := newSurveyServer()
	body := `{"course_id":"` + svTestCourseHTTPID + `","title":"T"}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys", body, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestSV_PostSurvey_AsLearner_403(t *testing.T) {
	srv, _ := newSurveyServer()
	body := `{"course_id":"` + svTestCourseHTTPID + `","title":"T"}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys", body, svTestLearnerHTTPGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestSV_PostSurvey_MissingTitle_400(t *testing.T) {
	srv, _ := newSurveyServer()
	body := `{"course_id":"` + svTestCourseHTTPID + `"}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys", body, svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSV_PostSurvey_MissingCourseID_400(t *testing.T) {
	srv, _ := newSurveyServer()
	body := `{"title":"T"}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys", body, svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSV_PostSurvey_InvalidQuestionType_400(t *testing.T) {
	srv, _ := newSurveyServer()
	body := `{
		"course_id": "` + svTestCourseHTTPID + `",
		"title": "T",
		"questions": [{"prompt": "?", "type": "FREE_FORM"}]
	}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys", body, svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/surveys
// -----------------------------------------------------------------------------

func TestSV_ListByTenant_ReturnsItems(t *testing.T) {
	srv, _ := newSurveyServer()
	_, _, _ = createDraftSurvey(t, srv)
	_, _, _ = createDraftSurvey(t, srv)
	rec := doSV(t, srv, "GET", "/api/v1/surveys", "", svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, ok := resp["items"].([]interface{})
	if !ok {
		t.Fatalf("items missing/wrong type body=%s", rec.Body.String())
	}
	if len(items) != 2 {
		t.Errorf("items len=%d want 2", len(items))
	}
}

func TestSV_ListByTenant_StateFilter(t *testing.T) {
	srv, repo := newSurveyServer()
	id, qid1, _ := createDraftSurvey(t, srv)
	_, _, _ = createDraftSurvey(t, srv)
	// Promote first to DISTRIBUTED via the repo (avoids handler RBAC re-entry).
	s, ok, _ := repo.Get(context.Background(), id)
	if !ok {
		t.Fatalf("setup: survey %s missing", id)
	}
	if err := s.Distribute([]string{svTestLearner1ID, svTestLearner2ID}); err != nil {
		t.Fatalf("setup distribute err=%v", err)
	}
	repo.Save(context.Background(), s)
	_ = qid1

	rec := doSV(t, srv, "GET", "/api/v1/surveys?state=DISTRIBUTED", "",
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, _ := resp["items"].([]interface{})
	if len(items) != 1 {
		t.Errorf("DISTRIBUTED items len=%d want 1", len(items))
	}
}

func TestSV_List_InvalidState_400(t *testing.T) {
	srv, _ := newSurveyServer()
	rec := doSV(t, srv, "GET", "/api/v1/surveys?state=GARBAGE", "",
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSV_List_NoTenantHeader_400(t *testing.T) {
	srv, _ := newSurveyServer()
	req := httptest.NewRequest("GET", "/api/v1/surveys", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/surveys/{id}
// -----------------------------------------------------------------------------

func TestSV_GetByID_ReturnsSurvey(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	rec := doSV(t, srv, "GET", "/api/v1/surveys/"+id, "",
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["id"] != id {
		t.Errorf("id=%v want %s", got["id"], id)
	}
}

func TestSV_GetByID_NotFound_404(t *testing.T) {
	srv, _ := newSurveyServer()
	rec := doSV(t, srv, "GET", "/api/v1/surveys/01970000-0000-0000-0000-000000000000", "",
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestSV_GetByID_CrossTenant_404(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	// Issue the GET with a different tenant header.
	req := httptest.NewRequest("GET", "/api/v1/surveys/"+id, nil)
	req.Header.Set("X-Tenant-Id", "different-tenant-id")
	req.Header.Set("gcid", svTestAuthorHTTPGCID)
	req.Header.Set("x-mesh-user-roles", "instructor")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/surveys/{id}/publish (DRAFT → DISTRIBUTED)
// -----------------------------------------------------------------------------

func TestSV_Publish_DraftToDistributed(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	body := `{"recipients": ["` + svTestLearner1ID + `", "` + svTestLearner2ID + `"]}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/publish", body,
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "DISTRIBUTED" {
		t.Errorf("state=%v want DISTRIBUTED", got["state"])
	}
	if got["distributed_at"] == nil {
		t.Errorf("distributed_at missing")
	}
	dt, _ := got["distributed_to"].([]interface{})
	if len(dt) != 2 {
		t.Errorf("distributed_to len=%d want 2", len(dt))
	}
}

func TestSV_Publish_AsLearner_403(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	body := `{"recipients": ["` + svTestLearner1ID + `"]}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/publish", body,
		svTestLearnerHTTPGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestSV_Publish_NoRecipients_400(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	body := `{"recipients": []}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/publish", body,
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSV_Publish_AlreadyDistributed_409(t *testing.T) {
	srv, repo := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	s, _, _ := repo.Get(context.Background(), id)
	_ = s.Distribute([]string{svTestLearner1ID})
	repo.Save(context.Background(), s)

	body := `{"recipients": ["` + svTestLearner1ID + `"]}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/publish", body,
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/surveys/{id}/close (DISTRIBUTED → CLOSED)
// -----------------------------------------------------------------------------

func TestSV_Close_DistributedToClosed(t *testing.T) {
	srv, repo := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	s, _, _ := repo.Get(context.Background(), id)
	_ = s.Distribute([]string{svTestLearner1ID})
	repo.Save(context.Background(), s)

	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/close", "",
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "CLOSED" {
		t.Errorf("state=%v want CLOSED", got["state"])
	}
	if got["closed_at"] == nil {
		t.Errorf("closed_at missing")
	}
}

func TestSV_Close_NotDistributed_409(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/close", "",
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSV_Close_AsLearner_403(t *testing.T) {
	srv, repo := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	s, _, _ := repo.Get(context.Background(), id)
	_ = s.Distribute([]string{svTestLearner1ID})
	repo.Save(context.Background(), s)

	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/close", "",
		svTestLearnerHTTPGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/surveys/{id}/responses
// -----------------------------------------------------------------------------

func TestSV_PostResponse_AsLearner_201(t *testing.T) {
	srv, repo := newSurveyServer()
	id, qid1, qid2 := createDraftSurvey(t, srv)
	s, _, _ := repo.Get(context.Background(), id)
	_ = s.Distribute([]string{svTestLearner1ID, svTestLearner2ID})
	repo.Save(context.Background(), s)

	body := `{
		"answers": [
			{"question_id": "` + qid1 + `", "value": "4"},
			{"question_id": "` + qid2 + `", "value": "slides were great"}
		]
	}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/responses", body,
		svTestLearner1ID, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["survey_id"] != id {
		t.Errorf("survey_id=%v want %s", got["survey_id"], id)
	}
	if got["gcid"] != svTestLearner1ID {
		t.Errorf("gcid=%v want %s", got["gcid"], svTestLearner1ID)
	}
	answers, _ := got["answers"].([]interface{})
	if len(answers) != 2 {
		t.Errorf("answers len=%d want 2", len(answers))
	}
}

func TestSV_PostResponse_SurveyNotDistributed_409(t *testing.T) {
	srv, _ := newSurveyServer()
	id, qid1, _ := createDraftSurvey(t, srv)
	body := `{"answers": [{"question_id": "` + qid1 + `", "value": "3"}]}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/responses", body,
		svTestLearner1ID, "")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSV_PostResponse_LikertOutOfRange_400(t *testing.T) {
	srv, repo := newSurveyServer()
	id, qid1, _ := createDraftSurvey(t, srv)
	s, _, _ := repo.Get(context.Background(), id)
	_ = s.Distribute([]string{svTestLearner1ID})
	repo.Save(context.Background(), s)

	body := `{"answers": [{"question_id": "` + qid1 + `", "value": "9"}]}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/responses", body,
		svTestLearner1ID, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSV_PostResponse_Duplicate_409(t *testing.T) {
	srv, repo := newSurveyServer()
	id, qid1, qid2 := createDraftSurvey(t, srv)
	s, _, _ := repo.Get(context.Background(), id)
	_ = s.Distribute([]string{svTestLearner1ID})
	repo.Save(context.Background(), s)

	body := `{
		"answers": [
			{"question_id": "` + qid1 + `", "value": "5"},
			{"question_id": "` + qid2 + `", "value": "ok"}
		]
	}`
	rec1 := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/responses", body,
		svTestLearner1ID, "")
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first response status=%d want 201 body=%s", rec1.Code, rec1.Body.String())
	}
	rec2 := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/responses", body,
		svTestLearner1ID, "")
	if rec2.Code != http.StatusConflict {
		t.Errorf("dup response status=%d want 409 body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestSV_PostResponse_NoGCID_401(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	body := `{"answers": []}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/responses", body, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/surveys/{id}/responses
// -----------------------------------------------------------------------------

func TestSV_GetResponses_AsAdmin_200(t *testing.T) {
	srv, repo := newSurveyServer()
	id, qid1, _ := createDraftSurvey(t, srv)
	s, _, _ := repo.Get(context.Background(), id)
	_ = s.Distribute([]string{svTestLearner1ID, svTestLearner2ID})
	repo.Save(context.Background(), s)

	// Two learners submit.
	body1 := `{"answers": [{"question_id": "` + qid1 + `", "value": "5"}]}`
	rec1 := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/responses", body1,
		svTestLearner1ID, "")
	if rec1.Code != http.StatusCreated {
		t.Fatalf("setup l1 status=%d", rec1.Code)
	}
	body2 := `{"answers": [{"question_id": "` + qid1 + `", "value": "3"}]}`
	rec2 := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/responses", body2,
		svTestLearner2ID, "")
	if rec2.Code != http.StatusCreated {
		t.Fatalf("setup l2 status=%d", rec2.Code)
	}

	rec := doSV(t, srv, "GET", "/api/v1/surveys/"+id+"/responses", "",
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, _ := resp["items"].([]interface{})
	if len(items) != 2 {
		t.Errorf("items len=%d want 2", len(items))
	}
}

func TestSV_GetResponses_AsLearner_403(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	rec := doSV(t, srv, "GET", "/api/v1/surveys/"+id+"/responses", "",
		svTestLearnerHTTPGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestSV_GetResponses_SurveyNotFound_404(t *testing.T) {
	srv, _ := newSurveyServer()
	rec := doSV(t, srv, "GET", "/api/v1/surveys/01970000-0000-0000-0000-000000000000/responses", "",
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Method-not-allowed / not-found dispatcher edges
// -----------------------------------------------------------------------------

func TestSV_PutSurvey_405(t *testing.T) {
	srv, _ := newSurveyServer()
	rec := doSV(t, srv, "PUT", "/api/v1/surveys", "", svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}

func TestSV_UnknownAction_404(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/garbage", "",
		svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}
