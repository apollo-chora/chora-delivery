// survey_test.go — table-driven tests for the Survey + SurveyResponse
// aggregates. Coverage target: ≥85% domain per
// .claude/rules/development-execution.md.
//
// TDD: written FIRST and drove the implementation in survey.go +
// survey_response.go.
package survey

import (
	"errors"
	"testing"
	"time"
)

const (
	stTenantID  = "019e2f93-d586-71b5-8c3d-e2b0d0d50300"
	stCourseID  = "019e2f93-d586-71b5-8c3d-e2b0d0d50301"
	stAdminGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d50302"
	stLearner1  = "019e2f93-d586-71b5-8c3d-e2b0d0d50311"
	stLearner2  = "019e2f93-d586-71b5-8c3d-e2b0d0d50312"
)

func validQuestions() []Question {
	return []Question{
		{Prompt: "How was the pacing?", Type: QuestionTypeLikert},
		{Prompt: "Any final thoughts?", Type: QuestionTypeText},
		{
			Prompt:  "Which module was most useful?",
			Type:    QuestionTypeMultipleChoice,
			Options: []string{"Atoms", "LearningPaths", "Quizzes"},
		},
	}
}

// -----------------------------------------------------------------------------
// NewSurvey
// -----------------------------------------------------------------------------

func TestNewSurvey_OK_PopulatesDraft(t *testing.T) {
	t.Parallel()
	s, err := NewSurvey(NewSurveyInput{
		TenantID:  stTenantID,
		CourseID:  stCourseID,
		Title:     "Module 1 feedback",
		Questions: validQuestions(),
	})
	if err != nil {
		t.Fatalf("NewSurvey err=%v want nil", err)
	}
	if s.ID == "" {
		t.Errorf("ID empty; want UUIDv7")
	}
	if s.State != SurveyStateDraft {
		t.Errorf("State=%q want DRAFT", s.State)
	}
	if s.TenantID != stTenantID {
		t.Errorf("TenantID=%q want %q", s.TenantID, stTenantID)
	}
	if s.CourseID != stCourseID {
		t.Errorf("CourseID=%q want %q", s.CourseID, stCourseID)
	}
	if s.Title != "Module 1 feedback" {
		t.Errorf("Title=%q want trimmed", s.Title)
	}
	if len(s.Questions) != 3 {
		t.Fatalf("len(Questions)=%d want 3", len(s.Questions))
	}
	for _, q := range s.Questions {
		if q.QuestionID == "" {
			t.Errorf("question_id auto-fill missing: %+v", q)
		}
	}
	if s.ResponseCount != 0 {
		t.Errorf("ResponseCount=%d want 0", s.ResponseCount)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set")
	}
	if s.DistributedAt != nil || s.ClosedAt != nil || s.DeletedAt != nil {
		t.Errorf("nil-timestamps not nil at DRAFT")
	}
}

