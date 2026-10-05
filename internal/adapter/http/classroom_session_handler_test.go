// classroom_session_handler_test.go — TDD coverage for the R+ Stage C-lite
// Wave-5 M8 classroom-session HTTP surface (LiveQuizSession).
//
// Endpoints under test:
//
//	POST /api/v1/live-quizzes/{quizId}/sessions   → start (ARMED)
//	POST /api/v1/classroom-sessions/{id}/start    → ARMED→LIVE
//	POST /api/v1/classroom-sessions/{id}/close    → LIVE→CLOSED
//	GET  /api/v1/classroom-sessions/{id}          → snapshot
//	POST /api/v1/classroom-sessions/{id}/responses → SubmitResponse (learner)
//
// Coverage target: >=60% adapter (per .claude/rules/development-execution.md).
//
// TDD: written FIRST then drove classroom_session_handler.go (RED→GREEN→
// REFACTOR per `feedback_strict_tdd`).
package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

const (
	csTenantA          = "019e2f93-d586-71b5-8c3d-e2b0d0d50300"
	csTenantB          = "019e2f93-d586-71b5-8c3d-e2b0d0d50301"
	csInstructorGCID   = "019e2f93-d586-71b5-8c3d-e2b0d0d50310"
	csLearner1GCID     = "019e2f93-d586-71b5-8c3d-e2b0d0d50320"
	csLearner2GCID     = "019e2f93-d586-71b5-8c3d-e2b0d0d50321"
	csQuizID           = "019e2f93-d586-71b5-8c3d-e2b0d0d50400"
	csQuestionID       = "019e2f93-d586-71b5-8c3d-e2b0d0d50500"
	csSecondQuestionID = "019e2f93-d586-71b5-8c3d-e2b0d0d50501"
)

// newClassroomSessionServer wires the classroom-session routes against a
// fresh in-mem repo per test. We pass only what the new handler needs +
// the bare NewServer prerequisites.
func newClassroomSessionServer() (http.Handler, *inmem.ClassroomSessionRepo) {
	repo := inmem.NewClassroomSessionRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:           inmem.NewCourseRepo(),
		Bookings:          inmem.NewBookingRepo(),
		ClassroomSessions: repo,
	})
	return srv, repo
}

// doCS issues a request with the standard headers for classroom-session
// routes. Mirrors doExam.
func doCS(t *testing.T, h http.Handler, method, path, body, tenantID, gcid, roles string) *httptest.ResponseRecorder {
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

// startSessionHelper creates a LiveQuizSession via POST and returns the
// session id. Fails the test on non-201.
func startSessionHelper(t *testing.T, srv http.Handler, tenantID string) string {
	t.Helper()
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions",
		"", tenantID, csInstructorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: start session: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("setup: unmarshal: %v", err)
	}
	id, ok := got["id"].(string)
	if !ok || id == "" {
		t.Fatalf("setup: missing id in response: %v", got)
	}
	return id
}

// transitionToLive flips ARMED→LIVE via the start endpoint.
func transitionToLive(t *testing.T, srv http.Handler, id, tenantID string) {
	t.Helper()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/start",
		"", tenantID, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: transitionToLive status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/live-quizzes/{quizId}/sessions — create ARMED session
// -----------------------------------------------------------------------------

func TestClassroomSession_Start_AsInstructor_201_ARMED(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "ARMED" {
		t.Errorf("state=%v want ARMED", got["state"])
	}
	if got["live_quiz_id"] != csQuizID {
		t.Errorf("live_quiz_id=%v want %s", got["live_quiz_id"], csQuizID)
	}
	if got["tenant_id"] != csTenantA {
		t.Errorf("tenant_id=%v want %s", got["tenant_id"], csTenantA)
	}
	if got["instructor_gcid"] != csInstructorGCID {
		t.Errorf("instructor_gcid=%v want %s", got["instructor_gcid"], csInstructorGCID)
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Errorf("id missing")
	}
}

func TestClassroomSession_Start_NoTenant_400(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions",
		"", "", csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestClassroomSession_Start_NoGCID_401(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions",
		"", csTenantA, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestClassroomSession_Start_AsLearner_403(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions",
		"", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestClassroomSession_Start_MissingQuizID_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	// No quiz-id segment: `/api/v1/live-quizzes/sessions` routes to the
	// sessions handler (HasSuffix "/sessions") whose empty-id guard returns
	// 404. (A `//sessions` double-slash is rewritten by net/http's ServeMux
	// with a 307 before the handler runs, so it can't exercise the guard.)
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/sessions",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/classroom-sessions/{id}/start — ARMED→LIVE
// -----------------------------------------------------------------------------

func TestClassroomSession_Transition_ArmedToLive_200(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/start",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "LIVE" {
		t.Errorf("state=%v want LIVE", got["state"])
	}
	if got["started_at"] == nil {
		t.Errorf("started_at missing on LIVE")
	}
}

func TestClassroomSession_Transition_StartIdempotent(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	// Re-fire start — idempotent in LIVE.
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/start",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Errorf("status=%d want 200 (idempotent)", rec.Code)
	}
}

func TestClassroomSession_Transition_StartUnknownID_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/does-not-exist/start",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestClassroomSession_Transition_StartCrossTenant_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/start",
		"", csTenantB, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (cross-tenant isolation)", rec.Code)
	}
}

