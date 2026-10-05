// live_quiz_session_stage_test.go — RED tests for the L5.2 Live Classroom stage domain
// surface (ADR-179, CHO-1704): join-code lobby + nicknames, server-enforced
// answer window (lazy lock), streak + double-points scoring, scoreboard with
// deterministic tie-break, Close-frozen podium, reveal gating.
package classroom

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var stageT0 = time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)

func newStageSession(t *testing.T) *LiveQuizSession {
	t.Helper()
	s, err := NewLiveQuizSession("quiz-1", "tenant-1", "instr-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	return s
}

func newLiveStageSession(t *testing.T) *LiveQuizSession {
	t.Helper()
	s := newStageSession(t)
	if err := s.Start(stageT0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return s
}

func stageQuestion(id string, timerSecs, points int, double bool) LiveQuizQuestion {
	return LiveQuizQuestion{
		QuestionID: id,
		Prompt:     "prompt?",
		Options: []LiveQuizOption{
			{Label: "right", IsCorrect: true},
			{Label: "wrong"},
		},
		TimerSecs:    timerSecs,
		Points:       points,
		DoublePoints: double,
	}
}

// --- Join + roster --------------------------------------------------------

func TestJoin_ClaimsNickname_RosterOrdered(t *testing.T) {
	s := newStageSession(t)
	p1, err := s.Join("gcid-1", "MathWizard", stageT0)
	if err != nil {
		t.Fatalf("join 1: %v", err)
	}
	if p1.Nickname != "MathWizard" || p1.GCID != "gcid-1" {
		t.Fatalf("participant 1 = %+v", p1)
	}
	if _, err := s.Join("gcid-2", "QuizKid", stageT0.Add(5*time.Second)); err != nil {
		t.Fatalf("join 2: %v", err)
	}
	roster := s.Roster()
	if len(roster) != 2 {
		t.Fatalf("roster len = %d, want 2", len(roster))
	}
	if roster[0].Nickname != "MathWizard" || roster[1].Nickname != "QuizKid" {
		t.Fatalf("roster order wrong: %+v", roster)
	}
}

func TestJoin_DuplicateNicknameRejected_CaseInsensitive(t *testing.T) {
	s := newStageSession(t)
	if _, err := s.Join("gcid-1", "MathWizard", stageT0); err != nil {
		t.Fatalf("join 1: %v", err)
	}
	if _, err := s.Join("gcid-2", "mathwizard", stageT0); !errors.Is(err, ErrLiveQuizSessionNicknameTaken) {
		t.Fatalf("err = %v, want ErrLiveQuizSessionNicknameTaken", err)
	}
}

func TestJoin_IdempotentPerGCID_RenameOnlyWhileArmed(t *testing.T) {
	s := newStageSession(t)
	if _, err := s.Join("gcid-1", "Alpha", stageT0); err != nil {
		t.Fatalf("join: %v", err)
	}
	// Rename while ARMED is allowed; JoinedAt preserved.
	p, err := s.Join("gcid-1", "Beta", stageT0.Add(time.Minute))
	if err != nil {
		t.Fatalf("rename while ARMED: %v", err)
	}
	if p.Nickname != "Beta" || !p.JoinedAt.Equal(stageT0) {
		t.Fatalf("rename result = %+v (JoinedAt want %v)", p, stageT0)
	}
	// Once LIVE, identity freezes — re-join returns the existing participant.
	if err := s.Start(stageT0.Add(2 * time.Minute)); err != nil {
		t.Fatalf("start: %v", err)
	}
	p2, err := s.Join("gcid-1", "Gamma", stageT0.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("re-join while LIVE: %v", err)
	}
	if p2.Nickname != "Beta" {
		t.Fatalf("nickname after LIVE re-join = %q, want frozen %q", p2.Nickname, "Beta")
	}
}

func TestJoin_RejectedWhenClosed(t *testing.T) {
	s := newLiveStageSession(t)
	if err := s.Close(stageT0.Add(time.Hour)); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.Join("gcid-1", "Late", stageT0.Add(2*time.Hour)); !errors.Is(err, ErrLiveQuizSessionCannotJoin) {
		t.Fatalf("err = %v, want ErrLiveQuizSessionCannotJoin", err)
	}
}

// --- AdvanceTo asked-question log ----------------------------------------

func TestAdvanceTo_RejectsAlreadyAskedQuestion(t *testing.T) {
	s := newLiveStageSession(t)
	if err := s.AdvanceTo("q1", stageT0); err != nil {
		t.Fatalf("advance q1: %v", err)
	}
	if err := s.AdvanceTo("q2", stageT0.Add(time.Minute)); err != nil {
		t.Fatalf("advance q2: %v", err)
	}
	if err := s.AdvanceTo("q1", stageT0.Add(2*time.Minute)); !errors.Is(err, ErrLiveQuizSessionQuestionAlreadyAsked) {
		t.Fatalf("re-advance err = %v, want ErrLiveQuizSessionQuestionAlreadyAsked", err)
	}
	if len(s.AskedQuestionIDs) != 2 || s.AskedQuestionIDs[0] != "q1" || s.AskedQuestionIDs[1] != "q2" {
		t.Fatalf("AskedQuestionIDs = %v, want [q1 q2]", s.AskedQuestionIDs)
	}
}

// --- Lock window ----------------------------------------------------------

func TestQuestionLocked_Boundaries(t *testing.T) {
	opened := stageT0
	cases := []struct {
		name   string
		at     time.Duration
		timer  int
		locked bool
	}{
		{"in window", 29 * time.Second, 30, false},
		{"at timer expiry (grace open)", 30 * time.Second, 30, false},
		{"inside grace", 31900 * time.Millisecond, 30, false},
		{"at grace boundary", 32 * time.Second, 30, true},
		{"well past", time.Hour, 30, true},
		{"timer 0 never locks", 24 * time.Hour, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := QuestionLocked(&opened, c.timer, opened.Add(c.at))
			if got != c.locked {
				t.Fatalf("QuestionLocked(+%v, timer=%d) = %v, want %v", c.at, c.timer, got, c.locked)
			}
		})
	}
	if QuestionLocked(nil, 30, stageT0) {
		t.Fatal("nil openedAt must not lock")
	}
}

func TestSubmitGraded_LockWindow(t *testing.T) {
	s := newLiveStageSession(t)
	q := stageQuestion("q1", 30, 10, false)
	if err := s.AdvanceTo("q1", stageT0); err != nil {
		t.Fatalf("advance: %v", err)
	}
	// Past timer + grace → locked.
	_, err := s.SubmitGraded(SubmitGradedInput{Question: q, LearnerGCID: "gcid-1", Choice: "right", At: stageT0.Add(33 * time.Second)})
	if !errors.Is(err, ErrLiveQuizSessionQuestionLocked) {
		t.Fatalf("late submit err = %v, want ErrLiveQuizSessionQuestionLocked", err)
	}
	if len(s.QuestionResponses) != 0 {
		t.Fatalf("locked submit must not record a response (got %d)", len(s.QuestionResponses))
	}
	// In-window submit records.
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q, LearnerGCID: "gcid-1", Choice: "right", At: stageT0.Add(5 * time.Second)}); err != nil {
		t.Fatalf("in-window submit: %v", err)
	}
}

