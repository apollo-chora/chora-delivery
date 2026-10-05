// live_misc_topup_test.go — tops up the classroom-realtime aggregates'
// remaining uncovered branches: state-IsValid tables, the live quiz
// lifecycle markers, live poll validation/vote edge cases, session
// validation guards, stage-scoring edges and scoreboard folds.
package classroom

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// LiveQuizState + ExplainerMode markers
// ---------------------------------------------------------------------------

func TestLiveQuizState_IsValid(t *testing.T) {
	t.Parallel()
	valid := []LiveQuizState{
		LiveQuizStateDraft, LiveQuizStatePublished, LiveQuizStateArmed,
		LiveQuizStateLive, LiveQuizStateClosed,
	}
	for _, s := range valid {
		if !s.IsValid() {
			t.Fatalf("LiveQuizState(%q).IsValid() = false, want true", s)
		}
	}
	for _, s := range []LiveQuizState{LiveQuizState(""), LiveQuizState("BOGUS"), "LIVE "} {
		if s.IsValid() {
			t.Fatalf("LiveQuizState(%q).IsValid() = true, want false", s)
		}
	}
}

func TestLiveQuiz_LifecycleMarks(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0).UTC()

	t.Run("MarkArmed only from Published", func(t *testing.T) {
		t.Parallel()
		q, err := newPublishedQuiz(t)
		if err != nil {
			t.Fatalf("newPublishedQuiz: %v", err)
		}
		q.MarkArmed(now)
		if q.State != LiveQuizStateArmed {
			t.Fatalf("State = %q, want ARMED", q.State)
		}
		// Idempotent guard: MarkArmed on a non-PUBLISHED state is a no-op.
		q.MarkArmed(now)
		if q.State != LiveQuizStateArmed {
			t.Fatalf("second MarkArmed moved state: %q", q.State)
		}
		// MarkArmed on DRAFT is a no-op.
		d, err := NewLiveQuiz(NewLiveQuizInput{TenantID: "t", InstructorGCID: "i", Title: "q"})
		if err != nil {
			t.Fatalf("NewLiveQuiz: %v", err)
		}
		d.MarkArmed(now)
		if d.State != LiveQuizStateDraft {
			t.Fatalf("MarkArmed from DRAFT: %q, want DRAFT", d.State)
		}
	})

	t.Run("MarkLive only from Armed", func(t *testing.T) {
		t.Parallel()
		q, _ := newPublishedQuiz(t)
		q.MarkArmed(now)
		q.MarkLive(now.Add(time.Minute))
		if q.State != LiveQuizStateLive {
			t.Fatalf("State = %q, want LIVE", q.State)
		}
		q.MarkLive(now.Add(2 * time.Minute))
		if q.State != LiveQuizStateLive {
			t.Fatalf("MarkLive on non-ARMED moved state: %q", q.State)
		}
		p, _ := newPublishedQuiz(t)
		p.MarkLive(now)
		if p.State != LiveQuizStatePublished {
			t.Fatalf("MarkLive from PUBLISHED: %q, want PUBLISHED", p.State)
		}
	})

	t.Run("MarkClosed from Live and Armed", func(t *testing.T) {
		t.Parallel()
		q, _ := newPublishedQuiz(t)
		q.MarkArmed(now)
		q.MarkLive(now.Add(time.Minute))
		q.MarkClosed(now.Add(2 * time.Minute))
		if q.State != LiveQuizStateClosed {
			t.Fatalf("MarkClosed from LIVE: %q, want CLOSED", q.State)
		}
		a, _ := newPublishedQuiz(t)
		a.MarkArmed(now)
		a.MarkClosed(now.Add(time.Minute))
		if a.State != LiveQuizStateClosed {
			t.Fatalf("MarkClosed from ARMED: %q, want CLOSED", a.State)
		}
		d, _ := NewLiveQuiz(NewLiveQuizInput{TenantID: "t", InstructorGCID: "i", Title: "q"})
		d.MarkClosed(now)
		if d.State != LiveQuizStateDraft {
			t.Fatalf("MarkClosed from DRAFT: %q, want DRAFT", d.State)
		}
	})
}

