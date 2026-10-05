// Package exam owns the Exam aggregate (proctored exam sitting).
//
// Hexagonal layout: pure-domain core for R+ Stage 3 wave 2 — the R+
// /r/exams admin surface. The aggregate models a SkillsFuture-aligned
// proctored sitting attached to a Course.
//
// The aggregate is intentionally separate from the `delivery` domain
// package so the R+ exams track is ADD-ONLY (no edits to
// internal/domain/delivery/* during this build-out). Cross-aggregate
// references (course_id) travel as opaque UUIDs — no Go-level FK,
// per .claude/rules/ddd-enforcement.md aggregate invariants.
//
// State machine (FSM):
//
//	DRAFT      → SCHEDULED   via Schedule (admin scheduling)
//	SCHEDULED  → OPEN        via Open     (registration opens)
//	SCHEDULED  → CLOSED      via Close    (admin cancel before opening)
//	OPEN       → CLOSED      via Close    (registration closes / sitting ends)
//	CLOSED     → GRADED      via Grade    (proctor + auto-grade complete)
//
// Capacity invariant: Enroll guards EnrolledCount <= Capacity in-memory;
// the M12+ pg adapter will mirror the guard as a CHECK constraint.
//
// Proctor methods (locked vocabulary per the R+ build-out plan):
//
//	PROCTOR_METHOD_AUTO_AI         — fully automated AI proctoring
//	PROCTOR_METHOD_HUMAN_LIVE      — live human proctor (synchronous)
//	PROCTOR_METHOD_HUMAN_RECORDED  — human review of recording (asynchronous)
//
// This file contains the domain only — NO HTTP, NO persistence imports.
package exam