func TestClassroomSession_Transition_StartAsLearner_403(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/start",
		"", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/classroom-sessions/{id}/close — LIVE→CLOSED
// -----------------------------------------------------------------------------

func TestClassroomSession_Transition_LiveToClosed_200(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/close",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "CLOSED" {
		t.Errorf("state=%v want CLOSED", got["state"])
	}
	if got["ended_at"] == nil {
		t.Errorf("ended_at missing on CLOSED")
	}
}

func TestClassroomSession_Transition_CloseFromARMED_409(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	// Skip start — close directly from ARMED.
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/close",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/classroom-sessions/{id} — snapshot
// -----------------------------------------------------------------------------

func TestClassroomSession_GetSnapshot_OK_EmptyState(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+id,
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "LIVE" {
		t.Errorf("state=%v want LIVE", got["state"])
	}
	if got["id"] != id {
		t.Errorf("id=%v want %s", got["id"], id)
	}
	// Empty-state defaults — these MUST be present even when zero (per
	// the FE polling contract; `feedback_no_stubs_real_wiring` fails
	// loud rather than hide the slot).
	if got["response_counts"] == nil {
		t.Errorf("response_counts missing — must be {} (not absent)")
	}
	if got["joined_learners"] == nil {
		t.Errorf("joined_learners missing — must be 0 (not absent)")
	}
	if got["current_question_id"] == nil {
		t.Errorf("current_question_id missing — must be empty string (not absent)")
	}
}

func TestClassroomSession_GetSnapshot_Unknown_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/does-not-exist",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestClassroomSession_GetSnapshot_CrossTenant_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+id,
		"", csTenantB, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (cross-tenant isolation)", rec.Code)
	}
}

func TestClassroomSession_GetSnapshot_NoTenantHeader_400(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/anything",
		"", "", csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/classroom-sessions/{id}/responses — SubmitResponse
// -----------------------------------------------------------------------------

func submitResponseBody(questionID, choice string) string {
	return `{"question_id":"` + questionID + `","choice":"` + choice + `"}`
}

func TestClassroomSession_SubmitResponse_AsLearner_201(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		submitResponseBody(csQuestionID, "B"),
		csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
}

func TestClassroomSession_SubmitResponse_DoubleSubmit_409(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	// First submit OK.
	rec1 := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		submitResponseBody(csQuestionID, "B"),
		csTenantA, csLearner1GCID, "learner")
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first submit status=%d body=%s", rec1.Code, rec1.Body.String())
	}
	// Second submit by same learner+question MUST 409 (first-write-wins
	// per LiveQuizSession.SubmitResponse).
	rec2 := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		submitResponseBody(csQuestionID, "C"),
		csTenantA, csLearner1GCID, "learner")
	if rec2.Code != http.StatusConflict {
		t.Errorf("dup submit status=%d want 409 body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestClassroomSession_SubmitResponse_WhenARMED_409(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	// Don't transition — session is ARMED.
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		submitResponseBody(csQuestionID, "B"),
		csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 (ARMED gating) body=%s", rec.Code, rec.Body.String())
	}
}

