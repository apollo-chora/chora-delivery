// Package scheduling owns scheduled-class extensions for chora-delivery.
//
// Scope (per the M11+ Phase D extend brief):
//   - Schedule a Class against a Course (course_id, instructor_gcid, room,
//     starts_at, ends_at, capacity).
//   - Reschedule (swap times + room).
//   - Soft-delete (cancel).
//   - ISO-8601 week computation for week-view listings.
//
// Distinct from internal/domain/delivery (the in-flight catalogue + enrollment
// agent's namespace) — this package owns ONLY scheduling concerns. Cross-
// domain references (course_id) travel as opaque UUIDs without FK constraints
// per .claude/rules/ddd-enforcement.md aggregate-invariant #3.
//
// Hexagonal: pure domain, no HTTP, no persistence. UUIDv7 IDs (sortable +
// time-correlated), soft-delete via DeletedAt, mutex-guarded mutations.
package scheduling

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

var (
	// ErrInvalidArgument signals a guard-clause failure.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrInvalidISOWeek is returned by ParseISOWeek for malformed input.
	ErrInvalidISOWeek = errors.New("invalid ISO week format (expected YYYY-Www)")
	// ErrRoomDoubleBooked signals that a class's (tenant, room_id, [starts_at,
	// ends_at)) window overlaps an existing ACTIVE, roomed class in the same room.
	//
	// CHO-2299 / ADR-237 O3. ADR-236 D2 designates ScheduledClass and
	// OfferingSession as the two sanctioned durable scheduled-meeting models, but
	// ADR-237's gate covered only offering_sessions, so the ratified
	// no-double-book invariant was evadable by booking through /r/scheduling.
	//
	// This is a SET-level invariant ACROSS aggregates, so no single aggregate can
	// enforce it: the authority is a DB EXCLUDE (mig 0059) and the pg adapter
	// translates SQLSTATE 23P01 into this sentinel, exactly as offering_session
	// already does.
	ErrRoomDoubleBooked = errors.New("room double-booked")
)

// -----------------------------------------------------------------------------
// UUIDv7 — local generator (deps-free; mirrors internal/domain/delivery)
// -----------------------------------------------------------------------------

// NewUUIDv7 returns a freshly generated UUIDv7 string per RFC 9562 §5.7.
//
// Local copy keeps the package dependency-free in line with the M11.4
// chora-contracts boundary deferral. The wider monorepo will adopt a typed
// `uuidv7` helper once contracts ship.
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

// -----------------------------------------------------------------------------
// ScheduledClass aggregate
// -----------------------------------------------------------------------------

// ScheduledClass is one delivery instance scheduled against a Course.
//
// Differs from the existing internal/domain/delivery.Class by being
// self-contained — it does NOT take a *Course pointer. We store the
// course_id as a cross-aggregate UUID reference per ddd-enforcement.md
// aggregate-invariant #3.
type ScheduledClass struct {
	ID             string
	TenantID       string
	CourseID       string
	InstructorGCID string
	// RoomID is the stable key of the booked Room aggregate (campusops.Room,
	// chora_delivery.rooms), a cross-aggregate UUID reference with no FK per
	// ddd-enforcement #3. Optional: "" means roomless, which is exempt from the
	// double-book gate. The ratified gate keys on THIS, never on the name below
	// (ADR-237; a typo in a name forks a second room and looks fixed).
	RoomID string
	// Room is the display name derived from the booked Room. It is never a
	// free-text booking key: a name without a RoomID is rejected by
	// campusops.ValidateRoomBooking.
	Room        string
	StartsAt    time.Time
	EndsAt      time.Time
	MaxCapacity int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time

	mu sync.Mutex
}

// NewScheduledClassInput is the value-bag for NewScheduledClass. It replaces the
// former positional signature so the room booking travels as a pair (RoomID +
// display name) and cannot be transposed with the other string arguments.
type NewScheduledClassInput struct {
	TenantID       string
	CourseID       string
	InstructorGCID string
	// RoomID is the stable Room key; "" = roomless (exempt from the gate).
	RoomID string
	// Room is the display name; a name without a RoomID is REJECTED.
	Room        string
	StartsAt    time.Time
	EndsAt      time.Time
	MaxCapacity int
}