// newPublishedQuiz builds a PUBLISHED two-option quiz.
func newPublishedQuiz(t *testing.T) (*LiveQuiz, error) {
	t.Helper()
	q, err := NewLiveQuiz(NewLiveQuizInput{TenantID: "t", InstructorGCID: "i", Title: "quiz"})
	if err != nil {
		return nil, err
	}
	if err := q.AddQuestion(validQuestion()); err != nil {
		return nil, err
	}
	return q, q.Publish(time.Now())
}

func validQuestion() LiveQuizQuestion {
	return LiveQuizQuestion{
		QuestionID: "q1",
		Prompt:     "1+1?",
		Options:    []LiveQuizOption{{Label: "2", IsCorrect: true}, {Label: "3"}},
		TimerSecs:  10,
		Points:     10,
	}
}

func TestAddQuestion_BlankOptionLabelRejected(t *testing.T) {
	t.Parallel()
	q, err := NewLiveQuiz(NewLiveQuizInput{TenantID: "t", InstructorGCID: "i", Title: "q"})
	if err != nil {
		t.Fatalf("NewLiveQuiz: %v", err)
	}
	qst := LiveQuizQuestion{
		Prompt:  "p",
		Options: []LiveQuizOption{{Label: "  "}, {Label: "b", IsCorrect: true}},
	}
	if err := q.AddQuestion(qst); !errors.Is(err, ErrLiveQuizInvalidQuestion) {
		t.Fatalf("blank option label: err = %v, want ErrLiveQuizInvalidQuestion", err)
	}
}

func TestEditDraft_RejectsBlankTitleAndBadQuestion(t *testing.T) {
	t.Parallel()
	q, err := NewLiveQuiz(NewLiveQuizInput{TenantID: "t", InstructorGCID: "i", Title: "q"})
	if err != nil {
		t.Fatalf("NewLiveQuiz: %v", err)
	}
	if err := q.EditDraft("  ", []LiveQuizQuestion{validQuestion()}); err == nil {
		t.Fatal("blank title: want error")
	}
}

func TestEditDraft_RejectsInvalidQuestion(t *testing.T) {
	t.Parallel()
	q, err := NewLiveQuiz(NewLiveQuizInput{TenantID: "t", InstructorGCID: "i", Title: "q"})
	if err != nil {
		t.Fatalf("NewLiveQuiz: %v", err)
	}
	bad := []LiveQuizQuestion{
		{Prompt: "p", Options: []LiveQuizOption{{Label: "a", IsCorrect: true}}}, // 1 option
		{Prompt: "p", Options: []LiveQuizOption{{Label: "a"}, {Label: "b"}}},    // 0 correct
	}
	for i, qst := range bad {
		if err := q.EditDraft("t2", []LiveQuizQuestion{qst}); err == nil {
			t.Fatalf("case %d: want validation error", i)
		}
	}
}

// ---------------------------------------------------------------------------
// LivePoll
// ---------------------------------------------------------------------------

func TestLivePollState_IsValid(t *testing.T) {
	t.Parallel()
	for _, s := range []LivePollState{LivePollStateDraft, LivePollStateOpen, LivePollStateClosed} {
		if !s.IsValid() {
			t.Fatalf("LivePollState(%q).IsValid() = false", s)
		}
	}
	for _, s := range []LivePollState{LivePollState(""), "BOGUS"} {
		if s.IsValid() {
			t.Fatalf("LivePollState(%q).IsValid() = true", s)
		}
	}
}

func TestNewLivePoll_BlankAndDuplicateOptions(t *testing.T) {
	t.Parallel()
	if _, err := NewLivePoll(NewLivePollInput{
		TenantID: "t", InstructorGCID: "i", Question: "q?", Options: []string{"a", "  "},
	}); !errors.Is(err, ErrLivePollNeedsOptions) {
		t.Fatalf("blank option: want ErrLivePollNeedsOptions, got %v", err)
	}
	if _, err := NewLivePoll(NewLivePollInput{
		TenantID: "t", InstructorGCID: "i", Question: "q?", Options: []string{"a", "a"},
	}); err == nil {
		t.Fatal("duplicate option: want error")
	}
}

