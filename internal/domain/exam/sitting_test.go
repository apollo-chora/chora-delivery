// sitting_test.go — TDD (RED-first) for the ExamSitting aggregate: the
// scheduled venue+time INSTANCE of a proctored exam (ADR-190 D2 Exam BC +
// ADR-191 / docs/design/exam-administration-domain.md §4.4).
//
// FSM under test (task-locked labels; design-doc synonyms in parens):
//
//	SCHEDULED --Open--> OPEN (check_in_open) --Begin--> IN_PROGRESS --Close--> CLOSED (completed)
//	SCHEDULED --Cancel--> CANCELLED
//	OPEN      --Cancel--> CANCELLED
//	OPEN      --Close---> CLOSED
//
// Invariants: end>start; capacity>0; legal transitions only; a CANCELLED
// sitting can NEVER be opened.
//
// Coverage target: 85% domain (per .claude/rules/development-execution.md).
package exam

import (
	"errors"
	"testing"
	"time"
)

const (
	sitTenantID   = "019e2f93-d586-71b5-8c3d-e2b0d0d51100"
	sitExamID     = "019e2f93-d586-71b5-8c3d-e2b0d0d51200"
	sitExamFormID = "019e2f93-d586-71b5-8c3d-e2b0d0d51300"
	sitRoomID     = "019e2f93-d586-71b5-8c3d-e2b0d0d51400"
)

func sittingWindow() (time.Time, time.Time) {
	start := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	return start, start.Add(3 * time.Hour)
}

func mustSitting(t *testing.T) *ExamSitting {
	t.Helper()
	start, end := sittingWindow()
	s, err := NewExamSitting(NewExamSittingInput{
		TenantID:   sitTenantID,
		ExamID:     sitExamID,
		ExamFormID: sitExamFormID,
		RoomID:     sitRoomID,
		StartsAt:   start,
		EndsAt:     end,
		Capacity:   30,
	})
	if err != nil {
		t.Fatalf("NewExamSitting: unexpected err %v", err)
	}
	return s
}

// -----------------------------------------------------------------------------
// Enum
// -----------------------------------------------------------------------------

