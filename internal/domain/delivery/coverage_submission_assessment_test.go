// coverage_submission_assessment_test.go — statement-coverage top-up for the
// Submission + Assessment aggregate branches not exercised by the existing
// state-machine tests: default/error paths, zero-guards, provenance helpers and
// the lens functions.
package delivery

import (
	"errors"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// Submission — small helpers + state helpers
// -----------------------------------------------------------------------------

func TestSubmissionState_WireState_ArchivedAndUnknown(t *testing.T) {
	if got := SubmissionStateArchived.WireState(); got != "ARCHIVED" {
		t.Fatalf("ARCHIVED wire: want ARCHIVED, got %q", got)
	}
	if got := SubmissionState("BOGUS").WireState(); got != "BOGUS" {
		t.Fatalf("unknown wire: want passthrough, got %q", got)
	}
}

func TestProvenance_IsHuman(t *testing.T) {
	if !ProvenanceHuman.IsHuman() {
		t.Fatal("HUMAN provenance must be human")
	}
	if ProvenanceAI.IsHuman() {
		t.Fatal("AI provenance must not be human")
	}
	if Provenance("").IsHuman() {
		t.Fatal("empty provenance must not be human")
	}
}

func TestSubmission_HasOEAnswers(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	if s.HasOEAnswers() {
		t.Fatal("no answers ⇒ no OE answers")
	}
	s.Answers = []SubmissionAnswer{
		{TestSetQuestionID: "q1", QuestionType: QuestionTypeMCQ},
		{TestSetQuestionID: "q2", QuestionType: QuestionTypeOE, OEResponseText: "   "}, // blank text ignored
	}
	if s.HasOEAnswers() {
		t.Fatal("blank OE text must not count")
	}
	s.Answers[1].OEResponseText = "essay"
	if !s.HasOEAnswers() {
		t.Fatal("answered OE must count")
	}
}

func TestSubmission_TimeRemainingSeconds_ZeroGuards(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	var zero time.Time
	if got := s.TimeRemainingSeconds(zero); got != 0 {
		t.Fatalf("zero now: want 0, got %d", got)
	}
	s.ClosesAt = time.Time{}
	if got := s.TimeRemainingSeconds(time.Now()); got != 0 {
		t.Fatalf("zero closes_at: want 0, got %d", got)
	}
	s.ClosesAt = time.Now().Add(-1 * time.Hour)
	if got := s.TimeRemainingSeconds(time.Now()); got != 0 {
		t.Fatalf("expired: want 0, got %d", got)
	}
	// Unknown state ⇒ 0 (a RELEASED submission carries no window).
	released, _ := NewSubmission(validSubmissionInput())
	released.State = SubmissionStateReleased
	if got := released.TimeRemainingSeconds(time.Now()); got != 0 {
		t.Fatalf("released: want 0, got %d", got)
	}
}

func TestSubmission_CanAutosave_Guards(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.State = SubmissionStateSubmitted
	if err := s.CanAutosave(time.Now()); err != ErrSubmissionRequiresInProgress {
		t.Fatalf("not-in-progress: want ErrSubmissionRequiresInProgress, got %v", err)
	}
	s.State = SubmissionStateInProgress
	s.ClosesAt = time.Now().Add(-1 * time.Minute)
	if err := s.CanAutosave(time.Now()); err != ErrSubmissionWindowClosed {
		t.Fatalf("past close: want ErrSubmissionWindowClosed, got %v", err)
	}
	s.ClosesAt = time.Now().Add(1 * time.Hour)
	if err := s.CanAutosave(time.Now()); err != nil {
		t.Fatalf("ok window: want nil, got %v", err)
	}
}

func TestSubmission_MergeAnswers_EdgePaths(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	now := time.Now()
	// Empty incoming → nil, no LastSavedAt bump.
	if err := s.MergeAnswers(now, nil); err != nil {
		t.Fatalf("empty merge: %v", err)
	}
	if s.LastSavedAt != nil {
		t.Fatal("empty merge must not bump LastSavedAt")
	}
	if s.State != SubmissionStateStarted {
		t.Fatalf("empty merge must not flip state: %s", s.State)
	}
	// Blank TestSetQuestionID entries are skipped.
	if err := s.MergeAnswers(now, []SubmissionAnswer{{TestSetQuestionID: "  "}}); err != nil {
		t.Fatalf("blank-tsq merge: %v", err)
	}
	if len(s.Answers) != 0 {
		t.Fatalf("blank tsq must be skipped, got %d answers", len(s.Answers))
	}
	// Overwriting preserves existing grading fields.
	s.Answers = []SubmissionAnswer{{
		TestSetQuestionID: "q1", QuestionType: QuestionTypeMCQ, MCQChoiceID: "A",
		MCQCorrect: boolPtr(true), PointsEarned: 10, PointsPossible: 10,
		GradingDispatch: GradingDispatchDeterministic,
	}}
	if err := s.MergeAnswers(now, []SubmissionAnswer{{TestSetQuestionID: "q1", QuestionType: QuestionTypeMCQ, MCQChoiceID: "B"}}); err != nil {
		t.Fatalf("overwrite merge: %v", err)
	}
	q1 := s.Answers[0]
	if q1.MCQChoiceID != "B" || q1.MCQCorrect == nil || !*q1.MCQCorrect || q1.PointsEarned != 10 || q1.PointsPossible != 10 {
		t.Fatalf("grading fields not preserved: %+v", q1)
	}
	// First real save flips STARTED → IN_PROGRESS.
	if s.State != SubmissionStateInProgress {
		t.Fatalf("state: want IN_PROGRESS after first merge, got %s", s.State)
	}
}

func TestSubmission_MarkSubmitted_DefaultRejected(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.State = SubmissionStateArchived
	if err := s.MarkSubmitted(time.Now()); err != ErrSubmissionRequiresInProgress {
		t.Fatalf("archived submit: want ErrSubmissionRequiresInProgress, got %v", err)
	}
	pending, _ := NewSubmission(validSubmissionInput())
	pending.State = SubmissionStatePendingOEGrading
	if err := pending.MarkSubmitted(time.Now()); err != ErrSubmissionAlreadySubmitted {
		t.Fatalf("pending-OE submit: want ErrSubmissionAlreadySubmitted, got %v", err)
	}
}

func TestSubmission_MoveToOEPending_OnlyFromSubmitted(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.State = SubmissionStateStarted
	s.MoveToOEPending(time.Now())
	if s.State != SubmissionStateStarted {
		t.Fatalf("STARTED must not move to OE pending: %s", s.State)
	}
}

func TestSubmission_MarkGradedPendingRelease_NoOpOtherStates(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	before := s.State
	s.MarkGradedPendingRelease(time.Now())
	if s.State != before || s.GradedAt != nil {
		t.Fatalf("STARTED must not grade-flip: %s", s.State)
	}
	// MaxScore=0 ⇒ Passed stays nil (no passing math possible).
	s.State = SubmissionStateSubmitted
	s.MaxScore = 0
	s.MarkGradedPendingRelease(time.Now())
	if s.Passed != nil {
		t.Fatalf("MaxScore=0 must leave Passed nil, got %v", *s.Passed)
	}
}

// -----------------------------------------------------------------------------
// ApplyOEGradingBatch / ApplyGrading — defensive + partial paths
// -----------------------------------------------------------------------------

func TestApplyOEGradingBatch_NilReceiverAndBlankID(t *testing.T) {
	var s *Submission
	if _, err := s.ApplyOEGradingBatch(time.Now(), OEGradingBatch{}); err == nil {
		t.Fatal("nil receiver must error")
	}
	sub := oePendingSubmission(SubmissionAnswer{
		TestSetQuestionID: "oe-1", QuestionType: QuestionTypeOE, OEResponseText: "real answer",
	})
	// Blank SubmissionID in the batch is tolerated (subscriber passes it);
	// an unknown/foreign target grades nothing → not all-graded.
	all, err := sub.ApplyOEGradingBatch(time.Now(), OEGradingBatch{
		BatchOutcome: "ok",
		Results: []OEGradingResult{{
			TestSetQuestionID: "no-such", PointsEarned: 5, PointsPossible: 10,
		}},
	})
	if err != nil {
		t.Fatalf("blank-id apply: %v", err)
	}
	if all {
		t.Fatal("unknown target leaving a real OE answer ungraded must not be 'all graded'")
	}
}

func TestApplyOEGradingBatch_FillsPointsPossibleWhenZero(t *testing.T) {
	s := oePendingSubmission(SubmissionAnswer{
		TestSetQuestionID: "oe-1", QuestionType: QuestionTypeOE, OEResponseText: "x",
	})
	all, err := s.ApplyOEGradingBatch(time.Now(), OEGradingBatch{
		SubmissionID: s.ID, BatchOutcome: "ok",
		Results: []OEGradingResult{{TestSetQuestionID: "oe-1", PointsEarned: 3, PointsPossible: 10}},
	})
	if err != nil || !all {
		t.Fatalf("apply: all=%v err=%v", all, err)
	}
	if s.Answers[s.findOEAnswer("oe-1")].PointsPossible != 10 {
		t.Fatalf("points_possible: want 10, got %d", s.Answers[s.findOEAnswer("oe-1")].PointsPossible)
	}
}

func TestApplyGrading_NilReceiverBlankIDAndPartial(t *testing.T) {
	var s *Submission
	if _, err := s.ApplyGrading(time.Now(), SubmissionGrading{}); err == nil {
		t.Fatal("nil receiver must error")
	}
	// Two OE answers; only one graded → PARTIAL: stays PENDING_OE_GRADING.
	sub := oePendingSubmission(
		SubmissionAnswer{TestSetQuestionID: "oe-1", QuestionType: QuestionTypeOE, OEResponseText: "one"},
		SubmissionAnswer{TestSetQuestionID: "oe-2", QuestionType: QuestionTypeOE, OEResponseText: "two"},
	)
	all, err := sub.ApplyGrading(time.Now(), SubmissionGrading{
		Outcome: "SUCCESS",
		Graded: []OEQuestionGrade{{
			TestSetQuestionID: "oe-1", PointsEarned: 8, PointsPossible: 10, Comment: "ok",
		}},
	})
	if err != nil {
		t.Fatalf("ApplyGrading: %v", err)
	}
	if all {
		t.Fatal("partial batch must not be all-graded")
	}
	if sub.State != SubmissionStatePendingOEGrading {
		t.Fatalf("partial: state must stay PENDING_OE_GRADING, got %s", sub.State)
	}
	oe1 := sub.Answers[sub.findOEAnswer("oe-1")]
	if oe1.AIPointsEarned == nil || *oe1.AIPointsEarned != 8 || oe1.PointsPossible != 10 {
		t.Fatalf("OE scoring not applied: %+v", oe1)
	}

	// A second grading for oe-2 completes the set (blank SubmissionID OK).
	all, err = sub.ApplyGrading(time.Now(), SubmissionGrading{
		Outcome:        "SUCCESS",
		OverallComment: "",
		Graded: []OEQuestionGrade{{
			TestSetQuestionID: "oe-2", PointsEarned: 7, PointsPossible: 10, Comment: "c",
		}},
	})
	if err != nil || !all {
		t.Fatalf("completion apply: all=%v err=%v", all, err)
	}
	if sub.State != SubmissionStateGradedPendingRelease || sub.ReviewStatus != ReviewStatusPendingReview {
		t.Fatalf("completed: %s/%q", sub.State, sub.ReviewStatus)
	}
	// Empty overall comment left the AI draft unset.
	if sub.OverallComment != "" {
		t.Fatalf("empty comment must not be stored, got %q", sub.OverallComment)
	}
}

func TestFindOEAnswer_Missing(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.Answers = []SubmissionAnswer{{TestSetQuestionID: "m", QuestionType: QuestionTypeMCQ}}
	if i := s.findOEAnswer("m"); i != -1 {
		t.Fatalf("MCQ answer must not be found as OE: %d", i)
	}
}

// -----------------------------------------------------------------------------
// HITL edits — remaining error/edge paths
// -----------------------------------------------------------------------------

func TestOverrideQuestionGrade_ErrorPaths(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)

	if _, err := s.OverrideQuestionGrade(time.Now(), "i", "no-such-oe", nil, nil, ""); err != ErrOEAnswerNotFound {
		t.Fatalf("unknown tsq: want ErrOEAnswerNotFound, got %v", err)
	}
	// nil score AND nil comment → no-op, no audit rows.
	out, err := s.OverrideQuestionGrade(time.Now(), "i", "oe-1", nil, nil, "")
	if err != nil || len(out) != 0 {
		t.Fatalf("no-op override: out=%v err=%v", out, err)
	}
	neg := -1.0
	if _, err := s.OverrideQuestionGrade(time.Now(), "i", "oe-1", &neg, nil, ""); err != ErrScoreOutOfRange {
		t.Fatalf("negative score: want ErrScoreOutOfRange, got %v", err)
	}
	tooHigh := 999.0
	if _, err := s.OverrideQuestionGrade(time.Now(), "i", "oe-1", &tooHigh, nil, ""); err != ErrScoreOutOfRange {
		t.Fatalf("over-possible score: want ErrScoreOutOfRange, got %v", err)
	}
}

