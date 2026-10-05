// Package wbl is the pure domain core for Work-Based Learning placements
// — instances where a learner is placed at a host organisation for
// practical on-the-job training as part of a Course (e.g. internships,
// industry attachments, SkillsFuture work-study programmes).
//
// WblPlacement is an aggregate owned by Content Delivery, holding the
// host organisation + supervisor + duration + hours tracking + a small
// state machine:
//
//	SCHEDULED → IN_PROGRESS via Start()        (learner starts onsite)
//	IN_PROGRESS → COMPLETED via Complete()     (hours met, evaluator signs off)
//	SCHEDULED → WITHDRAWN via Withdraw()       (either side cancels pre-start)
//	IN_PROGRESS → WITHDRAWN via Withdraw()     (early termination)
//
// Per ddd-enforcement.md aggregate invariants:
//   - Cross-domain references travel as opaque UUIDs (course_id, gcid)
//   - Soft delete is a state transition to WITHDRAWN (not a deleted_at flag)
//   - Append-only update semantics — no DELETE of fields
//
// Hexagonal: this file depends on the standard library only. NO HTTP, NO
// persistence, NO event publishing. The adapter layer wires those.
package wbl

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxEvaluatorNotesLen is the upper bound on the SetEvaluatorNotes
// payload size; chosen to fit comfortably in a Postgres TEXT column
// without burning RLS-policy index budget. The H+ form caps the input
// at the same value so the client + server stay aligned.
const MaxEvaluatorNotesLen = 4_000

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrTenantRequired — TenantID must be non-empty.
	ErrTenantRequired = errors.New("wbl: tenant_id required")
	// ErrGCIDRequired — GCID (learner identity) must be non-empty.
	ErrGCIDRequired = errors.New("wbl: gcid required")
	// ErrCourseIDRequired — CourseID (parent Course reference) must be non-empty.
	ErrCourseIDRequired = errors.New("wbl: course_id required")
	// ErrHostOrgRequired — HostOrgName must be non-empty after trim.
	ErrHostOrgRequired = errors.New("wbl: host_org_name required")
	// ErrSupervisorRequired — SupervisorName must be non-empty.
	ErrSupervisorRequired = errors.New("wbl: supervisor_name required")
	// ErrSupervisorEmailInvalid — SupervisorEmail must contain '@'.
	ErrSupervisorEmailInvalid = errors.New("wbl: supervisor_email invalid")
	// ErrHoursRequiredPositive — HoursRequired must be > 0.
	ErrHoursRequiredPositive = errors.New("wbl: hours_required must be > 0")
	// ErrEndBeforeStart — EndDate must be at or after StartDate.
	ErrEndBeforeStart = errors.New("wbl: end_date must be on or after start_date")
	// ErrNotScheduled — Start() requires SCHEDULED state.
	ErrNotScheduled = errors.New("wbl: placement not in SCHEDULED state")
	// ErrNotInProgress — Complete() requires IN_PROGRESS state.
	ErrNotInProgress = errors.New("wbl: placement not in IN_PROGRESS state")
	// ErrPlacementClosed — operations rejected on COMPLETED or WITHDRAWN.
	ErrPlacementClosed = errors.New("wbl: placement is closed (COMPLETED or WITHDRAWN)")
	// ErrHoursNegative — RecordHours rejects negative deltas.
	ErrHoursNegative = errors.New("wbl: hours_completed cannot be negative")
	// ErrHoursExceedsRequired — hours_completed cannot exceed hours_required.
	ErrHoursExceedsRequired = errors.New("wbl: hours_completed cannot exceed hours_required")
	// ErrEvaluatorNotesTooLong — SetEvaluatorNotes rejects oversized payloads.
	ErrEvaluatorNotesTooLong = errors.New("wbl: evaluator_notes exceeds max length")
)

// -----------------------------------------------------------------------------
// PlacementState — FSM
// -----------------------------------------------------------------------------

// PlacementState models the WBL placement lifecycle FSM.
type PlacementState string

const (
	// PlacementStateScheduled — placement created, learner has not started.
	PlacementStateScheduled PlacementState = "SCHEDULED"
	// PlacementStateInProgress — learner is onsite accruing hours.
	PlacementStateInProgress PlacementState = "IN_PROGRESS"
	// PlacementStateCompleted — hours met + evaluator signed off.
	PlacementStateCompleted PlacementState = "COMPLETED"
	// PlacementStateWithdrawn — terminal cancel (pre-start or mid-flight).
	PlacementStateWithdrawn PlacementState = "WITHDRAWN"
)

// IsValid reports whether s is a recognised PlacementState.
func (s PlacementState) IsValid() bool {
	switch s {
	case PlacementStateScheduled,
		PlacementStateInProgress,
		PlacementStateCompleted,
		PlacementStateWithdrawn:
		return true
	}
	return false
}

// isClosed reports whether s is a terminal state (COMPLETED or WITHDRAWN).
func (s PlacementState) isClosed() bool {
	return s == PlacementStateCompleted || s == PlacementStateWithdrawn
}

// -----------------------------------------------------------------------------
// Placement aggregate
// -----------------------------------------------------------------------------

