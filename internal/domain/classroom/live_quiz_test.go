// live_quiz_test.go — RED-phase TDD coverage for the LiveQuiz aggregate
// (R+ M8/M9/M10/M11 classroom-realtime bundle).
//
// Per `feedback_strict_tdd`: written BEFORE live_quiz.go exists; tests MUST
// fail at compile time until the implementation lands (GREEN phase).
package classroom

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NewLiveQuiz — input validation
// ---------------------------------------------------------------------------

func TestNewLiveQuiz_TenantRequired(t *testing.T) {
	_, err := NewLiveQuiz(NewLiveQuizInput{
		TenantID:       "",
		InstructorGCID: "11111111-1111-7111-8111-111111111111",
		Title:          "Sprint Planning",
	})
	if err == nil || err != ErrLiveQuizTenantRequired {
		t.Fatalf("want ErrLiveQuizTenantRequired; got %v", err)
	}
}

func TestNewLiveQuiz_InstructorRequired(t *testing.T) {
	_, err := NewLiveQuiz(NewLiveQuizInput{
		TenantID:       "00000000-0000-7000-8000-000000000000",
		InstructorGCID: "",
		Title:          "Sprint Planning",
	})
	if err == nil || err != ErrLiveQuizInstructorRequired {
		t.Fatalf("want ErrLiveQuizInstructorRequired; got %v", err)
	}
}

func TestNewLiveQuiz_TitleRequired(t *testing.T) {
	_, err := NewLiveQuiz(NewLiveQuizInput{
		TenantID:       "00000000-0000-7000-8000-000000000000",
		InstructorGCID: "11111111-1111-7111-8111-111111111111",
		Title:          "  ",
	})
	if err == nil || err != ErrLiveQuizTitleRequired {
		t.Fatalf("want ErrLiveQuizTitleRequired; got %v", err)
	}
}

