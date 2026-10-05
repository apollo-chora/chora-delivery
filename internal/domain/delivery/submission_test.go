package delivery

import (
	"testing"
	"time"
)

func validSubmissionInput() NewSubmissionInput {
	return NewSubmissionInput{
		AssessmentID:   "01985e7f-1234-7abc-8def-000000000a01",
		TenantID:       "11111111-1111-7111-8111-111111111111",
		LearnerGCID:    "00000000-0000-7000-8000-000000001999",
		AttemptNumber:  1,
		OpensAt:        time.Now(),
		ClosesAt:       time.Now().Add(2 * time.Hour),
		MaxScore:       100,
		PassingPercent: 70,
	}
}

func TestNewSubmission_HappyPath(t *testing.T) {
	s, err := NewSubmission(validSubmissionInput())
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if s.State != SubmissionStateStarted {
		t.Fatalf("state: want STARTED, got %s", s.State)
	}
	if s.TimeLimitSecs <= 0 {
		t.Fatalf("time_limit_secs: want >0, got %d", s.TimeLimitSecs)
	}
}

func TestNewSubmission_RejectsMissing(t *testing.T) {
	in := validSubmissionInput()
	in.AssessmentID = ""
	if _, err := NewSubmission(in); err != ErrSubmissionAssessmentRequired {
		t.Fatalf("want ErrSubmissionAssessmentRequired, got %v", err)
	}
	in = validSubmissionInput()
	in.LearnerGCID = ""
	if _, err := NewSubmission(in); err != ErrSubmissionLearnerRequired {
		t.Fatalf("want ErrSubmissionLearnerRequired, got %v", err)
	}
}

func TestSubmission_MergeAnswers_Idempotent(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	first := []SubmissionAnswer{
		{TestSetQuestionID: "q1", QuestionID: "q1q", QuestionType: QuestionTypeMCQ, MCQChoiceID: "A"},
		{TestSetQuestionID: "q2", QuestionID: "q2q", QuestionType: QuestionTypeOE, OEResponseText: "first"},
	}
	if err := s.MergeAnswers(time.Now(), first); err != nil {
		t.Fatalf("merge1: %v", err)
	}
	if len(s.Answers) != 2 {
		t.Fatalf("answers: want 2, got %d", len(s.Answers))
	}
	if s.State != SubmissionStateInProgress {
		t.Fatalf("state after merge: want IN_PROGRESS, got %s", s.State)
	}
	if s.LastSavedAt == nil {
		t.Fatalf("last_saved_at not set")
	}

	// Second merge — q1 overwritten, q3 added.
	second := []SubmissionAnswer{
		{TestSetQuestionID: "q1", QuestionID: "q1q", QuestionType: QuestionTypeMCQ, MCQChoiceID: "B"},
		{TestSetQuestionID: "q3", QuestionID: "q3q", QuestionType: QuestionTypeMCQ, MCQChoiceID: "C"},
	}
	if err := s.MergeAnswers(time.Now(), second); err != nil {
		t.Fatalf("merge2: %v", err)
	}
	if len(s.Answers) != 3 {
		t.Fatalf("answers after merge2: want 3, got %d", len(s.Answers))
	}
	var q1 SubmissionAnswer
	for _, a := range s.Answers {
		if a.TestSetQuestionID == "q1" {
			q1 = a
		}
	}
	if q1.MCQChoiceID != "B" {
		t.Fatalf("q1 not overwritten: got %s", q1.MCQChoiceID)
	}
}

func TestSubmission_AutosaveWindowGuard(t *testing.T) {
	in := validSubmissionInput()
	in.ClosesAt = time.Now().Add(-1 * time.Minute) // in the past
	s, _ := NewSubmission(in)
	err := s.MergeAnswers(time.Now(),
		[]SubmissionAnswer{{TestSetQuestionID: "q1", QuestionID: "q1q"}},
	)
	if err != ErrSubmissionWindowClosed {
		t.Fatalf("want ErrSubmissionWindowClosed, got %v", err)
	}
}

func TestSubmission_MarkSubmitted_Idempotent(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	if err := s.MarkSubmitted(time.Now()); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if s.State != SubmissionStateSubmitted {
		t.Fatalf("state: want SUBMITTED, got %s", s.State)
	}
	// Second call returns sentinel (caller maps to 200).
	if err := s.MarkSubmitted(time.Now()); err != ErrSubmissionAlreadySubmitted {
		t.Fatalf("second submit: want ErrSubmissionAlreadySubmitted, got %v", err)
	}
}

