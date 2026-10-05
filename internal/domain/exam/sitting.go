// sitting.go owns the ExamSitting aggregate — the scheduled venue+time INSTANCE
// of a proctored exam (ADR-190 D2 Exam BC + ADR-191; design reference
// docs/design/exam-administration-domain.md §4.4).
//
// An ExamSitting is a specific date/time window in a specific room for a
// specific exam — the operational unit that candidates book and invigilators
// staff. It is a SEPARATE aggregate root from Exam (Brick-1) keyed by its own
// UUIDv7; it holds CROSS-AGGREGATE references (exam_id, exam_form_id) and a
// CROSS-AGGREGATE/opaque ROOM reference (room_id) — all as opaque UUIDs with NO
// Go-level FK, per .claude/rules/ddd-enforcement.md Invariant #3.
//
// ROOM REFERENCE SHAPE (assumption, grounded in the Exam-BC design): room_id is
// an OPTIONAL opaque UUID reference to a room defined inside a venue's `rooms`
// JSONB array (design doc §4.4: "room_id UUID — references `rooms` JSONB
// room_id"). There is NO rooms/venue TABLE to key an FK against (the venue
// brick is out of this brick's scope), and it is distinct from the legacy
// free-text delivery.Class.Room string. The aggregate therefore treats room_id
// as an opaque, optional reference — trimmed, no shape validation, empty ⇒ "no
// room assigned yet / roaming". It rides in the JSONB snapshot only (not a
// query axis), so no column-type coupling is imposed.
//
// State machine (FSM) — task-locked labels (design-doc synonyms in parens):
//
//	SCHEDULED   → OPEN         via Open   (registration / check-in window opens ≈ check_in_open)
//	OPEN        → IN_PROGRESS  via Begin  (first candidate starts)
//	OPEN        → CLOSED       via Close  (window closed without/after running)
//	IN_PROGRESS → CLOSED       via Close  (sitting completes ≈ completed)
//	SCHEDULED   → CANCELLED    via Cancel (admin cancel before opening)
//	OPEN        → CANCELLED    via Cancel (admin cancel after opening, pre-run)
//
// INVARIANT (compliance): a CANCELLED sitting can NEVER be opened — Open refuses
// with ErrExamSittingCancelled. Cancel is refused once IN_PROGRESS or CLOSED.
//
// This file contains the domain only — NO HTTP, NO persistence imports. The
// UUIDv7 generator (newUUIDv7) is shared with exam.go in this package.
package exam