// Placement is the WBL aggregate root.
type Placement struct {
	ID              string
	TenantID        string
	GCID            string
	CourseID        string
	HostOrgName     string
	SupervisorName  string
	SupervisorEmail string
	StartDate       time.Time
	EndDate         time.Time
	HoursRequired   int
	HoursCompleted  int
	State           PlacementState
	EvaluatorNotes  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewPlacementInput is the constructor argument for NewPlacement.
type NewPlacementInput struct {
	TenantID        string
	GCID            string
	CourseID        string
	HostOrgName     string
	SupervisorName  string
	SupervisorEmail string
	StartDate       time.Time
	EndDate         time.Time
	HoursRequired   int
}

// NewPlacement constructs a SCHEDULED-state Placement after validating
// every required field. Returns a sentinel error per invariant violation.
func NewPlacement(in NewPlacementInput) (*Placement, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, ErrTenantRequired
	}
	if strings.TrimSpace(in.GCID) == "" {
		return nil, ErrGCIDRequired
	}
	if strings.TrimSpace(in.CourseID) == "" {
		return nil, ErrCourseIDRequired
	}
	host := strings.TrimSpace(in.HostOrgName)
	if host == "" {
		return nil, ErrHostOrgRequired
	}
	sup := strings.TrimSpace(in.SupervisorName)
	if sup == "" {
		return nil, ErrSupervisorRequired
	}
	email := strings.TrimSpace(in.SupervisorEmail)
	if !looksLikeEmail(email) {
		return nil, ErrSupervisorEmailInvalid
	}
	if in.HoursRequired <= 0 {
		return nil, ErrHoursRequiredPositive
	}
	if in.EndDate.Before(in.StartDate) {
		return nil, ErrEndBeforeStart
	}
	now := time.Now().UTC()
	return &Placement{
		ID:              newUUIDv7(),
		TenantID:        strings.TrimSpace(in.TenantID),
		GCID:            strings.TrimSpace(in.GCID),
		CourseID:        strings.TrimSpace(in.CourseID),
		HostOrgName:     host,
		SupervisorName:  sup,
		SupervisorEmail: email,
		StartDate:       in.StartDate.UTC(),
		EndDate:         in.EndDate.UTC(),
		HoursRequired:   in.HoursRequired,
		HoursCompleted:  0,
		State:           PlacementStateScheduled,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// Start transitions SCHEDULED → IN_PROGRESS.
func (p *Placement) Start() error {
	if p.State != PlacementStateScheduled {
		return ErrNotScheduled
	}
	p.State = PlacementStateInProgress
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// Complete transitions IN_PROGRESS → COMPLETED.
func (p *Placement) Complete() error {
	if p.State != PlacementStateInProgress {
		return ErrNotInProgress
	}
	p.State = PlacementStateCompleted
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// Withdraw transitions SCHEDULED or IN_PROGRESS → WITHDRAWN. Stores the
// caller-supplied reason in EvaluatorNotes (append-only; if EvaluatorNotes
// already carries content the reason is appended on a new line, never
// overwriting existing audit text).
func (p *Placement) Withdraw(reason string) error {
	if p.State.isClosed() {
		return ErrPlacementClosed
	}
	reason = strings.TrimSpace(reason)
	if reason != "" {
		marker := "[WITHDRAWN] " + reason
		if p.EvaluatorNotes == "" {
			p.EvaluatorNotes = marker
		} else {
			p.EvaluatorNotes = p.EvaluatorNotes + "\n" + marker
		}
	}
	p.State = PlacementStateWithdrawn
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// RecordHours sets HoursCompleted to hours. Rejected when the placement
// is in a closed state, when hours is negative, or when hours exceeds
// HoursRequired (the latter prevents accidental over-credit; explicit
// over-credit is a separate concern out of scope for v1).
func (p *Placement) RecordHours(hours int) error {
	if p.State.isClosed() {
		return ErrPlacementClosed
	}
	if hours < 0 {
		return ErrHoursNegative
	}
	if hours > p.HoursRequired {
		return ErrHoursExceedsRequired
	}
	p.HoursCompleted = hours
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// SetEvaluatorNotes replaces the evaluator notes. Rejected when the
// trimmed payload exceeds MaxEvaluatorNotesLen.
func (p *Placement) SetEvaluatorNotes(notes string) error {
	notes = strings.TrimSpace(notes)
	if len(notes) > MaxEvaluatorNotesLen {
		return ErrEvaluatorNotesTooLong
	}
	p.EvaluatorNotes = notes
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// -----------------------------------------------------------------------------
// Helpers — kept package-local so wbl does not depend on the delivery package.
// -----------------------------------------------------------------------------

// looksLikeEmail performs a minimal RFC-tolerant email check sufficient
// for the form gate. The authoritative check (DNS / MX) is the
// SendGrid bounce loop's concern.
func looksLikeEmail(s string) bool {
	at := strings.Index(s, "@")
	if at < 1 || at == len(s)-1 {
		return false
	}
	if strings.Contains(s, " ") {
		return false
	}
	if !strings.Contains(s[at+1:], ".") {
		return false
	}
	return true
}

// newUUIDv7 mirrors delivery.NewUUIDv7 to keep the wbl package dep-free.
// Follows RFC 9562 §5.7: unix_ts_ms (48 bits) || ver (4) || rand_a (12) ||
// var (2) || rand_b (62).
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