func TestAmendModelAnswer_UnknownOE(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)
	if _, err := s.AmendModelAnswer(time.Now(), "i", "nope", "replacement"); err != ErrOEAnswerNotFound {
		t.Fatalf("unknown tsq: want ErrOEAnswerNotFound, got %v", err)
	}
}

func TestEditOverallComment_NotGraded(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.State = SubmissionStatePendingOEGrading
	if _, err := s.EditOverallComment(time.Now(), "i", "x"); err != ErrSubmissionNotGraded {
		t.Fatalf("want ErrSubmissionNotGraded, got %v", err)
	}
}

func TestApproveAsIs_ReleasedIsNoOp(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.State = SubmissionStateReleased
	if err := s.ApproveAsIs(time.Now(), "i"); err != nil {
		t.Fatalf("released approve must be a no-op: %v", err)
	}
	if s.ReviewStatus != ReviewStatusNotRequired {
		t.Fatalf("released approve must not touch review_status: %q", s.ReviewStatus)
	}
}

func TestSubmission_CanRelease_Matrix(t *testing.T) {
	cases := []struct {
		state SubmissionState
		rev   ReviewStatus
		want  bool
	}{
		{SubmissionStateReleased, ReviewStatusNotRequired, true},
		{SubmissionStateGradedPendingRelease, ReviewStatusApproved, true},
		{SubmissionStateGradedPendingRelease, ReviewStatusNotRequired, true},
		{SubmissionStateGradedPendingRelease, ReviewStatusPendingReview, false},
		{SubmissionStateSubmitted, ReviewStatusNotRequired, false},
	}
	for _, c := range cases {
		s := &Submission{State: c.state, ReviewStatus: c.rev}
		if got := s.CanRelease(); got != c.want {
			t.Errorf("CanRelease(%s,%q): want %v, got %v", c.state, c.rev, c.want, got)
		}
	}
}