func TestSubmission_MarkGradedPendingRelease_ComputesScore(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.Answers = []SubmissionAnswer{
		{TestSetQuestionID: "q1", QuestionType: QuestionTypeMCQ, PointsEarned: 50, PointsPossible: 50},
		{TestSetQuestionID: "q2", QuestionType: QuestionTypeMCQ, PointsEarned: 30, PointsPossible: 50},
	}
	s.State = SubmissionStateSubmitted
	s.MarkGradedPendingRelease(time.Now())
	if s.State != SubmissionStateGradedPendingRelease {
		t.Fatalf("state: want GRADED_PENDING_RELEASE, got %s", s.State)
	}
	if s.TotalScore != 80 {
		t.Fatalf("total: want 80, got %f", s.TotalScore)
	}
	if s.Passed == nil || !*s.Passed {
		t.Fatalf("passed: want true (80%% >= 70%%), got %v", s.Passed)
	}
}

func TestSubmission_MarkReleased_OnlyFromGradedPending(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.State = SubmissionStateGradedPendingRelease
	s.MarkReleased(time.Now())
	if s.State != SubmissionStateReleased {
		t.Fatalf("state: want RELEASED, got %s", s.State)
	}
	if s.ReleasedAt == nil {
		t.Fatalf("released_at not set")
	}
	// Released from SUBMITTED is skipped (still SUBMITTED).
	s2, _ := NewSubmission(validSubmissionInput())
	s2.State = SubmissionStateSubmitted
	s2.MarkReleased(time.Now())
	if s2.State == SubmissionStateReleased {
		t.Fatalf("SUBMITTED → RELEASED should be SKIPPED (only GRADED_PENDING_RELEASE releases)")
	}
}

func TestSubmission_WireState(t *testing.T) {
	cases := map[SubmissionState]string{
		SubmissionStateStarted:              "IN_PROGRESS",
		SubmissionStateInProgress:           "IN_PROGRESS",
		SubmissionStateSubmitted:            "SUBMITTED",
		SubmissionStatePendingOEGrading:     "GRADING",
		SubmissionStateGradedPendingRelease: "GRADED",
		SubmissionStateReleased:             "RELEASED",
	}
	for in, want := range cases {
		if got := in.WireState(); got != want {
			t.Errorf("wirestate %s: want %s, got %s", in, want, got)
		}
	}
}

func TestSubmission_IsPendingRelease(t *testing.T) {
	for _, s := range []SubmissionState{
		SubmissionStateSubmitted,
		SubmissionStatePendingOEGrading,
		SubmissionStateGradedPendingRelease,
	} {
		sub := &Submission{State: s}
		if !sub.IsPendingRelease() {
			t.Errorf("state %s: want IsPendingRelease()=true", s)
		}
	}
	released := &Submission{State: SubmissionStateReleased}
	if released.IsPendingRelease() {
		t.Errorf("RELEASED should NOT be pending")
	}
}

func TestGradeMCQAnswer_SingleCorrect(t *testing.T) {
	pts, correct := GradeMCQAnswer("A", nil, []string{"A"}, 10, "single_correct")
	if !correct || pts != 10 {
		t.Errorf("single_correct A vs A: want 10/true, got %f/%v", pts, correct)
	}
	pts, correct = GradeMCQAnswer("B", nil, []string{"A"}, 10, "single_correct")
	if correct || pts != 0 {
		t.Errorf("single_correct B vs A: want 0/false, got %f/%v", pts, correct)
	}
	pts, _ = GradeMCQAnswer("", nil, []string{"A"}, 10, "single_correct")
	if pts != 0 {
		t.Errorf("unanswered: want 0, got %f", pts)
	}
}

func TestGradeMCQAnswer_AllOrNothing(t *testing.T) {
	pts, correct := GradeMCQAnswer("", []string{"A", "B"}, []string{"A", "B"}, 10, "all_or_nothing")
	if !correct || pts != 10 {
		t.Errorf("all_or_nothing exact: want 10/true, got %f/%v", pts, correct)
	}
	pts, _ = GradeMCQAnswer("", []string{"A"}, []string{"A", "B"}, 10, "all_or_nothing")
	if pts != 0 {
		t.Errorf("all_or_nothing partial: want 0, got %f", pts)
	}
	pts, _ = GradeMCQAnswer("", []string{"A", "B", "C"}, []string{"A", "B"}, 10, "all_or_nothing")
	if pts != 0 {
		t.Errorf("all_or_nothing extra: want 0, got %f", pts)
	}
}

func TestSubmission_TimeRemainingSeconds(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	rem := s.TimeRemainingSeconds(time.Now())
	if rem <= 0 || rem > 7300 {
		t.Errorf("remaining: want ~7200, got %d", rem)
	}
	// Post-submit
	s.State = SubmissionStateSubmitted
	if s.TimeRemainingSeconds(time.Now()) != 0 {
		t.Errorf("post-submit remaining: want 0, got %d", s.TimeRemainingSeconds(time.Now()))
	}
}

