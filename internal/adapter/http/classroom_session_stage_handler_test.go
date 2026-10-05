// classroom_session_stage_handler_test.go — L5.2 Live Classroom stage HTTP surface
// (ADR-179, CHO-1704): join-by-code + nickname lobby, learner-safe
// open_question + quiz projections (leak-scans), server-enforced answer
// window, streak+double scoring through the wire, scoreboard/podium, reveal
// gating per explainer_mode.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// newStageServer wires sessions + quizzes (the graded path needs the quiz
// template) against fresh in-mem repos.
func newStageServer() (http.Handler, *inmem.ClassroomSessionRepo, *inmem.LiveQuizRepo) {
	sessions := inmem.NewClassroomSessionRepo()
	quizzes := inmem.NewLiveQuizRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:           inmem.NewCourseRepo(),
		Bookings:          inmem.NewBookingRepo(),
		ClassroomSessions: sessions,
		LiveQuizzes:       quizzes,
	})
	return srv, sessions, quizzes
}

// seedStageQuiz publishes a 2-question quiz (q2 is a double-points round).
// Timer 0 on both → no decay variance + never auto-locks (deterministic
// scoring asserts).
func seedStageQuiz(t *testing.T, quizzes *inmem.LiveQuizRepo, mode classroom.ExplainerMode) *classroom.LiveQuiz {
	t.Helper()
	quiz, err := classroom.NewLiveQuiz(classroom.NewLiveQuizInput{
		TenantID: csTenantA, InstructorGCID: csInstructorGCID,
		Title: "Stage Quiz", ExplainerMode: mode,
	})
	if err != nil {
		t.Fatalf("NewLiveQuiz: %v", err)
	}
	q1 := classroom.LiveQuizQuestion{
		QuestionID: "q1", Prompt: "What is 2+2?",
		Options: []classroom.LiveQuizOption{
			{Label: "4", IsCorrect: true, Explainer: "two plus two"},
			{Label: "5"},
		},
		TimerSecs: 0, Points: 10,
	}
	q2 := classroom.LiveQuizQuestion{
		QuestionID: "q2", Prompt: "What is 3+3?",
		Options: []classroom.LiveQuizOption{
			{Label: "6", IsCorrect: true},
			{Label: "7"},
		},
		TimerSecs: 0, Points: 10, DoublePoints: true,
	}
	if err := quiz.AddQuestion(q1); err != nil {
		t.Fatal(err)
	}
	if err := quiz.AddQuestion(q2); err != nil {
		t.Fatal(err)
	}
	if err := quiz.Publish(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := quizzes.Save(quiz); err != nil {
		t.Fatal(err)
	}
	return quiz
}

// createStageSession launches an ARMED session and returns (sessionID, joinCode).
func createStageSession(t *testing.T, srv http.Handler, quizID string) (string, string) {
	t.Helper()
	rec := doCS(t, srv, "POST", "/api/v1/live-quizzes/"+quizID+"/sessions", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	id, _ := body["id"].(string)
	code, _ := body["join_code"].(string)
	if id == "" || len(code) != 6 {
		t.Fatalf("session create body missing id/join_code: %s", rec.Body.String())
	}
	return id, code
}

func decodeStageMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode body: %v (%s)", err, b)
	}
	return m
}

// --- Join + lobby ----------------------------------------------------------

