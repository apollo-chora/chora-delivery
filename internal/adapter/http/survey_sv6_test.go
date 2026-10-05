// survey_sv6_test.go — statement-coverage battery for
// internal/adapter/http/survey_handler.go. Supplements survey_handler_test.go
// + survey_list_scoping_test.go with the remaining reachable branches:
//
//   - surveysSubHandler method guards (405 on /{id}, /publish, /close,
//     /responses) + multi-segment / empty-part 404s
//   - handleSurveyCreate / Publish / ResponseCreate decode-error 400s
//   - Publish / Close / ResponseCreate missing-survey 404s
//   - handleSurveyList fail-closed learner scope with NO gcid
//   - callerGCID X-Chora-GCID fallback (no lowercase gcid header)
//   - handleSurveyResponsesList empty-result 200
//   - surveyDTO MULTIPLE_CHOICE options branch
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// -----------------------------------------------------------------------------
// surveysSubHandler — dispatch guards + 404 shapes
// -----------------------------------------------------------------------------

func TestSv6_SurveysSub_MethodGuards(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)

	// case 1: non-GET on /{id}
	if rec := doSV(t, srv, "PUT", "/api/v1/surveys/"+id, "", svTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /{id}: status=%d want 405 body=%s", rec.Code, rec.Body.String())
	}
	// case 2 publish non-POST
	if rec := doSV(t, srv, "GET", "/api/v1/surveys/"+id+"/publish", "", svTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /{id}/publish: status=%d want 405", rec.Code)
	}
	// case 2 close non-POST
	if rec := doSV(t, srv, "PUT", "/api/v1/surveys/"+id+"/close", "", svTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /{id}/close: status=%d want 405", rec.Code)
	}
	// case 2 responses non-POST/GET
	if rec := doSV(t, srv, "PUT", "/api/v1/surveys/"+id+"/responses", "", svTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /{id}/responses: status=%d want 405", rec.Code)
	}
	// strip trailing slash: /api/v1/surveys/ → rest == "" → 404
	if rec := doSV(t, srv, "GET", "/api/v1/surveys/", "", svTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/surveys/: status=%d want 404", rec.Code)
	}
	// 3+ segments → 404
	if rec := doSV(t, srv, "GET", "/api/v1/surveys/a/b/c", "", svTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /a/b/c: status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// handleSurveyCreate — decode-error 400
// -----------------------------------------------------------------------------

func TestSv6_SurveyCreate_BadJSON_400(t *testing.T) {
	srv, _ := newSurveyServer()
	rec := doSV(t, srv, "POST", "/api/v1/surveys", `{not-json`, svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// surveyDTO options branch — MULTIPLE_CHOICE question with Options round-trips
// through the create response.
func TestSv6_SurveyCreate_MultipleChoice_RoundTrip(t *testing.T) {
	srv, _ := newSurveyServer()
	body := `{
		"course_id": "` + svTestCourseHTTPID + `",
		"title": "MCQ survey",
		"questions": [
			{"prompt": "Pick a theme", "type": "MULTIPLE_CHOICE", "options": ["A", "B"]}
		]
	}`
	rec := doSV(t, srv, "POST", "/api/v1/surveys", body, svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	qs, _ := got["questions"].([]interface{})
	if len(qs) != 1 {
		t.Fatalf("questions len=%d want 1 body=%s", len(qs), rec.Body.String())
	}
	q := qs[0].(map[string]interface{})
	opts, _ := q["options"].([]interface{})
	if len(opts) != 2 {
		t.Errorf("options len=%d want 2 (DTO options branch)", len(opts))
	}
}

// -----------------------------------------------------------------------------
// handleSurveyList — learner scope fail-closed on missing gcid
// -----------------------------------------------------------------------------

func TestSv6_SurveyList_NoGCID_FailsClosedEmpty(t *testing.T) {
	// A caller with NO gcid at all must see nothing, never the tenant's
	// surveys — surveysVisibleToLearner returns early on an empty identity.
	srv, _ := newSurveyServer()
	_, _, _ = createDraftSurvey(t, srv)

	rec := doSV(t, srv, "GET", "/api/v1/surveys", "", "", "") // no roles, no gcid
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, _ := resp["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("items len=%d want 0 (empty-gcid scope must fail closed)", len(items))
	}
}

// callerGCID fallback: no lowercase `gcid` header → X-Chora-GCID is used to
// scope the learner list.
func TestSv6_SurveyList_XChoraGCIDFallback_ScopesLearner(t *testing.T) {
	srv, repo := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	_, _, _ = createDraftSurvey(t, srv)

	s, _, _ := repo.Get(context.Background(), id)
	if err := s.Distribute([]string{svTestLearner1ID}); err != nil {
		t.Fatalf("setup distribute: %v", err)
	}
	repo.Save(context.Background(), s)

	req := httptest.NewRequest("GET", "/api/v1/surveys", nil)
	req.Header.Set("X-Tenant-Id", svTestTenantHTTPID)
	req.Header.Set("X-Chora-GCID", svTestLearner1ID) // no `gcid` header
	req.Header.Set("x-mesh-user-roles", "learner")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, _ := resp["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("X-Chora-GCID fallback scope: want 1 survey, got %d", len(items))
	}
}

// -----------------------------------------------------------------------------
// handleSurveyPublish / Close — decode errors + missing survey
// -----------------------------------------------------------------------------

func TestSv6_SurveyPublish_BadJSON_400(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/publish", `{oops`, svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_SurveyPublish_UnknownSurvey_404(t *testing.T) {
	srv, _ := newSurveyServer()
	rec := doSV(t, srv, "POST", "/api/v1/surveys/no-such-survey/publish", `{"recipients":["x"]}`, svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_SurveyClose_UnknownSurvey_404(t *testing.T) {
	srv, _ := newSurveyServer()
	rec := doSV(t, srv, "POST", "/api/v1/surveys/no-such-survey/close", "", svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleSurveyResponseCreate — missing survey + decode error
// -----------------------------------------------------------------------------

func TestSv6_SurveyResponseCreate_UnknownSurvey_404(t *testing.T) {
	srv, _ := newSurveyServer()
	rec := doSV(t, srv, "POST", "/api/v1/surveys/no-such-survey/responses", `{"answers":[]}`, svTestLearner1ID, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_SurveyResponseCreate_BadJSON_400(t *testing.T) {
	srv, _ := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	rec := doSV(t, srv, "POST", "/api/v1/surveys/"+id+"/responses", `{oops`, svTestLearner1ID, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleSurveyResponsesList — empty result renders []
// -----------------------------------------------------------------------------

func TestSv6_SurveyResponsesList_NoResponses_EmptyItems(t *testing.T) {
	srv, repo := newSurveyServer()
	id, _, _ := createDraftSurvey(t, srv)
	s, _, _ := repo.Get(context.Background(), id)
	if err := s.Distribute([]string{svTestLearner1ID}); err != nil {
		t.Fatalf("setup distribute: %v", err)
	}
	repo.Save(context.Background(), s)

	rec := doSV(t, srv, "GET", "/api/v1/surveys/"+id+"/responses", "", svTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, _ := resp["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("items len=%d want 0", len(items))
	}
}