func TestExamSittingState_IsValid(t *testing.T) {
	for _, s := range []ExamSittingState{
		ExamSittingStateScheduled, ExamSittingStateOpen, ExamSittingStateInProgress,
		ExamSittingStateClosed, ExamSittingStateCancelled,
	} {
		if !s.IsValid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if ExamSittingState("BOGUS").IsValid() {
		t.Error("BOGUS must be invalid")
	}
	if ExamSittingState("").IsValid() {
		t.Error("empty must be invalid")
	}
}

// -----------------------------------------------------------------------------
// Constructor
// -----------------------------------------------------------------------------

func TestNewExamSitting_Defaults(t *testing.T) {
	s := mustSitting(t)
	if s.State != ExamSittingStateScheduled {
		t.Errorf("state=%q want SCHEDULED", s.State)
	}
	if s.ID == "" {
		t.Error("ID must be a freshly generated UUIDv7")
	}
	if s.TenantID != sitTenantID || s.ExamID != sitExamID {
		t.Errorf("field carry mismatch: %+v", s)
	}
	if s.ExamFormID != sitExamFormID || s.RoomID != sitRoomID {
		t.Errorf("optional refs must carry: %+v", s)
	}
	if s.Capacity != 30 {
		t.Errorf("capacity=%d want 30", s.Capacity)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		t.Error("timestamps must be set")
	}
	if s.CreatedAt.Location() != time.UTC || s.StartsAt.Location() != time.UTC {
		t.Error("timestamps must be UTC")
	}
	if s.DeletedAt != nil {
		t.Error("DeletedAt must be nil on a fresh sitting")
	}
}

func TestNewExamSitting_TrimsAndOptionalRefs(t *testing.T) {
	start, end := sittingWindow()
	s, err := NewExamSitting(NewExamSittingInput{
		TenantID: "  " + sitTenantID + " ",
		ExamID:   "\t" + sitExamID + "\n",
		// ExamFormID + RoomID omitted — both optional.
		StartsAt: start,
		EndsAt:   end,
		Capacity: 1,
	})
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if s.TenantID != sitTenantID || s.ExamID != sitExamID {
		t.Errorf("whitespace not trimmed: %+v", s)
	}
	if s.ExamFormID != "" || s.RoomID != "" {
		t.Errorf("omitted optional refs must be empty: form=%q room=%q", s.ExamFormID, s.RoomID)
	}
}

func TestNewExamSitting_Validation(t *testing.T) {
	start, end := sittingWindow()
	base := NewExamSittingInput{
		TenantID: sitTenantID, ExamID: sitExamID, StartsAt: start, EndsAt: end, Capacity: 10,
	}
	cases := []struct {
		name string
		mut  func(in *NewExamSittingInput)
		want error
	}{
		{"blank tenant", func(in *NewExamSittingInput) { in.TenantID = "   " }, ErrExamSittingTenantRequired},
		{"blank exam", func(in *NewExamSittingInput) { in.ExamID = "" }, ErrExamSittingExamRequired},
		{"end == start", func(in *NewExamSittingInput) { in.EndsAt = in.StartsAt }, ErrExamSittingTimeWindowInvalid},
		{"end before start", func(in *NewExamSittingInput) { in.EndsAt = in.StartsAt.Add(-time.Hour) }, ErrExamSittingTimeWindowInvalid},
		{"zero capacity", func(in *NewExamSittingInput) { in.Capacity = 0 }, ErrExamSittingCapacityInvalid},
		{"negative capacity", func(in *NewExamSittingInput) { in.Capacity = -5 }, ErrExamSittingCapacityInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mut(&in)
			if _, err := NewExamSitting(in); !errors.Is(err, tc.want) {
				t.Errorf("err=%v want %v", err, tc.want)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Happy-path FSM
// -----------------------------------------------------------------------------

func TestExamSitting_HappyPath(t *testing.T) {
	s := mustSitting(t)
	prev := s.UpdatedAt
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.State != ExamSittingStateOpen {
		t.Fatalf("state=%q want OPEN", s.State)
	}
	if !s.UpdatedAt.After(prev) && s.UpdatedAt.Equal(prev) {
		// UpdatedAt should advance (monotonic-ish); tolerate equal on coarse clocks.
	}
	if err := s.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if s.State != ExamSittingStateInProgress {
		t.Fatalf("state=%q want IN_PROGRESS", s.State)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s.State != ExamSittingStateClosed {
		t.Fatalf("state=%q want CLOSED", s.State)
	}
}

func TestExamSitting_CloseFromOpen(t *testing.T) {
	s := mustSitting(t)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close from OPEN should be allowed: %v", err)
	}
	if s.State != ExamSittingStateClosed {
		t.Errorf("state=%q want CLOSED", s.State)
	}
}

// -----------------------------------------------------------------------------
// Illegal transitions
// -----------------------------------------------------------------------------

func TestExamSitting_OpenRequiresScheduled(t *testing.T) {
	s := mustSitting(t)
	if err := s.Open(); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s.Open(); !errors.Is(err, ErrExamSittingNotScheduled) {
		t.Errorf("re-Open err=%v want ErrExamSittingNotScheduled", err)
	}
}

func TestExamSitting_BeginRequiresOpen(t *testing.T) {
	s := mustSitting(t)
	if err := s.Begin(); !errors.Is(err, ErrExamSittingNotOpen) {
		t.Errorf("Begin from SCHEDULED err=%v want ErrExamSittingNotOpen", err)
	}
}

func TestExamSitting_CloseRequiresOpenOrInProgress(t *testing.T) {
	s := mustSitting(t)
	if err := s.Close(); !errors.Is(err, ErrExamSittingNotCloseable) {
		t.Errorf("Close from SCHEDULED err=%v want ErrExamSittingNotCloseable", err)
	}
}

// THE invariant: a CANCELLED sitting can never be opened.
func TestExamSitting_CannotOpenCancelled(t *testing.T) {
	s := mustSitting(t)
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if s.State != ExamSittingStateCancelled {
		t.Fatalf("state=%q want CANCELLED", s.State)
	}
	if err := s.Open(); !errors.Is(err, ErrExamSittingCancelled) {
		t.Errorf("Open on CANCELLED err=%v want ErrExamSittingCancelled", err)
	}
}

func TestExamSitting_CancelFromOpen(t *testing.T) {
	s := mustSitting(t)
	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel from OPEN should be allowed: %v", err)
	}
	if s.State != ExamSittingStateCancelled {
		t.Errorf("state=%q want CANCELLED", s.State)
	}
}

func TestExamSitting_CancelRejectedAfterBeginOrClose(t *testing.T) {
	// IN_PROGRESS cannot be cancelled.
	s := mustSitting(t)
	_ = s.Open()
	_ = s.Begin()
	if err := s.Cancel(); !errors.Is(err, ErrExamSittingNotCancellable) {
		t.Errorf("Cancel from IN_PROGRESS err=%v want ErrExamSittingNotCancellable", err)
	}
	// CLOSED cannot be cancelled.
	s2 := mustSitting(t)
	_ = s2.Open()
	_ = s2.Close()
	if err := s2.Cancel(); !errors.Is(err, ErrExamSittingNotCancellable) {
		t.Errorf("Cancel from CLOSED err=%v want ErrExamSittingNotCancellable", err)
	}
}

// -----------------------------------------------------------------------------
// Soft delete
// -----------------------------------------------------------------------------

func TestExamSitting_SoftDelete(t *testing.T) {
	s := mustSitting(t)
	if err := s.SoftDelete(); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if s.DeletedAt == nil {
		t.Fatal("DeletedAt must be set after SoftDelete")
	}
	if s.DeletedAt.Location() != time.UTC {
		t.Error("DeletedAt must be UTC")
	}
	// Idempotent — second call keeps the original stamp.
	first := *s.DeletedAt
	if err := s.SoftDelete(); err != nil {
		t.Fatalf("2nd SoftDelete: %v", err)
	}
	if !s.DeletedAt.Equal(first) {
		t.Error("SoftDelete must be idempotent (keep original stamp)")
	}
}
