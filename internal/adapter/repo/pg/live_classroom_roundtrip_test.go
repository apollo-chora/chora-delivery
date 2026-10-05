// live_classroom_roundtrip_test.go — local (no-DB) verification that the
// JSONB snapshot persistence is LOSSLESS for the LiveQuiz + LiveQuizSession
// aggregates. The pg adapters store json.Marshal(aggregate) in the `data`
// column and json.Unmarshal it back on Get/List; this asserts that round-trip
// preserves every field the realtime/scoring logic depends on (nested
// questions/options/explainer, time-clock fields, question responses). The
// SQL execution itself is covered by the DSN-gated integration_test.go in CI.
package pg

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

func TestJSONBRoundTrip_LiveQuiz_Lossless(t *testing.T) {
	now := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	pub := now.Add(time.Minute)
	q := &classroom.LiveQuiz{
		ID:             "019e72cc-6108-77a1-a129-4b5fb3a56c11",
		TenantID:       "019e2f93-d586-71b5-8c3d-e2b0d0d50300",
		CourseID:       "019e2f93-d586-71b5-8c3d-e2b0d0d50400",
		InstructorGCID: "019e2f93-d586-71b5-8c3d-e2b0d0d50310",
		Title:          "Scrum Basics",
		State:          classroom.LiveQuizStatePublished,
		Questions: []classroom.LiveQuizQuestion{{
			QuestionID: "q1", Prompt: "Pick A", TimerSecs: 30, Points: 1000, AtomID: "atom-1",
			Options: []classroom.LiveQuizOption{
				{Label: "A", IsCorrect: true, Explainer: "because"},
				{Label: "B", IsCorrect: false},
			},
		}},
		QuizTimeLimitSecs: 600,
		ExplainerMode:     classroom.ExplainerModeEndOfQuestion,
		PublishedAt:       &pub,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got classroom.LiveQuiz
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(*q, got) {
		t.Fatalf("LiveQuiz round-trip lossy:\n want %#v\n  got %#v", *q, got)
	}
}

func TestJSONBRoundTrip_LiveQuizSession_Lossless(t *testing.T) {
	now := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	opened := now.Add(2 * time.Minute)
	started := now.Add(time.Minute)
	s := &classroom.LiveQuizSession{
		ID:                      "019e72d1-dede-7aaf-93d9-14040876f4ff",
		LiveQuizID:              "019e72cc-6108-77a1-a129-4b5fb3a56c11",
		TenantID:                "019e2f93-d586-71b5-8c3d-e2b0d0d50300",
		InstructorGCID:          "019e2f93-d586-71b5-8c3d-e2b0d0d50310",
		State:                   classroom.LiveQuizSessionStateLive,
		CurrentQuestionID:       "q1",
		CurrentQuestionOpenedAt: &opened,
		StartedAt:               &started,
		QuestionResponses: []classroom.QuestionResponse{
			{QuestionID: "q1", LearnerGCID: "019e2f93-d586-71b5-8c3d-e2b0d0d50320", Choice: "A", SubmittedAt: opened.Add(3 * time.Second)},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got classroom.LiveQuizSession
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(*s, got) {
		t.Fatalf("LiveQuizSession round-trip lossy:\n want %#v\n  got %#v", *s, got)
	}
}

// nilTxRunner adapters fail LOUD: an unwired repo is a WIRING BUG, so Save AND
// Get both return ErrNotImplemented. Get used to "fail safe" by reporting a
// clean miss — which is not safe at all: it reports an unwired adapter as an
// empty database, and the caller cannot tell (CHO-2184).
func TestNilTxRunner_FailSafe(t *testing.T) {
	lq := NewLiveQuizRepo(nil)
	if err := lq.Save(&classroom.LiveQuiz{ID: "x"}); err != ErrNotImplemented {
		t.Errorf("nil-tx LiveQuiz.Save = %v, want ErrNotImplemented", err)
	}
	if _, ok, err := lq.Get("x"); ok || err != ErrNotImplemented {
		t.Errorf("nil-tx LiveQuiz.Get = (ok=%v, err=%v), want (false, ErrNotImplemented)", ok, err)
	}
	cs := NewClassroomSessionRepo(nil)
	if err := cs.Save(&classroom.LiveQuizSession{ID: "x"}); err != ErrNotImplemented {
		t.Errorf("nil-tx Session.Save = %v, want ErrNotImplemented", err)
	}
	if got := cs.ListByTenant("t"); got != nil {
		t.Errorf("nil-tx Session.ListByTenant = %v, want nil", got)
	}
}