func TestStage_JoinNickname_RosterAndCollisions(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/join",
		`{"nickname":"MathWizard"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("join = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	parts, _ := body["participants"].([]any)
	if len(parts) != 1 {
		t.Fatalf("participants = %v", body["participants"])
	}
	first, _ := parts[0].(map[string]any)
	if first["nickname"] != "MathWizard" {
		t.Fatalf("roster nickname = %v", first["nickname"])
	}
	if _, hasGCID := first["gcid"]; hasGCID {
		t.Fatal("roster entry leaks gcid (nicknames only on the wire)")
	}
	me, _ := body["me"].(map[string]any)
	if me == nil || me["nickname"] != "MathWizard" {
		t.Fatalf("me block = %v", body["me"])
	}

	// Case-insensitive collision from another gcid → 409.
	rec = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/join",
		`{"nickname":"mathwizard"}`, csTenantA, csLearner2GCID, "learner")
	if rec.Code != http.StatusConflict {
		t.Fatalf("dup nickname = %d, want 409", rec.Code)
	}
	// Profane (leet-folded) → 400.
	rec = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/join",
		`{"nickname":"sh1tlord"}`, csTenantA, csLearner2GCID, "learner")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("profane nickname = %d, want 400", rec.Code)
	}
}

func TestStage_ResolveByCode(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, code := createStageSession(t, srv, quiz.ID)

	// Messy user input normalises (lowercase + dash + spaces).
	messy := strings.ToLower(code[:3]) + "-" + strings.ToLower(code[3:])
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/by-code/"+messy, "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("by-code = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	if body["session_id"] != sid || body["quiz_title"] != "Stage Quiz" {
		t.Fatalf("by-code body = %s", rec.Body.String())
	}
	rec = doCS(t, srv, "GET", "/api/v1/classroom-sessions/by-code/ZZZZZZ", "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown code = %d, want 404", rec.Code)
	}
}

// --- Learner-safe projections (leak scans) ----------------------------------

func TestStage_SnapshotOpenQuestion_NoAnswerKey(t *testing.T) {
	srv, sessions, quizzes := newStageServer()
	quiz, err := classroom.NewLiveQuiz(classroom.NewLiveQuizInput{
		TenantID: csTenantA, InstructorGCID: csInstructorGCID, Title: "Timed"})
	if err != nil {
		t.Fatal(err)
	}
	timed := classroom.LiveQuizQuestion{
		QuestionID: "tq1", Prompt: "Timed?",
		Options:   []classroom.LiveQuizOption{{Label: "yes", IsCorrect: true, Explainer: "secret"}, {Label: "no"}},
		TimerSecs: 30, Points: 10,
	}
	if err := quiz.AddQuestion(timed); err != nil {
		t.Fatal(err)
	}
	if err := quiz.Publish(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := quizzes.Save(quiz); err != nil {
		t.Fatal(err)
	}
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"tq1"}`, csTenantA, csInstructorGCID, "instructor")

	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot = %d", rec.Code)
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "is_correct") || strings.Contains(raw, "explainer\"") || strings.Contains(raw, "secret") {
		t.Fatalf("snapshot leaks the answer key (ADR-179 D3): %s", raw)
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	oq, _ := body["open_question"].(map[string]any)
	if oq == nil || oq["prompt"] != "Timed?" {
		t.Fatalf("open_question missing/wrong: %v", body["open_question"])
	}
	if oq["locks_at"] == nil || oq["opened_at"] == nil {
		t.Fatalf("open_question missing countdown anchors: %v", oq)
	}
	if oq["locked"] != false {
		t.Fatalf("freshly advanced question must be unlocked: %v", oq["locked"])
	}
	opts, _ := oq["options"].([]any)
	if len(opts) != 2 {
		t.Fatalf("open_question options = %v", oq["options"])
	}
	_ = sessions // (used by sibling tests)
}

