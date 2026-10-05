// classroom_session_handler_cs3_test.go — statement-coverage battery for
// classroom_session_handler.go (external package httpapi_test). Supplements
// classroom_session_handler_test.go + classroom_session_stage_handler_test.go
// with the branches they never reach:
//
//   - liveQuizSessionsRootHandler method guard + blank-quiz-id 400
//   - handleStartSession failure paths (join-code probe 500, code-collision
//     regeneration, Save 500) — the inmem repo never errors, so a failing
//     SessionStore wrapper (cs3SessionStore) is required for the CHO-2184
//     fail-loud 500s
//   - transition start/close error paths (CLOSED re-start 409, unknown 404,
//     learner 403, lookup 500) + the ADR-168 durable started/ended events
//     (incl. derefTime's nil branch via a hand-set LIVE session)
//   - getSnapshot nil-repo 503 + lookup 500; submit nil-repo 503, quiz
//     lookup 500, Mutate ErrSessionNotFound 404, unknown-error 400 default
//   - join/resolve-by-code nil-repo 503, role 403, bad-path 404/400,
//     lookup 500, quiz-title-degrade
//   - classroomSessionsSubHandler dispatch guards (405 per action, 404
//     shapes, trailing-slash id, bare collection)
//   - role-header parsing gaps (missing role header, admin/tenant_admin)
//   - classroomSessionDTO/scoreboardWire leftovers (scoreboard cap at
//     leaderboardTopN, podium top-5, final-board nil fallback, `me` without
//     last_result, ghost-question open_question omission, quiz read failure
//     degrade)
package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// -----------------------------------------------------------------------------
// Failing-store fakes — the inmem repos never error, so the handler 500 paths
// (CHO-2184: a read error must fail loud, never degrade to not-found) are
// unreachable without wrappers that inject failures on demand.
// -----------------------------------------------------------------------------

// cs3SessionStore wraps an inmem ClassroomSessionRepo and can force any of
// the SessionStore methods to fail (or report a join-code collision).
type cs3SessionStore struct {
	*inmem.ClassroomSessionRepo
	getErr             error
	getByJoinCodeErr   error
	getByJoinCodeTaken bool
	saveErr            error
	mutateErr          error
}

func (s *cs3SessionStore) Get(id string) (*classroom.LiveQuizSession, bool, error) {
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	return s.ClassroomSessionRepo.Get(id)
}

func (s *cs3SessionStore) GetByJoinCode(tenantID, code string) (*classroom.LiveQuizSession, bool, error) {
	if s.getByJoinCodeErr != nil {
		return nil, false, s.getByJoinCodeErr
	}
	if s.getByJoinCodeTaken {
		// Register every probe as a collision so handleStartSession exercises
		// the bounded regenerate loop before its first Save.
		return &classroom.LiveQuizSession{ID: "cs3-taken"}, true, nil
	}
	return s.ClassroomSessionRepo.GetByJoinCode(tenantID, code)
}

func (s *cs3SessionStore) Save(sess *classroom.LiveQuizSession) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.ClassroomSessionRepo.Save(sess)
}

func (s *cs3SessionStore) Mutate(id string, fn func(*classroom.LiveQuizSession) error) error {
	if s.mutateErr != nil {
		return s.mutateErr
	}
	return s.ClassroomSessionRepo.Mutate(id, fn)
}

// cs3QuizStore wraps an inmem LiveQuizRepo and can force Get failures.
type cs3QuizStore struct {
	*inmem.LiveQuizRepo
	getErr error
}

func (s *cs3QuizStore) Get(id string) (*classroom.LiveQuiz, bool, error) {
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	return s.LiveQuizRepo.Get(id)
}

// cs3Server wires the classroom-session surface with fresh inmem sessions +
// quizzes repos plus any extra deps via mut (Publisher / realtime ports).
func cs3Server(t *testing.T, sessions classroom.SessionStore, quizzes classroom.QuizStore, mut func(*httpapi.Deps)) http.Handler {
	t.Helper()
	if sessions == nil {
		sessions = inmem.NewClassroomSessionRepo()
	}
	if quizzes == nil {
		quizzes = inmem.NewLiveQuizRepo()
	}
	deps := httpapi.Deps{
		Courses:           inmem.NewCourseRepo(),
		Bookings:          inmem.NewBookingRepo(),
		ClassroomSessions: sessions,
		LiveQuizzes:       quizzes,
	}
	if mut != nil {
		mut(&deps)
	}
	return httpapi.NewServer(deps)
}

