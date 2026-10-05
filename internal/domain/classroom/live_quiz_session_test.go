// live_quiz_session_test.go — RED-phase TDD coverage for the
// LiveQuizSession aggregate (R+ M8/M9/M10/M11).
package classroom

import (
	"testing"
	"time"
)

func TestNewLiveQuizSession_RequiresFields(t *testing.T) {
	_, err := NewLiveQuizSession("", "00000000-0000-7000-8000-000000000000", "instr")
	if err == nil {
		t.Fatalf("want err when live_quiz_id empty; got nil")
	}
	_, err = NewLiveQuizSession("quiz-1", "", "instr")
	if err == nil {
		t.Fatalf("want err when tenant_id empty; got nil")
	}
}

func TestNewLiveQuizSession_StartsArmed(t *testing.T) {
	s, err := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s.State != LiveQuizSessionStateArmed {
		t.Fatalf("want ARMED; got %s", s.State)
	}
	if s.ID == "" {
		t.Fatalf("want UUIDv7 id")
	}
	if len(s.QuestionResponses) != 0 {
		t.Fatalf("want 0 responses; got %d", len(s.QuestionResponses))
	}
}

func TestStart_ArmedToLive(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	now := time.Now()
	if err := s.Start(now); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	if s.State != LiveQuizSessionStateLive {
		t.Fatalf("want LIVE; got %s", s.State)
	}
	if s.StartedAt == nil || !s.StartedAt.Equal(now.UTC()) {
		t.Fatalf("StartedAt must equal now")
	}
}

func TestStart_NotIdempotent_FromOther(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	_ = s.Start(time.Now())
	if err := s.Close(time.Now()); err != nil {
		t.Fatalf("Close err: %v", err)
	}
	if err := s.Start(time.Now()); err == nil {
		t.Fatalf("want err starting CLOSED session; got nil")
	}
}

func TestStart_IdempotentWhenAlreadyLive(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	now := time.Now()
	_ = s.Start(now)
	first := *s.StartedAt
	if err := s.Start(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("idempotent Start err: %v", err)
	}
	if !s.StartedAt.Equal(first) {
		t.Fatalf("StartedAt should not change on idempotent re-start")
	}
}

func TestSubmitResponse_OnlyWhenLive(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	// ARMED → reject
	if err := s.SubmitResponse("q1", "learner-a", "Sprint Planning", time.Now()); err == nil {
		t.Fatalf("want err submitting to ARMED session; got nil")
	}
	_ = s.Start(time.Now())
	if err := s.SubmitResponse("q1", "learner-a", "Sprint Planning", time.Now()); err != nil {
		t.Fatalf("submit err: %v", err)
	}
	if len(s.QuestionResponses) != 1 {
		t.Fatalf("want 1 response; got %d", len(s.QuestionResponses))
	}
}

func TestSubmitResponse_FirstWriteWins_PerLearnerPerQuestion(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	_ = s.Start(time.Now())
	_ = s.SubmitResponse("q1", "learner-a", "Sprint Planning", time.Now())
	// second submit for the same (learner, question) must be rejected as duplicate.
	err := s.SubmitResponse("q1", "learner-a", "Sprint Review", time.Now().Add(time.Second))
	if err == nil {
		t.Fatalf("want duplicate-submit error; got nil")
	}
	if len(s.QuestionResponses) != 1 {
		t.Fatalf("want 1 response post-dup; got %d", len(s.QuestionResponses))
	}
	if s.QuestionResponses[0].Choice != "Sprint Planning" {
		t.Fatalf("first-wins must preserve original choice; got %s", s.QuestionResponses[0].Choice)
	}
}

func TestSubmitResponse_DifferentLearnersAllowed(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	_ = s.Start(time.Now())
	_ = s.SubmitResponse("q1", "learner-a", "B", time.Now())
	_ = s.SubmitResponse("q1", "learner-b", "C", time.Now())
	if len(s.QuestionResponses) != 2 {
		t.Fatalf("want 2 responses; got %d", len(s.QuestionResponses))
	}
}

