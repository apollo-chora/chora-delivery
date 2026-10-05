// Package offering_session owns the OfferingSession aggregate — a scheduled
// delivery session (a class meeting with a time + room) that belongs to a
// delivery Offering.
//
// Scope (R+ Four-Mode, Schedule & Rooms tab): schedule a session against an
// Offering (offering_id, title, room, instructor_gcid, starts_at, ends_at);
// list an offering's sessions. Distinct from scheduling.ScheduledClass, which
// is COURSE-scoped and week-queried; OfferingSession is OFFERING-scoped and the
// stable anchor an Attendance record references (session_id).
//
// Cross-aggregate references (offering_id, instructor_gcid) travel as opaque
// UUID/GCID strings without FK constraints per ddd-enforcement.md invariant #3.
//
// Hexagonal: pure domain, no HTTP, no persistence. UUIDv7 IDs (sortable +
// time-correlated), soft-delete via DeletedAt.
package offering_session

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidArgument signals a guard-clause failure in NewOfferingSession.
var ErrInvalidArgument = errors.New("invalid argument")

// ErrRoomDoubleBooked signals that a session's (tenant, room_id, [starts_at,
// ends_at)) window collides with an existing active session — the DB EXCLUDE
// gate (offering_sessions mig 0052, ratified CHO-2191 / ADR-237) was breached.
// The create handler maps it to HTTP 409. Roomless (room_id NULL) / soft-deleted
// sessions are exempt (an unbooked or cancelled session cannot double-book a
// room). Keyed on room_id — a stable Room identity — NOT the free-text name a
// typo could fork ("Room A " vs "Room A"), which is why slice-1's string gate
// was superseded.
var ErrRoomDoubleBooked = errors.New("room double-booked")

// NewUUIDv7 returns a freshly generated UUIDv7 string per RFC 9562 §5.7. Local
// copy keeps the package dependency-free (mirrors scheduling.NewUUIDv7).
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

// OfferingSession is one scheduled delivery session belonging to an Offering.
//
// Every exported field round-trips through json.Marshal losslessly — the pg
// adapter stores the whole struct as a JSONB snapshot (the scheduled_classes /
// offerings pattern). Structs carry NO json tags, so JSONB keys are the
// PascalCase field names.
type OfferingSession struct {
	ID         string
	TenantID   string
	OfferingID string
	Title      string
	// RoomID is the stable key of the booked Room aggregate (campusops.Room,
	// chora_delivery.rooms). Optional — a roomless session leaves it "" (room_id
	// NULL), exempt from the double-book EXCLUDE (mig 0052). The ratified gate
	// keys on this, NOT the free-text name.
	RoomID string
	// Room is the display name, derived from the booked Room at schedule time
	// (a free-text room supplied without a room_id is REJECTED, not stored).
	Room           string
	InstructorGCID string
	StartsAt       time.Time
	EndsAt         time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
}

// NewOfferingSessionInput is the value-bag for NewOfferingSession.
type NewOfferingSessionInput struct {
	TenantID       string
	OfferingID     string
	Title          string
	RoomID         string // stable Room key; "" = roomless (exempt from the gate)
	Room           string // display name (derived from the Room; never free text)
	InstructorGCID string
	StartsAt       time.Time
	EndsAt         time.Time
}

// NewOfferingSession constructs a session with all guards.
//
// Validation:
//   - tenant_id, offering_id, title all required (non-blank);
//   - ends_at strictly after starts_at.
//
// Room + instructor_gcid are optional (a session may not have a room booked or
// an instructor assigned yet) — empty strings are allowed.
func NewOfferingSession(in NewOfferingSessionInput) (*OfferingSession, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return nil, fmt.Errorf("%w: tenant_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.OfferingID) == "" {
		return nil, fmt.Errorf("%w: offering_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("%w: title required", ErrInvalidArgument)
	}
	if !in.EndsAt.After(in.StartsAt) {
		return nil, fmt.Errorf("%w: ends_at must be after starts_at", ErrInvalidArgument)
	}
	now := time.Now().UTC()
	return &OfferingSession{
		ID:             NewUUIDv7(),
		TenantID:       in.TenantID,
		OfferingID:     in.OfferingID,
		Title:          strings.TrimSpace(in.Title),
		RoomID:         strings.TrimSpace(in.RoomID),
		Room:           strings.TrimSpace(in.Room),
		InstructorGCID: strings.TrimSpace(in.InstructorGCID),
		StartsAt:       in.StartsAt.UTC(),
		EndsAt:         in.EndsAt.UTC(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}