// cs3Publisher returns the ADR-168 in-memory publisher for durable lifecycle
// events.
func cs3Publisher() *events.InMemoryPublisher {
	return events.NewInMemoryPublisher("chora-489812", "chora-delivery")
}

// cs3Topics counts publisher history entries per topic.
func cs3Topics(pub *events.InMemoryPublisher) map[string]int {
	out := map[string]int{}
	for _, ev := range pub.History() {
		out[ev.Topic]++
	}
	return out
}

// -----------------------------------------------------------------------------
// liveQuizSessionsRootHandler — method guard + session-create error paths
// -----------------------------------------------------------------------------

func TestCs3_SessionsRoot_MethodGuard_405(t *testing.T) {
	srv := cs3Server(t, nil, nil, nil)
	rec := doCS(t, srv, "GET", "/api/v1/live-quizzes/"+csQuizID+"/sessions", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET sessions root: want 405, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_StartSession_BlankQuizID_400(t *testing.T) {
	srv := cs3Server(t, nil, nil, nil)
	// A whitespace quiz-id segment passes the path-shape guard but fails
	// classroom.NewLiveQuizSession's blank-field validation → 400.
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/%20/sessions", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank quiz id: want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_StartSession_JoinCodeProbeError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), getByJoinCodeErr: errors.New("store down")}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("probe error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "join-code uniqueness check failed") {
		t.Fatalf("expected uniqueness-probe envelope, got %s", rec.Body.String())
	}
}

func TestCs3_StartSession_JoinCodeCollision_RegeneratesAndSaves(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), getByJoinCodeTaken: true}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("collision fallback: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_StartSession_SaveError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), saveErr: errors.New("disk full")}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "persist failed") {
		t.Fatalf("save error: want 500 persist-failed, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// Role-header parsing gaps in hasClassroomInstructorRole (missing header,
// admin-style spellings).
func TestCs3_StartSession_NoRoleHeader_403(t *testing.T) {
	srv := cs3Server(t, nil, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions", "", csTenantA, csInstructorGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no role header: want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_StartSession_TenantAdminRole_201(t *testing.T) {
	srv := cs3Server(t, nil, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions", "", csTenantA, csInstructorGCID, "tenant_admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("tenant_admin: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_StartSession_TrainingAdminUnderscoreRole_201(t *testing.T) {
	srv := cs3Server(t, nil, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+csQuizID+"/sessions", "", csTenantA, csInstructorGCID, "training_admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("training_admin: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleTransitionStart — CLOSED re-start 409, lookup 500, durable started
// event, derefTime nil branch
// -----------------------------------------------------------------------------

func TestCs3_TransitionStart_FromClosed_409(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/close", "", csTenantA, csInstructorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("setup close: %d body=%s", rec.Code, rec.Body.String())
	}
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/start", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Fatalf("start from CLOSED: want 409, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_TransitionStart_LookupError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), getErr: errors.New("db down")}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/any-id/start", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("start lookup error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_TransitionStart_PublishesStartedEvent(t *testing.T) {
	pub := cs3Publisher()
	srv := cs3Server(t, nil, nil, func(d *httpapi.Deps) { d.Publisher = pub })
	id := startSessionHelper(t, srv, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/start", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("start: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if topics := cs3Topics(pub); topics[events.TopicLiveQuizSessionStarted] != 1 {
		t.Fatalf("expected one started event, got %v", topics)
	}
}

func TestCs3_TransitionStart_NilStartedAt_PublishesZeroTime(t *testing.T) {
	pub := cs3Publisher()
	store := inmem.NewClassroomSessionRepo()
	sess, err := classroom.NewLiveQuizSession(csQuizID, csTenantA, csInstructorGCID)
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	// Hand-set LIVE without Start() so StartedAt stays nil — derefTime's
	// nil branch then hands the publisher a zero timestamp.
	sess.State = classroom.LiveQuizSessionStateLive
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	srv := cs3Server(t, store, nil, func(d *httpapi.Deps) { d.Publisher = pub })
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sess.ID+"/start", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("idempotent start: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if topics := cs3Topics(pub); topics[events.TopicLiveQuizSessionStarted] != 1 {
		t.Fatalf("expected one started event, got %v", topics)
	}
}

// -----------------------------------------------------------------------------
// handleTransitionClose — unknown 404, learner 403, lookup 500, ended event +
// session_closed fan-out
// -----------------------------------------------------------------------------

func TestCs3_TransitionClose_UnknownSession_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/does-not-exist/close", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("close unknown: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_TransitionClose_AsLearner_403(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/close", "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("close as learner: want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_TransitionClose_LookupError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), getErr: errors.New("db down")}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/any-id/close", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("close lookup error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_TransitionClose_PublishesEndedAndFansOut(t *testing.T) {
	pub := cs3Publisher()
	bp := realtime.NewMemBackplane()
	srv := cs3Server(t, nil, nil, func(d *httpapi.Deps) {
		d.Publisher = pub
		d.RealtimeBackplane = bp
	})
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	ch, cancel, err := bp.PSubscribe(context.Background(), "rt:quiz:*")
	if err != nil {
		t.Fatalf("PSubscribe: %v", err)
	}
	defer cancel()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/close", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("close: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case msg := <-ch:
		if msg.Channel != realtime.QuizChannel(id) {
			t.Fatalf("fan-out channel: %s", msg.Channel)
		}
	case <-time.After(time.Second):
		t.Fatalf("no session_closed fan-out")
	}
	if topics := cs3Topics(pub); topics[events.TopicLiveQuizSessionEnded] != 1 {
		t.Fatalf("expected one ended event, got %v", topics)
	}
}

// -----------------------------------------------------------------------------
// handleGetSnapshot — lookup 500 (the nil-repo 503 branches are unreachable
// via the public mux: the /classroom-sessions subtree only mounts when
// deps.ClassroomSessions != nil, handlers.go:607-610).
// -----------------------------------------------------------------------------

func TestCs3_GetSnapshot_LookupError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), getErr: errors.New("db down")}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/any-id", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("snapshot lookup error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleSubmitResponse — nil-repo 503, quiz lookup 500, Mutate sentinel
// mapping (404 session_not_found + 400 bad_request default), ungraded
// fallback for a question the quiz never taught, role variants
// -----------------------------------------------------------------------------

func TestCs3_SubmitResponse_QuizLookupError_500(t *testing.T) {
	quizzes := &cs3QuizStore{LiveQuizRepo: inmem.NewLiveQuizRepo(), getErr: errors.New("quiz store down")}
	srv := cs3Server(t, nil, quizzes, nil)
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"q1","choice":"B"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "live quiz lookup failed") {
		t.Fatalf("quiz lookup error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_SubmitResponse_MutateNotFound_404Sentinel(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), mutateErr: classroom.ErrSessionNotFound}
	srv := cs3Server(t, store, nil, nil)
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"q1","choice":"B"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("mutate not-found: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"session_not_found"`) || !strings.Contains(rec.Body.String(), "classroom session not found") {
		t.Fatalf("expected session_not_found envelope, got %s", rec.Body.String())
	}
}

func TestCs3_SubmitResponse_MutateUnknownError_400Default(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), mutateErr: errors.New("mystery failure")}
	srv := cs3Server(t, store, nil, nil)
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"q1","choice":"B"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"bad_request"`) {
		t.Fatalf("unknown mutate error: want 400 bad_request, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_SubmitResponse_QuestionNotInQuiz_Ungraded201(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"ghost-q","choice":"4"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusCreated {
		t.Fatalf("unknown question ungraded: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	if _, ok := body["your_result"]; ok {
		t.Fatalf("unknown question must NOT grade (no your_result): %v", body)
	}
	if body["current_question_id"] != "ghost-q" {
		t.Fatalf("current_question_id=%v want ghost-q", body["current_question_id"])
	}
}

// hasClassroomLearnerOrInstructorRole parsing gaps + allowed variants.
func TestCs3_SubmitResponse_NoRoleHeader_403(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"q1","choice":"B"}`, csTenantA, csLearner1GCID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no role submit: want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_SubmitResponse_StaffRole_403(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"q1","choice":"B"}`, csTenantA, csLearner1GCID, "staff")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("staff submit: want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_SubmitResponse_AdminRole_201(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"q1","choice":"B"}`, csTenantA, csLearner1GCID, "admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin submit: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_SubmitResponse_InstructorRole_201(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"q1","choice":"B"}`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("instructor submit: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleJoinSession — nil-repo 503, role 403, unknown 404, bad JSON 400,
// lookup 500, Mutate 404
// -----------------------------------------------------------------------------

func TestCs3_JoinSession_StaffRole_403(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/join", `{"nickname":"X"}`, csTenantA, csLearner1GCID, "staff")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("join staff: want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_JoinSession_UnknownSession_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/does-not-exist/join", `{"nickname":"X"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("join unknown: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_JoinSession_BadJSON_400(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/join", `{not-json`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("join bad json: want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_JoinSession_LookupError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), getErr: errors.New("db down")}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/any-id/join", `{"nickname":"X"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("join lookup error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_JoinSession_MutateNotFound_404(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), mutateErr: classroom.ErrSessionNotFound}
	srv := cs3Server(t, store, nil, nil)
	id := startSessionHelper(t, srv, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/join", `{"nickname":"X"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("join mutate not-found: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleResolveByCode — nil-repo 503, no tenant 400, blank code 404,
// lookup 500, quiz lookup 500, quiz-missing title degrade
// -----------------------------------------------------------------------------

func TestCs3_ResolveByCode_NoTenant_400(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/by-code/ABCDEF", "", "", csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("by-code no tenant: want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_ResolveByCode_BlankCode_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	// NormalizeJoinCode("-") is "" → the code-empty guard 404s.
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/by-code/-", "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("blank code: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_ResolveByCode_LookupError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), getByJoinCodeErr: errors.New("store down")}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/by-code/ABCDEF", "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("by-code lookup error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_ResolveByCode_QuizLookupError_500(t *testing.T) {
	quizzes := &cs3QuizStore{LiveQuizRepo: inmem.NewLiveQuizRepo(), getErr: errors.New("quiz down")}
	srv := cs3Server(t, nil, quizzes, nil)
	_, code := createStageSession(t, srv, csQuizID)
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/by-code/"+code, "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "live quiz lookup failed") {
		t.Fatalf("by-code quiz error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_ResolveByCode_QuizMissing_TitleBlank(t *testing.T) {
	srv, _, _ := newStageServer()
	sid, code := createStageSession(t, srv, csQuizID)
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/by-code/"+code, "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("by-code quiz missing: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	if body["session_id"] != sid {
		t.Fatalf("session_id=%v want %s", body["session_id"], sid)
	}
	if title, ok := body["quiz_title"].(string); !ok || title != "" {
		t.Fatalf("quiz_title=%v want empty (missing quiz)", body["quiz_title"])
	}
}

// -----------------------------------------------------------------------------
// classroomSessionsSubHandler — dispatch guards
// -----------------------------------------------------------------------------

func TestCs3_Sub_BareCollection_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bare collection: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_Sub_ByCode_WrongMethod_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/by-code/ABCDEF", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("by-code POST: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_Sub_ByCode_NoCodeSegment_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/by-code", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("by-code no code: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_Sub_StartMethodGuard_405(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/any-id/start", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET start: want 405, got %d", rec.Code)
	}
}

func TestCs3_Sub_CloseMethodGuard_405(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "PUT", "/api/v1/classroom-sessions/any-id/close", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT close: want 405, got %d", rec.Code)
	}
}

func TestCs3_Sub_AdvanceMethodGuard_405(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/any-id/advance", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET advance: want 405, got %d", rec.Code)
	}
}

func TestCs3_Sub_ResponsesMethodGuard_405(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/any-id/responses", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET responses: want 405, got %d", rec.Code)
	}
}

func TestCs3_Sub_JoinMethodGuard_405(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/any-id/join", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET join: want 405, got %d", rec.Code)
	}
}