func TestSubmission_ScorePercent(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.MaxScore = 0
	if got := s.ScorePercent(); got != 0 {
		t.Fatalf("MaxScore=0: want 0, got %f", got)
	}
	s.MaxScore = 200
	s.TotalScore = 150
	if got := s.ScorePercent(); got != 75 {
		t.Fatalf("percent: want 75, got %f", got)
	}
}

func TestSubmission_MarkReleased_AlreadyReleasedNoOp(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.State = SubmissionStateReleased
	stamp := time.Unix(0, 1)
	s.MarkReleased(stamp)
	if s.ReleasedAt != nil && s.ReleasedAt.UnixNano() == stamp.UnixNano() {
		t.Fatal("re-release must not restamp ReleasedAt")
	}
}

// -----------------------------------------------------------------------------
// MCQSnapshot + grader — remaining guard branches
// -----------------------------------------------------------------------------

func TestMCQSnapshot_FromJSON_ErrorsAndDefaults(t *testing.T) {
	if _, err := MCQSnapshotFromJSON("{not json"); err == nil {
		t.Fatal("invalid JSON must error")
	}
	if _, err := MCQSnapshotFromJSON(`{"scoring":{"mode":"x"}}`); err == nil {
		t.Fatal("missing options must error")
	}
	snap, err := MCQSnapshotFromJSON(`{"options":[{"option_id":"a","is_correct":true}]}`)
	if err != nil {
		t.Fatalf("no-scoring parse: %v", err)
	}
	if snap.ScoringMode != "single_correct" {
		t.Fatalf("default mode: want single_correct, got %q", snap.ScoringMode)
	}
	img := "https://img"
	snap, err = MCQSnapshotFromJSON(`{"options":[{"option_id":"a","is_correct":true}],"scoring":{"mode":"multi_correct"},"answer_image_url":"https://img","image_url":"https://img"}`)
	if err != nil {
		t.Fatalf("image parse: %v", err)
	}
	if snap.AnswerImageURL == nil || *snap.AnswerImageURL != img || snap.ImageURL == nil || *snap.ImageURL != img {
		t.Fatalf("image URLs not captured: %+v", snap)
	}
}

