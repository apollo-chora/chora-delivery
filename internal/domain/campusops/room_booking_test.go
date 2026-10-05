// room_booking_test.go - the ratified no-free-text-bypass invariant, as a
// DOMAIN rule (CHO-2299, closing ADR-237 O3).
//
// WHY THIS LIVES HERE. Before this, the rule existed in exactly two places:
// prose, and an HTTP adapter. offering_session/session.go:77-79 asserts in a
// godoc that "a free-text room supplied without a room_id is REJECTED, not
// stored", but NewOfferingSession contains no such guard and no test pinned it.
// The actual enforcement sat in offering_schedule_handler.go:131-160, which is a
// business rule in an adapter.
//
// CHO-2299 has to apply the same rule to two more aggregates (ScheduledClass and
// Section). Copying an adapter-level rule into two more adapters would triple a
// layering violation and leave three copies free to drift. So the invariant
// becomes one domain function that every scheduled-meeting aggregate calls, and
// the adapters go back to their real job: mapping a domain error to a status
// code.
//
// The rule itself is the owner's ratified ruling (2026-07-16): a room is booked
// by room_id ONLY. A free-text name is the typo-bypass that was rejected,
// because "Room A " and "Room A" silently fork into two rooms and re-open the
// double-book, "a worse failure, because it looks fixed".
package campusops_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

const bookingRoomID = "01985e7f-6666-7abc-8def-0000000000f9"

// Roomless is legitimate: a class or session may simply have no room booked.
// It is exempt from the double-book gate (room_id NULL).
func TestValidateRoomBooking_RoomlessIsAllowed(t *testing.T) {
	t.Parallel()
	if err := campusops.ValidateRoomBooking("", ""); err != nil {
		t.Fatalf("a roomless booking must be allowed; got %v", err)
	}
}

// Whitespace-only input is roomless, not a free-text name. Without this, a
// stray space would trip the rejection and block a legitimate roomless booking.
func TestValidateRoomBooking_WhitespaceOnlyIsRoomless(t *testing.T) {
	t.Parallel()
	if err := campusops.ValidateRoomBooking("   ", "  \t "); err != nil {
		t.Fatalf("whitespace-only must be treated as roomless; got %v", err)
	}
}

func TestValidateRoomBooking_RoomIDWithDisplayNameIsAllowed(t *testing.T) {
	t.Parallel()
	if err := campusops.ValidateRoomBooking(bookingRoomID, "Lab A"); err != nil {
		t.Fatalf("a room_id with its display name must be allowed; got %v", err)
	}
}

// A room_id with no display name is fine: the name is derived at read time.
func TestValidateRoomBooking_RoomIDWithoutDisplayNameIsAllowed(t *testing.T) {
	t.Parallel()
	if err := campusops.ValidateRoomBooking(bookingRoomID, ""); err != nil {
		t.Fatalf("a room_id alone must be allowed; got %v", err)
	}
}

// THE LOAD-BEARING ONE. This is the typo-bypass the owner rejected, and the
// case the domain previously did not guard at all.
func TestValidateRoomBooking_FreeTextWithoutRoomIDIsRejected(t *testing.T) {
	t.Parallel()
	err := campusops.ValidateRoomBooking("", "Room A")
	if err == nil {
		t.Fatalf("a free-text room name with no room_id must be REJECTED; got nil. " +
			"This is the typo-bypass that forks 'Room A ' from 'Room A' and re-opens the double-book")
	}
	if !errors.Is(err, campusops.ErrFreeTextRoom) {
		t.Fatalf("expected ErrFreeTextRoom so adapters can map it to a stable code; got %v", err)
	}
}

// The trailing-space case is the concrete failure the ruling cites by name.
func TestValidateRoomBooking_TrailingSpaceNameStillRejected(t *testing.T) {
	t.Parallel()
	if err := campusops.ValidateRoomBooking("", "Room A "); !errors.Is(err, campusops.ErrFreeTextRoom) {
		t.Fatalf("'Room A ' with no room_id must be rejected; got %v", err)
	}
}

// The error must be identifiable by sentinel, not by string matching, so the
// HTTP layer can map it to the ratified `room_required` code without coupling to
// wording.
func TestErrFreeTextRoom_IsAStableSentinel(t *testing.T) {
	t.Parallel()
	if campusops.ErrFreeTextRoom == nil {
		t.Fatalf("ErrFreeTextRoom must exist as a stable sentinel")
	}
	wrapped := errors.Join(errors.New("context"), campusops.ErrFreeTextRoom)
	if !errors.Is(wrapped, campusops.ErrFreeTextRoom) {
		t.Fatalf("ErrFreeTextRoom must survive wrapping")
	}
}
