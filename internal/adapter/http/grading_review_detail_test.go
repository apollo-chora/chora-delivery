// grading_review_detail_test.go — OE authored model-answer projection (ADR-172).
//
// A freshly AI-graded OE submission has no instructor-amended model answer, so
// buildGradingReviewDetail used to emit no `model_answer` and the grading-queue
// review textarea rendered empty — even though the authored model answer is
// generated + persisted at authoring and snapshotted into the test-set
// question's payload_snapshot. These tests pin the fallback to that snapshot.
package httpapi

import (
	"strings"
	"testing"
	"time"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestParseAuthoredOESnapshot_ExtractsModelAnswerPromptRubric(t *testing.T) {
	snap := `{"stem":"Explain the three Sprint Planning topics.","model_answer":"Topic One… Topic Two… Topic Three…","rubric":[{"criterion_id":"c1","description":"Topic One Identification","weight":0.33},{"criterion_id":"c2","description":"Topic Two Identification","weight":0.34}]}`
	got := parseAuthoredOESnapshot(snap)
	if !strings.Contains(got.ModelAnswer, "Topic One") {
		t.Fatalf("model_answer not parsed: %q", got.ModelAnswer)
	}
	if got.Prompt != "Explain the three Sprint Planning topics." {
		t.Fatalf("prompt (stem) not parsed: %q", got.Prompt)
	}
	if len(got.Rubric) != 2 {
		t.Fatalf("rubric len = %d, want 2", len(got.Rubric))
	}
	if got.Rubric[0]["description"] != "Topic One Identification" {
		t.Fatalf("rubric[0].description = %v", got.Rubric[0]["description"])
	}
}

func TestParseAuthoredOESnapshot_EmptyOrBad(t *testing.T) {
	if got := parseAuthoredOESnapshot(""); got.ModelAnswer != "" || got.Prompt != "" {
		t.Fatalf("empty snapshot should yield zero value, got %+v", got)
	}
	if got := parseAuthoredOESnapshot("not json"); got.ModelAnswer != "" {
		t.Fatalf("bad json should yield zero value, got %+v", got)
	}
}

func newOESubmissionForDetail(t *testing.T) *domain.Submission {
	t.Helper()
	sub, err := domain.NewSubmission(domain.NewSubmissionInput{
		AssessmentID:   "01985e7f-1234-7abc-8def-000000000a01",
		TenantID:       "11111111-1111-7111-8111-111111111111",
		LearnerGCID:    "00000000-0000-7000-8000-000000002999",
		AttemptNumber:  1,
		OpensAt:        time.Now().Add(-time.Minute),
		ClosesAt:       time.Now().Add(time.Hour),
		MaxScore:       1,
		PassingPercent: 50,
	})
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	sub.Answers = []domain.SubmissionAnswer{
		{
			TestSetQuestionID: "tsq-oe",
			QuestionID:        "q-oe",
			QuestionType:      domain.QuestionTypeOE,
			OEResponseText:    "learner essay",
			PointsPossible:    1,
		},
	}
	return sub
}

func TestBuildGradingReviewDetail_OEModelAnswerFallsBackToAuthored(t *testing.T) {
	sub := newOESubmissionForDetail(t)
	authored := map[string]oeAuthored{
		"tsq-oe": {
			Prompt:      "Explain the three topics.",
			ModelAnswer: "Authored model answer.",
			Rubric: []map[string]any{
				{"criterion_id": "c1", "description": "Topic One", "weight": 0.33},
			},
		},
	}
	detail := buildGradingReviewDetail(sub, authored)
	qs, ok := detail["questions"].([]map[string]any)
	if !ok || len(qs) != 1 {
		t.Fatalf("questions shape = %T len?", detail["questions"])
	}
	q := qs[0]
	if q["model_answer"] != "Authored model answer." {
		t.Fatalf("model_answer = %v, want authored fallback", q["model_answer"])
	}
	if q["prompt"] != "Explain the three topics." {
		t.Fatalf("prompt = %v, want authored stem", q["prompt"])
	}
	if _, has := q["rubric"]; !has {
		t.Fatalf("rubric not projected from the authored snapshot")
	}
}

func TestBuildGradingReviewDetail_AmendedModelAnswerWins(t *testing.T) {
	sub := newOESubmissionForDetail(t)
	sub.Answers[0].AmendedModelAnswer = "Instructor corrected answer."
	authored := map[string]oeAuthored{
		"tsq-oe": {ModelAnswer: "Authored model answer."},
	}
	detail := buildGradingReviewDetail(sub, authored)
	qs, _ := detail["questions"].([]map[string]any)
	if qs[0]["model_answer"] != "Instructor corrected answer." {
		t.Fatalf("amended model_answer should win, got %v", qs[0]["model_answer"])
	}
}
