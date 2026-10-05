// Package classroom contains the R+ (Rhythm+) classroom-realtime aggregates
// for the Content Delivery domain — LiveQuiz, LiveQuizSession, and LivePoll.
//
// These are distinct from `mid_class_quiz` (legacy, single-state) and from
// the assessments stack (chora-delivery/internal/domain/delivery/assessment.go,
// async + cohort-scoped). The classroom-realtime trio supports the M8/M9/M10
// classroom presenter UX:
//
//   - LiveQuiz aggregate (DRAFT → PUBLISHED → ARMED → LIVE → CLOSED) holds
//     the authored quiz template (questions + options + timers + points).
//   - LiveQuizSession aggregate (ARMED → LIVE → CLOSED) is created per
//     "running" instance of a published quiz — captures per-(learner,question)
//     responses with first-write-wins idempotency.
//   - LivePoll aggregate (DRAFT → OPEN → CLOSED) is a simpler multi-choice
//     poll with per-option vote counts (no per-question concept).
//
// Aggregate invariants per `.claude/rules/ddd-enforcement.md`:
//   - Soft-delete: classroom-realtime aggregates are ephemeral (per-class
//     session); they ARE kept post-CLOSED for analytics but the M14 cleanup
//     job hard-deletes after 30 days. No deleted_at column needed in v1.
//   - Cross-domain refs: course_id is a UUID with NO foreign-key constraint
//     (course is in the same DB but the classroom slice is independent).
//   - UUIDv7 ids generated via the package-local NewUUIDv7() (matches the
//     pattern in domain/classroom/mid_class_quiz/quiz.go).
package classroom

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// LiveQuiz state machine
// -----------------------------------------------------------------------------

// LiveQuizState models the LiveQuiz lifecycle FSM per the R+ classroom-realtime
// plan: DRAFT → PUBLISHED → ARMED → LIVE → CLOSED.
//
// Per spec each transition is one-way; ARMED → LIVE → CLOSED happens on the
// LiveQuizSession aggregate, but we mirror the state on the parent LiveQuiz so
// the FE quiz-builder + presenter surfaces see a unified state.
type LiveQuizState string

const (
	LiveQuizStateDraft     LiveQuizState = "DRAFT"
	LiveQuizStatePublished LiveQuizState = "PUBLISHED"
	LiveQuizStateArmed     LiveQuizState = "ARMED"
	LiveQuizStateLive      LiveQuizState = "LIVE"
	LiveQuizStateClosed    LiveQuizState = "CLOSED"
)

// IsValid reports whether s is one of the canonical FSM states.
func (s LiveQuizState) IsValid() bool {
	switch s {
	case LiveQuizStateDraft, LiveQuizStatePublished,
		LiveQuizStateArmed, LiveQuizStateLive, LiveQuizStateClosed:
		return true
	}
	return false
}

// ExplainerMode controls WHEN per-option explainers are revealed to learners
// during a live session (ADR-168 Live Classroom authoring). The explainer text
// itself lives on each LiveQuizOption.Explainer.
type ExplainerMode string

const (
	// ExplainerModeNever — explainers are never shown (instructor-only review).
	ExplainerModeNever ExplainerMode = "NEVER"
	// ExplainerModeImmediate — reveal the correct option + explainer right
	// after each learner submits (default; classic live-stage feel).
	ExplainerModeImmediate ExplainerMode = "IMMEDIATE"
	// ExplainerModeEndOfQuestion — reveal when the instructor advances past
	// the question (everyone sees it together).
	ExplainerModeEndOfQuestion ExplainerMode = "END_OF_QUESTION"
	// ExplainerModeEndOfSession — reveal all explainers only after CLOSE.
	ExplainerModeEndOfSession ExplainerMode = "END_OF_SESSION"
)