func TestMCQSnapshot_CorrectOptionIDs_SkipNonStrings(t *testing.T) {
	snap := MCQSnapshot{Options: []map[string]any{
		{"option_id": "A", "is_correct": true},
		{"option_id": 42, "is_correct": true},   // non-string id — skipped
		{"option_id": "", "is_correct": true},   // blank — skipped
		{"option_id": "B", "is_correct": false}, // not correct — skipped
	}}
	got := snap.CorrectOptionIDs()
	if len(got) != 1 || got[0] != "A" {
		t.Fatalf("correct ids: want [A], got %v", got)
	}
}

func TestGradeMCQAnswer_GuardBranches(t *testing.T) {
	if pts, ok := GradeMCQAnswer("A", nil, nil, 10, "single_correct"); pts != 0 || ok {
		t.Fatalf("empty correct set: %f/%v", pts, ok)
	}
	if pts, ok := GradeMCQAnswer("A", nil, []string{"A"}, 0, "single_correct"); pts != 0 || ok {
		t.Fatalf("zero points: %f/%v", pts, ok)
	}
	// multi_correct with an extra selection → 0.
	if pts, ok := GradeMCQAnswer("", []string{"A", "B", "C"}, []string{"A", "B"}, 10, "multi_correct"); pts != 0 || ok {
		t.Fatalf("multi extra: %f/%v", pts, ok)
	}
	// multi_correct exact → full.
	if pts, ok := GradeMCQAnswer("", []string{"B", "A"}, []string{"A", "B"}, 10, "multi_correct"); pts != 10 || !ok {
		t.Fatalf("multi exact: %f/%v", pts, ok)
	}
	// single_correct with a wrong pick → 0.
	if pts, ok := GradeMCQAnswer("Z", nil, []string{"A"}, 10, "single_correct"); pts != 0 || ok {
		t.Fatalf("single wrong: %f/%v", pts, ok)
	}
	// Unknown scoring mode falls back to single_correct semantics.
	if pts, ok := GradeMCQAnswer("", []string{"A", "B"}, []string{"A", "B"}, 10, "patched"); pts != 0 || ok {
		t.Fatalf("unknown mode: %f/%v", pts, ok)
	}
}

