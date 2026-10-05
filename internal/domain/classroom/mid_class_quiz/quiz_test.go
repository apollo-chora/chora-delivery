// Package mid_class_quiz_test pins the contract for the mid-lesson quiz
// aggregate per CHO-13 + docs/design/ux_classroom_experience.md "LivePlay"
// step (instructor launches mid-quiz; aggregates real-time response).
//
// Tests are written FIRST per .claude/rules/development-execution.md TDD.
package mid_class_quiz_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/classroom/mid_class_quiz"
)

const (
	tenantA  = "01970000-0000-7000-8000-000000000001"
	classA   = "01970000-0000-7000-7100-000000000001"
	atomA    = "atom://0xK3-NETWORK-DEFENSES"
	teacherA = "01970000-0000-7000-9000-000000000001"
	gcid1    = "01970000-0000-7000-A000-000000000001"
	gcid2    = "01970000-0000-7000-A000-000000000002"
	gcid3    = "01970000-0000-7000-A000-000000000003"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// -----------------------------------------------------------------------------
// NewQuiz + AddOption guards
// -----------------------------------------------------------------------------

func TestNewQuiz_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		tenant  string
		class   string
		instr   string
		atom    string
		stem    string
		seconds int
		wantErr error
	}{
		{"happy", tenantA, classA, teacherA, atomA, "Which mitigates ARP spoofing?", 30, nil},
		{"missing tenant", "", classA, teacherA, atomA, "stem", 30, mid_class_quiz.ErrInvalidArgument},
		{"missing class", tenantA, "", teacherA, atomA, "stem", 30, mid_class_quiz.ErrInvalidArgument},
		{"missing instructor", tenantA, classA, "", atomA, "stem", 30, mid_class_quiz.ErrInvalidArgument},
		{"missing atom", tenantA, classA, teacherA, "", "stem", 30, mid_class_quiz.ErrInvalidArgument},
		{"missing stem", tenantA, classA, teacherA, atomA, "", 30, mid_class_quiz.ErrInvalidArgument},
		{"zero seconds", tenantA, classA, teacherA, atomA, "stem", 0, mid_class_quiz.ErrInvalidArgument},
		{"negative seconds", tenantA, classA, teacherA, atomA, "stem", -5, mid_class_quiz.ErrInvalidArgument},
		{"too long seconds", tenantA, classA, teacherA, atomA, "stem", 600, mid_class_quiz.ErrInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, err := mid_class_quiz.NewQuiz(c.tenant, c.class, c.instr, c.atom, c.stem, c.seconds)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("got %v want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if q.State != mid_class_quiz.StateDraft {
				t.Errorf("expected Draft, got %s", q.State)
			}
		})
	}
}

