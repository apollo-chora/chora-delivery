// Package survey holds the Survey + SurveyResponse aggregates for the R+
// training-feedback-survey surface (M15d). A Survey is authored by a
// training-admin against a Course, distributed to enrolled learners, then
// closed once responses are no longer being accepted.
//
// State machine:
//
//	DRAFT       → DISTRIBUTED via Distribute (training-admin)
//	DISTRIBUTED → CLOSED      via Close      (training-admin)
//
// SurveyResponse is a separate aggregate (one row per learner per survey):
// distributing a survey does NOT pre-create responses — responses are only
// inserted when a learner submits. The Survey aggregate tracks the set of
// gcids the survey was distributed to (`distributed_to`) + a running
// `response_count` for the admin dashboard. Both fields are mutated only
// through the aggregate methods to keep invariants centralised.
//
// Per ddd-enforcement.md aggregate invariants:
//   - All entities use UUIDv7 ids
//   - Soft delete via deleted_at (NOT a state transition; orthogonal)
//   - Cross-aggregate references are UUIDs only (CourseID, GCID, etc.)
//
// All state transitions are pure (no IO); persistence + event-emission
// concerns live in the http handler / repo layer.
package survey

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SurveyState models the Survey lifecycle FSM.
type SurveyState string

const (
	// SurveyStateDraft — admin is still authoring questions.
	SurveyStateDraft SurveyState = "DRAFT"
	// SurveyStateDistributed — survey has been pushed to the cohort and
	// is accepting responses.
	SurveyStateDistributed SurveyState = "DISTRIBUTED"
	// SurveyStateClosed — survey has been closed, no further responses
	// accepted. Existing responses remain readable.
	SurveyStateClosed SurveyState = "CLOSED"
)

// IsValid reports whether s is one of the canonical Survey states.
func (s SurveyState) IsValid() bool {
	switch s {
	case SurveyStateDraft, SurveyStateDistributed, SurveyStateClosed:
		return true
	}
	return false
}

// QuestionType enumerates the supported survey question shapes.
type QuestionType string

const (
	// QuestionTypeLikert — 1..5 numeric scale (FE renders radio buttons).
	QuestionTypeLikert QuestionType = "LIKERT"
	// QuestionTypeText — free-form text answer.
	QuestionTypeText QuestionType = "TEXT"
	// QuestionTypeMultipleChoice — single-pick from author-defined Options.
	QuestionTypeMultipleChoice QuestionType = "MULTIPLE_CHOICE"
)

// IsValid reports whether t is a supported question type.
func (t QuestionType) IsValid() bool {
	switch t {
	case QuestionTypeLikert, QuestionTypeText, QuestionTypeMultipleChoice:
		return true
	}
	return false
}

// Question is a single survey question authored against a Survey.
//
// QuestionID is stable across the Survey's lifecycle and is the value
// echoed back inside SurveyResponse.Answers so responses correlate to
// questions even if the author shuffles the array between drafts.
//
// Options applies only when Type == MULTIPLE_CHOICE.
type Question struct {
	QuestionID string
	Prompt     string
	Type       QuestionType
	Options    []string
}

// Survey aggregate root — owns its Questions + the distribution + response
// counter projection.
type Survey struct {
	ID            string
	TenantID      string
	CourseID      string
	Title         string
	Questions     []Question
	DistributedTo []string // gcids the survey was pushed to
	State         SurveyState
	DistributedAt *time.Time
	ClosedAt      *time.Time
	ResponseCount int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     *time.Time
}

