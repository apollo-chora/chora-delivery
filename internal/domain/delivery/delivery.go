// Package delivery is the pure-domain core of the Content Delivery service.
//
// It owns the 5 aggregates called out in the M11+ Phase D brief:
//
//   - Course        — primary aggregate; cohort_id, schedule, atom_ids, max_capacity
//   - Class         — single delivery instance of a Course
//   - Booking       — learner registration on a Class
//   - Rostering     — assigns Bookings to time slots
//   - Certification — issued upon completion (append-only)
//
// Hard rules (per .claude/rules/ddd-enforcement.md):
//
//   - LearningAtom is the global aggregate root for content; here we only hold
//     atom_ids[] as opaque cross-domain references (no FK).
//   - All cross-domain references travel as UUIDs.
//   - UUIDv7 IDs (sortable + lexicographically equivalent to creation order).
//   - Soft delete on Course / Class / Booking via DeletedAt.
//   - Certification is append-only — no UPDATE / DELETE methods exist.
//   - Capacity invariant enforced in-memory via mutex; M12 will move to a DB
//     constraint per the brief.
//
// This file contains the domain only — NO HTTP, NO persistence.
package delivery

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	// ErrInvalidArgument signals a guard-clause failure inside a constructor.
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrClassAtCapacity is returned when a booking would push
	// Class.BookingsCount past Class.MaxCapacity.
	ErrClassAtCapacity = errors.New("class at capacity")

	// ErrIllegalStatusTransition is returned for booking state-machine
	// transitions that are not on the allowed paths.
	ErrIllegalStatusTransition = errors.New("illegal booking status transition")

	// ErrCertAlreadyIssued is returned by CertificationRegistry.Issue when
	// the (gcid, course_id) pair has already received a cert.
	ErrCertAlreadyIssued = errors.New("certification already issued")
)

// -----------------------------------------------------------------------------
// UUIDv7 — minimal local generator (deps-free skeleton)
// -----------------------------------------------------------------------------

// NewUUIDv7 returns a freshly generated UUIDv7 string.
//
// We generate UUIDv7 ourselves to keep the skeleton dependency-free
// (the brief says the M11.4 chora-contracts package supplies typed wrappers
// later; this in-memory skeleton avoids importing it). The format follows
// RFC 9562 §5.7: unix_ts_ms (48 bits) || ver (4) || rand_a (12) || var (2)
// || rand_b (62).
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
		// rand.Read failure is exceedingly rare; fall back to time bits.
		// We do not panic so that the service stays up under entropy
		// starvation — every CI environment ships with /dev/urandom.
		for i := 6; i < buflen; i++ {
			b[i] = byte(now >> uint(8*(i-6)))
		}
	}
	// Set version (7) on the high nibble of byte 6.
	b[6] = (b[6] & 0x0F) | 0x70
	// Set IETF variant on the high two bits of byte 8.
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// -----------------------------------------------------------------------------
// Course
// -----------------------------------------------------------------------------

// Course is the primary aggregate root for Content Delivery (per the brief).
// It carries an opaque cohort identifier, a list of LearningAtom IDs, and
// a capacity budget that flows down into every Class scheduled from it.
//
// CJ#2 extension (state-FSM authoring + release; migration 0014):
//   - State           : DRAFT / AWAITING_REVIEW / PUBLISHED / ARCHIVED
//   - AuthorGCID      : the authoring instructor's GCID (set at /create)
//   - Description     : free-form course description
//   - LearningObjectives + PrerequisiteNotes : author-supplied content
//   - TestSetIDs      : cross-aggregate reference into test_sets (CJ#2)
//   - InstructorGCIDs : training-admin-set roster (populated at /release)
//   - PriceSGDCents   : commercial price (set at /release)
//   - SFEligible      : SkillsFuture Singapore funding (set at /release)
//   - ScheduledOpenAt : optional open date (set at /release)
//   - ReviewNotes     : training-admin feedback on /reject
//   - PublishedAt     : when /release transitioned to PUBLISHED
//
// Per CJ#2 directive row at
// `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3.
type Course struct {
	ID             string
	TenantID       string
	InstructorGCID string // GCID of the authoring instructor — required by the
	// `courses.instructor_gcid` column. Empty for legacy in-memory tests
	// that never reach the pgx adapter; callers persisting through the
	// pgx adapter MUST populate this field.
	Title       string
	AtomIDs     []string
	MaxCapacity int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time

	// -------- CJ#2 fields (migration 0014) --------
	State              CourseState
	AuthorGCID         string
	Description        string
	LearningObjectives []string
	PrerequisiteNotes  []string
	TestSetIDs         []string
	InstructorGCIDs    []string
	PriceSGDCents      int64
	SFEligible         bool
	ScheduledOpenAt    *time.Time
	ReviewNotes        string
	PublishedAt        *time.Time

	// Certification — the cert this course awards on completion (CHO-1795).
	// Zero value (Enabled=false) ⇒ no certificate. See course_cert.go.
	Certification CertDefinition
}

