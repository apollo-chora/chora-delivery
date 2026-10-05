// submission_hitl_test.go — ADR-172 per-submission OE grading + HITL gate.
//
// Exercises the aggregate operations the new oe_grading_crew inbox + the R+
// grading-queue handlers call: ApplyGrading (per-submission AI grading with
// provenance=AI + overall comment + the PENDING_REVIEW gate), the mandatory
// HITL release gate, and the opt-in instructor overrides (score / comment /
// model-answer / overall comment) with AI→HUMAN provenance + append-only audit.
package delivery

import (
	"testing"
	"time"
)

// gradedOEPendingSubmission builds a submission with one MCQ (50/50) + one OE
// answer, runs ApplyGrading so it lands GRADED_PENDING_RELEASE + PENDING_REVIEW.
func gradedOEPendingSubmission(t *testing.T, oePoints float64) *Submission {
	t.Helper()
	s := oePendingSubmission(SubmissionAnswer{
		TestSetQuestionID: "oe-1",
		QuestionID:        "oe-q-1",
		QuestionType:      QuestionTypeOE,
		OEResponseText:    "the mitochondria is the powerhouse of the cell",
		PointsPossible:    50,
	})
	now := time.Now().UTC()
	allGraded, err := s.ApplyGrading(now, SubmissionGrading{
		SubmissionID:   s.ID,
		AssessmentID:   s.AssessmentID,
		Outcome:        "SUCCESS",
		OverallComment: "Solid effort — 90%. Tighten articulation on cell biology.",
		GradedAt:       now,
		Graded: []OEQuestionGrade{{
			TestSetQuestionID:   "oe-1",
			QuestionID:          "oe-q-1",
			PointsEarned:        oePoints,
			PointsPossible:      50,
			CriterionScoresJSON: `{"accuracy":4,"clarity":3}`,
			Comment:             "Correct, but be precise about the process.",
			GradingModelID:      "gemini-3.1-pro-preview",
			GradingResponseID:   "resp-abc",
		}},
	})
	if err != nil {
		t.Fatalf("ApplyGrading: %v", err)
	}
	if !allGraded {
		t.Fatalf("expected allGraded=true")
	}
	return s
}

func TestApplyGrading_LandsPendingReviewWithAIProvenance(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)

	if s.State != SubmissionStateGradedPendingRelease {
		t.Fatalf("state = %s, want GRADED_PENDING_RELEASE", s.State)
	}
	if s.ReviewStatus != ReviewStatusPendingReview {
		t.Fatalf("review_status = %q, want PENDING_REVIEW", s.ReviewStatus)
	}
	if s.OverallComment == "" || s.OverallCommentProvenance != ProvenanceAI {
		t.Fatalf("overall comment/provenance not set as AI: %q/%q", s.OverallComment, s.OverallCommentProvenance)
	}
	if s.AIOverallComment != s.OverallComment {
		t.Fatalf("AIOverallComment should mirror the AI draft")
	}
	// Total = 50 (MCQ) + 45 (OE) = 95.
	if s.TotalScore != 95 {
		t.Fatalf("total = %v, want 95", s.TotalScore)
	}
	oe := s.Answers[s.findOEAnswer("oe-1")]
	if oe.ScoreProvenance != ProvenanceAI || oe.CommentProvenance != ProvenanceAI {
		t.Fatalf("OE provenance not AI: %q/%q", oe.ScoreProvenance, oe.CommentProvenance)
	}
	if oe.OEComment == "" || oe.AIComment != oe.OEComment {
		t.Fatalf("OE comment / AI original not set")
	}
	if oe.AIPointsEarned == nil || *oe.AIPointsEarned != 45 {
		t.Fatalf("AIPointsEarned not preserved")
	}
	if oe.GradingModelID != "gemini-3.1-pro-preview" {
		t.Fatalf("model provenance missing")
	}
}