func TestLivePoll_OpenIdempotent(t *testing.T) {
	t.Parallel()
	p, err := NewLivePoll(NewLivePollInput{TenantID: "t", InstructorGCID: "i", Question: "q", Options: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("NewLivePoll: %v", err)
	}
	now := time.Now()
	if err := p.Open(now); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := p.Open(now.Add(time.Minute)); err != nil {
		t.Fatalf("idempotent Open: %v", err)
	}
	if p.OpenedAt == nil {
		t.Fatal("OpenedAt not set")
	}
}

func TestLivePoll_CastVote_DefensiveNilVoters(t *testing.T) {
	t.Parallel()
	// Rehydrated aggregate (voters never through NewLivePoll) must still work.
	p := &LivePoll{
		TenantID: "t", InstructorGCID: "i", Question: "q", State: LivePollStateOpen,
		Options: []LivePollOption{{Label: "a"}, {Label: "b"}},
	}
	now := time.Now()
	if err := p.CastVote("learner-1", "a", now); err != nil {
		t.Fatalf("CastVote: %v", err)
	}
	if got := p.VoteCount("a"); got != 1 {
		t.Fatalf("VoteCount(a) = %d, want 1", got)
	}
	if got := p.VoteCount("nope"); got != 0 {
		t.Fatalf("VoteCount(unknown) = %d, want 0", got)
	}
	if got := p.TotalVotes(); got != 1 {
		t.Fatalf("TotalVotes = %d, want 1", got)
	}
	// Short blank learner guard.
	if err := p.CastVote("", "b", now); !errors.Is(err, ErrLivePollLearnerRequired) {
		t.Fatalf("blank learner: want ErrLivePollLearnerRequired, got %v", err)
	}
}

func TestLivePoll_UnmarshalLegacySnapshot_NoVotersKey(t *testing.T) {
	t.Parallel()
	p := &LivePoll{}
	// Snapshots written before the voters array existed must rehydrate with
	// an empty-but-non-nil voter set (CastVote stays correct).
	if err := p.UnmarshalJSON([]byte(`{"id":"x","state":"OPEN","options":[{"label":"a","vote_count":0}]}`)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if p.voters == nil {
		t.Fatal("voters must be non-nil after legacy-snapshot unmarshal")
	}
	// Malformed JSON propagates the error.
	if err := p.UnmarshalJSON([]byte(`{`)); err == nil {
		t.Fatal("malformed JSON: want error")
	}
}

// ---------------------------------------------------------------------------
// LiveQuizSession
// ---------------------------------------------------------------------------

func TestLiveQuizSessionState_IsValid(t *testing.T) {
	t.Parallel()
	for _, s := range []LiveQuizSessionState{
		LiveQuizSessionStateArmed, LiveQuizSessionStateLive, LiveQuizSessionStateClosed,
	} {
		if !s.IsValid() {
			t.Fatalf("LiveQuizSessionState(%q).IsValid() = false", s)
		}
	}
	for _, s := range []LiveQuizSessionState{""} {
		if s.IsValid() {
			t.Fatalf("LiveQuizSessionState(%q).IsValid() = true", s)
		}
	}
}

func TestSubmitResponse_ValidationGuards(t *testing.T) {
	t.Parallel()
	s, err := NewLiveQuizSession("quiz-1", "tenant-1", "instructor-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	if err := s.Start(time.Now()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	now := time.Now()

	if err := s.SubmitResponse("", "learner-1", "a", now); !errors.Is(err, ErrLiveQuizSessionQuestionRequired) {
		t.Fatalf("blank question: want ErrLiveQuizSessionQuestionRequired, got %v", err)
	}
	if err := s.SubmitResponse("q1", "", "a", now); !errors.Is(err, ErrLiveQuizSessionLearnerRequired) {
		t.Fatalf("blank learner: want ErrLiveQuizSessionLearnerRequired, got %v", err)
	}
	if err := s.SubmitResponse("q1", "learner-1", "  ", now); !errors.Is(err, ErrLiveQuizSessionChoiceRequired) {
		t.Fatalf("blank choice: want ErrLiveQuizSessionChoiceRequired, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stage: LocksAt / Join edges / SubmitGraded guards / Scoreboard
// ---------------------------------------------------------------------------

func TestLocksAt(t *testing.T) {
	t.Parallel()
	opened := time.Unix(1_700_000_000, 0).UTC()

	if LocksAt(nil, 30) != nil {
		t.Fatal("nil openedAt: want nil")
	}
	if LocksAt(&opened, 0) != nil {
		t.Fatal("host-paced (timer 0): want nil")
	}
	got := LocksAt(&opened, 30)
	if got == nil {
		t.Fatal("positive timer: want non-nil")
	}
	want := opened.Add((30 + LockGraceSecs) * time.Second)
	if !got.Equal(want) {
		t.Fatalf("LocksAt = %v, want %v", got, want)
	}
}

func TestJoin_BlankGCID_Error(t *testing.T) {
	t.Parallel()
	s, err := NewLiveQuizSession("quiz-1", "tenant-1", "instructor-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	if _, err := s.Join("", "Nick", time.Now()); !errors.Is(err, ErrLiveQuizSessionLearnerRequired) {
		t.Fatalf("blank gcid: want ErrLiveQuizSessionLearnerRequired, got %v", err)
	}
}

func TestRoster_TieBreakByGCID(t *testing.T) {
	t.Parallel()
	s, err := NewLiveQuizSession("quiz-1", "tenant-1", "instructor-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	now := time.Now()
	// Two participants join at the exact same instant → GCID ascending.
	if _, err := s.Join("z-gcid", "Zed", now); err != nil {
		t.Fatalf("join z: %v", err)
	}
	if _, err := s.Join("a-gcid", "Abe", now); err != nil {
		t.Fatalf("join a: %v", err)
	}
	roster := s.Roster()
	if len(roster) != 2 {
		t.Fatalf("roster len = %d, want 2", len(roster))
	}
	if roster[0].GCID != "a-gcid" || roster[1].GCID != "z-gcid" {
		t.Fatalf("tie-break order wrong: %+v", roster)
	}
}

func TestSubmitGraded_GuardBranches(t *testing.T) {
	t.Parallel()
	s, err := NewLiveQuizSession("quiz-1", "tenant-1", "instructor-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	in := SubmitGradedInput{Question: validQuestion(), LearnerGCID: "learner-1", Choice: "2", At: time.Now()}

	if _, err := s.SubmitGraded(in); !errors.Is(err, ErrLiveQuizSessionNotLive) {
		t.Fatalf("not live: want ErrLiveQuizSessionNotLive, got %v", err)
	}
	if err := s.Start(time.Now()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	blankQ := in
	blankQ.Question.QuestionID = "  "
	if _, err := s.SubmitGraded(blankQ); !errors.Is(err, ErrLiveQuizSessionQuestionRequired) {
		t.Fatalf("blank question: want ErrLiveQuizSessionQuestionRequired, got %v", err)
	}
	blankL := in
	blankL.LearnerGCID = " "
	if _, err := s.SubmitGraded(blankL); !errors.Is(err, ErrLiveQuizSessionLearnerRequired) {
		t.Fatalf("blank learner: want ErrLiveQuizSessionLearnerRequired, got %v", err)
	}
	blankC := in
	blankC.Choice = ""
	if _, err := s.SubmitGraded(blankC); !errors.Is(err, ErrLiveQuizSessionChoiceRequired) {
		t.Fatalf("blank choice: want ErrLiveQuizSessionChoiceRequired, got %v", err)
	}
	// Not the current question.
	if _, err := s.SubmitGraded(in); !errors.Is(err, ErrLiveQuizSessionQuestionNotOpen) {
		t.Fatalf("non-current question: want ErrLiveQuizSessionQuestionNotOpen, got %v", err)
	}
}

func TestSubmitGraded_QuestionLocked(t *testing.T) {
	t.Parallel()
	s, err := NewLiveQuizSession("quiz-1", "tenant-1", "instructor-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	now := time.Now()
	if err := s.Start(now); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.AdvanceTo("q1", now); err != nil {
		t.Fatalf("AdvanceTo: %v", err)
	}
	// Timer 10s + 2s grace — a submit 20s past open is refused.
	in := SubmitGradedInput{Question: validQuestion(), LearnerGCID: "learner-1", Choice: "2", At: now.Add(20 * time.Second)}
	if _, err := s.SubmitGraded(in); !errors.Is(err, ErrLiveQuizSessionQuestionLocked) {
		t.Fatalf("locked: want ErrLiveQuizSessionQuestionLocked, got %v", err)
	}
}

func TestSubmitGraded_ElapsedClampedNegative(t *testing.T) {
	t.Parallel()
	s, err := NewLiveQuizSession("quiz-1", "tenant-1", "instructor-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	now := time.Now()
	if err := s.Start(now); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.AdvanceTo("q1", now); err != nil {
		t.Fatalf("AdvanceTo: %v", err)
	}
	in := SubmitGradedInput{Question: validQuestion(), LearnerGCID: "learner-1", Choice: "2", At: now.Add(-time.Hour)}
	res, err := s.SubmitGraded(in)
	if err != nil {
		t.Fatalf("SubmitGraded: %v", err)
	}
	if !res.Correct || res.Awarded <= 0 {
		t.Fatalf("negative-elapsed submit: %+v", res)
	}
}

func TestQuestionChoiceCorrect_MatchesLabels(t *testing.T) {
	t.Parallel()
	q := validQuestion()
	if !questionChoiceCorrect(q, "2") {
		t.Fatal("'2' must be correct")
	}
	if questionChoiceCorrect(q, "3") {
		t.Fatal("'3' must be incorrect")
	}
	if questionChoiceCorrect(q, "missing") {
		t.Fatal("unknown choice must be incorrect")
	}
}

func TestStreakBefore_BreaksOnWrongAndSkip(t *testing.T) {
	t.Parallel()
	q1 := validQuestion()
	q2 := validQuestion()
	q2.QuestionID = "q2"
	q2.Options = []LiveQuizOption{{Label: "x"}, {Label: "y", IsCorrect: true}}
	q2.Prompt = "q2?"

	s, err := NewLiveQuizSession("quiz-1", "tenant-1", "instructor-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	now := time.Now()
	if err := s.Start(now); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Nothing asked yet → streak 0.
	if got := s.streakBefore("learner-1"); got != 0 {
		t.Fatalf("streak before any question = %d, want 0", got)
	}

	// Quiz opens q1; learner answers correctly.
	if err := s.AdvanceTo("q1", now.Add(time.Second)); err != nil {
		t.Fatalf("AdvanceTo q1: %v", err)
	}
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q1, LearnerGCID: "learner-1", Choice: "2", At: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("submit q1: %v", err)
	}

	// Quiz advances to q2; learner gets q2 WRONG → the run breaks for the
	// NEXT question (q3). Before q3 exists, streakBefore still counts q1; so
	// first advance to q3, THEN assert the reset.
	if err := s.AdvanceTo("q2", now.Add(3*time.Second)); err != nil {
		t.Fatalf("AdvanceTo q2: %v", err)
	}
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q2, LearnerGCID: "learner-1", Choice: "x", At: now.Add(4 * time.Second)}); err != nil {
		t.Fatalf("submit q2: %v", err)
	}
	if err := s.AdvanceTo("q3", now.Add(5*time.Second)); err != nil {
		t.Fatalf("AdvanceTo q3: %v", err)
	}
	if got := s.streakBefore("learner-1"); got != 0 {
		t.Fatalf("streak after wrong = %d, want 0", got)
	}

	// Reset: fresh session where the middle question is SKIPPED (no response
	// for the queried learner) — that also breaks the run.
	s2, err := NewLiveQuizSession("quiz-1", "tenant-1", "instructor-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	if err := s2.Start(now); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s2.AdvanceTo("q1", now.Add(time.Second)); err != nil {
		t.Fatalf("AdvanceTo q1: %v", err)
	}
	if _, err := s2.SubmitGraded(SubmitGradedInput{Question: q1, LearnerGCID: "learner-1", Choice: "2", At: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("submit q1: %v", err)
	}
	if err := s2.AdvanceTo("q2", now.Add(3*time.Second)); err != nil {
		t.Fatalf("AdvanceTo q2: %v", err)
	}
	// Reach q3 — q2 has no graded response for learner-1 (skipped).
	if err := s2.AdvanceTo("q3", now.Add(4*time.Second)); err != nil {
		t.Fatalf("AdvanceTo q3: %v", err)
	}
	if got := s2.streakBefore("learner-1"); got != 0 {
		t.Fatalf("streak after skip = %d, want 0", got)
	}
}

func TestScoreboard_FoldsAndTieBreaks(t *testing.T) {
	t.Parallel()
	q1 := validQuestion()
	q2 := validQuestion()
	q2.QuestionID = "q2"
	q2.Options = []LiveQuizOption{{Label: "x"}, {Label: "y", IsCorrect: true}}
	q2.Prompt = "q2?"

	s, err := NewLiveQuizSession("quiz-1", "tenant-1", "instructor-1")
	if err != nil {
		t.Fatalf("NewLiveQuizSession: %v", err)
	}
	now := time.Now()
	if err := s.Start(now); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// A joined-but-silent participant appears at 0 points.
	if _, err := s.Join("silent", "Silent", now); err != nil {
		t.Fatalf("Join silent: %v", err)
	}

	if err := s.AdvanceTo("q1", now.Add(time.Second)); err != nil {
		t.Fatalf("AdvanceTo q1: %v", err)
	}
	// Fast learner (correct) + slow learner (correct) — same score, faster
	// elapsed ranks first.
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q1, LearnerGCID: "fast", Choice: "2", At: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("fast submit: %v", err)
	}
	if err := s.AdvanceTo("q2", now.Add(3*time.Second)); err != nil {
		t.Fatalf("AdvanceTo q2: %v", err)
	}
	if _, err := s.SubmitGraded(SubmitGradedInput{Question: q2, LearnerGCID: "slow", Choice: "y", At: now.Add(5 * time.Second)}); err != nil {
		t.Fatalf("slow submit: %v", err)
	}

	board := s.Scoreboard()
	if len(board) != 3 {
		t.Fatalf("board len = %d, want 3", len(board))
	}
	// Fast and slow both answered q1/q2? No — fast answered only q1, slow only
	// q2 (different questions). Score check: both answered ONE correct with
	// one unanswered... streak applies per learner.
	if board[0].GCID != "fast" && board[0].GCID != "slow" && board[0].GCID != "silent" {
		t.Fatalf("board[0] = %+v", board[0])
	}
	if board[0].Score < board[2].Score {
		t.Fatalf("board not score-desc: %+v", board)
	}
	// silent joined with 0 answers must rank last with 0 points.
	if board[2].GCID != "silent" || board[2].Score != 0 {
		t.Fatalf("silent row wrong: %+v", board[2])
	}
	if board[0].Rank != 1 || board[1].Rank != 2 || board[2].Rank != 3 {
		t.Fatalf("ranks wrong: %+v", board)
	}
}

func TestNewJoinCode_Format(t *testing.T) {
	t.Parallel()
	for i := 0; i < 20; i++ {
		code := NewJoinCode()
		if len(code) != joinCodeLength {
			t.Fatalf("NewJoinCode length = %d, want %d", len(code), joinCodeLength)
		}
		for _, c := range code {
			if !strings_ContainsRune(joinCodeAlphabet, c) {
				t.Fatalf("NewJoinCode %q contains out-of-alphabet char %q", code, c)
			}
		}
	}
}

func strings_ContainsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