import (
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// State + ProctorMethod
// -----------------------------------------------------------------------------

// ExamState models the Exam sitting lifecycle FSM.
type ExamState string

const (
	// ExamStateDraft — admin is still authoring the sitting.
	ExamStateDraft ExamState = "DRAFT"
	// ExamStateScheduled — date/duration/capacity locked; registration not yet open.
	ExamStateScheduled ExamState = "SCHEDULED"
	// ExamStateOpen — registration open; enrolments accepted up to capacity.
	ExamStateOpen ExamState = "OPEN"
	// ExamStateClosed — registration closed or sitting has ended; no more enrolments.
	ExamStateClosed ExamState = "CLOSED"
	// ExamStateGraded — grading complete; certifications can issue from here.
	ExamStateGraded ExamState = "GRADED"
)

// IsValid reports whether s is one of the canonical Exam states.
func (s ExamState) IsValid() bool {
	switch s {
	case ExamStateDraft, ExamStateScheduled, ExamStateOpen,
		ExamStateClosed, ExamStateGraded:
		return true
	}
	return false
}

// ProctorMethod identifies the proctoring approach for the sitting.
type ProctorMethod string

const (
	// ProctorMethodAutoAI — fully automated AI proctoring.
	ProctorMethodAutoAI ProctorMethod = "PROCTOR_METHOD_AUTO_AI"
	// ProctorMethodHumanLive — live human proctor (synchronous).
	ProctorMethodHumanLive ProctorMethod = "PROCTOR_METHOD_HUMAN_LIVE"
	// ProctorMethodHumanRecorded — human reviews the recording later (async).
	ProctorMethodHumanRecorded ProctorMethod = "PROCTOR_METHOD_HUMAN_RECORDED"
)

// IsValid reports whether pm is one of the canonical proctor methods.
func (pm ProctorMethod) IsValid() bool {
	switch pm {
	case ProctorMethodAutoAI, ProctorMethodHumanLive, ProctorMethodHumanRecorded:
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrExamTenantRequired — tenant_id must be non-empty.
	ErrExamTenantRequired = errors.New("exam: tenant_id required")
	// ErrExamCourseRequired — course_id must be non-empty.
	ErrExamCourseRequired = errors.New("exam: course_id required")
	// ErrExamCourseInvalid - course_id must be a well-formed UUID. Distinct
	// from ErrExamCourseRequired so the 400 tells an admin whether they OMITTED
	// the course or MISTYPED it.
	ErrExamCourseInvalid = errors.New("exam: course_id must be a UUID")
	// ErrExamTitleRequired — title must be trim-non-empty.
	ErrExamTitleRequired = errors.New("exam: title required")
	// ErrExamDurationInvalid — duration_minutes must be > 0.
	ErrExamDurationInvalid = errors.New("exam: duration_minutes must be > 0")
	// ErrExamCapacityInvalid — capacity must be > 0.
	ErrExamCapacityInvalid = errors.New("exam: capacity must be > 0")
	// ErrExamProctorMethodInvalid — proctor_method must be one of the
	// canonical PROCTOR_METHOD_* values.
	ErrExamProctorMethodInvalid = errors.New("exam: proctor_method invalid")
	// ErrExamNotDraft — operation requires DRAFT state.
	ErrExamNotDraft = errors.New("exam: not in DRAFT state")
	// ErrExamNotScheduled — operation requires SCHEDULED state.
	ErrExamNotScheduled = errors.New("exam: not in SCHEDULED state")
	// ErrExamNotCloseable — Close requires OPEN or SCHEDULED state.
	ErrExamNotCloseable = errors.New("exam: state is not closeable (must be OPEN or SCHEDULED)")
	// ErrExamNotClosed — Grade requires CLOSED state.
	ErrExamNotClosed = errors.New("exam: not in CLOSED state")
	// ErrExamNotOpen — Enroll requires OPEN state.
	ErrExamNotOpen = errors.New("exam: not in OPEN state")
	// ErrExamAtCapacity — Enroll would push EnrolledCount past Capacity.
	ErrExamAtCapacity = errors.New("exam: at capacity")
)

// -----------------------------------------------------------------------------
// Aggregate
// -----------------------------------------------------------------------------

// Exam is the proctored exam-sitting aggregate root.
//
// Cross-domain references travel as opaque UUIDs (CourseID has no FK).
// Soft delete via DeletedAt; transitions advance UpdatedAt.
type Exam struct {
	ID              string
	TenantID        string
	CourseID        string
	Title           string
	ScheduledAt     time.Time
	DurationMinutes int
	Capacity        int
	EnrolledCount   int
	State           ExamState
	ProctorMethod   ProctorMethod
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// NewExamInput is the constructor input bag.
type NewExamInput struct {
	TenantID        string
	CourseID        string
	Title           string
	ScheduledAt     time.Time
	DurationMinutes int
	Capacity        int
	ProctorMethod   ProctorMethod
}

// NewExam constructs a DRAFT-state Exam shell.
//
// Validation guards (rejected with specific sentinel):
//   - tenant_id trim-non-empty
//   - course_id trim-non-empty AND a well-formed UUID (stored canonical)
//   - title trim-non-empty
//   - duration_minutes > 0
//   - capacity > 0
//   - proctor_method either empty (defaults to AUTO_AI) or one of the
//     canonical PROCTOR_METHOD_* values
//
// Returned exam is in ExamStateDraft, with current UTC timestamps.
// The aggregate ID is freshly generated UUIDv7.
//
// # Why course_id is shape-checked HERE
//
// A live R+ walk scheduled an exam with course_id "course-cspo" (the R+ form's
// own placeholder hint suggested that shape). It was accepted, because `exams`
// keeps course_id inside its JSONB `data` column (mig 0020) where Postgres has
// no typed column to reject it. The value then sat inertly until a candidate
// PASSED, at which point the cert engine tried to anchor a Certification on it
// and `certifications.course_id UUID NOT NULL` (mig 0001) refused with SQLSTATE
// 22P02 - inside a Pub/Sub subscriber, which NACK-retried it into the DLQ. The
// credential was earned and never minted.
//
// So there is NO database guard behind this constructor for course_id: JSONB
// accepts any string. This function is the only chokepoint every creation path
// crosses, which makes it the only place the invariant can actually hold.
func NewExam(in NewExamInput) (*Exam, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, ErrExamTenantRequired
	}
	courseID, err := canonicalCourseID(in.CourseID)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, ErrExamTitleRequired
	}
	if in.DurationMinutes <= 0 {
		return nil, ErrExamDurationInvalid
	}
	if in.Capacity <= 0 {
		return nil, ErrExamCapacityInvalid
	}
	pm := in.ProctorMethod
	if pm == "" {
		pm = ProctorMethodAutoAI
	}
	if !pm.IsValid() {
		return nil, ErrExamProctorMethodInvalid
	}
	now := time.Now().UTC()
	return &Exam{
		ID:              newUUIDv7(),
		TenantID:        strings.TrimSpace(in.TenantID),
		CourseID:        courseID,
		Title:           title,
		ScheduledAt:     in.ScheduledAt.UTC(),
		DurationMinutes: in.DurationMinutes,
		Capacity:        in.Capacity,
		EnrolledCount:   0,
		State:           ExamStateDraft,
		ProctorMethod:   pm,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// canonicalCourseID validates the opaque course reference and returns it in
// canonical UUID form, or a sentinel describing which mistake was made.
//
// "Opaque" constrains the reference's MEANING (this domain never dereferences a
// course_id, per the no-Go-level-FK rule) - it does not license an arbitrary
// SHAPE. Downstream, chora_delivery's own certifications.course_id is UUID
// NOT NULL, so a non-UUID here is a delayed-action fault, not a free-form label.
//
// It returns the CANONICAL form rather than the raw input on purpose.
// uuid.Parse is deliberately lenient: it accepts "urn:uuid:<uuid>", "{<uuid>}"
// and the 32-char dashless form, and Postgres accepts only two of those three
// (the urn: prefix is a 22P02 there). Validating with Parse but storing the raw
// text would therefore leave the exact bug this guard exists to stop still
// reachable, just through a narrower door. Normalising to uuid.String() closes
// it: whatever shape an admin pastes, what gets STORED is the one form every
// downstream uuid cast accepts.
func canonicalCourseID(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrExamCourseRequired
	}
	u, err := uuid.Parse(trimmed)
	if err != nil {
		return "", ErrExamCourseInvalid
	}
	return u.String(), nil
}

// -----------------------------------------------------------------------------
// State transitions
// -----------------------------------------------------------------------------

// Schedule transitions DRAFT → SCHEDULED.
func (e *Exam) Schedule() error {
	if e.State != ExamStateDraft {
		return ErrExamNotDraft
	}
	e.State = ExamStateScheduled
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// Open transitions SCHEDULED → OPEN (registration opens).
func (e *Exam) Open() error {
	if e.State != ExamStateScheduled {
		return ErrExamNotScheduled
	}
	e.State = ExamStateOpen
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// Close transitions OPEN | SCHEDULED → CLOSED. Two legitimate entry points:
//   - registration closes / sitting ends (OPEN  → CLOSED)
//   - admin cancel before opening      (SCHEDULED → CLOSED)
func (e *Exam) Close() error {
	if e.State != ExamStateOpen && e.State != ExamStateScheduled {
		return ErrExamNotCloseable
	}
	e.State = ExamStateClosed
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// Grade transitions CLOSED → GRADED.
func (e *Exam) Grade() error {
	if e.State != ExamStateClosed {
		return ErrExamNotClosed
	}
	e.State = ExamStateGraded
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// Enroll increments EnrolledCount by 1, guarded by State == OPEN and
// EnrolledCount < Capacity. Returns ErrExamNotOpen / ErrExamAtCapacity.
func (e *Exam) Enroll() error {
	if e.State != ExamStateOpen {
		return ErrExamNotOpen
	}
	if e.EnrolledCount >= e.Capacity {
		return ErrExamAtCapacity
	}
	e.EnrolledCount++
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// -----------------------------------------------------------------------------
// UUIDv7
// -----------------------------------------------------------------------------

// newUUIDv7 returns a freshly generated UUIDv7 string. Mirrors
// delivery.NewUUIDv7 — duplicated here to keep the exam package free
// of cross-package coupling. RFC 9562 §5.7 layout:
//
//	unix_ts_ms (48b) | ver (4b) | rand_a (12b) | var (2b) | rand_b (62b)
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
		// Fail loud — caller must surface as 500.
		panic("exam: rand.Read failed for UUIDv7: " + err.Error())
	}
	// Version 7 in the high nibble of byte 6.
	b[6] = (b[6] & 0x0F) | 0x70
	// Variant 10 in the high two bits of byte 8.
	b[8] = (b[8] & 0x3F) | 0x80
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	dashes := map[int]bool{8: true, 13: true, 18: true, 23: true}
	si := 0
	for i := 0; i < 36; i++ {
		if dashes[i] {
			out[i] = '-'
			continue
		}
		if si%2 == 0 {
			out[i] = hex[b[si/2]>>4]
		} else {
			out[i] = hex[b[si/2]&0x0F]
		}
		si++
	}
	return string(out)
}