// Survey domain errors.
var (
	// ErrSurveyTenantRequired — tenant_id is mandatory at construction.
	ErrSurveyTenantRequired = errors.New("survey: tenant_id required")
	// ErrSurveyCourseRequired — course_id is mandatory at construction.
	ErrSurveyCourseRequired = errors.New("survey: course_id required")
	// ErrSurveyTitleRequired — title trim-non-empty is mandatory.
	ErrSurveyTitleRequired = errors.New("survey: title required")
	// ErrSurveyQuestionsRequired — distribute requires ≥1 question.
	ErrSurveyQuestionsRequired = errors.New("survey: at least one question required")
	// ErrSurveyQuestionPromptRequired — every question needs a non-empty prompt.
	ErrSurveyQuestionPromptRequired = errors.New("survey: question prompt required")
	// ErrSurveyQuestionTypeInvalid — only LIKERT / TEXT / MULTIPLE_CHOICE.
	ErrSurveyQuestionTypeInvalid = errors.New("survey: invalid question type")
	// ErrSurveyMCQOptionsRequired — multiple_choice question must have ≥2 options.
	ErrSurveyMCQOptionsRequired = errors.New("survey: multiple_choice question requires ≥2 options")
	// ErrSurveyNotDraft — operation requires DRAFT state.
	ErrSurveyNotDraft = errors.New("survey: not in DRAFT state")
	// ErrSurveyNotDistributed — operation requires DISTRIBUTED state.
	ErrSurveyNotDistributed = errors.New("survey: not in DISTRIBUTED state")
	// ErrSurveyDistributeRecipientsRequired — distribute requires ≥1 gcid.
	ErrSurveyDistributeRecipientsRequired = errors.New("survey: at least one recipient gcid required at distribute")
)

// NewSurveyInput captures the constructor input for a DRAFT survey.
type NewSurveyInput struct {
	TenantID  string
	CourseID  string
	Title     string
	Questions []Question
}

// NewSurvey constructs a DRAFT-state Survey shell.
//
// Questions are NOT required at construction time — the admin can add /
// edit them via UpdateDraft before distributing. Any questions supplied
// here are validated for shape (prompt non-empty, type valid, MCQ options
// ≥2 when applicable) but the empty list is allowed at DRAFT.
//
// Each question is assigned a freshly generated UUIDv7 question_id when
// none is supplied so the FE can author + reorder without minting ids
// itself.
func NewSurvey(in NewSurveyInput) (*Survey, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, ErrSurveyTenantRequired
	}
	if strings.TrimSpace(in.CourseID) == "" {
		return nil, ErrSurveyCourseRequired
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, ErrSurveyTitleRequired
	}
	qs, err := normaliseQuestions(in.Questions)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &Survey{
		ID:            newUUIDv7(),
		TenantID:      in.TenantID,
		CourseID:      in.CourseID,
		Title:         strings.TrimSpace(in.Title),
		Questions:     qs,
		DistributedTo: []string{},
		State:         SurveyStateDraft,
		ResponseCount: 0,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// UpdateDraftInput captures partial-update fields for a DRAFT survey.
// Title nil → ignored; empty string → ErrSurveyTitleRequired.
// Questions nil → ignored; empty slice → clears (and Distribute will fail).
type UpdateDraftInput struct {
	Title     *string
	Questions []Question
}

// UpdateDraft mutates a DRAFT survey's content fields. Returns
// ErrSurveyNotDraft when the survey is not in DRAFT state.
func (s *Survey) UpdateDraft(in UpdateDraftInput) error {
	if s.State != SurveyStateDraft {
		return ErrSurveyNotDraft
	}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return ErrSurveyTitleRequired
		}
		s.Title = t
	}
	if in.Questions != nil {
		qs, err := normaliseQuestions(in.Questions)
		if err != nil {
			return err
		}
		s.Questions = qs
	}
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// SoftDelete soft-deletes the survey (sets DeletedAt). Idempotent: a
// second call updates the timestamp but does not error.
func (s *Survey) SoftDelete() {
	now := time.Now().UTC()
	s.DeletedAt = &now
	s.UpdatedAt = now
}

