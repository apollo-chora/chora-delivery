// survey_response.go — SurveyResponse aggregate. One row per learner per
// survey: the (SurveyID, GCID) tuple is unique within a tenant. Responses
// are append-only at the aggregate level (no edit-in-place); a second
// POST from the same learner is rejected with ErrSurveyResponseDuplicate.
//
// Per ddd-enforcement.md aggregate invariants:
//   - UUIDv7 id
//   - Cross-aggregate references are UUIDs only (SurveyID, GCID)
//   - Append-only: no UpdateAnswers method; resubmission rejected
package survey

import (
	"errors"
	"strings"
	"time"
)

// Answer is a single learner answer to a single survey Question.
//
// QuestionID matches the Survey's Question.QuestionID so the response can
// be correlated even if the Survey's questions array is reordered post-
// distribute. Value is the raw learner input:
//   - LIKERT          : "1".."5" (string form for protocol stability)
//   - TEXT            : free-form learner text
//   - MULTIPLE_CHOICE : one of the question's Options entries
//
// The aggregate enforces the QuestionID's existence on the parent Survey
// + the LIKERT/MCQ range checks; the handler does not.
type Answer struct {
	QuestionID string
	Value      string
}

// SurveyResponse aggregate root — one row per (SurveyID, GCID).
type SurveyResponse struct {
	ID          string
	SurveyID    string
	GCID        string
	Answers     []Answer
	SubmittedAt time.Time
}

// SurveyResponse domain errors.
var (
	// ErrSurveyResponseSurveyRequired — survey_id is mandatory.
	ErrSurveyResponseSurveyRequired = errors.New("survey_response: survey_id required")
	// ErrSurveyResponseGCIDRequired — gcid is mandatory.
	ErrSurveyResponseGCIDRequired = errors.New("survey_response: gcid required")
	// ErrSurveyResponseSurveyNotOpen — survey must be DISTRIBUTED.
	ErrSurveyResponseSurveyNotOpen = errors.New("survey_response: survey is not accepting responses (must be DISTRIBUTED)")
	// ErrSurveyResponseAnswersRequired — at least one answer required.
	ErrSurveyResponseAnswersRequired = errors.New("survey_response: at least one answer required")
	// ErrSurveyResponseUnknownQuestion — answer references a question not on the survey.
	ErrSurveyResponseUnknownQuestion = errors.New("survey_response: answer references unknown question")
	// ErrSurveyResponseEmptyValue — answer value cannot be empty/blank.
	ErrSurveyResponseEmptyValue = errors.New("survey_response: answer value required")
	// ErrSurveyResponseLikertOutOfRange — LIKERT answer must be "1".."5".
	ErrSurveyResponseLikertOutOfRange = errors.New("survey_response: LIKERT answer must be 1..5")
	// ErrSurveyResponseMCQUnknownOption — MCQ answer value not in question Options.
	ErrSurveyResponseMCQUnknownOption = errors.New("survey_response: MULTIPLE_CHOICE answer not in options")
	// ErrSurveyResponseDuplicate — learner has already responded to this survey.
	ErrSurveyResponseDuplicate = errors.New("survey_response: learner has already responded")
)

// NewSurveyResponseInput captures the constructor input.
type NewSurveyResponseInput struct {
	Survey  *Survey
	GCID    string
	Answers []Answer
}

// NewSurveyResponse constructs a SurveyResponse, validating that:
//   - the parent Survey is currently accepting responses (DISTRIBUTED)
//   - the learner gcid is non-empty
//   - every Answer references a Question that exists on the Survey
//   - LIKERT values are "1".."5"
//   - MULTIPLE_CHOICE values appear in the Question's Options
//   - every Answer value is non-blank
//
// The handler is responsible for the cross-aggregate duplicate check
// (NewSurveyResponse cannot see the repo). Pass a duplicate-detection
// result to the handler so it can reject with ErrSurveyResponseDuplicate.
func NewSurveyResponse(in NewSurveyResponseInput) (*SurveyResponse, error) {
	if in.Survey == nil {
		return nil, ErrSurveyResponseSurveyRequired
	}
	if strings.TrimSpace(in.GCID) == "" {
		return nil, ErrSurveyResponseGCIDRequired
	}
	if !in.Survey.CanAcceptResponse() {
		return nil, ErrSurveyResponseSurveyNotOpen
	}
	if len(in.Answers) == 0 {
		return nil, ErrSurveyResponseAnswersRequired
	}
	cleaned, err := validateAnswers(in.Survey, in.Answers)
	if err != nil {
		return nil, err
	}
	return &SurveyResponse{
		ID:          newUUIDv7(),
		SurveyID:    in.Survey.ID,
		GCID:        strings.TrimSpace(in.GCID),
		Answers:     cleaned,
		SubmittedAt: time.Now().UTC(),
	}, nil
}

// validateAnswers walks each Answer + checks it against the parent survey.
// Returns a defensive copy of the answers slice with values trimmed.
func validateAnswers(s *Survey, in []Answer) ([]Answer, error) {
	out := make([]Answer, 0, len(in))
	for _, a := range in {
		qid := strings.TrimSpace(a.QuestionID)
		if qid == "" {
			return nil, ErrSurveyResponseUnknownQuestion
		}
		q, ok := s.QuestionByID(qid)
		if !ok {
			return nil, ErrSurveyResponseUnknownQuestion
		}
		val := strings.TrimSpace(a.Value)
		if val == "" {
			return nil, ErrSurveyResponseEmptyValue
		}
		switch q.Type {
		case QuestionTypeLikert:
			if !isLikert1to5(val) {
				return nil, ErrSurveyResponseLikertOutOfRange
			}
		case QuestionTypeMultipleChoice:
			if !containsString(q.Options, val) {
				return nil, ErrSurveyResponseMCQUnknownOption
			}
		case QuestionTypeText:
			// no extra value-shape check beyond non-blank
		}
		out = append(out, Answer{QuestionID: qid, Value: val})
	}
	return out, nil
}

// isLikert1to5 reports whether s is exactly "1", "2", "3", "4", or "5".
func isLikert1to5(s string) bool {
	switch s {
	case "1", "2", "3", "4", "5":
		return true
	}
	return false
}

// containsString reports whether needle is in haystack.
func containsString(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
