// coverage_test.go — closes the remaining statement gaps in the survey
// aggregates: UpdateDraft's question-validation error path and
// validateAnswers' blank-question-id rejection.
package survey

import (
	"errors"
	"testing"
)

func TestSurvey_UpdateDraft_RejectsInvalidQuestions(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	// A question with a blank prompt fails normaliseQuestions → the error path
	// inside UpdateDraft's question-processing branch.
	if err := s.UpdateDraft(UpdateDraftInput{
		Questions: []Question{{Prompt: "  ", Type: QuestionTypeText}},
	}); !errors.Is(err, ErrSurveyQuestionPromptRequired) {
		t.Errorf("err=%v want ErrSurveyQuestionPromptRequired", err)
	}
	// The rejected update must not clobber the existing question set.
	if len(s.Questions) != 3 {
		t.Errorf("len(Questions)=%d want 3 (rejected update must not mutate)", len(s.Questions))
	}
}

func TestSurveyResponse_BlankQuestionIDRejected(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s, GCID: stLearner1,
		Answers: []Answer{{QuestionID: "  ", Value: "1"}},
	})
	if !errors.Is(err, ErrSurveyResponseUnknownQuestion) {
		t.Errorf("blank question_id: err=%v want ErrSurveyResponseUnknownQuestion", err)
	}
}