func TestSubmitGraded_RejectsNonCurrentQuestion(t *testing.T) {
	s := newLiveStageSession(t)
	// Nothing advanced yet.
	_, err := s.SubmitGraded(SubmitGradedInput{Question: stageQuestion("q1", 30, 10, false), LearnerGCID: "g", Choice: "right", At: stageT0})
	if !errors.Is(err, ErrLiveQuizSessionQuestionNotOpen) {
		t.Fatalf("pre-advance err = %v, want ErrLiveQuizSessionQuestionNotOpen", err)
	}
	if err := s.AdvanceTo("q2", stageT0); err != nil {
		t.Fatalf("advance: %v", err)
	}
	_, err = s.SubmitGraded(SubmitGradedInput{Question: stageQuestion("q1", 30, 10, false), LearnerGCID: "g", Choice: "right", At: stageT0.Add(time.Second)})
	if !errors.Is(err, ErrLiveQuizSessionQuestionNotOpen) {
		t.Fatalf("stale-question err = %v, want ErrLiveQuizSessionQuestionNotOpen", err)
	}
}

func TestSubmitGraded_FirstWriteWins_AndAutoRegistersParticipant(t *testing.T) {
	s := newLiveStageSession(t)
	q := stageQuestion("q1", 30, 10, false)
	if err := s.AdvanceTo("q1", stageT0); err != nil {
		t.Fatalf("advance: %v", err)
	}
	gcid := "00000000-0000-7000-8000-000000001999"
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q, LearnerGCID: gcid, Choice: "right", At: stageT0.Add(time.Second)}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	_, err := s.SubmitGraded(SubmitGradedInput{Question: q, LearnerGCID: gcid, Choice: "wrong", At: stageT0.Add(2 * time.Second)})
	if !errors.Is(err, ErrLiveQuizSessionDuplicateResponse) {
		t.Fatalf("dup err = %v, want ErrLiveQuizSessionDuplicateResponse", err)
	}
	p, ok := s.Participants[gcid]
	if !ok {
		t.Fatal("submitter not auto-registered as participant")
	}
	if p.Nickname != "Player-1999" {
		t.Fatalf("fallback nickname = %q, want Player-1999", p.Nickname)
	}
}

