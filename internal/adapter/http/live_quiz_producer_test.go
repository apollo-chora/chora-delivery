// live_quiz_producer_test.go — ADR-168 producer hot-path TDD: instructor
// advance opens the question clock + fans out; a graded learner answer bumps
// the authoritative tally + ZSET leaderboard, fans out answer_graded, and emits
// the durable score_awarded event.
package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/adapter/realtime"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

type producerRig struct {
	sessions  *inmem.ClassroomSessionRepo
	bp        *realtime.MemBackplane
	tally     *realtime.MemTally
	lb        *realtime.MemLeaderboard
	pub       *events.InMemoryPublisher
	sessionID string
	quizID    string
}

// buildProducerServer seeds a LIVE session for a published quiz (q1: correct
// "Sprint Planning", 1000 pts, 30s timer) and wires full realtime deps.
func buildProducerServer(t *testing.T) (http.Handler, *producerRig) {
	t.Helper()
	quizRepo := inmem.NewLiveQuizRepo()
	sessions := inmem.NewClassroomSessionRepo()
	bp, tally, lb := realtime.NewMemBackplane(), realtime.NewMemTally(), realtime.NewMemLeaderboard()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")

	quiz, err := classroom.NewLiveQuiz(classroom.NewLiveQuizInput{
		TenantID: csTenantA, InstructorGCID: csInstructorGCID, Title: "Scrum Basics",
	})
	if err != nil {
		t.Fatalf("NewLiveQuiz: %v", err)
	}
	if err := quiz.AddQuestion(classroom.LiveQuizQuestion{
		QuestionID: "q1", Prompt: "Which ceremony starts a Sprint?", TimerSecs: 30, Points: 1000,
		// CR2-C3: question composed by linking a LearningAtom — atom_id +
		// denormalised topic_tags flow through to the durable score_awarded.
		AtomID: "atom-q1-uuid", TopicTags: []string{"scrum", "ceremonies"},
		Options: []classroom.LiveQuizOption{
			{Label: "Sprint Planning", IsCorrect: true, Explainer: "It kicks off the Sprint."},
			{Label: "Daily Scrum", IsCorrect: false},
		},
	}); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	_ = quiz.Publish(time.Now())
	quizRepo.Save(quiz)

	s, err := classroom.NewLiveQuizSession(quiz.ID, csTenantA, csInstructorGCID)
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	_ = s.Start(time.Now())
	sessions.Save(s)

	srv := httpapi.NewServer(httpapi.Deps{
		ClassroomSessions:   sessions,
		LiveQuizzes:         quizRepo,
		RealtimeBackplane:   bp,
		RealtimeTally:       tally,
		RealtimeLeaderboard: lb,
		Publisher:           pub,
	})
	return srv, &producerRig{sessions: sessions, bp: bp, tally: tally, lb: lb, pub: pub, sessionID: s.ID, quizID: quiz.ID}
}

func TestAdvance_OpensQuestionAndFansOut(t *testing.T) {
	srv, rig := buildProducerServer(t)
	ch, cancel, _ := rig.bp.PSubscribe(context.Background(), "rt:quiz:*")
	defer cancel()

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("advance status=%d body=%s", rec.Code, rec.Body.String())
	}
	s, _, _ := rig.sessions.Get(rig.sessionID)
	if s.CurrentQuestionID != "q1" || s.CurrentQuestionOpenedAt == nil {
		t.Fatalf("advance did not open the question clock: %+v", s)
	}
	select {
	case msg := <-ch:
		if msg.Channel != realtime.QuizChannel(rig.sessionID) {
			t.Fatalf("fan-out wrong channel: %s", msg.Channel)
		}
	case <-time.After(time.Second):
		t.Fatalf("no question_advanced fan-out")
	}
}

func TestAdvance_RejectsNonInstructor(t *testing.T) {
	srv, rig := buildProducerServer(t)
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 for learner advancing; got %d", rec.Code)
	}
}

func TestSubmit_GradesTallyLeaderboardFanOutDurable(t *testing.T) {
	srv, rig := buildProducerServer(t)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")

	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/responses",
		`{"question_id":"q1","choice":"Sprint Planning"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	if snap, _ := rig.tally.Snapshot(ctx, rig.sessionID, "q1"); snap["Sprint Planning"] != 1 {
		t.Fatalf("tally not bumped: %#v", snap)
	}
	top, _ := rig.lb.Top(ctx, rig.sessionID, 10)
	if len(top) != 1 || top[0].GCID != csLearner1GCID || top[0].Score <= 0 {
		t.Fatalf("leaderboard wrong: %#v", top)
	}
	found := false
	for _, e := range rig.pub.History() {
		if e.Topic == events.TopicLiveQuizScoreAwarded {
			found = true
			if e.Payload["correct"] != true {
				t.Fatalf("score event should be correct: %#v", e.Payload)
			}
		}
	}
	if !found {
		t.Fatalf("no durable score_awarded event emitted")
	}
}

// CR2-C3: a learner answering an atom-LINKED question produces a durable
// score_awarded carrying atom_id + topic_tags so chora-consumption's derived-
// weakness subscriber maps (atom, correct) → mastery without a cross-DB read.
func TestSubmit_AtomLinkedQuestion_ScoreCarriesAtomAndTags(t *testing.T) {
	srv, rig := buildProducerServer(t)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/responses",
		`{"question_id":"q1","choice":"Sprint Planning"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	found := false
	for _, e := range rig.pub.History() {
		if e.Topic == events.TopicLiveQuizScoreAwarded {
			payload, found = e.Payload, true
		}
	}
	if !found {
		t.Fatalf("no score_awarded event emitted")
	}
	if payload["atom_id"] != "atom-q1-uuid" {
		t.Errorf("atom_id=%#v want atom-q1-uuid", payload["atom_id"])
	}
	tags, ok := payload["topic_tags"].([]string)
	if !ok || len(tags) != 2 || tags[0] != "scrum" || tags[1] != "ceremonies" {
		t.Errorf("topic_tags=%#v want [scrum ceremonies]", payload["topic_tags"])
	}
}

func TestSubmit_WrongAnswerScoresZero(t *testing.T) {
	srv, rig := buildProducerServer(t)
	_ = doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/advance",
		`{"question_id":"q1"}`, csTenantA, csInstructorGCID, "instructor")
	rec := doCS(t, srv, "POST", "/api/v1/classroom-sessions/"+rig.sessionID+"/responses",
		`{"question_id":"q1","choice":"Daily Scrum"}`, csTenantA, csLearner1GCID, "learner")
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status=%d", rec.Code)
	}
	if top, _ := rig.lb.Top(context.Background(), rig.sessionID, 10); len(top) != 0 {
		t.Fatalf("wrong answer must not credit leaderboard: %#v", top)
	}
}
