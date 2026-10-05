// submission_oe_grading_test.go — Fix-E (ADR-155 Lane B completion-event):
// the agentic-dispatch chain emits chora.delivery.grading.oe_batch_requested.v1
// on /submit; the chora-ai-kernel-orchestrator (Python LangGraph) invokes the
// oe_grader Vertex engine and emits chora.delivery.grading.oe_batch_completed.v1.
//
// chora-delivery is the inbox subscriber for the completion event. This file
// exercises the domain-side aggregate operation that the subscriber calls
// after decode: Submission.ApplyOEGradingBatch — replays the LLM-evaluator
// scores onto submission_answers + advances the FSM to GRADED_PENDING_RELEASE
// when ALL OE answers are graded.
//
// Per .claude/rules/ddd-enforcement.md #2 "child entities accessed ONLY
// through their aggregate root" — the subscriber MUST NOT touch
// submission_answers directly; ApplyOEGradingBatch is the load-bearing
// aggregate operation.
package delivery

import (
	"strings"
	"testing"
	"time"
)

// oePendingSubmission builds a SUBMITTED + MoveToOEPending submission with the
// supplied OE answers. MCQ answers are already graded (test setup).
func oePendingSubmission(oeAnswers ...SubmissionAnswer) *Submission {
	s, _ := NewSubmission(validSubmissionInput())
	now := time.Now()
	// Seed a graded MCQ answer worth 50 — the OE grader fills the rest.
	mcqCorrect := true
	s.Answers = []SubmissionAnswer{
		{
			TestSetQuestionID: "mcq-1",
			QuestionID:        "mcq-q-1",
			QuestionType:      QuestionTypeMCQ,
			MCQChoiceID:       "A",
			MCQCorrect:        &mcqCorrect,
			PointsEarned:      50,
			PointsPossible:    50,
			GradingDispatch:   GradingDispatchDeterministic,
		},
	}
	s.Answers = append(s.Answers, oeAnswers...)
	s.State = SubmissionStateSubmitted
	s.MoveToOEPending(now)
	return s
}

func TestSubmission_ApplyOEGradingBatch_AllOEGraded_AdvancesToGradedPendingRelease(t *testing.T) {
	now := time.Now().UTC()
	s := oePendingSubmission(
		SubmissionAnswer{
			TestSetQuestionID: "oe-1",
			QuestionID:        "oe-q-1",
			QuestionType:      QuestionTypeOE,
			OEResponseText:    "essay text",
			PointsPossible:    50,
		},
	)
	in := OEGradingBatch{
		SubmissionID: s.ID,
		AssessmentID: s.AssessmentID,
		BatchOutcome: "ok",
		GradedAt:     now,
		Results: []OEGradingResult{{
			TestSetQuestionID:    "oe-1",
			QuestionID:           "oe-q-1",
			PointsEarned:         40,
			PointsPossible:       50,
			CriterionScoresJSON:  `{"clarity":4}`,
			LLMEvaluatorFeedback: "good clarity",
		}},
	}
	allGraded, err := s.ApplyOEGradingBatch(now, in)
	if err != nil {
		t.Fatalf("ApplyOEGradingBatch: %v", err)
	}
	if !allGraded {
		t.Fatalf("allGraded: want true (only OE answer in batch), got false")
	}
	// OE answer now carries the score + feedback + criterion JSON + graded_at.
	var oe SubmissionAnswer
	for _, a := range s.Answers {
		if a.TestSetQuestionID == "oe-1" {
			oe = a
		}
	}
	if oe.PointsEarned != 40 {
		t.Errorf("oe.PointsEarned: want 40, got %f", oe.PointsEarned)
	}
	if oe.OEFeedback != "good clarity" {
		t.Errorf("oe.OEFeedback: want %q, got %q", "good clarity", oe.OEFeedback)
	}
	if oe.OECriterionJSON != `{"clarity":4}` {
		t.Errorf("oe.OECriterionJSON: want %q, got %q", `{"clarity":4}`, oe.OECriterionJSON)
	}
	if oe.OEGradedAt == nil {
		t.Fatalf("oe.OEGradedAt: want non-nil")
	}
	if oe.GradingDispatch != GradingDispatchLLMEvaluator {
		t.Errorf("oe.GradingDispatch: want LLM_EVALUATOR, got %s", oe.GradingDispatch)
	}
	// FSM advanced: PENDING_OE_GRADING → GRADED_PENDING_RELEASE; aggregate
	// totals computed (MCQ 50 + OE 40 = 90).
	if s.State != SubmissionStateGradedPendingRelease {
		t.Fatalf("state: want GRADED_PENDING_RELEASE, got %s", s.State)
	}
	if s.OEScore != 40 {
		t.Errorf("OEScore: want 40, got %f", s.OEScore)
	}
	if s.MCQScore != 50 {
		t.Errorf("MCQScore: want 50, got %f", s.MCQScore)
	}
	if s.TotalScore != 90 {
		t.Errorf("TotalScore: want 90, got %f", s.TotalScore)
	}
	if s.Passed == nil || !*s.Passed {
		t.Errorf("Passed: want true (90%% >= 70%%), got %v", s.Passed)
	}
}