func TestAddOption_GuardsAndAppend(t *testing.T) {
	q, err := mid_class_quiz.NewQuiz(tenantA, classA, teacherA, atomA, "stem", 30)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := q.AddOption("a", "Static ARP entries", true); err != nil {
		t.Fatalf("a: %v", err)
	}
	if err := q.AddOption("b", "Disable DNS", false); err != nil {
		t.Fatalf("b: %v", err)
	}
	if got := len(q.Options); got != 2 {
		t.Errorf("len=%d want 2", got)
	}

	// Duplicate id rejected
	if err := q.AddOption("a", "dup", false); !errors.Is(err, mid_class_quiz.ErrInvalidArgument) {
		t.Errorf("duplicate id: got %v want ErrInvalidArgument", err)
	}
	// Empty fields rejected
	if err := q.AddOption("", "x", false); !errors.Is(err, mid_class_quiz.ErrInvalidArgument) {
		t.Errorf("empty id: got %v", err)
	}
	if err := q.AddOption("c", "", false); !errors.Is(err, mid_class_quiz.ErrInvalidArgument) {
		t.Errorf("empty label: got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Lifecycle: Draft → Live → Closed
// -----------------------------------------------------------------------------

func TestQuiz_LaunchRequiresOptionsAndCorrect(t *testing.T) {
	q, _ := mid_class_quiz.NewQuiz(tenantA, classA, teacherA, atomA, "stem", 30)

	now := mustTime(t, "2026-06-01T09:00:00Z")
	// no options: cannot Launch
	if err := q.Launch(now); !errors.Is(err, mid_class_quiz.ErrInsufficientOptions) {
		t.Errorf("launch no opts: got %v want ErrInsufficientOptions", err)
	}

	_ = q.AddOption("a", "Static ARP", true)
	// only 1 option still insufficient (require >= 2)
	if err := q.Launch(now); !errors.Is(err, mid_class_quiz.ErrInsufficientOptions) {
		t.Errorf("launch 1 opt: got %v", err)
	}

	q2, _ := mid_class_quiz.NewQuiz(tenantA, classA, teacherA, atomA, "stem", 30)
	_ = q2.AddOption("a", "Static ARP", false)
	_ = q2.AddOption("b", "Disable DNS", false)
	// no correct option — Launch rejects
	if err := q2.Launch(now); !errors.Is(err, mid_class_quiz.ErrNoCorrectOption) {
		t.Errorf("launch no correct: got %v want ErrNoCorrectOption", err)
	}

	q3, _ := mid_class_quiz.NewQuiz(tenantA, classA, teacherA, atomA, "stem", 30)
	_ = q3.AddOption("a", "x", true)
	_ = q3.AddOption("b", "y", false)
	if err := q3.Launch(now); err != nil {
		t.Fatalf("happy launch: %v", err)
	}
	if q3.State != mid_class_quiz.StateLive {
		t.Errorf("state: got %s want Live", q3.State)
	}
	if q3.LaunchedAt == nil || !q3.LaunchedAt.Equal(now.UTC()) {
		t.Errorf("LaunchedAt not stamped")
	}
	// Cannot Launch from Live
	if err := q3.Launch(now); !errors.Is(err, mid_class_quiz.ErrInvalidTransition) {
		t.Errorf("relaunch: got %v", err)
	}
}

func TestQuiz_ResponseAggregation(t *testing.T) {
	q, _ := mid_class_quiz.NewQuiz(tenantA, classA, teacherA, atomA, "stem", 30)
	_ = q.AddOption("a", "Static ARP", true)
	_ = q.AddOption("b", "Disable DNS", false)
	_ = q.AddOption("c", "Higher MTU", false)

	now := mustTime(t, "2026-06-01T09:00:00Z")
	if err := q.Launch(now); err != nil {
		t.Fatalf("launch: %v", err)
	}

	// Cannot submit before launch — but we just launched. Test pre-launch
	// rejection in another instance.
	q2, _ := mid_class_quiz.NewQuiz(tenantA, classA, teacherA, atomA, "stem", 30)
	_ = q2.AddOption("a", "x", true)
	_ = q2.AddOption("b", "y", false)
	if err := q2.Submit(gcid1, "a", now); !errors.Is(err, mid_class_quiz.ErrInvalidTransition) {
		t.Errorf("submit before launch: got %v", err)
	}

	// Three learners submit
	if err := q.Submit(gcid1, "a", now.Add(5*time.Second)); err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	if err := q.Submit(gcid2, "a", now.Add(10*time.Second)); err != nil {
		t.Fatalf("submit 2: %v", err)
	}
	if err := q.Submit(gcid3, "b", now.Add(15*time.Second)); err != nil {
		t.Fatalf("submit 3: %v", err)
	}

	dist := q.Distribution()
	if dist["a"] != 2 || dist["b"] != 1 || dist["c"] != 0 {
		t.Errorf("distribution: %+v", dist)
	}
	if got := q.TotalResponses(); got != 3 {
		t.Errorf("total: got %d want 3", got)
	}

	// Idempotency: second submit by same gcid is no-op (first wins)
	if err := q.Submit(gcid1, "b", now.Add(20*time.Second)); err != nil {
		t.Fatalf("re-submit: %v", err)
	}
	if got := q.TotalResponses(); got != 3 {
		t.Errorf("after re-submit total: got %d want 3", got)
	}
	dist = q.Distribution()
	if dist["a"] != 2 {
		t.Errorf("first-wins broken: a=%d", dist["a"])
	}

	// Bogus option id
	if err := q.Submit(gcid1, "z", now); !errors.Is(err, mid_class_quiz.ErrUnknownOption) {
		t.Errorf("bogus option: got %v", err)
	}
}

func TestQuiz_CloseAndExpire(t *testing.T) {
	q, _ := mid_class_quiz.NewQuiz(tenantA, classA, teacherA, atomA, "stem", 30)
	_ = q.AddOption("a", "x", true)
	_ = q.AddOption("b", "y", false)

	now := mustTime(t, "2026-06-01T09:00:00Z")
	_ = q.Launch(now)

	// Submission within window OK
	if err := q.Submit(gcid1, "a", now.Add(20*time.Second)); err != nil {
		t.Fatalf("in-window: %v", err)
	}
	// Submission AFTER expiry rejected (30s timer)
	if err := q.Submit(gcid2, "a", now.Add(31*time.Second)); !errors.Is(err, mid_class_quiz.ErrTimerExpired) {
		t.Errorf("expired: got %v", err)
	}

	// Close transitions Live → Closed; idempotent on already-Closed
	if err := q.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if q.State != mid_class_quiz.StateClosed {
		t.Errorf("expected Closed, got %s", q.State)
	}
	// Submit after Close rejected
	if err := q.Submit(gcid3, "a", now.Add(10*time.Second)); !errors.Is(err, mid_class_quiz.ErrInvalidTransition) {
		t.Errorf("submit after close: got %v", err)
	}
	// Re-Close from Closed is invalid
	if err := q.Close(); !errors.Is(err, mid_class_quiz.ErrInvalidTransition) {
		t.Errorf("re-close: got %v", err)
	}
}

func TestQuiz_CorrectRate(t *testing.T) {
	q, _ := mid_class_quiz.NewQuiz(tenantA, classA, teacherA, atomA, "stem", 30)
	_ = q.AddOption("a", "x", true)
	_ = q.AddOption("b", "y", false)

	now := mustTime(t, "2026-06-01T09:00:00Z")
	_ = q.Launch(now)

	if got := q.CorrectRate(); got != 0.0 {
		t.Errorf("empty: got %v want 0.0", got)
	}

	_ = q.Submit(gcid1, "a", now.Add(2*time.Second)) // correct
	_ = q.Submit(gcid2, "b", now.Add(5*time.Second)) // wrong
	_ = q.Submit(gcid3, "a", now.Add(8*time.Second)) // correct

	got := q.CorrectRate()
	want := 2.0 / 3.0
	if got < want-1e-9 || got > want+1e-9 {
		t.Errorf("CorrectRate: got %v want %v", got, want)
	}
}