// ----------------------------------------------------------------------------
// Fix-F: Lane B real MCQ grading via snapshot lookup
// (chora-delivery does NOT cross-DB-query chora_creation per ddd-enforcement #3
// — the MCQ payload is snapshot-cached at TestSet.Publish() via gRPC; see
// migrations/0011_test_set_questions_snapshot.up.sql and the SnapshotQuestionByID
// proto on chora.services.creation.v1.Creation in chora-contracts).
// ----------------------------------------------------------------------------

// makeMCQSnapshotPayload builds a canonical MCQPayload JSON-shape map matching
// chora-contracts/openapi/creation-questions.yaml MCQPayload — used to drive
// the grader from the test side without depending on gRPC stubs.
func makeMCQSnapshotPayload(scoringMode string, options []map[string]any) MCQSnapshot {
	return MCQSnapshot{
		Options:     options,
		ScoringMode: scoringMode,
	}
}

func TestSubmission_GradeMCQAnswersFromSnapshot_AllCorrect(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.Answers = []SubmissionAnswer{
		{
			TestSetQuestionID: "tsq-1",
			QuestionID:        "q-1",
			QuestionType:      QuestionTypeMCQ,
			MCQChoiceID:       "opt-correct",
			PointsPossible:    10,
		},
	}
	snapshots := map[string]MCQSnapshot{
		"tsq-1": makeMCQSnapshotPayload("single_correct", []map[string]any{
			{"option_id": "opt-correct", "is_correct": true},
			{"option_id": "opt-wrong", "is_correct": false},
		}),
	}
	if err := s.GradeMCQAnswersFromSnapshot(snapshots); err != nil {
		t.Fatalf("GradeMCQAnswersFromSnapshot: %v", err)
	}
	if got := s.Answers[0].PointsEarned; got != 10 {
		t.Errorf("points_earned: want 10, got %f", got)
	}
	if s.Answers[0].MCQCorrect == nil || !*s.Answers[0].MCQCorrect {
		t.Errorf("mcq_correct: want true, got %v", s.Answers[0].MCQCorrect)
	}
	if s.Answers[0].GradingDispatch != GradingDispatchDeterministic {
		t.Errorf("grading_dispatch: want DETERMINISTIC, got %s", s.Answers[0].GradingDispatch)
	}
}

func TestSubmission_GradeMCQAnswersFromSnapshot_AllWrong(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.Answers = []SubmissionAnswer{
		{
			TestSetQuestionID: "tsq-1",
			QuestionID:        "q-1",
			QuestionType:      QuestionTypeMCQ,
			MCQChoiceID:       "opt-wrong",
			PointsPossible:    10,
		},
	}
	snapshots := map[string]MCQSnapshot{
		"tsq-1": makeMCQSnapshotPayload("single_correct", []map[string]any{
			{"option_id": "opt-correct", "is_correct": true},
			{"option_id": "opt-wrong", "is_correct": false},
		}),
	}
	if err := s.GradeMCQAnswersFromSnapshot(snapshots); err != nil {
		t.Fatalf("GradeMCQAnswersFromSnapshot: %v", err)
	}
	if got := s.Answers[0].PointsEarned; got != 0 {
		t.Errorf("points_earned (wrong): want 0, got %f", got)
	}
	if s.Answers[0].MCQCorrect == nil || *s.Answers[0].MCQCorrect {
		t.Errorf("mcq_correct (wrong): want false, got %v", s.Answers[0].MCQCorrect)
	}
}

func TestSubmission_GradeMCQAnswersFromSnapshot_AllOrNothingPartial(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.Answers = []SubmissionAnswer{
		{
			TestSetQuestionID: "tsq-1",
			QuestionID:        "q-1",
			QuestionType:      QuestionTypeMCQ,
			MCQChoiceIDs:      []string{"opt-A"}, // only A, missed B → 0 credit
			PointsPossible:    10,
		},
	}
	snapshots := map[string]MCQSnapshot{
		"tsq-1": makeMCQSnapshotPayload("all_or_nothing", []map[string]any{
			{"option_id": "opt-A", "is_correct": true},
			{"option_id": "opt-B", "is_correct": true},
			{"option_id": "opt-C", "is_correct": false},
		}),
	}
	if err := s.GradeMCQAnswersFromSnapshot(snapshots); err != nil {
		t.Fatalf("GradeMCQAnswersFromSnapshot: %v", err)
	}
	if got := s.Answers[0].PointsEarned; got != 0 {
		t.Errorf("all_or_nothing partial: want 0, got %f", got)
	}
}