func TestNewLiveQuiz_OK_StartsAsDraft(t *testing.T) {
	q, err := NewLiveQuiz(NewLiveQuizInput{
		TenantID:       "00000000-0000-7000-8000-000000000000",
		InstructorGCID: "11111111-1111-7111-8111-111111111111",
		Title:          "Sprint Planning",
		CourseID:       "course-cspo",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if q.State != LiveQuizStateDraft {
		t.Fatalf("want DRAFT; got %s", q.State)
	}
	if q.ID == "" {
		t.Fatalf("want non-empty UUIDv7 id")
	}
	if q.CreatedAt.IsZero() {
		t.Fatalf("want CreatedAt set")
	}
	if len(q.Questions) != 0 {
		t.Fatalf("want 0 questions on a fresh draft; got %d", len(q.Questions))
	}
}

// ---------------------------------------------------------------------------
// AddQuestion — edits only allowed in DRAFT
// ---------------------------------------------------------------------------

func TestAddQuestion_OnlyInDraft(t *testing.T) {
	q := mustNewLiveQuiz(t)
	if err := q.AddQuestion(makeValidQuestion("q1")); err != nil {
		t.Fatalf("AddQuestion DRAFT err: %v", err)
	}
	// flip to PUBLISHED
	if err := q.Publish(time.Now()); err != nil {
		t.Fatalf("Publish err: %v", err)
	}
	if err := q.AddQuestion(makeValidQuestion("q2")); err == nil {
		t.Fatalf("want error when adding question to PUBLISHED quiz; got nil")
	}
}

func TestAddQuestion_PromptRequired(t *testing.T) {
	q := mustNewLiveQuiz(t)
	err := q.AddQuestion(LiveQuizQuestion{
		QuestionID: "q1",
		Prompt:     "",
		Options:    []LiveQuizOption{{Label: "A", IsCorrect: true}, {Label: "B"}},
	})
	if err == nil {
		t.Fatalf("want prompt-required error; got nil")
	}
}

func TestAddQuestion_AtLeastTwoOptions(t *testing.T) {
	q := mustNewLiveQuiz(t)
	err := q.AddQuestion(LiveQuizQuestion{
		QuestionID: "q1",
		Prompt:     "Which?",
		Options:    []LiveQuizOption{{Label: "A", IsCorrect: true}},
	})
	if err == nil {
		t.Fatalf("want >=2 options error; got nil")
	}
}

func TestAddQuestion_ExactlyOneCorrect(t *testing.T) {
	q := mustNewLiveQuiz(t)
	err := q.AddQuestion(LiveQuizQuestion{
		QuestionID: "q1",
		Prompt:     "Which?",
		Options: []LiveQuizOption{
			{Label: "A", IsCorrect: false},
			{Label: "B", IsCorrect: false},
		},
	})
	if err == nil {
		t.Fatalf("want exactly-one-correct error when zero options correct; got nil")
	}
}

// ---------------------------------------------------------------------------
// Publish — DRAFT → PUBLISHED
// ---------------------------------------------------------------------------

func TestPublish_NeedsQuestions(t *testing.T) {
	q := mustNewLiveQuiz(t)
	if err := q.Publish(time.Now()); err == nil {
		t.Fatalf("want publish-needs-questions error; got nil")
	}
}

func TestPublish_DraftToPublished(t *testing.T) {
	q := mustNewLiveQuiz(t)
	_ = q.AddQuestion(makeValidQuestion("q1"))
	now := time.Now()
	if err := q.Publish(now); err != nil {
		t.Fatalf("Publish err: %v", err)
	}
	if q.State != LiveQuizStatePublished {
		t.Fatalf("want PUBLISHED; got %s", q.State)
	}
	if q.PublishedAt == nil || !q.PublishedAt.Equal(now.UTC()) {
		t.Fatalf("want PublishedAt set to now")
	}
}

func TestPublish_Idempotent(t *testing.T) {
	q := mustNewLiveQuiz(t)
	_ = q.AddQuestion(makeValidQuestion("q1"))
	now := time.Now()
	_ = q.Publish(now)
	first := *q.PublishedAt
	if err := q.Publish(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("second Publish (idempotent) err: %v", err)
	}
	if !q.PublishedAt.Equal(first) {
		t.Fatalf("PublishedAt should not change on idempotent re-publish")
	}
}

func TestPublish_FromInvalidState(t *testing.T) {
	q := mustNewLiveQuiz(t)
	_ = q.AddQuestion(makeValidQuestion("q1"))
	_ = q.Publish(time.Now())
	// Cannot re-publish from ARMED via plain Publish().
	q.State = LiveQuizStateArmed
	if err := q.Publish(time.Now()); err == nil {
		t.Fatalf("want error publishing from ARMED state; got nil")
	}
}

// ---------------------------------------------------------------------------
// CanArm — used by sessions repo gate
// ---------------------------------------------------------------------------

func TestCanArm_OnlyFromPublished(t *testing.T) {
	q := mustNewLiveQuiz(t)
	_ = q.AddQuestion(makeValidQuestion("q1"))
	if q.CanArm() {
		t.Fatalf("DRAFT must NOT be arm-able")
	}
	_ = q.Publish(time.Now())
	if !q.CanArm() {
		t.Fatalf("PUBLISHED must be arm-able")
	}
}

// ---------------------------------------------------------------------------
// EditDraft — title + question replacement only in DRAFT
// ---------------------------------------------------------------------------

func TestEditDraft_TitleAndQuestions(t *testing.T) {
	q := mustNewLiveQuiz(t)
	_ = q.AddQuestion(makeValidQuestion("q1"))
	if err := q.EditDraft("New Title", []LiveQuizQuestion{makeValidQuestion("q2"), makeValidQuestion("q3")}); err != nil {
		t.Fatalf("EditDraft err: %v", err)
	}
	if q.Title != "New Title" {
		t.Fatalf("want title updated; got %s", q.Title)
	}
	if len(q.Questions) != 2 {
		t.Fatalf("want 2 questions after edit; got %d", len(q.Questions))
	}
	if q.Questions[0].QuestionID != "q2" {
		t.Fatalf("want first question q2; got %s", q.Questions[0].QuestionID)
	}
}

func TestEditDraft_RejectedAfterPublish(t *testing.T) {
	q := mustNewLiveQuiz(t)
	_ = q.AddQuestion(makeValidQuestion("q1"))
	_ = q.Publish(time.Now())
	if err := q.EditDraft("X", []LiveQuizQuestion{makeValidQuestion("q2")}); err == nil {
		t.Fatalf("want EditDraft to reject after publish; got nil")
	}
}

// ---------------------------------------------------------------------------
// Live Classroom deltas — ADR-168 (explainer reveal, atom composition,
// per-quiz time-limit). RED-phase: written before live_quiz.go carries them.
// ---------------------------------------------------------------------------

func TestNewLiveQuiz_DefaultsExplainerImmediate(t *testing.T) {
	q := mustNewLiveQuiz(t)
	if q.ExplainerMode != ExplainerModeImmediate {
		t.Fatalf("want default ExplainerMode IMMEDIATE; got %q", q.ExplainerMode)
	}
}

func TestNewLiveQuiz_AcceptsAuthoringOptions(t *testing.T) {
	q, err := NewLiveQuiz(NewLiveQuizInput{
		TenantID:          "00000000-0000-7000-8000-000000000000",
		InstructorGCID:    "11111111-1111-7111-8111-111111111111",
		Title:             "Sprint Planning",
		QuizTimeLimitSecs: 600,
		ExplainerMode:     ExplainerModeEndOfSession,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if q.QuizTimeLimitSecs != 600 {
		t.Fatalf("want QuizTimeLimitSecs 600; got %d", q.QuizTimeLimitSecs)
	}
	if q.ExplainerMode != ExplainerModeEndOfSession {
		t.Fatalf("want END_OF_SESSION; got %q", q.ExplainerMode)
	}
}

func TestNewLiveQuiz_RejectsInvalidExplainerMode(t *testing.T) {
	_, err := NewLiveQuiz(NewLiveQuizInput{
		TenantID:       "00000000-0000-7000-8000-000000000000",
		InstructorGCID: "11111111-1111-7111-8111-111111111111",
		Title:          "T",
		ExplainerMode:  ExplainerMode("BOGUS"),
	})
	if err == nil {
		t.Fatalf("want error for invalid ExplainerMode; got nil")
	}
}

func TestConfigureAuthoring_DraftOnly(t *testing.T) {
	q := mustNewLiveQuiz(t)
	if err := q.ConfigureAuthoring(300, ExplainerModeEndOfQuestion); err != nil {
		t.Fatalf("ConfigureAuthoring DRAFT err: %v", err)
	}
	if q.QuizTimeLimitSecs != 300 || q.ExplainerMode != ExplainerModeEndOfQuestion {
		t.Fatalf("authoring options not applied: %d %q", q.QuizTimeLimitSecs, q.ExplainerMode)
	}
	if err := q.ConfigureAuthoring(10, ExplainerMode("NOPE")); err == nil {
		t.Fatalf("want invalid-mode error; got nil")
	}
	_ = q.AddQuestion(makeValidQuestion("q1"))
	_ = q.Publish(time.Now())
	if err := q.ConfigureAuthoring(10, ExplainerModeNever); err == nil {
		t.Fatalf("want ConfigureAuthoring rejected after publish; got nil")
	}
}

func TestQuestion_CarriesAtomIDAndOptionExplainer(t *testing.T) {
	q := mustNewLiveQuiz(t)
	question := LiveQuizQuestion{
		QuestionID: "q-atom",
		AtomID:     "aaaaaaaa-0000-7000-8000-000000000001",
		Prompt:     "Composed from a LearningAtom?",
		TimerSecs:  30,
		Points:     1000,
		Options: []LiveQuizOption{
			{Label: "Yes", IsCorrect: true, Explainer: "Atoms are the aggregate root."},
			{Label: "No", IsCorrect: false, Explainer: "Collections only query atoms."},
		},
	}
	if err := q.AddQuestion(question); err != nil {
		t.Fatalf("AddQuestion err: %v", err)
	}
	got := q.Questions[0]
	if got.AtomID != question.AtomID {
		t.Fatalf("AtomID not preserved; got %q", got.AtomID)
	}
	if got.Options[0].Explainer == "" {
		t.Fatalf("option Explainer not preserved")
	}
}

func TestExplainerMode_IsValid(t *testing.T) {
	for _, m := range []ExplainerMode{
		ExplainerModeNever, ExplainerModeImmediate,
		ExplainerModeEndOfQuestion, ExplainerModeEndOfSession,
	} {
		if !m.IsValid() {
			t.Fatalf("%q should be valid", m)
		}
	}
	if ExplainerMode("X").IsValid() {
		t.Fatalf("bogus mode should be invalid")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustNewLiveQuiz(t *testing.T) *LiveQuiz {
	t.Helper()
	q, err := NewLiveQuiz(NewLiveQuizInput{
		TenantID:       "00000000-0000-7000-8000-000000000000",
		InstructorGCID: "11111111-1111-7111-8111-111111111111",
		Title:          "Sprint Planning",
		CourseID:       "course-cspo",
	})
	if err != nil {
		t.Fatalf("mustNewLiveQuiz err: %v", err)
	}
	return q
}

func makeValidQuestion(id string) LiveQuizQuestion {
	return LiveQuizQuestion{
		QuestionID: id,
		Prompt:     "Which Scrum ceremony kicks off a Sprint?",
		TimerSecs:  60,
		Points:     10,
		Options: []LiveQuizOption{
			{Label: "Sprint Review", IsCorrect: false},
			{Label: "Sprint Planning", IsCorrect: true},
			{Label: "Daily Scrum", IsCorrect: false},
			{Label: "Retrospective", IsCorrect: false},
		},
	}
}

// Sanity check on Title trimming.
func TestNewLiveQuiz_TitleTrimmed(t *testing.T) {
	q, err := NewLiveQuiz(NewLiveQuizInput{
		TenantID:       "00000000-0000-7000-8000-000000000000",
		InstructorGCID: "11111111-1111-7111-8111-111111111111",
		Title:          "  Trimmed Title  ",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.TrimSpace(q.Title) != q.Title {
		t.Fatalf("title should be trimmed; got %q", q.Title)
	}
}