func TestClassroomSession_SubmitResponse_NoGCID_401(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		submitResponseBody(csQuestionID, "B"),
		csTenantA, "", "learner")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestClassroomSession_SubmitResponse_CrossTenant_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		submitResponseBody(csQuestionID, "B"),
		csTenantB, csLearner1GCID, "learner")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (cross-tenant isolation) body=%s", rec.Code, rec.Body.String())
	}
}

func TestClassroomSession_SubmitResponse_BadJSON_400(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{not-valid`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestClassroomSession_SubmitResponse_MissingQuestionID_400(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"","choice":"B"}`,
		csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Snapshot reflects accumulated responses + joined learner count
// -----------------------------------------------------------------------------

func TestClassroomSession_Snapshot_ReflectsResponseCounts(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	// 2 learners, both answer question1 (B + B). 1 learner answers
	// question2 (A). Snapshot must aggregate by choice for the current
	// (latest) question, but expose joined_learners as the unique
	// participant count over the session.
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		submitResponseBody(csQuestionID, "B"),
		csTenantA, csLearner1GCID, "learner")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		submitResponseBody(csQuestionID, "B"),
		csTenantA, csLearner2GCID, "learner")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		submitResponseBody(csSecondQuestionID, "A"),
		csTenantA, csLearner1GCID, "learner")

	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+id,
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	// current_question_id should pin to the most-recently-answered q
	// (the one we expect the FE to render).
	if got["current_question_id"] != csSecondQuestionID {
		t.Errorf("current_question_id=%v want %s", got["current_question_id"], csSecondQuestionID)
	}
	counts, ok := got["response_counts"].(map[string]interface{})
	if !ok {
		t.Fatalf("response_counts wrong type: %T", got["response_counts"])
	}
	// Counts are scoped to current_question_id (the latest q) — so just
	// "A":1 from learner1's q2 answer.
	if c, _ := counts["A"].(float64); int(c) != 1 {
		t.Errorf("counts[A]=%v want 1", counts["A"])
	}
	// joined_learners is unique participants across all questions.
	if jl, _ := got["joined_learners"].(float64); int(jl) != 2 {
		t.Errorf("joined_learners=%v want 2 (learner1 + learner2)", got["joined_learners"])
	}
}

// TestClassroomSession_Snapshot_ReflectsAdvancedQuestion proves the REST
// snapshot surfaces the instructor-opened question (ADR-168 AdvanceTo) BEFORE
// any response exists. The R+ presenter + learner-answer views poll this DTO
// and must render the open question so the learner can answer it. Regression:
// current_question_id was derived ONLY from the latest QuestionResponse, so it
// stayed "" after an advance with no answers yet → the learner never saw the
// question to answer it (chicken-and-egg).
func TestClassroomSession_Snapshot_ReflectsAdvancedQuestion(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)

	// Instructor opens a question — NO learner has answered yet.
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/advance",
		`{"question_id":"`+csQuestionID+`"}`, csTenantA, csInstructorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("advance status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+id,
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	// Must pin to the instructor-opened question, not "".
	if got["current_question_id"] != csQuestionID {
		t.Errorf("current_question_id=%v want %s (instructor-opened, pre-response)",
			got["current_question_id"], csQuestionID)
	}
	// No responses yet → empty counts + zero joined.
	if counts, ok := got["response_counts"].(map[string]interface{}); !ok || len(counts) != 0 {
		t.Errorf("response_counts=%v want empty", got["response_counts"])
	}
	if jl, _ := got["joined_learners"].(float64); int(jl) != 0 {
		t.Errorf("joined_learners=%v want 0", got["joined_learners"])
	}
}

// -----------------------------------------------------------------------------
// Wire-safety: nil ClassroomSessions repo MUST return 503 (no stubs)
// -----------------------------------------------------------------------------

func TestClassroomSession_NilRepo_503(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:  inmem.NewCourseRepo(),
		Bookings: inmem.NewBookingRepo(),
		// ClassroomSessions deliberately omitted.
	})
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d want 503 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Method dispatch
// -----------------------------------------------------------------------------

func TestClassroomSession_MethodNotAllowed(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "DELETE", "/api/v1/classroom-sessions/anything",
		"", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}
