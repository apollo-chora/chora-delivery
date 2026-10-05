// live_quiz_handler_test.go — TDD coverage for the R+ M9 LiveQuiz HTTP
// CRUD surface (Stage C-lite wave-5).
//
// Endpoints under test:
//
//	POST  /api/v1/live-quizzes              → create DRAFT (instructor/admin)
//	GET   /api/v1/live-quizzes              → list (current tenant)
//	GET   /api/v1/live-quizzes/{id}         → fetch one (tenant-scoped 404)
//	PATCH /api/v1/live-quizzes/{id}         → EditDraft (DRAFT-only; 409 otherwise)
//	POST  /api/v1/live-quizzes/{id}/publish → DRAFT → PUBLISHED transition
//
// Coverage target: >=60% adapter (per .claude/rules/development-execution.md).
// Pattern mirrors exam_handler_test.go (composer table + same setup wiring).
//
// Per `feedback_strict_tdd` — written BEFORE live_quiz_handler.go: tests MUST
// fail at compile time until the implementation lands (GREEN phase).
package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

// timeRef returns a stable time pointer used by the FSM-progression tests.
func timeRef() time.Time { return time.Now().UTC() }

const (
	lqTestTenantID    = "019e2f93-d586-71b5-8c3d-e2b0d0d70100"
	lqTestOtherTenant = "019e2f93-d586-71b5-8c3d-e2b0d0d70199"
	lqTestInstructor  = "019e2f93-d586-71b5-8c3d-e2b0d0d70101"
	lqTestLearner     = "019e2f93-d586-71b5-8c3d-e2b0d0d70102"
	lqTestCourseID    = "019e2f93-d586-71b5-8c3d-e2b0d0d70200"
)

// newLiveQuizServer wires the LiveQuiz routes against a fresh in-mem repo
// per test. Other Deps stay default so the rest of the mux still mounts.
func newLiveQuizServer() (http.Handler, *inmem.LiveQuizRepo) {
	repo := inmem.NewLiveQuizRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:     inmem.NewCourseRepo(),
		Bookings:    inmem.NewBookingRepo(),
		LiveQuizzes: repo,
	})
	return srv, repo
}