func TestSubmission_GradeMCQ_WithSnapshotMissingContinues(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.Answers = []SubmissionAnswer{
		{TestSetQuestionID: "tsq-missing", QuestionType: QuestionTypeMCQ, MCQChoiceID: "x", PointsPossible: 5},
		{TestSetQuestionID: "tsq-ok", QuestionType: QuestionTypeMCQ, MCQChoiceID: "A", PointsPossible: 5},
	}
	snapshots := map[string]MCQSnapshot{
		"tsq-ok": MCQSnapshotFromJSONMust(t, `{"options":[{"option_id":"A","is_correct":true}],"scoring":{"mode":"single_correct"}}`),
	}
	err := s.GradeMCQAnswersFromSnapshot(snapshots)
	if !errors.Is(err, ErrMCQSnapshotMissing) {
		t.Fatalf("want ErrMCQSnapshotMissing, got %v", err)
	}
	// The missing-snapshot answer is zeroed; the other one is graded.
	if s.Answers[0].PointsEarned != 0 || s.Answers[0].MCQCorrect != nil {
		t.Fatalf("missing-snapshot answer must zero out: %+v", s.Answers[0])
	}
	if s.Answers[1].PointsEarned != 5 || s.Answers[1].MCQCorrect == nil || !*s.Answers[1].MCQCorrect {
		t.Fatalf("good answer must still grade: %+v", s.Answers[1])
	}
}

// MCQSnapshotFromJSONMust parses a snapshot or fails the test.
func MCQSnapshotFromJSONMust(t *testing.T, payload string) MCQSnapshot {
	t.Helper()
	snap, err := MCQSnapshotFromJSON(payload)
	if err != nil {
		t.Fatalf("MCQSnapshotFromJSON: %v", err)
	}
	return snap
}

func boolPtr(b bool) *bool { return &b }