func TestStage_QuizGet_RoleForkedProjection(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)

	// Learner role → stripped.
	rec := doCS(t, srv, "GET", "/api/v1/live-quizzes/"+quiz.ID, "", csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("learner quiz GET = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "is_correct") || strings.Contains(rec.Body.String(), "two plus two") {
		t.Fatalf("learner quiz GET leaks the answer key (ADR-179 D3): %s", rec.Body.String())
	}
	// Instructor role → full authoring DTO (incl. double_points flag).
	rec = doCS(t, srv, "GET", "/api/v1/live-quizzes/"+quiz.ID, "", csTenantA, csInstructorGCID, "instructor")
	if !strings.Contains(rec.Body.String(), "is_correct") {
		t.Fatalf("instructor quiz GET lost the answer key: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"double_points":true`) {
		t.Fatalf("quiz DTO lost double_points: %s", rec.Body.String())
	}
}

// --- Answer window ----------------------------------------------------------

func TestStage_SubmitAfterLock_409(t *testing.T) {
	srv, sessions, quizzes := newStageServer()
	quiz, err := classroom.NewLiveQuiz(classroom.NewLiveQuizInput{
		TenantID: csTenantA, InstructorGCID: csInstructorGCID, Title: "Timed"})
	if err != nil {
		t.Fatal(err)
	}
	timed := classroom.LiveQuizQuestion{
		QuestionID: "tq1", Prompt: "Timed?",
		Options:   []classroom.LiveQuizOption{{Label: "yes", IsCorrect: true}, {Label: "no"}},
		TimerSecs: 30, Points: 10,
	}
	if err := quiz.AddQuestion(timed); err != nil {
		t.Fatal(err)
	}
	if err := quiz.Publish(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := quizzes.Save(quiz); err != nil {
		t.Fatal(err)
	}
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"tq1"}`, csTenantA, csInstructorGCID, "instructor")

	// Time-travel the open clock past timer+grace (inmem shares the pointer).
	s, ok, _ := sessions.Get(sid)
	if !ok {
		t.Fatal("session missing")
	}
	past := time.Now().UTC().Add(-40 * time.Second)
	s.CurrentQuestionOpenedAt = &past

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"tq1","choice":"yes"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "locked") {
		t.Fatalf("late submit = %d body=%s, want 409 locked", rec.Code, rec.Body.String())
	}
	if got := len(s.QuestionResponses); got != 0 {
		t.Fatalf("locked submit recorded a response (%d)", got)
	}
}

func TestStage_SubmitNonCurrentQuestion_409(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"q2","choice":"6"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not the current open question") {
		t.Fatalf("non-current submit = %d body=%s, want 409 question-not-open", rec.Code, rec.Body.String())
	}
}

func TestStage_AdvanceReAskedQuestion_409(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q2"}`, csTenantA, csInstructorGCID, "instructor")

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already asked") {
		t.Fatalf("re-advance = %d body=%s, want 409 already-asked", rec.Code, rec.Body.String())
	}
}

// --- Scoring through the wire ------------------------------------------------

func TestStage_GradedSubmit_StreakDoubleAndScoreboard(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/join", `{"nickname":"Ace"}`, csTenantA, csLearner1GCID, "learner")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")

	// Q1: correct, timer 0 → full 10, streak 1.
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"q1","choice":"4"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusCreated {
		t.Fatalf("q1 submit = %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	yr, _ := body["your_result"].(map[string]any)
	if yr == nil || yr["awarded_points"] != float64(10) || yr["streak_after"] != float64(1) {
		t.Fatalf("q1 your_result = %v", body["your_result"])
	}

	// Q2: double-points round on streak 1 → 2·10 + round(20·0.1·1)=2 → 22.
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q2"}`, csTenantA, csInstructorGCID, "instructor")
	rec = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"q2","choice":"6"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusCreated {
		t.Fatalf("q2 submit = %d body=%s", rec.Code, rec.Body.String())
	}
	body = decodeStageMap(t, rec.Body.Bytes())
	yr, _ = body["your_result"].(map[string]any)
	if yr == nil || yr["awarded_points"] != float64(22) || yr["streak_bonus"] != float64(2) || yr["streak_after"] != float64(2) {
		t.Fatalf("q2 your_result = %v", body["your_result"])
	}
	me, _ := body["me"].(map[string]any)
	if me == nil || me["score"] != float64(32) || me["rank"] != float64(1) || me["nickname"] != "Ace" {
		t.Fatalf("me = %v", body["me"])
	}
	sb, _ := body["scoreboard"].([]any)
	if len(sb) == 0 {
		t.Fatalf("scoreboard missing once LIVE: %s", rec.Body.String())
	}
	top, _ := sb[0].(map[string]any)
	if top["nickname"] != "Ace" || top["score"] != float64(32) {
		t.Fatalf("scoreboard top = %v", sb[0])
	}
	if _, hasGCID := top["gcid"]; hasGCID {
		t.Fatal("scoreboard leaks gcid")
	}
}

// --- Podium + reveal ---------------------------------------------------------

func TestStage_PodiumOnClose(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"q1","choice":"4"}`, csTenantA, csLearner1GCID, "learner")

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/close", "", csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("close = %d", rec.Code)
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	podium, _ := body["podium"].([]any)
	finalBoard, _ := body["final_scoreboard"].([]any)
	if len(podium) != 1 || len(finalBoard) != 1 {
		t.Fatalf("podium/final_scoreboard = %v / %v", body["podium"], body["final_scoreboard"])
	}
	first, _ := podium[0].(map[string]any)
	if first["score"] != float64(10) || first["rank"] != float64(1) {
		t.Fatalf("podium[0] = %v", podium[0])
	}
}

func TestStage_RevealRespectsExplainerMode(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeEndOfSession)
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"q1","choice":"4"}`, csTenantA, csLearner1GCID, "learner")

	// END_OF_SESSION: no reveal while LIVE, even after the caller answered.
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csLearner1GCID, "learner")
	body := decodeStageMap(t, rec.Body.Bytes())
	if _, has := body["reveal"]; has {
		t.Fatalf("reveal present while LIVE under END_OF_SESSION: %v", body["reveal"])
	}
	// After close → reveal with the correct label.
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/close", "", csTenantA, csInstructorGCID, "instructor")
	rec = doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csLearner1GCID, "learner")
	body = decodeStageMap(t, rec.Body.Bytes())
	reveal, _ := body["reveal"].(map[string]any)
	if reveal == nil || reveal["correct_label"] != "4" {
		t.Fatalf("reveal after close = %v", body["reveal"])
	}
}