func TestNewSurvey_TrimsTitle(t *testing.T) {
	t.Parallel()
	s, err := NewSurvey(NewSurveyInput{
		TenantID:  stTenantID,
		CourseID:  stCourseID,
		Title:     "  Trim Me  ",
		Questions: nil,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if s.Title != "Trim Me" {
		t.Errorf("Title=%q want %q", s.Title, "Trim Me")
	}
}

func TestNewSurvey_AllowsEmptyQuestionsAtDraft(t *testing.T) {
	t.Parallel()
	s, err := NewSurvey(NewSurveyInput{
		TenantID:  stTenantID,
		CourseID:  stCourseID,
		Title:     "x",
		Questions: nil,
	})
	if err != nil {
		t.Fatalf("err=%v want nil (DRAFT allows 0 questions)", err)
	}
	if s.Questions == nil {
		t.Errorf("Questions nil; want empty non-nil slice for stable JSON")
	}
	if len(s.Questions) != 0 {
		t.Errorf("len(Questions)=%d want 0", len(s.Questions))
	}
}

func TestNewSurvey_PreservesProvidedQuestionID(t *testing.T) {
	t.Parallel()
	s, err := NewSurvey(NewSurveyInput{
		TenantID: stTenantID,
		CourseID: stCourseID,
		Title:    "x",
		Questions: []Question{
			{QuestionID: "qid-supplied", Prompt: "p", Type: QuestionTypeText},
		},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if s.Questions[0].QuestionID != "qid-supplied" {
		t.Errorf("question_id=%q want qid-supplied", s.Questions[0].QuestionID)
	}
}

func TestNewSurvey_RejectsBadInputs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   NewSurveyInput
		want error
	}{
		{
			"missing tenant",
			NewSurveyInput{CourseID: stCourseID, Title: "x"},
			ErrSurveyTenantRequired,
		},
		{
			"missing course",
			NewSurveyInput{TenantID: stTenantID, Title: "x"},
			ErrSurveyCourseRequired,
		},
		{
			"blank title",
			NewSurveyInput{TenantID: stTenantID, CourseID: stCourseID, Title: "   "},
			ErrSurveyTitleRequired,
		},
		{
			"question with empty prompt",
			NewSurveyInput{
				TenantID: stTenantID, CourseID: stCourseID, Title: "x",
				Questions: []Question{{Prompt: "  ", Type: QuestionTypeText}},
			},
			ErrSurveyQuestionPromptRequired,
		},
		{
			"question with invalid type",
			NewSurveyInput{
				TenantID: stTenantID, CourseID: stCourseID, Title: "x",
				Questions: []Question{{Prompt: "p", Type: QuestionType("UNKNOWN")}},
			},
			ErrSurveyQuestionTypeInvalid,
		},
		{
			"mcq with one option",
			NewSurveyInput{
				TenantID: stTenantID, CourseID: stCourseID, Title: "x",
				Questions: []Question{{
					Prompt: "p", Type: QuestionTypeMultipleChoice,
					Options: []string{"only"},
				}},
			},
			ErrSurveyMCQOptionsRequired,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewSurvey(tc.in)
			if !errors.Is(err, tc.want) {
				t.Errorf("err=%v want %v", err, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// UpdateDraft
// -----------------------------------------------------------------------------

func TestSurvey_UpdateDraft_AppliesPartial(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	newTitle := "Module 1 feedback (revised)"
	newQs := []Question{{Prompt: "Only one Q now", Type: QuestionTypeLikert}}
	if err := s.UpdateDraft(UpdateDraftInput{Title: &newTitle, Questions: newQs}); err != nil {
		t.Fatalf("UpdateDraft err=%v", err)
	}
	if s.Title != newTitle {
		t.Errorf("Title=%q want %q", s.Title, newTitle)
	}
	if len(s.Questions) != 1 {
		t.Errorf("len(Questions)=%d want 1", len(s.Questions))
	}
}

func TestSurvey_UpdateDraft_RejectsBlankTitle(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	blank := "   "
	if err := s.UpdateDraft(UpdateDraftInput{Title: &blank}); !errors.Is(err, ErrSurveyTitleRequired) {
		t.Errorf("err=%v want ErrSurveyTitleRequired", err)
	}
}

func TestSurvey_UpdateDraft_RejectsAfterDistribute(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	if err := s.Distribute([]string{stLearner1}); err != nil {
		t.Fatalf("Distribute err=%v", err)
	}
	if err := s.UpdateDraft(UpdateDraftInput{Title: strPtr("nope")}); !errors.Is(err, ErrSurveyNotDraft) {
		t.Errorf("err=%v want ErrSurveyNotDraft", err)
	}
}

// -----------------------------------------------------------------------------
// Distribute
// -----------------------------------------------------------------------------

func TestSurvey_Distribute_DraftToDistributed(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	if err := s.Distribute([]string{stLearner1, stLearner2}); err != nil {
		t.Fatalf("Distribute err=%v", err)
	}
	if s.State != SurveyStateDistributed {
		t.Errorf("State=%q want DISTRIBUTED", s.State)
	}
	if s.DistributedAt == nil {
		t.Errorf("DistributedAt nil")
	}
	if len(s.DistributedTo) != 2 {
		t.Errorf("len(DistributedTo)=%d want 2", len(s.DistributedTo))
	}
}

func TestSurvey_Distribute_DedupesRecipients(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	if err := s.Distribute([]string{stLearner1, " ", stLearner1, stLearner2}); err != nil {
		t.Fatalf("Distribute err=%v", err)
	}
	if len(s.DistributedTo) != 2 {
		t.Errorf("len(DistributedTo)=%d want 2 after dedupe", len(s.DistributedTo))
	}
}

func TestSurvey_Distribute_RejectsNoRecipients(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	if err := s.Distribute(nil); !errors.Is(err, ErrSurveyDistributeRecipientsRequired) {
		t.Errorf("err=%v want ErrSurveyDistributeRecipientsRequired", err)
	}
}

func TestSurvey_Distribute_RejectsNoQuestions(t *testing.T) {
	t.Parallel()
	s, _ := NewSurvey(NewSurveyInput{
		TenantID: stTenantID, CourseID: stCourseID, Title: "empty",
	})
	if err := s.Distribute([]string{stLearner1}); !errors.Is(err, ErrSurveyQuestionsRequired) {
		t.Errorf("err=%v want ErrSurveyQuestionsRequired", err)
	}
}

func TestSurvey_Distribute_RejectsTwice(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	if err := s.Distribute([]string{stLearner2}); !errors.Is(err, ErrSurveyNotDraft) {
		t.Errorf("err=%v want ErrSurveyNotDraft on second distribute", err)
	}
}

// -----------------------------------------------------------------------------
// Close
// -----------------------------------------------------------------------------

func TestSurvey_Close_DistributedToClosed(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	if err := s.Close(); err != nil {
		t.Fatalf("Close err=%v", err)
	}
	if s.State != SurveyStateClosed {
		t.Errorf("State=%q want CLOSED", s.State)
	}
	if s.ClosedAt == nil {
		t.Errorf("ClosedAt nil")
	}
}

func TestSurvey_Close_RejectsDraft(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	if err := s.Close(); !errors.Is(err, ErrSurveyNotDistributed) {
		t.Errorf("err=%v want ErrSurveyNotDistributed", err)
	}
}

func TestSurvey_CanAcceptResponse_OnlyWhenDistributedAndAlive(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	if s.CanAcceptResponse() {
		t.Errorf("DRAFT: CanAcceptResponse=true want false")
	}
	_ = s.Distribute([]string{stLearner1})
	if !s.CanAcceptResponse() {
		t.Errorf("DISTRIBUTED: CanAcceptResponse=false want true")
	}
	s.SoftDelete()
	if s.CanAcceptResponse() {
		t.Errorf("DELETED: CanAcceptResponse=true want false")
	}
}

func TestSurvey_IncrementResponseCount(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	before := s.UpdatedAt
	time.Sleep(2 * time.Millisecond)
	s.IncrementResponseCount()
	if s.ResponseCount != 1 {
		t.Errorf("ResponseCount=%d want 1", s.ResponseCount)
	}
	if !s.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt not bumped after increment")
	}
}

func TestSurvey_QuestionByID(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	first := s.Questions[0]
	got, ok := s.QuestionByID(first.QuestionID)
	if !ok || got.QuestionID != first.QuestionID {
		t.Errorf("QuestionByID hit miss: ok=%v got=%+v", ok, got)
	}
	if _, ok := s.QuestionByID("nope"); ok {
		t.Errorf("QuestionByID miss got hit")
	}
}

func TestSurvey_SoftDelete_SetsDeletedAt(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	s.SoftDelete()
	if s.DeletedAt == nil {
		t.Errorf("DeletedAt nil after SoftDelete")
	}
}

// -----------------------------------------------------------------------------
// NewSurveyResponse
// -----------------------------------------------------------------------------

func TestNewSurveyResponse_OK_OnDistributedSurvey(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})

	r, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s,
		GCID:   stLearner1,
		Answers: []Answer{
			{QuestionID: s.Questions[0].QuestionID, Value: "4"},
			{QuestionID: s.Questions[1].QuestionID, Value: "All good"},
			{QuestionID: s.Questions[2].QuestionID, Value: "Atoms"},
		},
	})
	if err != nil {
		t.Fatalf("NewSurveyResponse err=%v", err)
	}
	if r.ID == "" {
		t.Errorf("ID empty")
	}
	if r.SurveyID != s.ID {
		t.Errorf("SurveyID=%q want %q", r.SurveyID, s.ID)
	}
	if r.GCID != stLearner1 {
		t.Errorf("GCID=%q want %q", r.GCID, stLearner1)
	}
	if len(r.Answers) != 3 {
		t.Errorf("len(Answers)=%d want 3", len(r.Answers))
	}
	if r.SubmittedAt.IsZero() {
		t.Errorf("SubmittedAt zero")
	}
}

func TestNewSurveyResponse_RejectsBlankGCID(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s,
		GCID:   "   ",
		Answers: []Answer{
			{QuestionID: s.Questions[0].QuestionID, Value: "3"},
		},
	})
	if !errors.Is(err, ErrSurveyResponseGCIDRequired) {
		t.Errorf("err=%v want ErrSurveyResponseGCIDRequired", err)
	}
}

