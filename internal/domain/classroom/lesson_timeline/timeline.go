// Package lesson_timeline owns the minute-by-minute lesson plan aggregate
// for the Classroom Experience inside chora-delivery.
//
// Per CHO-13 + docs/design/ux_classroom_experience.md "QuizPrep" step the
// instructor builds a session by choosing atom segments, quiz blocks, and
// JamBoard segments and arranging them on a timeline. This package is the
// pure-domain home of that timeline:
//
//   - Timeline (the aggregate root) — keyed by (tenant, class)
//   - Segment (child entity) — { atom_id, type, start_min, end_min, label }
//   - 5-state machine: Draft → Published → Live → Complete (+ Cancelled)
//
// The Timeline does not own atoms — atom_id is a cross-aggregate reference
// (UUID without FK constraint) per .claude/rules/ddd-enforcement.md
// aggregate-invariant #3. AtomRevisions live in chora-creation; pull-by-event.
//
// Hexagonal: pure domain, no HTTP, no persistence. UUIDv7 IDs (sortable +
// time-correlated). No infra imports.
//
// Distinct from scheduling.ScheduledClass and delivery.Class — those own
// when/where; this owns what-happens-when-during.
package lesson_timeline

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrInvalidArgument signals a guard-clause failure on inputs.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrSegmentOverlap is returned when AddSegment would overlap an
	// existing segment (start < other.end && end > other.start).
	ErrSegmentOverlap = errors.New("segment overlaps existing segment")
	// ErrSegmentOutOfBounds is returned when AddSegment exceeds the
	// timeline's [0, TotalMinutes] window.
	ErrSegmentOutOfBounds = errors.New("segment out of bounds")
	// ErrInvalidTransition is returned by Publish/Start/Complete/Cancel
	// when called from the wrong state.
	ErrInvalidTransition = errors.New("invalid state transition")
	// ErrEmptyTimeline is returned by Publish when no segments exist.
	ErrEmptyTimeline = errors.New("cannot publish empty timeline")
)

// -----------------------------------------------------------------------------
// State + SegmentType enums
// -----------------------------------------------------------------------------

// State is the lesson timeline lifecycle state.
type State string

const (
	// StateDraft — instructor is editing segments. AddSegment allowed.
	StateDraft State = "draft"
	// StatePublished — segments locked. Ready to start.
	StatePublished State = "published"
	// StateLive — lesson is in flight (instructor tapped Start).
	StateLive State = "live"
	// StateComplete — lesson finished (instructor tapped End).
	StateComplete State = "complete"
	// StateCancelled — terminal abandonment from any non-terminal state.
	StateCancelled State = "cancelled"
)

// SegmentType is what kind of activity a segment represents.
type SegmentType string

const (
	// SegmentTypeAtom — show/teach a single atom.
	SegmentTypeAtom SegmentType = "atom"
	// SegmentTypeQuiz — mid-class quiz block.
	SegmentTypeQuiz SegmentType = "quiz"
	// SegmentTypeJamBoard — collaborative whiteboard.
	SegmentTypeJamBoard SegmentType = "jamboard"
	// SegmentTypeBreak — instructor-scheduled pause.
	SegmentTypeBreak SegmentType = "break"
)