// --- Machine error codes -----------------------------------------------------

// assertStageErrCode pins the stage 4xx envelope:
// {"error": <status text>, "message": <human>, "code": <machine>}.
func assertStageErrCode(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, wantStatus, rec.Body.String())
	}
	body := decodeStageMap(t, rec.Body.Bytes())
	if body["code"] != wantCode {
		t.Fatalf("code = %v, want %q (body=%s)", body["code"], wantCode, rec.Body.String())
	}
	if msg, _ := body["message"].(string); msg == "" {
		t.Fatalf("message missing from error body: %s", rec.Body.String())
	}
}

// TestStage_ErrorBodiesCarryMachineCode — HANDOFF_L52 §4.3: the FE play-stage
// maps `code` onto distinct inline copy (profane vs taken vs closed) instead
// of guessing from the HTTP status, so every writeClassroomSessionError path
// must carry the contract-documented machine code.
func TestStage_ErrorBodiesCarryMachineCode(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeImmediate)
	sid, _ := createStageSession(t, srv, quiz.ID)

	join := "/api/v1/classroom-sessions/" + sid + "/join"
	if rec := doCS(t, srv, "POST", join, `{"nickname":"MathWizard"}`, csTenantA, csLearner1GCID, "learner"); rec.Code != http.StatusOK {
		t.Fatalf("seed join = %d body=%s", rec.Code, rec.Body.String())
	}

	// Nickname sanitiser → 400 with per-cause codes.
	assertStageErrCode(t,
		doCS(t, srv, "POST", join, `{"nickname":"sh1tlord"}`, csTenantA, csLearner2GCID, "learner"),
		http.StatusBadRequest, "nickname_profane")
	assertStageErrCode(t,
		doCS(t, srv, "POST", join, `{"nickname":"x"}`, csTenantA, csLearner2GCID, "learner"),
		http.StatusBadRequest, "nickname_required")

	// Roster collision (case-insensitive, other gcid) → 409 nickname_taken.
	assertStageErrCode(t,
		doCS(t, srv, "POST", join, `{"nickname":"mathwizard"}`, csTenantA, csLearner2GCID, "learner"),
		http.StatusConflict, "nickname_taken")

	// First-write-wins duplicate graded submit → 409 duplicate_response.
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	if rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"q1","choice":"4"}`, csTenantA, csLearner1GCID, "learner"); rec.Code != http.StatusCreated {
		t.Fatalf("first submit = %d body=%s", rec.Code, rec.Body.String())
	}
	assertStageErrCode(t,
		doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
			`{"question_id":"q1","choice":"5"}`, csTenantA, csLearner1GCID, "learner"),
		http.StatusConflict, "duplicate_response")

	// Re-presenting an asked question → 409 question_already_asked.
	assertStageErrCode(t,
		doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor"),
		http.StatusConflict, "question_already_asked")

	// CLOSED lobby → 409 session_closed.
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/close", "", csTenantA, csInstructorGCID, "instructor")
	assertStageErrCode(t,
		doCS(t, srv, "POST", join, `{"nickname":"Latecomer"}`, csTenantA, csLearner2GCID, "learner"),
		http.StatusConflict, "session_closed")
}

// --- Post-close full answer review (`reveals`) -------------------------------

// TestStage_RevealsAllQuestionsOnClose — HANDOFF_L52 §4.6: the single `reveal`
// block scopes to the CURRENT question, so END_OF_SESSION only ever revealed
// the last question. Once CLOSED the snapshot now carries a per-caller
// `reveals` array covering EVERY asked question (correct label + explainer +
// the caller's own choice/grade), for any explainer_mode except NEVER.
func TestStage_RevealsAllQuestionsOnClose(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeEndOfSession)
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/join", `{"nickname":"Ace"}`, csTenantA, csLearner1GCID, "learner")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"q1","choice":"4"}`, csTenantA, csLearner1GCID, "learner")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q2"}`, csTenantA, csInstructorGCID, "instructor")
	// q2 deliberately skipped by the learner.

	// While LIVE under END_OF_SESSION: neither reveal nor reveals.
	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csLearner1GCID, "learner")
	body := decodeStageMap(t, rec.Body.Bytes())
	if _, has := body["reveals"]; has {
		t.Fatalf("reveals present while LIVE: %v", body["reveals"])
	}

	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/close", "", csTenantA, csInstructorGCID, "instructor")
	rec = doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csLearner1GCID, "learner")
	body = decodeStageMap(t, rec.Body.Bytes())
	reveals, _ := body["reveals"].([]any)
	if len(reveals) != 2 {
		t.Fatalf("reveals len = %d, want 2 (body=%s)", len(reveals), rec.Body.String())
	}
	first, _ := reveals[0].(map[string]any)
	if first["question_id"] != "q1" || first["prompt"] != "What is 2+2?" ||
		first["correct_label"] != "4" || first["explainer"] != "two plus two" {
		t.Fatalf("reveals[0] = %v", reveals[0])
	}
	if first["your_choice"] != "4" || first["your_correct"] != true {
		t.Fatalf("reveals[0] caller grade = %v", reveals[0])
	}
	second, _ := reveals[1].(map[string]any)
	if second["question_id"] != "q2" || second["correct_label"] != "6" {
		t.Fatalf("reveals[1] = %v", reveals[1])
	}
	if _, has := second["your_choice"]; has {
		t.Fatal("skipped question must not carry your_choice")
	}

	// Header-only caller (no gcid) → per-caller blocks stay omitted.
	rec = doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, "", "")
	body = decodeStageMap(t, rec.Body.Bytes())
	if _, has := body["reveals"]; has {
		t.Fatal("reveals must be per-caller (gcid) only")
	}
}

// NEVER mode stays dark even after close — no review, no reveal.
func TestStage_RevealsNeverModeAbsentOnClose(t *testing.T) {
	srv, _, quizzes := newStageServer()
	quiz := seedStageQuiz(t, quizzes, classroom.ExplainerModeNever)
	sid, _ := createStageSession(t, srv, quiz.ID)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/start", "", csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/advance", `{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/responses",
		`{"question_id":"q1","choice":"4"}`, csTenantA, csLearner1GCID, "learner")
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+sid+"/close", "", csTenantA, csInstructorGCID, "instructor")

	rec := doCS(t, srv, "GET", "/api/v1/classroom-sessions/"+sid, "", csTenantA, csLearner1GCID, "learner")
	body := decodeStageMap(t, rec.Body.Bytes())
	if _, has := body["reveals"]; has {
		t.Fatalf("NEVER mode leaked reveals: %v", body["reveals"])
	}
	if _, has := body["reveal"]; has {
		t.Fatalf("NEVER mode leaked reveal: %v", body["reveal"])
	}
}