// doLiveQuiz issues a request with the standard headers for live-quiz routes.
func doLiveQuiz(t *testing.T, h http.Handler, method, path, body, tenantID, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
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

// createDefaultLiveQuizBody returns a minimal valid create-live-quiz body.
func createDefaultLiveQuizBody() string {
	return `{
		"course_id": "` + lqTestCourseID + `",
		"title": "CSPO Sprint Planning"
	}`
}

// validQuestionsArr is a JSON-encoded slice of 2 valid MCQ questions, used by
// the PATCH (EditDraft) and Publish coverage.
func validQuestionsArr() string {
	return `[
		{
			"question_id": "q1",
			"prompt": "Which Scrum ceremony kicks off a Sprint?",
			"timer_seconds": 60,
			"points": 10,
			"options": [
				{"label": "Sprint Review", "is_correct": false},
				{"label": "Sprint Planning", "is_correct": true},
				{"label": "Daily Scrum", "is_correct": false},
				{"label": "Retrospective", "is_correct": false}
			]
		},
		{
			"question_id": "q2",
			"prompt": "Who owns the Product Backlog?",
			"timer_seconds": 60,
			"points": 10,
			"options": [
				{"label": "Scrum Master", "is_correct": false},
				{"label": "Stakeholders", "is_correct": false},
				{"label": "Product Owner", "is_correct": true},
				{"label": "Developers", "is_correct": false}
			]
		}
	]`
}

// -----------------------------------------------------------------------------
// POST /api/v1/live-quizzes — create DRAFT
// -----------------------------------------------------------------------------

func TestLiveQuizzes_PostLiveQuiz_AsInstructor_201(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "DRAFT" {
		t.Errorf("state=%v want DRAFT", got["state"])
	}
	if got["tenant_id"] != lqTestTenantID {
		t.Errorf("tenant_id=%v want %s", got["tenant_id"], lqTestTenantID)
	}
	if got["title"] != "CSPO Sprint Planning" {
		t.Errorf("title=%v", got["title"])
	}
	if got["instructor_gcid"] != lqTestInstructor {
		t.Errorf("instructor_gcid=%v want %s", got["instructor_gcid"], lqTestInstructor)
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Errorf("id missing")
	}
}

func TestLiveQuizzes_PostLiveQuiz_AsTrainingAdmin_201(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLiveQuizzes_PostLiveQuiz_AsLearner_403(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestLearner, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLiveQuizzes_PostLiveQuiz_NoTenantHeader_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), "", lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestLiveQuizzes_PostLiveQuiz_NoGCID_401(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestLiveQuizzes_PostLiveQuiz_MissingTitle_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	body := `{
		"course_id": "` + lqTestCourseID + `",
		"title": ""
	}`
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		body, lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLiveQuizzes_PostLiveQuiz_BadJSON_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		`{not-valid`, lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/live-quizzes (list)
// -----------------------------------------------------------------------------

func TestLiveQuizzes_ListLiveQuizzes_EmptyTenant_OK(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, ok := got["items"].([]interface{})
	if !ok {
		t.Fatalf("items missing or wrong type: %v", got)
	}
	if len(items) != 0 {
		t.Errorf("len(items)=%d want 0", len(items))
	}
}

func TestLiveQuizzes_ListLiveQuizzes_ReturnsCreatedRows(t *testing.T) {
	srv, _ := newLiveQuizServer()
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	body2 := `{
		"course_id": "` + lqTestCourseID + `",
		"title": "Daily Scrum Drill"
	}`
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		body2, lqTestTenantID, lqTestInstructor, "instructor")

	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("len(items)=%d want 2 body=%s", len(items), rec.Body.String())
	}
}

func TestLiveQuizzes_ListLiveQuizzes_ScopesByTenant(t *testing.T) {
	srv, _ := newLiveQuizServer()
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	// Cross-tenant list MUST be empty.
	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes",
		"", lqTestOtherTenant, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("cross-tenant leak: len(items)=%d want 0", len(items))
	}
}

func TestLiveQuizzes_ListLiveQuizzes_NoTenantHeader_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes",
		"", "", lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/live-quizzes?q=<term> — case-insensitive title substring filter
// (CHO-2134). Mirrors the test-sets `?q=` semantics: blank ⇒ all; non-blank ⇒
// case-insensitive substring on the quiz title; always tenant-scoped.
// -----------------------------------------------------------------------------

// liveQuizBodyTitled builds a minimal valid create body with a custom title.
func liveQuizBodyTitled(title string) string {
	return `{
		"course_id": "` + lqTestCourseID + `",
		"title": "` + title + `"
	}`
}

func TestLiveQuizzes_ListLiveQuizzes_FilterByQ_MatchesSubset(t *testing.T) {
	srv, _ := newLiveQuizServer()
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("CSPO Sprint Planning"), lqTestTenantID, lqTestInstructor, "instructor")
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("Daily Scrum Drill"), lqTestTenantID, lqTestInstructor, "instructor")

	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes?q=daily",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("len(items)=%d want 1 (only 'Daily Scrum Drill') body=%s", len(items), rec.Body.String())
	}
	if title := items[0].(map[string]interface{})["title"]; title != "Daily Scrum Drill" {
		t.Errorf("title=%v want 'Daily Scrum Drill'", title)
	}
}

func TestLiveQuizzes_ListLiveQuizzes_FilterByQ_BlankReturnsAll(t *testing.T) {
	srv, _ := newLiveQuizServer()
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("CSPO Sprint Planning"), lqTestTenantID, lqTestInstructor, "instructor")
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("Daily Scrum Drill"), lqTestTenantID, lqTestInstructor, "instructor")

	// Explicit blank q ⇒ unchanged behaviour (all tenant rows).
	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes?q=",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 2 {
		t.Errorf("blank q: len(items)=%d want 2 (all rows)", len(items))
	}
}