// Valid reports whether t is a recognised SegmentType.
func (t SegmentType) Valid() bool {
	switch t {
	case SegmentTypeAtom, SegmentTypeQuiz, SegmentTypeJamBoard, SegmentTypeBreak:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Segment (child entity)
// -----------------------------------------------------------------------------

// Segment is a single planned activity within a Timeline.
//
// StartMinute is inclusive; EndMinute is exclusive (i.e., a segment of
// 0..30 occupies minutes 0,1,...,29). This makes adjacency unambiguous —
// 30..50 follows 0..30 with no overlap.
type Segment struct {
	ID          string
	AtomID      string // cross-aggregate UUID; not FK-enforced
	Type        SegmentType
	Label       string
	StartMinute int
	EndMinute   int
	CreatedAt   time.Time
}

// Duration returns EndMinute - StartMinute.
func (s *Segment) Duration() int { return s.EndMinute - s.StartMinute }

// -----------------------------------------------------------------------------
// Timeline aggregate root
// -----------------------------------------------------------------------------

// Timeline is the lesson plan aggregate root, keyed by (tenant, class).
type Timeline struct {
	ID             string
	TenantID       string
	ClassID        string
	InstructorGCID string
	StartsAt       time.Time
	EndsAt         time.Time
	State          State
	StartedAt      *time.Time
	CompletedAt    *time.Time
	CancelledAt    *time.Time
	CancelReason   string
	Segments       []*Segment
	CreatedAt      time.Time
	UpdatedAt      time.Time

	mu sync.Mutex
}

// NewTimeline constructs a Timeline in StateDraft with no segments.
//
// Validation:
//   - tenant, class, instructor required (non-blank)
//   - ends_at strictly after starts_at.
func NewTimeline(tenantID, classID, instructorGCID string, starts, ends time.Time) (*Timeline, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(classID) == "" {
		return nil, fmt.Errorf("%w: class_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(instructorGCID) == "" {
		return nil, fmt.Errorf("%w: instructor_gcid required", ErrInvalidArgument)
	}
	if !ends.After(starts) {
		return nil, fmt.Errorf("%w: ends_at must be after starts_at", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Timeline{
		ID:             NewUUIDv7(),
		TenantID:       tenantID,
		ClassID:        classID,
		InstructorGCID: instructorGCID,
		StartsAt:       starts.UTC(),
		EndsAt:         ends.UTC(),
		State:          StateDraft,
		Segments:       make([]*Segment, 0, 8),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// TotalMinutes returns the timeline length in whole minutes (rounded down).
func (t *Timeline) TotalMinutes() int {
	d := t.EndsAt.Sub(t.StartsAt)
	return int(d / time.Minute)
}

// AddSegment appends a Segment, validating bounds + overlaps.
//
// Allowed only in StateDraft. atom_id required for SegmentTypeAtom and
// SegmentTypeQuiz; optional for SegmentTypeJamBoard and SegmentTypeBreak.
func (t *Timeline) AddSegment(segType SegmentType, atomID, label string, startMin, endMin int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.State != StateDraft {
		return fmt.Errorf("%w: AddSegment requires Draft, got %s", ErrInvalidTransition, t.State)
	}
	if !segType.Valid() {
		return fmt.Errorf("%w: invalid segment type %q", ErrInvalidArgument, segType)
	}
	if endMin <= startMin {
		return fmt.Errorf("%w: end_minute must be > start_minute", ErrInvalidArgument)
	}
	if startMin < 0 {
		return fmt.Errorf("%w: start_minute must be >= 0", ErrInvalidArgument)
	}
	if endMin > t.TotalMinutes() {
		return fmt.Errorf("%w: end_minute %d > total %d", ErrSegmentOutOfBounds, endMin, t.TotalMinutes())
	}
	if (segType == SegmentTypeAtom || segType == SegmentTypeQuiz) && strings.TrimSpace(atomID) == "" {
		return fmt.Errorf("%w: atom_id required for %s segment", ErrInvalidArgument, segType)
	}
	for _, s := range t.Segments {
		if startMin < s.EndMinute && endMin > s.StartMinute {
			return fmt.Errorf("%w: segment [%d..%d) overlaps existing [%d..%d)", ErrSegmentOverlap, startMin, endMin, s.StartMinute, s.EndMinute)
		}
	}
	t.Segments = append(t.Segments, &Segment{
		ID:          NewUUIDv7(),
		AtomID:      atomID,
		Type:        segType,
		Label:       label,
		StartMinute: startMin,
		EndMinute:   endMin,
		CreatedAt:   time.Now().UTC(),
	})
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// Publish locks segments + transitions Draft → Published.
func (t *Timeline) Publish() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State != StateDraft {
		return fmt.Errorf("%w: Publish requires Draft, got %s", ErrInvalidTransition, t.State)
	}
	if len(t.Segments) == 0 {
		return ErrEmptyTimeline
	}
	t.State = StatePublished
	t.UpdatedAt = time.Now().UTC()
	return nil
}

// Start transitions Published → Live, capturing wall-clock start.
func (t *Timeline) Start(now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State != StatePublished {
		return fmt.Errorf("%w: Start requires Published, got %s", ErrInvalidTransition, t.State)
	}
	stamp := now.UTC()
	t.StartedAt = &stamp
	t.State = StateLive
	t.UpdatedAt = stamp
	return nil
}

// Complete transitions Live → Complete.
func (t *Timeline) Complete() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State != StateLive {
		return fmt.Errorf("%w: Complete requires Live, got %s", ErrInvalidTransition, t.State)
	}
	stamp := time.Now().UTC()
	t.CompletedAt = &stamp
	t.State = StateComplete
	t.UpdatedAt = stamp
	return nil
}

// Cancel terminates the timeline from any non-terminal state. Idempotent
// against StateCancelled (returns ErrInvalidTransition for the second call
// to be unambiguous — operators should call this once).
func (t *Timeline) Cancel(reason string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.State == StateComplete || t.State == StateCancelled {
		return fmt.Errorf("%w: Cancel from %s not permitted", ErrInvalidTransition, t.State)
	}
	stamp := time.Now().UTC()
	t.CancelledAt = &stamp
	t.CancelReason = reason
	t.State = StateCancelled
	t.UpdatedAt = stamp
	return nil
}

// CurrentSegmentAt returns the segment covering `minute` (inclusive start,
// exclusive end), or nil if none. Useful for projector-state and
// signage-realtime to know what to display "right now".
func (t *Timeline) CurrentSegmentAt(minute int) *Segment {
	for _, s := range t.Segments {
		if minute >= s.StartMinute && minute < s.EndMinute {
			return s
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// UUIDv7 — local generator (deps-free; mirrors sibling packages)
// -----------------------------------------------------------------------------

// NewUUIDv7 returns a freshly generated UUIDv7 string per RFC 9562 §5.7.
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
