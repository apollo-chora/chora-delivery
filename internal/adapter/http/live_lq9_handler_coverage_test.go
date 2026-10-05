// live_lq9_handler_coverage_test.go — branch-coverage backfill for the
// live-quiz / live-poll HTTP handlers (agent λ surface). Drives the guard
// branches the Wave-5/6 suites left open:
//
//   - live-quiz PATCH/publish identity gates (missing tenant/gcid), decode
//     failures, and the ConfigureAuthoring invalid-mode 400
//   - live-poll create/open/close/vote/get identity gates (400/401/403),
//     domain-validation 400s (empty question, single option, unknown vote
//     choice, blank choice), double-transition 409s (close-DRAFT,
//     re-open-CLOSED), cross-tenant + missing-aggregate 404s
//   - the unwired-realtime vote path: nil tally + nil backplane must degrade
//     to a persisted vote, never a 500
//   - live-quiz advance producer guards: missing tenant, unknown/cross-tenant
//     session, decode failure, blank question, already-asked 409, and the
//     not-LIVE 409
//
// All helpers reused (doLiveQuiz/doCS/createPoll/buildProducerServer/
// buildPollServer/newLiveQuizServer); no new helpers introduced.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// -----------------------------------------------------------------------------
// live-quiz PATCH (EditDraft) — identity + decode + authoring-config guards
// -----------------------------------------------------------------------------