func TestLiveQuizzes_ListLiveQuizzes_FilterByQ_CaseInsensitive(t *testing.T) {
	srv, _ := newLiveQuizServer()
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("CSPO Sprint Planning"), lqTestTenantID, lqTestInstructor, "instructor")
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("Daily Scrum Drill"), lqTestTenantID, lqTestInstructor, "instructor")

	// Upper-case needle still matches the mixed-case title.
	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes?q=DAILY",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("case-insensitive: len(items)=%d want 1 body=%s", len(items), rec.Body.String())
	}
	if title := items[0].(map[string]interface{})["title"]; title != "Daily Scrum Drill" {
		t.Errorf("title=%v want 'Daily Scrum Drill'", title)
	}
}

func TestLiveQuizzes_ListLiveQuizzes_FilterByQ_TenantScoped(t *testing.T) {
	srv, _ := newLiveQuizServer()
	// A cross-tenant row that also matches the needle MUST NEVER leak.
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("Daily Scrum (other tenant)"), lqTestOtherTenant, lqTestInstructor, "instructor")
	// The caller's tenant owns two rows; only one matches the needle.
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("Daily Standup"), lqTestTenantID, lqTestInstructor, "instructor")
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("Weekly Review"), lqTestTenantID, lqTestInstructor, "instructor")

	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes?q=daily",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("tenant-scoped q: len(items)=%d want 1 body=%s", len(items), rec.Body.String())
	}
	if title := items[0].(map[string]interface{})["title"]; title != "Daily Standup" {
		t.Errorf("title=%v want 'Daily Standup' (never the other-tenant row)", title)
	}
}