// --- Scoring --------------------------------------------------------------

func TestComposeScore(t *testing.T) {
	cases := []struct {
		name    string
		in      ScoreInput
		base    int
		bonus   int
		awarded int
		after   int
	}{
		{"wrong answer zeroes + resets streak",
			ScoreInput{Correct: false, Points: 10, TimerSecs: 30, StreakBefore: 4}, 0, 0, 0, 0},
		{"first correct no bonus",
			ScoreInput{Correct: true, Points: 10, TimerSecs: 30, StreakBefore: 0}, 10, 0, 10, 1},
		{"double + streak 2",
			ScoreInput{Correct: true, Points: 10, TimerSecs: 30, DoublePoints: true, StreakBefore: 2}, 10, 4, 24, 3},
		{"decay to half at expiry, streak 5",
			ScoreInput{Correct: true, Points: 10, Elapsed: 30 * time.Second, TimerSecs: 30, StreakBefore: 5}, 5, 3, 8, 6},
		{"streak capped at 5",
			ScoreInput{Correct: true, Points: 10, TimerSecs: 30, StreakBefore: 9}, 10, 5, 15, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ComposeScore(c.in)
			if got.Base != c.base || got.StreakBonus != c.bonus || got.Awarded != c.awarded || got.StreakAfter != c.after {
				t.Fatalf("ComposeScore(%+v) = %+v, want base=%d bonus=%d awarded=%d after=%d",
					c.in, got, c.base, c.bonus, c.awarded, c.after)
			}
		})
	}
}