// NewCourse constructs a Course aggregate with default values populated.
//
// Validation guards rejected with ErrInvalidArgument:
//   - tenantID required (cross-tenant policy enforced upstream by middleware)
//   - title trim-non-empty
//   - capacity > 0 (the brief requires capacity to be meaningful — Class
//     instances inherit it for booking enforcement).
func NewCourse(tenantID, title string, atomIDs []string, capacity int) (*Course, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("%w: title required", ErrInvalidArgument)
	}
	if capacity <= 0 {
		return nil, fmt.Errorf("%w: capacity must be > 0", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Course{
		ID:          NewUUIDv7(),
		TenantID:    tenantID,
		Title:       title,
		AtomIDs:     append([]string(nil), atomIDs...),
		MaxCapacity: capacity,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// SoftDelete marks the course deleted via DeletedAt; the row is preserved.
// Aligned with .claude/rules/ddd-enforcement.md §5 (soft delete).
func (c *Course) SoftDelete() {
	if c.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	c.DeletedAt = &now
}

// -----------------------------------------------------------------------------
// Booking
// -----------------------------------------------------------------------------

// BookingStatus is the booking lifecycle state.
//
// Allowed transitions:
//
//	pending   -> confirmed
//	confirmed -> attended
//	confirmed -> no-show
//
// Anything else is rejected by TransitionStatus.
type BookingStatus string

const (
	BookingStatusPending   BookingStatus = "pending"
	BookingStatusConfirmed BookingStatus = "confirmed"
	BookingStatusAttended  BookingStatus = "attended"
	BookingStatusNoShow    BookingStatus = "no-show"
)

// Booking is a learner's enrolment on a Class.
type Booking struct {
	ID        string
	ClassID   string
	CourseID  string
	TenantID  string
	LearnerID string // GCID
	Status    BookingStatus
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// NewBookingForClass builds a pending Booking from the PRIMITIVE identity of a
// durable scheduled class (id / course id / tenant), rather than a *Class
// pointer. This keeps the Booking aggregate decoupled from both the legacy
// delivery.Class and the scheduling.ScheduledClass package.
//
// Unlike NewBooking, it does NOT reserve a seat: under ADR-236 D2 the
// class-capacity invariant is enforced DURABLY and cross-pod-correctly at the
// persistence boundary (BookingPort.ReserveSeatAndSave, a single serialised txn
// that counts the class's active bookings under a per-class advisory lock),
// replacing the in-memory, single-pod-only Class.reserveSeat mutex. Callers
// MUST persist via ReserveSeatAndSave (never a bare Save) so the gate fires.
func NewBookingForClass(classID, courseID, tenantID, learnerGCID string) (*Booking, error) {
	if strings.TrimSpace(classID) == "" {
		return nil, fmt.Errorf("%w: class_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(courseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(learnerGCID) == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &Booking{
		ID:        NewUUIDv7(),
		ClassID:   classID,
		CourseID:  courseID,
		TenantID:  tenantID,
		LearnerID: learnerGCID,
		Status:    BookingStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// TransitionStatus advances Booking.Status along the allowed paths.
// Same-status transitions are rejected so consumers always check intent.
func (b *Booking) TransitionStatus(next BookingStatus) error {
	if !isAllowedTransition(b.Status, next) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalStatusTransition, b.Status, next)
	}
	b.Status = next
	b.UpdatedAt = time.Now().UTC()
	return nil
}

func isAllowedTransition(from, to BookingStatus) bool {
	switch from {
	case BookingStatusPending:
		return to == BookingStatusConfirmed
	case BookingStatusConfirmed:
		return to == BookingStatusAttended || to == BookingStatusNoShow
	default:
		return false
	}
}

// SoftDelete marks the booking deleted.
func (b *Booking) SoftDelete() {
	if b.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	b.DeletedAt = &now
}

// -----------------------------------------------------------------------------
// Certification (append-only)
// -----------------------------------------------------------------------------

// Certification is an append-only credential record. The struct exposes
// only a constructor + read-only fields; no UPDATE / DELETE method exists.
type Certification struct {
	ID              string
	TenantID        string
	LearnerID       string
	CourseID        string
	Accomplishments []string
	Hash            string // SHA-256 over (gcid, course_id, accomplishments)
	IssuedAt        time.Time
}

// IssueCertification creates a fresh Certification value.
//
// The hash is deterministic over (learner_gcid, course_id,
// accomplishments) so consumers can verify integrity without having
// to fetch the full record.
func IssueCertification(tenantID, learnerGCID, courseID string, accomplishments []string) (*Certification, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(learnerGCID) == "" {
		return nil, fmt.Errorf("%w: learner_gcid required", ErrInvalidArgument)
	}
	if strings.TrimSpace(courseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	return &Certification{
		ID:              NewUUIDv7(),
		TenantID:        tenantID,
		LearnerID:       learnerGCID,
		CourseID:        courseID,
		Accomplishments: append([]string(nil), accomplishments...),
		Hash:            certHash(learnerGCID, courseID, accomplishments),
		IssuedAt:        time.Now().UTC(),
	}, nil
}

// certHash returns SHA-256 hex of (gcid|course_id|accomplishments_joined).
func certHash(learnerGCID, courseID string, accomplishments []string) string {
	h := sha256.New()
	h.Write([]byte(learnerGCID))
	h.Write([]byte{0})
	h.Write([]byte(courseID))
	h.Write([]byte{0})
	for _, a := range accomplishments {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CertificationRegistry enforces the (gcid, course_id) uniqueness
// invariant across issued Certifications. The HTTP layer wraps this
// with a 409 Conflict response.
type CertificationRegistry struct {
	mu    sync.Mutex
	byKey map[string]*Certification // key = gcid|course_id
	byID  map[string]*Certification
}

// NewCertificationRegistry returns an empty registry.
func NewCertificationRegistry() *CertificationRegistry {
	return &CertificationRegistry{
		byKey: make(map[string]*Certification),
		byID:  make(map[string]*Certification),
	}
}

// Issue creates a cert and rejects duplicates with ErrCertAlreadyIssued.
func (r *CertificationRegistry) Issue(tenantID, learnerGCID, courseID string, accomplishments []string) (*Certification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := learnerGCID + "|" + courseID
	if _, exists := r.byKey[key]; exists {
		return nil, ErrCertAlreadyIssued
	}
	cert, err := IssueCertification(tenantID, learnerGCID, courseID, accomplishments)
	if err != nil {
		return nil, err
	}
	r.byKey[key] = cert
	r.byID[cert.ID] = cert
	return cert, nil
}

// Get returns a Certification by ID or (nil, false) if absent.
func (r *CertificationRegistry) Get(id string) (*Certification, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byID[id]
	return c, ok
}

// GetByLearnerCourse returns the cert (if any) for a (gcid, course) pair.
func (r *CertificationRegistry) GetByLearnerCourse(learnerGCID, courseID string) (*Certification, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byKey[learnerGCID+"|"+courseID]
	return c, ok
}