func TestApplyGrading_FailedOutcomeIsNoOp(t *testing.T) {
	s := oePendingSubmission(SubmissionAnswer{
		TestSetQuestionID: "oe-1", QuestionID: "oe-q-1", QuestionType: QuestionTypeOE,
		OEResponseText: "answer", PointsPossible: 50,
	})
	allGraded, err := s.ApplyGrading(time.Now(), SubmissionGrading{
		SubmissionID: s.ID, Outcome: "FAILED", FailureMessage: "armor blocked",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if allGraded {
		t.Fatalf("failed batch must not mark graded")
	}
	if s.State != SubmissionStatePendingOEGrading {
		t.Fatalf("state = %s, want PENDING_OE_GRADING", s.State)
	}
	if s.ReviewStatus != ReviewStatusNotRequired {
		t.Fatalf("failed batch must not set review_status")
	}
}

func TestMarkReleased_GatedOnApproval(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)

	// PENDING_REVIEW — release must be blocked.
	if s.CanRelease() {
		t.Fatalf("CanRelease should be false while PENDING_REVIEW")
	}
	s.MarkReleased(time.Now())
	if s.State == SubmissionStateReleased {
		t.Fatalf("MarkReleased must be a no-op while PENDING_REVIEW")
	}

	// Approve, then release succeeds.
	if err := s.ApproveAsIs(time.Now(), "instructor-gcid"); err != nil {
		t.Fatalf("ApproveAsIs: %v", err)
	}
	if !s.CanRelease() {
		t.Fatalf("CanRelease should be true after approval")
	}
	s.MarkReleased(time.Now())
	if s.State != SubmissionStateReleased {
		t.Fatalf("state = %s, want RELEASED after approve+release", s.State)
	}
}

func TestMarkReleased_MCQOnlyNotGated(t *testing.T) {
	// MCQ-only fast path: MarkGradedPendingRelease without ApplyGrading →
	// ReviewStatusNotRequired → releases freely.
	s, _ := NewSubmission(validSubmissionInput())
	mcqCorrect := true
	s.Answers = []SubmissionAnswer{{
		TestSetQuestionID: "mcq-1", QuestionID: "mcq-q-1", QuestionType: QuestionTypeMCQ,
		MCQCorrect: &mcqCorrect, PointsEarned: 100, PointsPossible: 100, GradingDispatch: GradingDispatchDeterministic,
	}}
	s.State = SubmissionStateSubmitted
	s.MarkGradedPendingRelease(time.Now())
	if s.ReviewStatus != ReviewStatusNotRequired {
		t.Fatalf("MCQ-only must not require review")
	}
	s.MarkReleased(time.Now())
	if s.State != SubmissionStateReleased {
		t.Fatalf("MCQ-only should release without approval")
	}
}

func TestOverrideQuestionGrade_ScoreAndComment_FlipProvenanceAndAudit(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)
	newScore := 30.0
	newComment := "Re-marked: missed the light-dependent reaction."
	overrides, err := s.OverrideQuestionGrade(time.Now(), "instructor-1", "oe-1", &newScore, &newComment, "rubric misread")
	if err != nil {
		t.Fatalf("OverrideQuestionGrade: %v", err)
	}
	if len(overrides) != 2 {
		t.Fatalf("expected 2 audit entries (score+comment), got %d", len(overrides))
	}
	oe := s.Answers[s.findOEAnswer("oe-1")]
	if oe.PointsEarned != 30 || oe.ScoreProvenance != ProvenanceHuman {
		t.Fatalf("score override not applied: %v/%q", oe.PointsEarned, oe.ScoreProvenance)
	}
	if *oe.AIPointsEarned != 45 {
		t.Fatalf("AI original score must be preserved (45), got %v", *oe.AIPointsEarned)
	}
	if oe.CommentProvenance != ProvenanceHuman || oe.AIComment == oe.OEComment {
		t.Fatalf("comment override not applied / AI original lost")
	}
	if oe.OverriddenByGCID != "instructor-1" {
		t.Fatalf("overrider not recorded")
	}
	// Totals re-derived: 50 (MCQ) + 30 (OE) = 80.
	if s.TotalScore != 80 {
		t.Fatalf("total after override = %v, want 80", s.TotalScore)
	}
}