func TestSubmission_ApplyOEGradingBatch_PartialBatch_KeepsStatePending(t *testing.T) {
	now := time.Now().UTC()
	s := oePendingSubmission(
		SubmissionAnswer{
			TestSetQuestionID: "oe-1",
			QuestionID:        "oe-q-1",
			QuestionType:      QuestionTypeOE,
			OEResponseText:    "essay 1",
			PointsPossible:    50,
		},
		SubmissionAnswer{
			TestSetQuestionID: "oe-2",
			QuestionID:        "oe-q-2",
			QuestionType:      QuestionTypeOE,
			OEResponseText:    "essay 2",
			PointsPossible:    50,
		},
	)
	// Only oe-1 graded in this batch — oe-2 still pending.
	in := OEGradingBatch{
		SubmissionID: s.ID,
		AssessmentID: s.AssessmentID,
		BatchOutcome: "ok",
		GradedAt:     now,
		Results: []OEGradingResult{{
			TestSetQuestionID: "oe-1",
			QuestionID:        "oe-q-1",
			PointsEarned:      35,
			PointsPossible:    50,
		}},
	}
	allGraded, err := s.ApplyOEGradingBatch(now, in)
	if err != nil {
		t.Fatalf("ApplyOEGradingBatch: %v", err)
	}
	if allGraded {
		t.Fatalf("allGraded: want false (oe-2 ungraded), got true")
	}
	// FSM stays in PENDING_OE_GRADING.
	if s.State != SubmissionStatePendingOEGrading {
		t.Fatalf("state: want PENDING_OE_GRADING, got %s", s.State)
	}
}

func TestSubmission_ApplyOEGradingBatch_Idempotent(t *testing.T) {
	now := time.Now().UTC()
	s := oePendingSubmission(SubmissionAnswer{
		TestSetQuestionID: "oe-1",
		QuestionID:        "oe-q-1",
		QuestionType:      QuestionTypeOE,
		OEResponseText:    "essay",
		PointsPossible:    50,
	})
	in := OEGradingBatch{
		SubmissionID: s.ID,
		BatchOutcome: "ok",
		GradedAt:     now,
		Results: []OEGradingResult{{
			TestSetQuestionID: "oe-1",
			PointsEarned:      40,
			PointsPossible:    50,
		}},
	}
	if _, err := s.ApplyOEGradingBatch(now, in); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	prevState := s.State
	prevTotal := s.TotalScore

	// Same event replayed → must NOT double-count points and must NOT regress
	// the FSM.
	if _, err := s.ApplyOEGradingBatch(now, in); err != nil {
		t.Fatalf("replay apply: %v", err)
	}
	if s.State != prevState {
		t.Errorf("state after replay: want %s, got %s", prevState, s.State)
	}
	if s.TotalScore != prevTotal {
		t.Errorf("total after replay: want %f, got %f", prevTotal, s.TotalScore)
	}
	if s.OEScore != 40 {
		t.Errorf("oe_score: want 40 (no double-count), got %f", s.OEScore)
	}
}