// NewScheduledClass constructs a ScheduledClass with all guards.
//
// Validation:
//   - tenant_id, course_id, instructor_gcid all required (non-blank)
//   - capacity > 0 (matches Course.MaxCapacity invariant in delivery pkg)
//   - ends_at strictly after starts_at
//   - the ratified room booking: a free-text room name with no room_id is
//     REJECTED (campusops.ErrFreeTextRoom). Roomless is allowed.
//
// CHO-2299: room was previously unvalidated here entirely. It was required only
// by the browser's canCreate gate, so a direct API call with room:"" was
// accepted and a typed room name became an unkeyed booking that the ADR-237
// double-book gate could not see.
func NewScheduledClass(in NewScheduledClassInput) (*ScheduledClass, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.CourseID) == "" {
		return nil, fmt.Errorf("%w: course_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.InstructorGCID) == "" {
		return nil, fmt.Errorf("%w: instructor_gcid required", ErrInvalidArgument)
	}
	if in.MaxCapacity <= 0 {
		return nil, fmt.Errorf("%w: capacity must be > 0", ErrInvalidArgument)
	}
	if !in.EndsAt.After(in.StartsAt) {
		return nil, fmt.Errorf("%w: ends_at must be after starts_at", ErrInvalidArgument)
	}
	if err := campusops.ValidateRoomBooking(in.RoomID, in.Room); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &ScheduledClass{
		ID:             NewUUIDv7(),
		TenantID:       in.TenantID,
		CourseID:       in.CourseID,
		InstructorGCID: in.InstructorGCID,
		RoomID:         strings.TrimSpace(in.RoomID),
		Room:           strings.TrimSpace(in.Room),
		StartsAt:       in.StartsAt.UTC(),
		EndsAt:         in.EndsAt.UTC(),
		MaxCapacity:    in.MaxCapacity,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Reschedule swaps the time window + room. The new range is validated.
// UpdatedAt is bumped to the call instant.
func (s *ScheduledClass) Reschedule(starts, ends time.Time, roomID, room string) error {
	if !ends.After(starts) {
		return fmt.Errorf("%w: ends_at must be after starts_at", ErrInvalidArgument)
	}
	// Validate BEFORE taking the lock or mutating: a rejected reschedule must
	// leave the aggregate exactly as it was.
	if err := campusops.ValidateRoomBooking(roomID, room); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StartsAt = starts.UTC()
	s.EndsAt = ends.UTC()
	s.RoomID = strings.TrimSpace(roomID)
	s.Room = strings.TrimSpace(room)
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// SoftDelete marks the class deleted; idempotent (preserves first DeletedAt).
func (s *ScheduledClass) SoftDelete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	s.DeletedAt = &now
}

// -----------------------------------------------------------------------------
// ISO-8601 week — used by GET /classes?week=YYYY-WW
// -----------------------------------------------------------------------------

// ISOWeek formats t as "YYYY-WNN" using ISO-8601 week-numbering.
func ISOWeek(t time.Time) string {
	y, wk := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, wk)
}

// ParseISOWeek parses "YYYY-WNN" → (year, week, error).
//
// Validation:
//   - exact format with literal "-W" separator
//   - week ∈ [1, 53] (ISO-8601 max week is 53; we do not bound by year)
//   - 4-digit year, 2-digit week.
func ParseISOWeek(s string) (int, int, error) {
	if len(s) != 8 || s[4] != '-' || s[5] != 'W' {
		return 0, 0, ErrInvalidISOWeek
	}
	y, err := strconv.Atoi(s[:4])
	if err != nil {
		return 0, 0, ErrInvalidISOWeek
	}
	wk, err := strconv.Atoi(s[6:])
	if err != nil {
		return 0, 0, ErrInvalidISOWeek
	}
	if wk < 1 || wk > 53 {
		return 0, 0, ErrInvalidISOWeek
	}
	return y, wk, nil
}

// InISOWeek reports whether the class's StartsAt falls in (year, week).
func InISOWeek(c *ScheduledClass, year, week int) bool {
	cy, cw := c.StartsAt.UTC().ISOWeek()
	return cy == year && cw == week
}
