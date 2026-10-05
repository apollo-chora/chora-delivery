// room_booking.go - the ratified room-booking invariant, owned by the DOMAIN
// (CHO-2299, closing ADR-237 O3).
//
// THE RULE (owner ruling, 2026-07-16): a room is booked by room_id ONLY. A
// free-text room name supplied without a stable room_id is REJECTED, never
// stored. The rejected alternative was auto-provisioning a Room from the typed
// name, because capacity would be unknown (killing the over-capacity check) and
// "Room A " would silently fork from "Room A", minting a second room and
// re-opening the double-book: "a worse failure, because it looks fixed".
//
// WHY THIS FUNCTION EXISTS. The rule was previously asserted in a domain godoc
// (offering_session/session.go: "a free-text room supplied without a room_id is
// REJECTED, not stored") while the only actual enforcement lived in an HTTP
// adapter (offering_schedule_handler.go). The domain constructor had no such
// guard and no test pinned it. CHO-2299 extends the rule to two more aggregates,
// and copying an adapter-level business rule into two more adapters would leave
// three drifting copies of a ratified invariant.
//
// So: the rule lives here, every scheduled-meeting aggregate calls it from its
// constructor, and the adapters return to their proper job of mapping a domain
// error onto a status code (400 room_required).
//
// Roomless is legitimate and stays allowed: a meeting with no room booked leaves
// both fields blank, persists room_id NULL, and is exempt from the double-book
// EXCLUDE.
package campusops

import (
	"errors"
	"strings"
)

// ErrFreeTextRoom is the stable sentinel for the no-free-text-bypass rule.
// Adapters map it to the ratified `room_required` 400 code; they must match on
// this sentinel via errors.Is, never on the message text.
var ErrFreeTextRoom = errors.New("room requires a room_id: a free-text room name is not a booking")

// ValidateRoomBooking enforces the ratified booking invariant for any aggregate
// that books a Room (OfferingSession, ScheduledClass, Section).
//
// Allowed:
//   - roomless: both blank (or whitespace-only)
//   - a room_id, with or without a display name
//
// Rejected:
//   - a display name with no room_id (the typo-bypass)
//
// Note this deliberately does NOT check that the Room exists or belongs to the
// tenant. That is a cross-aggregate fact an aggregate cannot reach, so it stays
// an application-layer concern resolved through a repository port, exactly as
// the offering-schedule lane already does (400 room_not_found).
func ValidateRoomBooking(roomID, displayName string) error {
	if strings.TrimSpace(roomID) != "" {
		return nil
	}
	if strings.TrimSpace(displayName) == "" {
		return nil // roomless
	}
	return ErrFreeTextRoom
}