func TestNewSurveyResponse_RejectsNilSurvey(t *testing.T) {
	t.Parallel()
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		GCID:    stLearner1,
		Answers: []Answer{{QuestionID: "q1", Value: "x"}},
	})
	if !errors.Is(err, ErrSurveyResponseSurveyRequired) {
		t.Errorf("err=%v want ErrSurveyResponseSurveyRequired", err)
	}
}

func TestNewSurveyResponse_RejectsClosedSurvey(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	_ = s.Close()
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s, GCID: stLearner1,
		Answers: []Answer{{QuestionID: s.Questions[0].QuestionID, Value: "3"}},
	})
	if !errors.Is(err, ErrSurveyResponseSurveyNotOpen) {
		t.Errorf("err=%v want ErrSurveyResponseSurveyNotOpen", err)
	}
}

func TestNewSurveyResponse_RejectsDraftSurvey(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s, GCID: stLearner1,
		Answers: []Answer{{QuestionID: s.Questions[0].QuestionID, Value: "3"}},
	})
	if !errors.Is(err, ErrSurveyResponseSurveyNotOpen) {
		t.Errorf("err=%v want ErrSurveyResponseSurveyNotOpen", err)
	}
}

func TestNewSurveyResponse_RejectsZeroAnswers(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s, GCID: stLearner1, Answers: nil,
	})
	if !errors.Is(err, ErrSurveyResponseAnswersRequired) {
		t.Errorf("err=%v want ErrSurveyResponseAnswersRequired", err)
	}
}