func TestSubmission_GradeMCQAnswersFromSnapshot_EmptyAnswerZero(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.Answers = []SubmissionAnswer{
		{
			TestSetQuestionID: "tsq-1",
			QuestionID:        "q-1",
			QuestionType:      QuestionTypeMCQ,
			MCQChoiceID:       "", // empty
			PointsPossible:    10,
		},
	}
	snapshots := map[string]MCQSnapshot{
		"tsq-1": makeMCQSnapshotPayload("single_correct", []map[string]any{
			{"option_id": "opt-correct", "is_correct": true},
		}),
	}
	if err := s.GradeMCQAnswersFromSnapshot(snapshots); err != nil {
		t.Fatalf("GradeMCQAnswersFromSnapshot: %v", err)
	}
	if got := s.Answers[0].PointsEarned; got != 0 {
		t.Errorf("empty answer: want 0, got %f", got)
	}
}

func TestSubmission_GradeMCQAnswersFromSnapshot_MissingSnapshotIsFailLoud(t *testing.T) {
	// Per feedback_no_stubs_real_wiring: when a snapshot is missing at grade
	// time, the grader MUST fail loud (return an error). The placeholder
	// "award-full-credit-on-any-selection" path is gone — never recreate it.
	s, _ := NewSubmission(validSubmissionInput())
	s.Answers = []SubmissionAnswer{
		{
			TestSetQuestionID: "tsq-1",
			QuestionID:        "q-1",
			QuestionType:      QuestionTypeMCQ,
			MCQChoiceID:       "anything",
			PointsPossible:    10,
		},
	}
	// Missing snapshot for tsq-1 → grader errors.
	err := s.GradeMCQAnswersFromSnapshot(map[string]MCQSnapshot{})
	if err == nil {
		t.Fatalf("want error for missing snapshot, got nil")
	}
	// Per-answer score is 0 (not silently green-pathed).
	if got := s.Answers[0].PointsEarned; got != 0 {
		t.Errorf("missing snapshot: points must be 0, got %f", got)
	}
}

func TestSubmission_GradeMCQAnswersFromSnapshot_SkipsOEAnswers(t *testing.T) {
	s, _ := NewSubmission(validSubmissionInput())
	s.Answers = []SubmissionAnswer{
		{
			TestSetQuestionID: "tsq-1",
			QuestionType:      QuestionTypeOE,
			OEResponseText:    "essay answer",
			PointsPossible:    20,
		},
		{
			TestSetQuestionID: "tsq-2",
			QuestionType:      QuestionTypeMCQ,
			MCQChoiceID:       "opt-correct",
			PointsPossible:    10,
		},
	}
	snapshots := map[string]MCQSnapshot{
		"tsq-2": makeMCQSnapshotPayload("single_correct", []map[string]any{
			{"option_id": "opt-correct", "is_correct": true},
		}),
	}
	if err := s.GradeMCQAnswersFromSnapshot(snapshots); err != nil {
		t.Fatalf("GradeMCQAnswersFromSnapshot: %v", err)
	}
	// OE untouched.
	if s.Answers[0].PointsEarned != 0 {
		t.Errorf("OE answer must NOT be graded by MCQ grader, got %f", s.Answers[0].PointsEarned)
	}
	if s.Answers[0].MCQCorrect != nil {
		t.Errorf("OE answer MCQCorrect should remain nil, got %v", *s.Answers[0].MCQCorrect)
	}
	// MCQ graded.
	if s.Answers[1].PointsEarned != 10 {
		t.Errorf("MCQ answer: want 10, got %f", s.Answers[1].PointsEarned)
	}
}

func TestMCQSnapshot_CorrectOptions(t *testing.T) {
	snap := MCQSnapshot{
		Options: []map[string]any{
			{"option_id": "A", "is_correct": true},
			{"option_id": "B", "is_correct": false},
			{"option_id": "C", "is_correct": true},
		},
	}
	got := snap.CorrectOptionIDs()
	want := map[string]bool{"A": true, "C": true}
	if len(got) != 2 {
		t.Fatalf("correct options: want 2, got %d", len(got))
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected correct option %q in %v", id, got)
		}
	}
}

func TestMCQSnapshot_FromPayloadJSON(t *testing.T) {
	jsonPayload := `{
		"options": [
			{"option_id": "opt-a", "label": "A", "text": "Foo", "is_correct": false, "explainer": ""},
			{"option_id": "opt-b", "label": "B", "text": "Bar", "is_correct": true,  "explainer": ""}
		],
		"scoring": {"mode": "single_correct"},
		"shuffle_options": false
	}`
	snap, err := MCQSnapshotFromJSON(jsonPayload)
	if err != nil {
		t.Fatalf("MCQSnapshotFromJSON: %v", err)
	}
	if snap.ScoringMode != "single_correct" {
		t.Errorf("scoring_mode: want single_correct, got %q", snap.ScoringMode)
	}
	if len(snap.Options) != 2 {
		t.Fatalf("options: want 2, got %d", len(snap.Options))
	}
	ids := snap.CorrectOptionIDs()
	if len(ids) != 1 || ids[0] != "opt-b" {
		t.Errorf("correct: want [opt-b], got %v", ids)
	}
}