func TestOverrideQuestionGrade_ScoreOutOfRange(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)
	bad := 999.0
	if _, err := s.OverrideQuestionGrade(time.Now(), "i", "oe-1", &bad, nil, ""); err != ErrScoreOutOfRange {
		t.Fatalf("err = %v, want ErrScoreOutOfRange", err)
	}
}

func TestOverride_RejectedAfterApproval(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)
	if err := s.ApproveAsIs(time.Now(), "i"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	score := 10.0
	if _, err := s.OverrideQuestionGrade(time.Now(), "i", "oe-1", &score, nil, ""); err != ErrSubmissionAlreadyApproved {
		t.Fatalf("err = %v, want ErrSubmissionAlreadyApproved", err)
	}
}

func TestAmendModelAnswer_FlipsProvenance(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)
	overrides, err := s.AmendModelAnswer(time.Now(), "instructor-1", "oe-1", "A corrected reference answer.")
	if err != nil {
		t.Fatalf("AmendModelAnswer: %v", err)
	}
	if len(overrides) != 1 || overrides[0].Field != "MODEL_ANSWER" {
		t.Fatalf("expected one MODEL_ANSWER audit entry")
	}
	oe := s.Answers[s.findOEAnswer("oe-1")]
	if oe.AmendedModelAnswer == "" || oe.ModelAnswerProvenance != ProvenanceHuman {
		t.Fatalf("model answer amendment not applied")
	}
}

func TestEditOverallComment_FlipsProvenance(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)
	ai := s.AIOverallComment
	overrides, err := s.EditOverallComment(time.Now(), "instructor-1", "Reworded overall feedback.")
	if err != nil {
		t.Fatalf("EditOverallComment: %v", err)
	}
	if len(overrides) != 1 || overrides[0].Field != "OVERALL_COMMENT" {
		t.Fatalf("expected one OVERALL_COMMENT audit entry")
	}
	if s.OverallCommentProvenance != ProvenanceHuman {
		t.Fatalf("overall comment provenance not flipped")
	}
	if s.AIOverallComment != ai || s.OverallComment == ai {
		t.Fatalf("AI original overall comment must be preserved and current value changed")
	}
}

func TestApproveAsIs_Idempotent(t *testing.T) {
	s := gradedOEPendingSubmission(t, 45)
	if err := s.ApproveAsIs(time.Now(), "i"); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if err := s.ApproveAsIs(time.Now(), "i"); err != nil {
		t.Fatalf("second approve must be idempotent: %v", err)
	}
	if s.ReviewStatus != ReviewStatusApproved {
		t.Fatalf("review_status = %q, want APPROVED", s.ReviewStatus)
	}
}

func TestHITLEdits_RejectedBeforeGrading(t *testing.T) {
	// A submission still PENDING_OE_GRADING cannot be edited/approved.
	s := oePendingSubmission(SubmissionAnswer{
		TestSetQuestionID: "oe-1", QuestionID: "oe-q-1", QuestionType: QuestionTypeOE,
		OEResponseText: "x", PointsPossible: 50,
	})
	score := 10.0
	if _, err := s.OverrideQuestionGrade(time.Now(), "i", "oe-1", &score, nil, ""); err != ErrSubmissionNotGraded {
		t.Fatalf("override err = %v, want ErrSubmissionNotGraded", err)
	}
	if err := s.ApproveAsIs(time.Now(), "i"); err != ErrSubmissionNotGraded {
		t.Fatalf("approve err = %v, want ErrSubmissionNotGraded", err)
	}
}