func TestSubmission_ApplyOEGradingBatch_RejectsWhenSubmissionMismatch(t *testing.T) {
	s := oePendingSubmission(SubmissionAnswer{
		TestSetQuestionID: "oe-1",
		QuestionID:        "oe-q-1",
		QuestionType:      QuestionTypeOE,
		PointsPossible:    50,
	})
	in := OEGradingBatch{
		SubmissionID: "different-submission-id",
		Results: []OEGradingResult{{
			TestSetQuestionID: "oe-1",
			PointsEarned:      40,
			PointsPossible:    50,
		}},
	}
	_, err := s.ApplyOEGradingBatch(time.Now(), in)
	if err == nil {
		t.Fatalf("want error on submission_id mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "submission") {
		t.Errorf("want error mentioning submission, got %v", err)
	}
}

func TestSubmission_ApplyOEGradingBatch_FailedBatchAttachesError(t *testing.T) {
	// When the orchestrator reports a failed batch (batch_outcome != "ok"),
	// the OE answer captures the failure message in OEFeedback + leaves
	// PointsEarned at zero. FSM stays PENDING_OE_GRADING so a retry can
	// drive it forward.
	now := time.Now().UTC()
	s := oePendingSubmission(SubmissionAnswer{
		TestSetQuestionID: "oe-1",
		QuestionID:        "oe-q-1",
		QuestionType:      QuestionTypeOE,
		PointsPossible:    50,
	})
	in := OEGradingBatch{
		SubmissionID:   s.ID,
		BatchOutcome:   "failed",
		FailureMessage: "vertex 5xx",
		GradedAt:       now,
		Results:        []OEGradingResult{},
	}
	allGraded, err := s.ApplyOEGradingBatch(now, in)
	if err != nil {
		t.Fatalf("ApplyOEGradingBatch (failed batch): %v", err)
	}
	if allGraded {
		t.Fatalf("allGraded: want false on failed batch")
	}
	if s.State != SubmissionStatePendingOEGrading {
		t.Errorf("state on failed batch: want PENDING_OE_GRADING, got %s", s.State)
	}
}

func TestSubmission_ApplyOEGradingBatch_IgnoresMCQResults(t *testing.T) {
	// The OE grader never returns MCQ results — but defensive: if the batch
	// carries a test_set_question_id that maps to an MCQ answer in the
	// submission, the domain MUST not overwrite the deterministic MCQ score.
	now := time.Now().UTC()
	s := oePendingSubmission(SubmissionAnswer{
		TestSetQuestionID: "oe-1",
		QuestionID:        "oe-q-1",
		QuestionType:      QuestionTypeOE,
		OEResponseText:    "essay",
		PointsPossible:    50,
	})
	in := OEGradingBatch{
		SubmissionID: s.ID,
		BatchOutcome: "ok",
		GradedAt:     now,
		Results: []OEGradingResult{
			{TestSetQuestionID: "mcq-1", PointsEarned: 0, PointsPossible: 50}, // MCQ — must be ignored
			{TestSetQuestionID: "oe-1", PointsEarned: 30, PointsPossible: 50},
		},
	}
	if _, err := s.ApplyOEGradingBatch(now, in); err != nil {
		t.Fatalf("ApplyOEGradingBatch: %v", err)
	}
	var mcq SubmissionAnswer
	for _, a := range s.Answers {
		if a.TestSetQuestionID == "mcq-1" {
			mcq = a
		}
	}
	if mcq.PointsEarned != 50 {
		t.Errorf("MCQ overwritten by OE grader: want 50, got %f", mcq.PointsEarned)
	}
}