// Distribute transitions DRAFT → DISTRIBUTED. Validates ≥1 question +
// ≥1 distinct recipient gcid; records the recipient roster + timestamp.
func (s *Survey) Distribute(recipients []string) error {
	if s.State != SurveyStateDraft {
		return ErrSurveyNotDraft
	}
	if len(s.Questions) == 0 {
		return ErrSurveyQuestionsRequired
	}
	clean := dedupeNonEmpty(recipients)
	if len(clean) == 0 {
		return ErrSurveyDistributeRecipientsRequired
	}
	now := time.Now().UTC()
	s.State = SurveyStateDistributed
	s.DistributedTo = clean
	s.DistributedAt = &now
	s.UpdatedAt = now
	return nil
}

// Close transitions DISTRIBUTED → CLOSED. Records the close timestamp.
func (s *Survey) Close() error {
	if s.State != SurveyStateDistributed {
		return ErrSurveyNotDistributed
	}
	now := time.Now().UTC()
	s.State = SurveyStateClosed
	s.ClosedAt = &now
	s.UpdatedAt = now
	return nil
}

// CanAcceptResponse reports whether the survey is currently accepting
// learner responses. Only DISTRIBUTED surveys accept responses.
func (s *Survey) CanAcceptResponse() bool {
	return s.State == SurveyStateDistributed && s.DeletedAt == nil
}

// IncrementResponseCount bumps the running response_count by 1. The
// handler calls this after persisting a fresh SurveyResponse.
func (s *Survey) IncrementResponseCount() {
	s.ResponseCount++
	s.UpdatedAt = time.Now().UTC()
}

// QuestionByID returns the Question with matching QuestionID and a bool ok.
func (s *Survey) QuestionByID(qid string) (Question, bool) {
	for _, q := range s.Questions {
		if q.QuestionID == qid {
			return q, true
		}
	}
	return Question{}, false
}

// -----------------------------------------------------------------------------
// Internal helpers
// -----------------------------------------------------------------------------

// normaliseQuestions validates + back-fills question_id on every question.
// Returns a defensive copy so the aggregate's slice is decoupled from
// caller-supplied backing arrays.
func normaliseQuestions(in []Question) ([]Question, error) {
	if in == nil {
		return []Question{}, nil
	}
	out := make([]Question, 0, len(in))
	for _, q := range in {
		prompt := strings.TrimSpace(q.Prompt)
		if prompt == "" {
			return nil, ErrSurveyQuestionPromptRequired
		}
		if !q.Type.IsValid() {
			return nil, ErrSurveyQuestionTypeInvalid
		}
		opts := dedupeNonEmpty(q.Options)
		if q.Type == QuestionTypeMultipleChoice && len(opts) < 2 {
			return nil, ErrSurveyMCQOptionsRequired
		}
		qid := strings.TrimSpace(q.QuestionID)
		if qid == "" {
			qid = newUUIDv7()
		}
		out = append(out, Question{
			QuestionID: qid,
			Prompt:     prompt,
			Type:       q.Type,
			Options:    opts,
		})
	}
	return out, nil
}

// dedupeNonEmpty returns a defensive trimmed copy of src with empty +
// duplicate entries removed. Preserves first-seen order.
func dedupeNonEmpty(src []string) []string {
	if src == nil {
		return []string{}
	}
	seen := make(map[string]struct{}, len(src))
	out := make([]string, 0, len(src))
	for _, v := range src {
		t := strings.TrimSpace(v)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// newUUIDv7 mirrors delivery.NewUUIDv7 — kept local to avoid a circular
// import (the delivery package may add survey references in the future).
//
// RFC 9562 §5.7: unix_ts_ms (48 bits) || ver (4) || rand_a (12) || var
// (2) || rand_b (62).
func newUUIDv7() string {
	const buflen = 16
	var b [buflen]byte
	now := uint64(time.Now().UnixMilli())
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		for i := 6; i < buflen; i++ {
			b[i] = byte(now >> uint(8*(i-6)))
		}
	}
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