func TestSubmitGraded_StreakProgression_SkipResets(t *testing.T) {
	s := newLiveStageSession(t)
	q1 := stageQuestion("q1", 0, 10, false)
	q2 := stageQuestion("q2", 0, 10, false)
	q3 := stageQuestion("q3", 0, 10, true) // double-points round

	if err := s.AdvanceTo("q1", stageT0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q1, LearnerGCID: "ace", Choice: "right", At: stageT0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q1, LearnerGCID: "skipper", Choice: "right", At: stageT0}); err != nil {
		t.Fatal(err)
	}

	if err := s.AdvanceTo("q2", stageT0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// ace answers q2 correct; skipper skips q2 entirely.
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q2, LearnerGCID: "ace", Choice: "right", At: stageT0.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	if err := s.AdvanceTo("q3", stageT0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	res, err := s.SubmitGraded(SubmitGradedInput{Question: q3, LearnerGCID: "ace", Choice: "right", At: stageT0.Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	// ace: streakBefore=2, double round, instant: base 10, m2 → 20 + round(20·0.1·2)=4 → 24.
	if res.Awarded != 24 || res.StreakAfter != 3 {
		t.Fatalf("ace q3 = %+v, want awarded 24 / streakAfter 3", res)
	}
	resSkip, err := s.SubmitGraded(SubmitGradedInput{Question: q3, LearnerGCID: "skipper", Choice: "right", At: stageT0.Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	// skipper skipped q2 → streak reset → no bonus: 2×10 = 20.
	if resSkip.Awarded != 20 || resSkip.StreakAfter != 1 {
		t.Fatalf("skipper q3 = %+v, want awarded 20 / streakAfter 1", resSkip)
	}
}

// --- Scoreboard + podium --------------------------------------------------

func TestScoreboard_TieBreakOrdering(t *testing.T) {
	s := newLiveStageSession(t)
	if _, err := s.Join("a", "Ada", stageT0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join("b", "Bob", stageT0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Join("c", "Cyn", stageT0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	q := stageQuestion("q1", 0, 10, false) // timer 0 → full points regardless of speed
	if err := s.AdvanceTo("q1", stageT0.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	opened := stageT0.Add(3 * time.Second)
	// a and b both score 10; a answered faster (smaller elapsed) → rank 1.
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q, LearnerGCID: "b", Choice: "right", At: opened.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q, LearnerGCID: "a", Choice: "right", At: opened.Add(1 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q, LearnerGCID: "c", Choice: "wrong", At: opened.Add(1 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	board := s.Scoreboard()
	if len(board) != 3 {
		t.Fatalf("board len = %d, want 3: %+v", len(board), board)
	}
	if board[0].Nickname != "Ada" || board[0].Rank != 1 || board[0].Score != 10 {
		t.Fatalf("rank 1 = %+v, want Ada/10", board[0])
	}
	if board[1].Nickname != "Bob" || board[1].Rank != 2 {
		t.Fatalf("rank 2 = %+v, want Bob", board[1])
	}
	if board[2].Nickname != "Cyn" || board[2].Rank != 3 || board[2].Score != 0 {
		t.Fatalf("rank 3 = %+v, want Cyn/0", board[2])
	}
}

func TestClose_FreezesFinalScoreboard_Idempotent(t *testing.T) {
	s := newLiveStageSession(t)
	q := stageQuestion("q1", 0, 10, false)
	if err := s.AdvanceTo("q1", stageT0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q, LearnerGCID: "a", Choice: "right", At: stageT0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(stageT0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(s.FinalScoreboard) != 1 || s.FinalScoreboard[0].Score != 10 {
		t.Fatalf("FinalScoreboard = %+v, want 1 entry score 10", s.FinalScoreboard)
	}
	frozen := s.FinalScoreboard
	if err := s.Close(stageT0.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(s.FinalScoreboard) != len(frozen) {
		t.Fatal("idempotent Close must not rebuild FinalScoreboard")
	}
}

// --- Join code ------------------------------------------------------------

func TestJoinCode_FormatAndNormalize(t *testing.T) {
	const alphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	code := NewJoinCode()
	if len(code) != 6 {
		t.Fatalf("len(%q) = %d, want 6", code, len(code))
	}
	for _, r := range code {
		if !strings.ContainsRune(alphabet, r) {
			t.Fatalf("code %q contains %q outside the unambiguous alphabet", code, r)
		}
	}
	if NormalizeJoinCode(" k7-m3 qx ") != "K7M3QX" {
		t.Fatalf("NormalizeJoinCode = %q, want K7M3QX", NormalizeJoinCode(" k7-m3 qx "))
	}
	// A fresh session carries a join code + empty participant map.
	s := newStageSession(t)
	if len(s.JoinCode) != 6 {
		t.Fatalf("session JoinCode = %q, want 6 chars", s.JoinCode)
	}
	if s.Participants == nil {
		t.Fatal("session Participants map must be initialised")
	}
}

// --- Reveal gating ---------------------------------------------------------

func TestRevealAllowed_PerExplainerMode(t *testing.T) {
	cases := []struct {
		name                            string
		mode                            ExplainerMode
		locked, callerSubmitted, closed bool
		want                            bool
	}{
		{"never", ExplainerModeNever, true, true, true, false},
		{"immediate after own submit", ExplainerModeImmediate, false, true, false, true},
		{"immediate after lock", ExplainerModeImmediate, true, false, false, true},
		{"immediate before submit", ExplainerModeImmediate, false, false, false, false},
		{"end-of-question locked", ExplainerModeEndOfQuestion, true, false, false, true},
		{"end-of-question open", ExplainerModeEndOfQuestion, false, true, false, false},
		{"end-of-session closed", ExplainerModeEndOfSession, false, false, true, true},
		{"end-of-session live", ExplainerModeEndOfSession, true, true, false, false},
		{"blank mode denies", ExplainerMode(""), true, true, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RevealAllowed(c.mode, c.locked, c.callerSubmitted, c.closed); got != c.want {
				t.Fatalf("RevealAllowed(%s) = %v, want %v", c.mode, got, c.want)
			}
		})
	}
}

// --- DoublePoints round-trips the builder ---------------------------------

func TestLiveQuizQuestion_DoublePointsRoundTrip(t *testing.T) {
	quiz, err := NewLiveQuiz(NewLiveQuizInput{TenantID: "t", InstructorGCID: "i", Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	q := stageQuestion("q1", 30, 10, true)
	if err := quiz.AddQuestion(q); err != nil {
		t.Fatal(err)
	}
	if !quiz.Questions[0].DoublePoints {
		t.Fatal("AddQuestion dropped DoublePoints")
	}
	if err := quiz.EditDraft("T2", []LiveQuizQuestion{q}); err != nil {
		t.Fatal(err)
	}
	if !quiz.Questions[0].DoublePoints {
		t.Fatal("EditDraft dropped DoublePoints")
	}
}