import (
	"errors"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// State enum
// -----------------------------------------------------------------------------

// ExamSittingState models the sitting lifecycle FSM.
type ExamSittingState string

const (
	// ExamSittingStateScheduled — sitting created; candidates can register.
	ExamSittingStateScheduled ExamSittingState = "SCHEDULED"
	// ExamSittingStateOpen — check-in window open (design doc: check_in_open).
	ExamSittingStateOpen ExamSittingState = "OPEN"
	// ExamSittingStateInProgress — exam underway; candidates are sitting it.
	ExamSittingStateInProgress ExamSittingState = "IN_PROGRESS"
	// ExamSittingStateClosed — sitting concluded (design doc: completed).
	ExamSittingStateClosed ExamSittingState = "CLOSED"
	// ExamSittingStateCancelled — sitting cancelled before it ran (terminal).
	ExamSittingStateCancelled ExamSittingState = "CANCELLED"
)

// IsValid reports whether s is one of the canonical sitting states.
func (s ExamSittingState) IsValid() bool {
	switch s {
	case ExamSittingStateScheduled, ExamSittingStateOpen, ExamSittingStateInProgress,
		ExamSittingStateClosed, ExamSittingStateCancelled:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors (sentinels)
// -----------------------------------------------------------------------------

var (
	// ErrExamSittingTenantRequired — tenant_id must be non-empty.
	ErrExamSittingTenantRequired = errors.New("exam_sitting: tenant_id required")
	// ErrExamSittingExamRequired — exam_id must be non-empty.
	ErrExamSittingExamRequired = errors.New("exam_sitting: exam_id required")
	// ErrExamSittingTimeWindowInvalid — ends_at must be strictly after starts_at.
	ErrExamSittingTimeWindowInvalid = errors.New("exam_sitting: ends_at must be after starts_at")
	// ErrExamSittingCapacityInvalid — capacity must be > 0.
	ErrExamSittingCapacityInvalid = errors.New("exam_sitting: capacity must be > 0")

	// ErrExamSittingNotScheduled — Open requires SCHEDULED state.
	ErrExamSittingNotScheduled = errors.New("exam_sitting: not in SCHEDULED state")
	// ErrExamSittingCancelled — THE invariant: a cancelled sitting cannot be opened.
	ErrExamSittingCancelled = errors.New("exam_sitting: sitting is CANCELLED and cannot be opened")
	// ErrExamSittingNotOpen — Begin requires OPEN state.
	ErrExamSittingNotOpen = errors.New("exam_sitting: not in OPEN state")
	// ErrExamSittingNotCloseable — Close requires OPEN or IN_PROGRESS state.
	ErrExamSittingNotCloseable = errors.New("exam_sitting: state is not closeable (must be OPEN or IN_PROGRESS)")
	// ErrExamSittingNotCancellable — Cancel requires SCHEDULED or OPEN state.
	ErrExamSittingNotCancellable = errors.New("exam_sitting: state is not cancellable (must be SCHEDULED or OPEN)")
)

// -----------------------------------------------------------------------------
// Aggregate
// -----------------------------------------------------------------------------

// ExamSitting is the scheduled venue+time instance of a proctored exam.
//
// ExamID / ExamFormID / RoomID are opaque cross-aggregate references (no FK).
// ExamFormID + RoomID are OPTIONAL (empty ⇒ unassigned). Soft delete via
// DeletedAt; transitions advance UpdatedAt.
type ExamSitting struct {
	ID         string
	TenantID   string
	ExamID     string
	ExamFormID string // optional — pinned form/paper for the sitting (opaque UUID ref)
	RoomID     string // optional — opaque room reference (see file header)
	StartsAt   time.Time
	EndsAt     time.Time
	Capacity   int
	State      ExamSittingState
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// NewExamSittingInput is the constructor input bag.
type NewExamSittingInput struct {
	TenantID   string
	ExamID     string
	ExamFormID string
	RoomID     string
	StartsAt   time.Time
	EndsAt     time.Time
	Capacity   int
}

// NewExamSitting constructs a SCHEDULED-state ExamSitting.
//
// Validation guards (rejected with a specific sentinel):
//   - tenant_id trim-non-empty
//   - exam_id   trim-non-empty
//   - ends_at strictly after starts_at
//   - capacity > 0
//
// exam_form_id + room_id are optional (trimmed; empty allowed). The aggregate
// ID is a freshly generated UUIDv7; timestamps are current UTC.
func NewExamSitting(in NewExamSittingInput) (*ExamSitting, error) {
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		return nil, ErrExamSittingTenantRequired
	}
	examID := strings.TrimSpace(in.ExamID)
	if examID == "" {
		return nil, ErrExamSittingExamRequired
	}
	if !in.EndsAt.After(in.StartsAt) {
		return nil, ErrExamSittingTimeWindowInvalid
	}
	if in.Capacity <= 0 {
		return nil, ErrExamSittingCapacityInvalid
	}
	now := time.Now().UTC()
	return &ExamSitting{
		ID:         newUUIDv7(),
		TenantID:   tenantID,
		ExamID:     examID,
		ExamFormID: strings.TrimSpace(in.ExamFormID),
		RoomID:     strings.TrimSpace(in.RoomID),
		StartsAt:   in.StartsAt.UTC(),
		EndsAt:     in.EndsAt.UTC(),
		Capacity:   in.Capacity,
		State:      ExamSittingStateScheduled,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// -----------------------------------------------------------------------------
// State transitions
// -----------------------------------------------------------------------------

// touch advances UpdatedAt to now (UTC).
func (s *ExamSitting) touch() { s.UpdatedAt = time.Now().UTC() }

// Open transitions SCHEDULED → OPEN. A CANCELLED sitting can NEVER be opened
// (returns ErrExamSittingCancelled); any other non-SCHEDULED state returns
// ErrExamSittingNotScheduled.
func (s *ExamSitting) Open() error {
	if s.State == ExamSittingStateCancelled {
		return ErrExamSittingCancelled
	}
	if s.State != ExamSittingStateScheduled {
		return ErrExamSittingNotScheduled
	}
	s.State = ExamSittingStateOpen
	s.touch()
	return nil
}

// Begin transitions OPEN → IN_PROGRESS (first candidate starts).
func (s *ExamSitting) Begin() error {
	if s.State != ExamSittingStateOpen {
		return ErrExamSittingNotOpen
	}
	s.State = ExamSittingStateInProgress
	s.touch()
	return nil
}

// Close transitions OPEN | IN_PROGRESS → CLOSED.
func (s *ExamSitting) Close() error {
	if s.State != ExamSittingStateOpen && s.State != ExamSittingStateInProgress {
		return ErrExamSittingNotCloseable
	}
	s.State = ExamSittingStateClosed
	s.touch()
	return nil
}

// Cancel transitions SCHEDULED | OPEN → CANCELLED. Refused once the sitting is
// IN_PROGRESS or CLOSED (ErrExamSittingNotCancellable).
func (s *ExamSitting) Cancel() error {
	if s.State != ExamSittingStateScheduled && s.State != ExamSittingStateOpen {
		return ErrExamSittingNotCancellable
	}
	s.State = ExamSittingStateCancelled
	s.touch()
	return nil
}

// SoftDelete stamps DeletedAt (idempotent — keeps the original stamp). NEVER a
// hard delete, per .claude/rules/ddd-enforcement.md Invariant #4.
func (s *ExamSitting) SoftDelete() error {
	if s.DeletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	s.DeletedAt = &now
	s.touch()
	return nil
}