// IsValid reports whether m is a canonical explainer-reveal mode.
func (m ExplainerMode) IsValid() bool {
	switch m {
	case ExplainerModeNever, ExplainerModeImmediate,
		ExplainerModeEndOfQuestion, ExplainerModeEndOfSession:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	ErrLiveQuizTenantRequired     = errors.New("classroom: tenant_id required")
	ErrLiveQuizInstructorRequired = errors.New("classroom: instructor_gcid required")
	ErrLiveQuizTitleRequired      = errors.New("classroom: title required")
	ErrLiveQuizNotDraft           = errors.New("classroom: live quiz not in DRAFT state")
	ErrLiveQuizNoQuestions        = errors.New("classroom: live quiz needs at least one question to publish")
	ErrLiveQuizInvalidQuestion    = errors.New("classroom: invalid question")
	ErrLiveQuizCannotPublish      = errors.New("classroom: live quiz not publishable from current state")
	ErrLiveQuizInvalidExplainer   = errors.New("classroom: invalid explainer_mode")
)

// -----------------------------------------------------------------------------
// Question + Option value objects
// -----------------------------------------------------------------------------

// LiveQuizOption is one multiple-choice option attached to a LiveQuizQuestion.
type LiveQuizOption struct {
	// Label is the answer-content text the author typed (per
	// `feedback_mcq_option_naming`: this is the answer-choice content, not
	// the positional marker A/B/C/D — the marker is computed at render time).
	Label     string `json:"label"`
	IsCorrect bool   `json:"is_correct"`
	// Explainer is the post-reveal explanation for this option (ADR-168).
	// Optional; shown per the parent quiz's ExplainerMode. This is the
	// `explainer` concept from `feedback_mcq_option_naming` — distinct from
	// the answer-content Label.
	Explainer string `json:"explainer,omitempty"`
}

// LiveQuizQuestion is one question within a LiveQuiz template.
type LiveQuizQuestion struct {
	QuestionID string           `json:"question_id"`
	Prompt     string           `json:"prompt"`
	Options    []LiveQuizOption `json:"options"`
	// TimerSecs is the per-question countdown. 0 means "use quiz default" —
	// FE clamps to MinTimerSecs..MaxTimerSecs at edit-time.
	TimerSecs int `json:"timer_seconds"`
	Points    int `json:"points"`
	// DoublePoints marks this question as a ×2 round (ADR-179 ruling 4): the
	// composed award doubles the time-decayed base BEFORE the streak bonus.
	// Authored via the quiz-builder per-question toggle.
	DoublePoints bool `json:"double_points,omitempty"`
	// AtomID optionally references the source LearningAtom (chora_creation)
	// this question was composed from (ADR-168 atom-centric composition).
	// UUID with NO foreign-key constraint per ddd-enforcement — content is
	// snapshotted inline at compose/publish time; AtomID is provenance only.
	AtomID string `json:"atom_id,omitempty"`
	// TopicTags are the linked atom's topic tags, denormalised onto the
	// question at compose time (the FE atom-picker supplies them) so the
	// durable score_awarded can carry them to chora-consumption's derived-
	// weakness subscriber WITHOUT a cross-DB read into chora_creation (CR2-C3).
	TopicTags []string `json:"topic_tags,omitempty"`
}

// validateQuestion enforces invariants per the FE quiz-builder rules:
//   - prompt required
//   - >=2 options
//   - exactly 1 IsCorrect=true
func validateQuestion(q LiveQuizQuestion) error {
	if strings.TrimSpace(q.Prompt) == "" {
		return fmt.Errorf("%w: prompt required", ErrLiveQuizInvalidQuestion)
	}
	if len(q.Options) < 2 {
		return fmt.Errorf("%w: need at least 2 options", ErrLiveQuizInvalidQuestion)
	}
	correct := 0
	for _, opt := range q.Options {
		if strings.TrimSpace(opt.Label) == "" {
			return fmt.Errorf("%w: option label required", ErrLiveQuizInvalidQuestion)
		}
		if opt.IsCorrect {
			correct++
		}
	}
	if correct != 1 {
		return fmt.Errorf("%w: exactly one option must be marked correct (got %d)", ErrLiveQuizInvalidQuestion, correct)
	}
	return nil
}

// -----------------------------------------------------------------------------
// LiveQuiz aggregate root
// -----------------------------------------------------------------------------

// LiveQuiz is the R+ classroom-realtime quiz template aggregate.
type LiveQuiz struct {
	ID             string             `json:"id"`
	TenantID       string             `json:"tenant_id"`
	CourseID       string             `json:"course_id,omitempty"`
	InstructorGCID string             `json:"instructor_gcid"`
	Title          string             `json:"title"`
	Questions      []LiveQuizQuestion `json:"questions"`
	State          LiveQuizState      `json:"state"`
	// QuizTimeLimitSecs is an optional overall session time-box (ADR-168).
	// 0 means "no quiz-wide limit" — per-question TimerSecs still applies.
	QuizTimeLimitSecs int `json:"quiz_time_limit_seconds,omitempty"`
	// ExplainerMode governs per-option explainer reveal timing (ADR-168).
	ExplainerMode ExplainerMode `json:"explainer_mode"`
	PublishedAt   *time.Time    `json:"published_at,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// NewLiveQuizInput is the constructor payload.
type NewLiveQuizInput struct {
	TenantID       string
	CourseID       string
	InstructorGCID string
	Title          string
	// QuizTimeLimitSecs + ExplainerMode are optional. ExplainerMode defaults
	// to IMMEDIATE when blank; a non-blank invalid value is rejected.
	QuizTimeLimitSecs int
	ExplainerMode     ExplainerMode
}

// NewLiveQuiz constructs a DRAFT LiveQuiz with full invariant checks.
func NewLiveQuiz(in NewLiveQuizInput) (*LiveQuiz, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, ErrLiveQuizTenantRequired
	}
	if strings.TrimSpace(in.InstructorGCID) == "" {
		return nil, ErrLiveQuizInstructorRequired
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, ErrLiveQuizTitleRequired
	}
	mode := in.ExplainerMode
	if mode == "" {
		mode = ExplainerModeImmediate
	}
	if !mode.IsValid() {
		return nil, ErrLiveQuizInvalidExplainer
	}
	now := time.Now().UTC()
	return &LiveQuiz{
		ID:                NewUUIDv7(),
		TenantID:          in.TenantID,
		CourseID:          strings.TrimSpace(in.CourseID),
		InstructorGCID:    in.InstructorGCID,
		Title:             strings.TrimSpace(in.Title),
		Questions:         []LiveQuizQuestion{},
		State:             LiveQuizStateDraft,
		QuizTimeLimitSecs: in.QuizTimeLimitSecs,
		ExplainerMode:     mode,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// ConfigureAuthoring sets the per-quiz time-limit + explainer-reveal mode while
// in DRAFT (ADR-168). Returns ErrLiveQuizNotDraft past DRAFT and
// ErrLiveQuizInvalidExplainer for an unknown mode.
func (q *LiveQuiz) ConfigureAuthoring(quizTimeLimitSecs int, mode ExplainerMode) error {
	if q.State != LiveQuizStateDraft {
		return ErrLiveQuizNotDraft
	}
	if !mode.IsValid() {
		return ErrLiveQuizInvalidExplainer
	}
	q.QuizTimeLimitSecs = quizTimeLimitSecs
	q.ExplainerMode = mode
	q.UpdatedAt = time.Now().UTC()
	return nil
}

// AddQuestion appends a question to a DRAFT quiz. Returns
// ErrLiveQuizNotDraft when the quiz has moved past DRAFT.
func (q *LiveQuiz) AddQuestion(question LiveQuizQuestion) error {
	if q.State != LiveQuizStateDraft {
		return ErrLiveQuizNotDraft
	}
	if err := validateQuestion(question); err != nil {
		return err
	}
	q.Questions = append(q.Questions, question)
	q.UpdatedAt = time.Now().UTC()
	return nil
}

// EditDraft replaces the title + question list while in DRAFT state. Returns
// ErrLiveQuizNotDraft when state ≠ DRAFT.
func (q *LiveQuiz) EditDraft(title string, questions []LiveQuizQuestion) error {
	if q.State != LiveQuizStateDraft {
		return ErrLiveQuizNotDraft
	}
	if strings.TrimSpace(title) == "" {
		return ErrLiveQuizTitleRequired
	}
	for _, qst := range questions {
		if err := validateQuestion(qst); err != nil {
			return err
		}
	}
	q.Title = strings.TrimSpace(title)
	q.Questions = append([]LiveQuizQuestion(nil), questions...)
	q.UpdatedAt = time.Now().UTC()
	return nil
}

// Publish transitions DRAFT → PUBLISHED. Idempotent if already PUBLISHED.
//
// Returns:
//   - nil on success (and when re-publishing an already-PUBLISHED quiz).
//   - ErrLiveQuizNoQuestions when Questions is empty.
//   - ErrLiveQuizCannotPublish when state is past PUBLISHED.
func (q *LiveQuiz) Publish(now time.Time) error {
	if q.State == LiveQuizStatePublished {
		return nil // idempotent
	}
	if q.State != LiveQuizStateDraft {
		return ErrLiveQuizCannotPublish
	}
	if len(q.Questions) == 0 {
		return ErrLiveQuizNoQuestions
	}
	q.State = LiveQuizStatePublished
	pa := now.UTC()
	q.PublishedAt = &pa
	q.UpdatedAt = pa
	return nil
}

// CanArm reports whether the quiz can transition to ARMED — only PUBLISHED
// quizzes accept session creation.
func (q *LiveQuiz) CanArm() bool {
	return q.State == LiveQuizStatePublished
}

// MarkArmed flips state to ARMED. Internal lifecycle helper called by the
// repo when a LiveQuizSession is created.
func (q *LiveQuiz) MarkArmed(now time.Time) {
	if q.State == LiveQuizStatePublished {
		q.State = LiveQuizStateArmed
		q.UpdatedAt = now.UTC()
	}
}

// MarkLive flips state to LIVE. Internal lifecycle helper.
func (q *LiveQuiz) MarkLive(now time.Time) {
	if q.State == LiveQuizStateArmed {
		q.State = LiveQuizStateLive
		q.UpdatedAt = now.UTC()
	}
}

// MarkClosed flips state to CLOSED. Internal lifecycle helper.
func (q *LiveQuiz) MarkClosed(now time.Time) {
	if q.State == LiveQuizStateLive || q.State == LiveQuizStateArmed {
		q.State = LiveQuizStateClosed
		q.UpdatedAt = now.UTC()
	}
}

// -----------------------------------------------------------------------------
// UUIDv7 — package-local generator (matches domain/classroom/mid_class_quiz)
// -----------------------------------------------------------------------------

// NewUUIDv7 returns a freshly generated UUIDv7 string. RFC 9562 §5.7 layout.
func NewUUIDv7() string {
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