func TestClose_LiveToClosed(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	_ = s.Start(time.Now())
	now := time.Now()
	if err := s.Close(now); err != nil {
		t.Fatalf("Close err: %v", err)
	}
	if s.State != LiveQuizSessionStateClosed {
		t.Fatalf("want CLOSED; got %s", s.State)
	}
	if s.EndedAt == nil {
		t.Fatalf("want EndedAt set")
	}
}

func TestClose_RejectsArmed(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	if err := s.Close(time.Now()); err == nil {
		t.Fatalf("want err closing ARMED session; got nil")
	}
}

func TestClose_IdempotentOnClosed(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	_ = s.Start(time.Now())
	_ = s.Close(time.Now())
	if err := s.Close(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("idempotent close should not err; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Live Classroom deltas — ADR-168: instructor advance sets the per-session
// question-open clock; TimeDecayScore grades correctness × answer speed.
// ---------------------------------------------------------------------------

func TestAdvanceTo_SetsCurrentQuestionAndOpenClock(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	// ARMED → reject
	if err := s.AdvanceTo("q1", time.Now()); err == nil {
		t.Fatalf("want err advancing an ARMED session; got nil")
	}
	_ = s.Start(time.Now())
	now := time.Now()
	if err := s.AdvanceTo("q1", now); err != nil {
		t.Fatalf("AdvanceTo err: %v", err)
	}
	if s.CurrentQuestionID != "q1" {
		t.Fatalf("want CurrentQuestionID q1; got %q", s.CurrentQuestionID)
	}
	if s.CurrentQuestionOpenedAt == nil || !s.CurrentQuestionOpenedAt.Equal(now.UTC()) {
		t.Fatalf("want CurrentQuestionOpenedAt == now")
	}
	// advancing to the next question re-opens the clock
	later := now.Add(time.Minute)
	_ = s.AdvanceTo("q2", later)
	if s.CurrentQuestionID != "q2" || !s.CurrentQuestionOpenedAt.Equal(later.UTC()) {
		t.Fatalf("advance to q2 must re-open clock")
	}
}

func TestAdvanceTo_RequiresQuestionID(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	_ = s.Start(time.Now())
	if err := s.AdvanceTo("  ", time.Now()); err == nil {
		t.Fatalf("want err for blank question id; got nil")
	}
}

func TestTimeDecayScore(t *testing.T) {
	const points, timer = 1000, 30
	cases := []struct {
		name    string
		correct bool
		elapsed time.Duration
		timer   int
		want    int
	}{
		{"wrong is zero", false, 0, timer, 0},
		{"correct instant is full", true, 0, timer, 1000},
		{"correct at half timer is 75%", true, 15 * time.Second, timer, 750},
		{"correct at full timer is half", true, 30 * time.Second, timer, 500},
		{"correct after timer clamps to half", true, 90 * time.Second, timer, 500},
		{"no timer means full when correct", true, 5 * time.Second, 0, 1000},
		{"zero points is zero", true, 0, timer, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pts := points
			if c.name == "zero points is zero" {
				pts = 0
			}
			got := TimeDecayScore(c.correct, pts, c.elapsed, c.timer)
			if got != c.want {
				t.Fatalf("TimeDecayScore(%v,%d,%v,%d)=%d; want %d",
					c.correct, pts, c.elapsed, c.timer, got, c.want)
			}
		})
	}
}

func TestResponseCounts(t *testing.T) {
	s, _ := NewLiveQuizSession("quiz-1", "00000000-0000-7000-8000-000000000000", "instr")
	_ = s.Start(time.Now())
	_ = s.SubmitResponse("q1", "a", "A", time.Now())
	_ = s.SubmitResponse("q1", "b", "B", time.Now())
	_ = s.SubmitResponse("q1", "c", "B", time.Now())
	counts := s.CountResponses("q1")
	if counts["A"] != 1 || counts["B"] != 2 {
		t.Fatalf("counts wrong: %#v", counts)
	}
	if got := s.CountResponses("missing"); len(got) != 0 {
		t.Fatalf("want empty counts for missing q; got %#v", got)
	}
}