func TestNewSurveyResponse_RejectsUnknownQuestion(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s, GCID: stLearner1,
		Answers: []Answer{{QuestionID: "nope", Value: "1"}},
	})
	if !errors.Is(err, ErrSurveyResponseUnknownQuestion) {
		t.Errorf("err=%v want ErrSurveyResponseUnknownQuestion", err)
	}
}

func TestNewSurveyResponse_LikertOutOfRange(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s, GCID: stLearner1,
		Answers: []Answer{{QuestionID: s.Questions[0].QuestionID, Value: "7"}},
	})
	if !errors.Is(err, ErrSurveyResponseLikertOutOfRange) {
		t.Errorf("err=%v want ErrSurveyResponseLikertOutOfRange", err)
	}
}

func TestNewSurveyResponse_MCQUnknownOption(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s, GCID: stLearner1,
		Answers: []Answer{{QuestionID: s.Questions[2].QuestionID, Value: "WrongChoice"}},
	})
	if !errors.Is(err, ErrSurveyResponseMCQUnknownOption) {
		t.Errorf("err=%v want ErrSurveyResponseMCQUnknownOption", err)
	}
}

func TestNewSurveyResponse_EmptyAnswerValueRejected(t *testing.T) {
	t.Parallel()
	s := mustNewSurvey(t)
	_ = s.Distribute([]string{stLearner1})
	_, err := NewSurveyResponse(NewSurveyResponseInput{
		Survey: s, GCID: stLearner1,
		Answers: []Answer{{QuestionID: s.Questions[1].QuestionID, Value: "  "}},
	})
	if !errors.Is(err, ErrSurveyResponseEmptyValue) {
		t.Errorf("err=%v want ErrSurveyResponseEmptyValue", err)
	}
}

// -----------------------------------------------------------------------------
// State + Type validity
// -----------------------------------------------------------------------------

func TestSurveyState_IsValid(t *testing.T) {
	t.Parallel()
	for _, s := range []SurveyState{SurveyStateDraft, SurveyStateDistributed, SurveyStateClosed} {
		if !s.IsValid() {
			t.Errorf("%q IsValid=false want true", s)
		}
	}
	if SurveyState("BOGUS").IsValid() {
		t.Errorf("BOGUS IsValid=true want false")
	}
}

func TestQuestionType_IsValid(t *testing.T) {
	t.Parallel()
	for _, q := range []QuestionType{
		QuestionTypeLikert, QuestionTypeText, QuestionTypeMultipleChoice,
	} {
		if !q.IsValid() {
			t.Errorf("%q IsValid=false want true", q)
		}
	}
	if QuestionType("WAT").IsValid() {
		t.Errorf("WAT IsValid=true want false")
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func mustNewSurvey(t *testing.T) *Survey {
	t.Helper()
	s, err := NewSurvey(NewSurveyInput{
		TenantID:  stTenantID,
		CourseID:  stCourseID,
		Title:     "Module 1 feedback",
		Questions: validQuestions(),
	})
	if err != nil {
		t.Fatalf("mustNewSurvey err=%v", err)
	}
	return s
}

func strPtr(s string) *string { return &s }