func TestCs3_Sub_UnknownAction_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/any-id/bogus", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_Sub_TooManySegments_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/any-id/start/extra", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("extra segment: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_Sub_TrailingSlashSnapshot_200(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	// TrimSuffix path: `{id}/` resolves as /{id} → snapshot.
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+id+"/", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("trailing slash snapshot: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Final statement sweep — sub-dispatch rest=="" 404, callerTenantGCID 401
// returns on the write handlers, transition Save 500s, submit lookup 500 +
// empty-choice 400, and the DTO reveals ghost-question skip.
// -----------------------------------------------------------------------------

func TestCs3_Sub_BareTrailingSlash_404(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bare trailing-slash collection: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_TransitionStart_NoGCID_401(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/any-id/start", "", csTenantA, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("start no gcid: want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_TransitionStart_SaveError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), saveErr: errors.New("disk full")}
	sess, err := classroom.NewLiveQuizSession(csQuizID, csTenantA, csInstructorGCID)
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	// Seed ARMED directly on the underlying repo so the injected Save error
	// only fires inside the handler.
	if err := store.ClassroomSessionRepo.Save(sess); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sess.ID+"/start", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "persist failed") {
		t.Fatalf("start save error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_TransitionClose_NoGCID_401(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/any-id/close", "", csTenantA, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("close no gcid: want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_TransitionClose_SaveError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), saveErr: errors.New("disk full")}
	sess, err := classroom.NewLiveQuizSession(csQuizID, csTenantA, csInstructorGCID)
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	if err := sess.Start(time.Now().UTC()); err != nil { // LIVE so Close is valid
		t.Fatalf("Start: %v", err)
	}
	if err := store.ClassroomSessionRepo.Save(sess); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sess.ID+"/close", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "persist failed") {
		t.Fatalf("close save error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_SubmitResponse_LookupError_500(t *testing.T) {
	store := &cs3SessionStore{ClassroomSessionRepo: inmem.NewClassroomSessionRepo(), getErr: errors.New("db down")}
	srv := cs3Server(t, store, nil, nil)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/any-id/responses",
		`{"question_id":"q","choice":"A"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("submit lookup error: want 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_SubmitResponse_EmptyChoice_400(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"q1","choice":""}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty choice: want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCs3_JoinSession_NoGCID_401(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/any-id/join", `{"nickname":"X"}`, csTenantA, "", "learner")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("join no gcid: want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestCs3_Snapshot_RevealsSkipsGhostQuestion — the post-close `reveals`
// loop walks AskedQuestionIDs; a question the quiz never taught must be
// skipped (continue) rather than crash the snapshot.
func TestCs3_Snapshot_RevealsSkipsGhostQuestion(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)
	transitionToLive(t, srv, sid, csTenantA)
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance",
		`{"question_id":"ghost"}`, csTenantA, csInstructorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("advance ghost: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/close", "", csTenantA, csInstructorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("close: %d body=%s", rec.Code, rec.Body.String())
	}
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	if reveals, ok := body["reveals"]; ok {
		if arr, _ := reveals.([]any); len(arr) != 0 {
			t.Fatalf("ghost-only session must not render reveals: %v", reveals)
		}
	}
}

// -----------------------------------------------------------------------------
// classroomSessionDTO / scoreboardWire leftovers
// -----------------------------------------------------------------------------

// TestCs3_Snapshot_ScoreboardCapAndPodiumTop5 — scoreboardWire's cap branch
// (n=leaderboardTopN) needs >20 scoreboard rows; joined-but-unanswered
// participants still appear at 0 points. The CLOSED podium caps at 5 while
// final_scoreboard stays uncapped.
func TestCs3_Snapshot_ScoreboardCapAndPodiumTop5(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)
	transitionToLive(t, srv, sid, csTenantA)
	for i := 0; i < 21; i++ {
		gcid := fmt.Sprintf("019e2f93-d586-71b5-8c3d-%012d", i)
		nick := fmt.Sprintf("P%d", i)
		if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/join", `{"nickname":"`+nick+`"}`, csTenantA, gcid, "learner"); rec.Code != http.StatusOK {
			t.Fatalf("join %d: status=%d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	sb, _ := body["scoreboard"].([]any)
	if len(sb) != 20 {
		t.Fatalf("scoreboard len=%d want 20 (capped at leaderboardTopN)", len(sb))
	}
	closeRec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/close", "", csTenantA, csInstructorGCID, "instructor")
	if closeRec.Code != http.StatusOK {
		t.Fatalf("close: %d body=%s", closeRec.Code, closeRec.Body.String())
	}
	body = decodeStageMap(t, closeRec.Body.Bytes())
	podium, _ := body["podium"].([]any)
	final, _ := body["final_scoreboard"].([]any)
	if len(podium) != 5 {
		t.Fatalf("podium len=%d want 5", len(podium))
	}
	if len(final) != 21 {
		t.Fatalf("final_scoreboard len=%d want 21 (uncapped)", len(final))
	}
}

// TestCs3_Snapshot_FinalScoreboardFallsBackToLiveBoard — a CLOSED session
// whose frozen FinalScoreboard is nil (pre-L5.2 writer) must fall back to
// the live fold for the final board + podium.
func TestCs3_Snapshot_FinalScoreboardFallsBackToLiveBoard(t *testing.T) {
	srv, sessions, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)
	transitionToLive(t, srv, sid, csTenantA)
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/join", `{"nickname":"Solo"}`, csTenantA, csLearner1GCID, "learner"); rec.Code != http.StatusOK {
		t.Fatalf("join: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/close", "", csTenantA, csInstructorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("close: %d body=%s", rec.Code, rec.Body.String())
	}
	s, ok, _ := sessions.Get(sid)
	if !ok {
		t.Fatalf("session missing")
	}
	s.FinalScoreboard = nil // simulate a pre-L5.2 writer
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	final, _ := body["final_scoreboard"].([]any)
	if len(final) != 1 {
		t.Fatalf("final_scoreboard len=%d want 1 (fallback to live fold)", len(final))
	}
}

// TestCs3_Snapshot_MeBlockWithoutLastResult — an UNGRADED submitter (no quiz
// wired) appears in the board with `me` but no last_result (no graded lines).
func TestCs3_Snapshot_MeBlockWithoutLastResult(t *testing.T) {
	srv, _ := newClassroomSessionServer()
	id := startSessionHelper(t, srv, csTenantA)
	transitionToLive(t, srv, id, csTenantA)
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/join", `{"nickname":"Neo"}`, csTenantA, csLearner1GCID, "learner"); rec.Code != http.StatusOK {
		t.Fatalf("join: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+id+"/responses",
		`{"question_id":"q1","choice":"B"}`, csTenantA, csLearner1GCID, "learner"); rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d body=%s", rec.Code, rec.Body.String())
	}
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+id, "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	me, _ := body["me"].(map[string]any)
	if me == nil || me["nickname"] != "Neo" {
		t.Fatalf("me=%v want Neo", body["me"])
	}
	if _, ok := me["last_result"]; ok {
		t.Fatalf("ungraded caller must not carry last_result: %v", me)
	}
}

// TestCs3_Snapshot_OpenQuestionOmittedForGhostQuestion — a CurrentQuestionID
// the quiz never taught renders the bare current_question_id but no
// open_question block.
func TestCs3_Snapshot_OpenQuestionOmittedForGhostQuestion(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)
	transitionToLive(t, srv, sid, csTenantA)
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance",
		`{"question_id":"ghost"}`, csTenantA, csInstructorGCID, "instructor"); rec.Code != http.StatusOK {
		t.Fatalf("advance: %d body=%s", rec.Code, rec.Body.String())
	}
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	if _, ok := body["open_question"]; ok {
		t.Fatalf("ghost question must not render open_question: %v", body["open_question"])
	}
	if body["current_question_id"] != "ghost" {
		t.Fatalf("current_question_id=%v want ghost", body["current_question_id"])
	}
}