// Filtering must run BEFORE / independent of the learner-safe projection: a
// non-admin caller still gets the q-filtered subset with is_correct stripped.
func TestLiveQuizzes_ListLiveQuizzes_FilterByQ_LearnerProjectionPreserved(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("CSPO Sprint Planning"), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)
	// Add real questions (carrying is_correct) so projection has something to strip.
	patchBody := `{
		"title": "CSPO Sprint Planning",
		"questions": ` + validQuestionsArr() + `
	}`
	_ = doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		patchBody, lqTestTenantID, lqTestInstructor, "instructor")
	// A sibling row that must NOT match the needle.
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		liveQuizBodyTitled("Daily Scrum Drill"), lqTestTenantID, lqTestInstructor, "instructor")

	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes?q=cspo",
		"", lqTestTenantID, lqTestLearner, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("learner q-filter: len(items)=%d want 1 body=%s", len(items), rec.Body.String())
	}
	quiz := items[0].(map[string]interface{})
	questions := quiz["questions"].([]interface{})
	if len(questions) == 0 {
		t.Fatalf("expected projected questions, got none: %s", rec.Body.String())
	}
	opts := questions[0].(map[string]interface{})["options"].([]interface{})
	if _, leaked := opts[0].(map[string]interface{})["is_correct"]; leaked {
		t.Errorf("learner-safe projection broken: is_correct leaked through q-filter path")
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/live-quizzes/{id}
// -----------------------------------------------------------------------------

func TestLiveQuizzes_GetByID_OK(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	if createRec.Code != http.StatusCreated {
		t.Fatalf("setup POST: %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes/"+id,
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["id"] != id {
		t.Errorf("id=%v want %s", got["id"], id)
	}
	if got["state"] != "DRAFT" {
		t.Errorf("state=%v want DRAFT", got["state"])
	}
}

func TestLiveQuizzes_GetByID_NotFound_404(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes/does-not-exist",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestLiveQuizzes_GetByID_CrossTenant_404(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes/"+id,
		"", lqTestOtherTenant, lqTestInstructor, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (cross-tenant isolation)", rec.Code)
	}
}

func TestLiveQuizzes_GetByID_NoTenantHeader_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "GET", "/api/v1/live-quizzes/anything",
		"", "", lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// PATCH /api/v1/live-quizzes/{id} — EditDraft (DRAFT-only)
// -----------------------------------------------------------------------------

func TestLiveQuizzes_PatchDraft_AsInstructor_OK(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	if createRec.Code != http.StatusCreated {
		t.Fatalf("setup POST: %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	body := `{
		"title": "CSPO Sprint Planning (edited)",
		"questions": ` + validQuestionsArr() + `
	}`
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		body, lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["title"] != "CSPO Sprint Planning (edited)" {
		t.Errorf("title=%v want edited", got["title"])
	}
	questions := got["questions"].([]interface{})
	if len(questions) != 2 {
		t.Errorf("len(questions)=%d want 2", len(questions))
	}
	if got["state"] != "DRAFT" {
		t.Errorf("state=%v want DRAFT after edit", got["state"])
	}
}

// CR2-C3: atoms-as-questions — the live-quiz DTOs must accept + round-trip
// atom_id + topic_tags (per question) and explainer (per option). The FE
// quiz-builder atom-picker supplies these when a question is composed by
// linking a LearningAtom.
func TestLiveQuizzes_PatchDraft_RoundTripsAtomLinkFields(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	if createRec.Code != http.StatusCreated {
		t.Fatalf("setup POST: %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	body := `{
		"title": "Atom-linked quiz",
		"questions": [{
			"question_id": "q1",
			"prompt": "Which ceremony starts a Sprint?",
			"atom_id": "atom-uuid-9",
			"topic_tags": ["scrum", "agile"],
			"timer_seconds": 30,
			"points": 1000,
			"options": [
				{"label": "Sprint Planning", "is_correct": true, "explainer": "It kicks off the Sprint."},
				{"label": "Daily Scrum", "is_correct": false}
			]
		}]
	}`
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		body, lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	questions := got["questions"].([]interface{})
	if len(questions) != 1 {
		t.Fatalf("len(questions)=%d want 1", len(questions))
	}
	q0 := questions[0].(map[string]interface{})
	if q0["atom_id"] != "atom-uuid-9" {
		t.Errorf("atom_id=%v want atom-uuid-9", q0["atom_id"])
	}
	tags, _ := q0["topic_tags"].([]interface{})
	if len(tags) != 2 || tags[0] != "scrum" || tags[1] != "agile" {
		t.Errorf("topic_tags=%v want [scrum agile]", q0["topic_tags"])
	}
	opts := q0["options"].([]interface{})
	opt0 := opts[0].(map[string]interface{})
	if opt0["explainer"] != "It kicks off the Sprint." {
		t.Errorf("option explainer=%v want set", opt0["explainer"])
	}
}

// L5: the live-quiz PATCH must accept + persist the quiz-builder's
// explainer-reveal mode + overall time-limit. The FE sends explainer_mode +
// quiz_time_limit_seconds alongside title+questions; previously these were
// unknown fields rejected by DisallowUnknownFields → 400, blocking save.
func TestLiveQuizzes_PatchDraft_AppliesAuthoringConfig(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	if createRec.Code != http.StatusCreated {
		t.Fatalf("setup POST: %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	body := `{
		"title": "Authoring-config quiz",
		"quiz_time_limit_seconds": 300,
		"explainer_mode": "END_OF_QUESTION",
		"questions": ` + validQuestionsArr() + `
	}`
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		body, lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["explainer_mode"] != "END_OF_QUESTION" {
		t.Errorf("explainer_mode=%v want END_OF_QUESTION", got["explainer_mode"])
	}
	if tl, _ := got["quiz_time_limit_seconds"].(float64); int(tl) != 300 {
		t.Errorf("quiz_time_limit_seconds=%v want 300", got["quiz_time_limit_seconds"])
	}
}

func TestLiveQuizzes_PatchDraft_AsLearner_403(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	body := `{
		"title": "evil edit",
		"questions": ` + validQuestionsArr() + `
	}`
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		body, lqTestTenantID, lqTestLearner, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestLiveQuizzes_PatchDraft_AfterPublish_409(t *testing.T) {
	srv, _ := newLiveQuizServer()
	// 1) Create a draft.
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)
	// 2) Patch in 2 valid questions so Publish is allowed.
	body := `{
		"title": "CSPO Sprint Planning",
		"questions": ` + validQuestionsArr() + `
	}`
	patchRec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		body, lqTestTenantID, lqTestInstructor, "instructor")
	if patchRec.Code != http.StatusOK {
		t.Fatalf("setup PATCH: %d body=%s", patchRec.Code, patchRec.Body.String())
	}
	// 3) Publish.
	pubRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes/"+id+"/publish",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if pubRec.Code != http.StatusOK {
		t.Fatalf("setup Publish: %d body=%s", pubRec.Code, pubRec.Body.String())
	}
	// 4) Now PATCH must 409 because state is no longer DRAFT.
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		body, lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 (PATCH after publish)", rec.Code)
	}
}

func TestLiveQuizzes_PatchDraft_CrossTenant_404(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	body := `{
		"title": "edit",
		"questions": ` + validQuestionsArr() + `
	}`
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		body, lqTestOtherTenant, lqTestInstructor, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (cross-tenant isolation)", rec.Code)
	}
}

func TestLiveQuizzes_PatchDraft_InvalidQuestion_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	// Question with zero correct options — domain rejects.
	body := `{
		"title": "edit",
		"questions": [
			{
				"question_id": "q1",
				"prompt": "P?",
				"options": [
					{"label": "A", "is_correct": false},
					{"label": "B", "is_correct": false}
				]
			}
		]
	}`
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		body, lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/live-quizzes/{id}/publish — DRAFT → PUBLISHED
// -----------------------------------------------------------------------------

func TestLiveQuizzes_Publish_DraftToPublished_OK(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)
	// Patch in valid questions first.
	patchBody := `{
		"title": "CSPO Sprint Planning",
		"questions": ` + validQuestionsArr() + `
	}`
	_ = doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		patchBody, lqTestTenantID, lqTestInstructor, "instructor")

	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes/"+id+"/publish",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "PUBLISHED" {
		t.Errorf("state=%v want PUBLISHED", got["state"])
	}
	if got["published_at"] == nil {
		t.Errorf("published_at missing on Publish response")
	}
}

func TestLiveQuizzes_Publish_AsLearner_403(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes/"+id+"/publish",
		"", lqTestTenantID, lqTestLearner, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestLiveQuizzes_Publish_NoQuestions_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)
	// Publish without adding questions → 400 domain rejects.
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes/"+id+"/publish",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLiveQuizzes_Publish_FromNonDraft_409(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)
	patchBody := `{
		"title": "CSPO Sprint Planning",
		"questions": ` + validQuestionsArr() + `
	}`
	_ = doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		patchBody, lqTestTenantID, lqTestInstructor, "instructor")
	_ = doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes/"+id+"/publish",
		"", lqTestTenantID, lqTestInstructor, "instructor") // 1st publish OK

	// Re-publish from PUBLISHED is idempotent per the aggregate (returns nil).
	// To force a 409, drive state past PUBLISHED via MarkArmed-style state.
	// The handler can't mutate state directly through HTTP, but the test
	// can reach into the repo to set state=ARMED to exercise the 409 path.
	srv2, repo := newLiveQuizServer()
	createRec2 := doLiveQuiz(t, srv2, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created2 map[string]interface{}
	_ = json.Unmarshal(createRec2.Body.Bytes(), &created2)
	id2 := created2["id"].(string)
	_ = doLiveQuiz(t, srv2, "PATCH", "/api/v1/live-quizzes/"+id2,
		patchBody, lqTestTenantID, lqTestInstructor, "instructor")
	q, _, _ := repo.Get(id2)
	q.Publish(timeRef()) // DRAFT → PUBLISHED via domain
	q.MarkArmed(timeRef())
	repo.Save(q)
	rec := doLiveQuiz(t, srv2, "POST", "/api/v1/live-quizzes/"+id2+"/publish",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 (publish from ARMED state)", rec.Code)
	}
}

func TestLiveQuizzes_Publish_NotFound_404(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes/does-not-exist/publish",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestLiveQuizzes_Publish_CrossTenant_404(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes/"+id+"/publish",
		"", lqTestOtherTenant, lqTestInstructor, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (cross-tenant isolation)", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Method dispatch — leaf path
// -----------------------------------------------------------------------------

func TestLiveQuizzes_Root_PutNotAllowed(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "PUT", "/api/v1/live-quizzes",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}

func TestLiveQuizzes_Sub_PutNotAllowed(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "PUT", "/api/v1/live-quizzes/anything",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}

func TestLiveQuizzes_PublishAction_DeleteNotAllowed(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "DELETE", "/api/v1/live-quizzes/anything/publish",
		"", lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}