func TestLq9_QuizPatch_NoTenant_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/anything",
		`{"title":"x"}`, "", lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_QuizPatch_NoGCID_401(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/anything",
		`{"title":"x"}`, lqTestTenantID, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestLq9_QuizPatch_BadJSON_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		`{not-valid`, lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLq9_QuizPatch_InvalidExplainerMode_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	createRec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes",
		createDefaultLiveQuizBody(), lqTestTenantID, lqTestInstructor, "instructor")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	body := `{
		"title": "CSPO Sprint Planning",
		"explainer_mode": "NOT_A_REAL_MODE",
		"questions": ` + validQuestionsArr() + `
	}`
	rec := doLiveQuiz(t, srv, "PATCH", "/api/v1/live-quizzes/"+id,
		body, lqTestTenantID, lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (invalid explainer_mode) body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// live-quiz publish — identity gates
// -----------------------------------------------------------------------------

func TestLq9_QuizPublish_NoTenant_400(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes/anything/publish",
		"", "", lqTestInstructor, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_QuizPublish_NoGCID_401(t *testing.T) {
	srv, _ := newLiveQuizServer()
	rec := doLiveQuiz(t, srv, "POST", "/api/v1/live-quizzes/anything/publish",
		"", lqTestTenantID, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// live-poll create — RBAC + identity + validation
// -----------------------------------------------------------------------------

func TestLq9_PollCreate_Learner_403(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls",
		`{"question":"Q?","options":["a","b"]}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLq9_PollCreate_NoTenant_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls",
		`{"question":"Q?","options":["a","b"]}`, "", csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_PollCreate_NoGCID_401(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls",
		`{"question":"Q?","options":["a","b"]}`, csTenantA, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestLq9_PollCreate_BadJSON_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls",
		`{not-valid`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_PollCreate_EmptyQuestion_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls",
		`{"question":"  ","options":["a","b"]}`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (question required)", rec.Code)
	}
}

func TestLq9_PollCreate_SingleOption_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls",
		`{"question":"Q?","options":["a"]}`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (needs >=2 options)", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// live-poll open/close — identity + FSM guards
// -----------------------------------------------------------------------------

func TestLq9_PollOpen_NoTenant_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/whatever/open",
		"", "", csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_PollOpen_NoGCID_401(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/whatever/open",
		"", csTenantA, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestLq9_PollOpen_ClosedPoll_409(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/close", "", csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-open of CLOSED poll want 409 got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLq9_PollClose_DraftPoll_409(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/close", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Fatalf("close of DRAFT poll want 409 got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLq9_PollClose_NoTenant_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/whatever/close",
		"", "", csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_PollClose_NoGCID_401(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/whatever/close",
		"", csTenantA, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// live-poll votes — validation + identity + tenant scoping
// -----------------------------------------------------------------------------

func TestLq9_PollVote_UnknownOption_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"no-such-option"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown option vote want 400 got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLq9_PollVote_NoRoles_403(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"a"}`, csTenantA, csLearner1GCID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("vote without roles want 403 got %d", rec.Code)
	}
}

func TestLq9_PollVote_NoTenant_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/whatever/votes",
		`{"choice":"a"}`, "", csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_PollVote_NoGCID_401(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/whatever/votes",
		`{"choice":"a"}`, csTenantA, "", "learner")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestLq9_PollVote_BadJSON_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{not-valid`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_PollVote_BlankChoice_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"   "}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank choice want 400 got %d", rec.Code)
	}
}

func TestLq9_PollVote_CrossTenant_404(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"a"}`, csTenantB, csLearner1GCID, "learner")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant vote want 404 got %d", rec.Code)
	}
}

func TestLq9_PollVote_MissingPoll_404(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/does-not-exist/votes",
		`{"choice":"a"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("vote on missing poll want 404 got %d", rec.Code)
	}
}

func TestLq9_PollVote_UnwiredRealtimeStillSaves(t *testing.T) {
	polls := inmem.NewLivePollRepo()
	srv := httpapi.NewServer(httpapi.Deps{LivePolls: polls})
	id := createPoll(t, srv)
	_ = doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/open", "", csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/live-polls/"+id+"/votes",
		`{"choice":"Too slow"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("vote with unwired realtime want 200 got %d body=%s", rec.Code, rec.Body.String())
	}
	p, ok, _ := polls.Get(id)
	if !ok {
		t.Fatalf("poll missing after vote")
	}
	if p.VoteCount("Too slow") != 1 {
		t.Fatalf("vote not persisted with nil tally/backplane: %#v", p.Options)
	}
}

// -----------------------------------------------------------------------------
// live-poll GET — tenant scoping + identity
// -----------------------------------------------------------------------------

func TestLq9_PollGet_CrossTenant_404(t *testing.T) {
	srv, _ := buildPollServer(t)
	id := createPoll(t, srv)
	rec := doCS(t, srv, "GET", "/api/v1/live-polls/"+id, "", csTenantB, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get want 404 got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLq9_PollGet_NoTenant_400(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "GET", "/api/v1/live-polls/whatever", "", "", csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_PollGet_MissingPoll_404(t *testing.T) {
	srv, _ := buildPollServer(t)
	rec := doCS(t, srv, "GET", "/api/v1/live-polls/does-not-exist", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get missing poll want 404 got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// live-quiz advance (producer) — guards + FSM conflicts
// -----------------------------------------------------------------------------

func TestLq9_Advance_NoTenant_400(t *testing.T) {
	srv, rig := buildProducerServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, "", csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_Advance_NotFound_404(t *testing.T) {
	srv, _ := buildProducerServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/does-not-exist/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("advance unknown session want 404 got %d", rec.Code)
	}
}

func TestLq9_Advance_CrossTenant_404(t *testing.T) {
	srv, rig := buildProducerServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantB, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant advance want 404 got %d", rec.Code)
	}
}

func TestLq9_Advance_BadJSON_400(t *testing.T) {
	srv, rig := buildProducerServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{not-valid`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestLq9_Advance_BlankQuestion_400(t *testing.T) {
	srv, rig := buildProducerServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"   "}`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank question id want 400 got %d", rec.Code)
	}
}

func TestLq9_Advance_AlreadyAsked_409(t *testing.T) {
	srv, rig := buildProducerServer(t)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-advance same question want 409 got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLq9_Advance_NotLiveSession_409(t *testing.T) {
	sessions := inmem.NewClassroomSessionRepo()
	sess, err := classroom.NewLiveQuizSession(csQuizID, csTenantA, csInstructorGCID)
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	sessions.Save(sess) // ARMED, never Started — AdvanceTo must 409
	srv := httpapi.NewServer(httpapi.Deps{ClassroomSessions: sessions})
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sess.ID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Fatalf("advance on ARMED session want 409 got %d body=%s", rec.Code, rec.Body.String())
	}
}
