// offering_schedule_room_gate_test.go — CHO-2191 SP2 (ratified room_id gate).
//
// The schedule POST enforces the room_id-keyed invariants that supersede the
// slice-1 free-text `room` gate:
//   - room_id double-book  → 409 room_double_booked (DB EXCLUDE, mirrored inmem)
//   - over-capacity        → 409 room_over_capacity (offering seat budget > room)
//   - free-text, no room_id→ 400 room_required (the typo-bypass the owner rejected)
//   - room_id not a room   → 400 room_not_found (no auto-provisioned phantom room)
//   - roomless sessions    → never collide (room_id NULL is exempt)
//
// A room is booked by room_id ONLY — a picker sends it (SP3). Mirrors the
// offering_schedule_handler_test.go admin-gated harness.
package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// newRoomGateServer wires Offerings + OfferingSessions + Rooms and seeds one
// offering under the standard tenant with seat budget offCap (0 = unbounded).
// Returns the server + the room repo so a test can seed rooms with capacities.
func newRoomGateServer(t *testing.T, offeringID string, offCap int) (http.Handler, *repoinmem.RoomRepo) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	sessRepo := repoinmem.NewOfferingSessionRepo()
	roomRepo := repoinmem.NewRoomRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:        oRepo,
		OfferingSessions: sessRepo,
		Rooms:            roomRepo,
	})
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{"01985e7f-6666-7abc-8def-000000000a01"},
		DeliveryType: delivery.DeliveryTypeShort,
		Label:        "Gate Run",
		Capacity:     offCap,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = offeringID
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
	return srv, roomRepo
}

// seedGateRoom stores a room with the given capacity under the standard tenant
// and returns its id (the room_id the schedule POST books against).
func seedGateRoom(t *testing.T, roomRepo *repoinmem.RoomRepo, name string, capacity int) string {
	t.Helper()
	room, err := campusops.NewRoom(campusops.NewRoomInput{TenantID: tenantID, Name: name, Capacity: capacity})
	if err != nil {
		t.Fatalf("NewRoom: %v", err)
	}
	if err := roomRepo.Save(context.Background(), room); err != nil {
		t.Fatalf("Save room: %v", err)
	}
	return room.ID
}

// sessionBodyRoomID is the ratified POST body: a room is booked by room_id.
func sessionBodyRoomID(title, roomID string, start, end time.Time) string {
	return `{"title":"` + title + `","room_id":"` + roomID +
		`","instructor_gcid":"` + instructor +
		`","starts_at":"` + start.UTC().Format(time.RFC3339) +
		`","ends_at":"` + end.UTC().Format(time.RFC3339) + `"}`
}

// -----------------------------------------------------------------------------
// Double-book — keyed on room_id (a physical room can't host two at once).

func TestOfferingSchedule_RoomID_DoubleBook_409(t *testing.T) {
	srv, rooms := newRoomGateServer(t, oschOfferingID, 0)
	roomID := seedGateRoom(t, rooms, "Room-A", 30)
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)

	first := postSession(t, srv, oschOfferingID, "instructor", sessionBodyRoomID("A", roomID, start, start.Add(2*time.Hour)))
	if first.Code != http.StatusCreated {
		t.Fatalf("first: %d body=%q", first.Code, first.Body.String())
	}
	// overlapping window, SAME room_id → 409 room_double_booked
	second := postSession(t, srv, oschOfferingID, "instructor", sessionBodyRoomID("B", roomID, start.Add(time.Hour), start.Add(3*time.Hour)))
	if second.Code != http.StatusConflict {
		t.Fatalf("want 409 double-book, got %d body=%q", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "room_double_booked") {
		t.Fatalf("want typed code room_double_booked, got %q", second.Body.String())
	}
}

func TestOfferingSchedule_RoomID_DifferentRoomOK(t *testing.T) {
	srv, rooms := newRoomGateServer(t, oschOfferingID, 0)
	roomA := seedGateRoom(t, rooms, "Room-A", 30)
	roomB := seedGateRoom(t, rooms, "Room-B", 30)
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)
	if w := postSession(t, srv, oschOfferingID, "instructor", sessionBodyRoomID("A", roomA, start, start.Add(2*time.Hour))); w.Code != http.StatusCreated {
		t.Fatalf("first: %d", w.Code)
	}
	// same window, DIFFERENT room_id → allowed
	if w := postSession(t, srv, oschOfferingID, "instructor", sessionBodyRoomID("B", roomB, start, start.Add(2*time.Hour))); w.Code != http.StatusCreated {
		t.Fatalf("different room must be allowed, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestOfferingSchedule_RoomID_AbuttingOK(t *testing.T) {
	srv, rooms := newRoomGateServer(t, oschOfferingID, 0)
	roomID := seedGateRoom(t, rooms, "Room-A", 30)
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)
	if w := postSession(t, srv, oschOfferingID, "instructor", sessionBodyRoomID("A", roomID, start, start.Add(2*time.Hour))); w.Code != http.StatusCreated {
		t.Fatalf("first: %d", w.Code)
	}
	// abutting: next starts exactly when the prev ends → half-open, allowed
	if w := postSession(t, srv, oschOfferingID, "instructor", sessionBodyRoomID("B", roomID, start.Add(2*time.Hour), start.Add(4*time.Hour))); w.Code != http.StatusCreated {
		t.Fatalf("abutting must be allowed, got %d body=%q", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Over-capacity — the offering's seat budget must not exceed the room's size.

func TestOfferingSchedule_OverCapacity_409(t *testing.T) {
	srv, rooms := newRoomGateServer(t, oschOfferingID, 25) // offering seat budget 25
	roomID := seedGateRoom(t, rooms, "Small", 20)          // room holds 20
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)
	w := postSession(t, srv, oschOfferingID, "instructor", sessionBodyRoomID("A", roomID, start, start.Add(2*time.Hour)))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 over-capacity, got %d body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "room_over_capacity") {
		t.Fatalf("want typed code room_over_capacity, got %q", w.Body.String())
	}
}

func TestOfferingSchedule_Capacity_EqualOK(t *testing.T) {
	srv, rooms := newRoomGateServer(t, oschOfferingID, 20) // budget 20
	roomID := seedGateRoom(t, rooms, "Exact", 20)          // room 20 → fits exactly
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)
	if w := postSession(t, srv, oschOfferingID, "instructor", sessionBodyRoomID("A", roomID, start, start.Add(2*time.Hour))); w.Code != http.StatusCreated {
		t.Fatalf("equal capacity must fit, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestOfferingSchedule_Capacity_UnboundedOfferingExempt(t *testing.T) {
	srv, rooms := newRoomGateServer(t, oschOfferingID, 0) // 0 = unbounded, exempt
	roomID := seedGateRoom(t, rooms, "Tiny", 5)
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)
	if w := postSession(t, srv, oschOfferingID, "instructor", sessionBodyRoomID("A", roomID, start, start.Add(2*time.Hour))); w.Code != http.StatusCreated {
		t.Fatalf("unbounded offering must be exempt, got %d body=%q", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// No free-text bypass + no auto-provision — a room is booked by room_id ONLY.

func TestOfferingSchedule_FreeTextRoom_NoRoomID_400(t *testing.T) {
	srv, _ := newRoomGateServer(t, oschOfferingID, 0)
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)
	// free-text room, NO room_id → the typo-bypass the owner rejected.
	body := `{"title":"X","room":"Room A ","instructor_gcid":"` + instructor +
		`","starts_at":"` + start.UTC().Format(time.RFC3339) +
		`","ends_at":"` + start.Add(2*time.Hour).UTC().Format(time.RFC3339) + `"}`
	w := postSession(t, srv, oschOfferingID, "instructor", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 room_required, got %d body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "room_required") {
		t.Fatalf("want typed code room_required, got %q", w.Body.String())
	}
}

func TestOfferingSchedule_RoomID_NotFound_400(t *testing.T) {
	srv, _ := newRoomGateServer(t, oschOfferingID, 0) // no rooms seeded
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)
	// well-formed room_id that resolves to no room → no phantom auto-provision.
	w := postSession(t, srv, oschOfferingID, "instructor",
		sessionBodyRoomID("X", "01985e7f-6666-7abc-8def-0000000000ff", start, start.Add(2*time.Hour)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 room_not_found, got %d body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "room_not_found") {
		t.Fatalf("want typed code room_not_found, got %q", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Roomless sessions never collide — room_id NULL is exempt from the gate.

func TestOfferingSchedule_Roomless_NeverCollide(t *testing.T) {
	srv, _ := newRoomGateServer(t, oschOfferingID, 0)
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)
	roomless := func(title string) string {
		return `{"title":"` + title + `","instructor_gcid":"` + instructor +
			`","starts_at":"` + start.UTC().Format(time.RFC3339) +
			`","ends_at":"` + start.Add(2*time.Hour).UTC().Format(time.RFC3339) + `"}`
	}
	if w := postSession(t, srv, oschOfferingID, "instructor", roomless("A")); w.Code != http.StatusCreated {
		t.Fatalf("first roomless: %d body=%q", w.Code, w.Body.String())
	}
	// second roomless session, same window → allowed (no shared room resource).
	if w := postSession(t, srv, oschOfferingID, "instructor", roomless("B")); w.Code != http.StatusCreated {
		t.Fatalf("roomless must never collide, got %d body=%q", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Wiring: booking a room_id needs the Rooms catalogue wired.

func TestOfferingSchedule_RoomID_503WhenRoomsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	sessRepo := repoinmem.NewOfferingSessionRepo()
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, OfferingSessions: sessRepo}) // Rooms nil
	o, _ := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID: tenantID, CourseIDs: []string{"01985e7f-6666-7abc-8def-000000000a01"},
		DeliveryType: delivery.DeliveryTypeShort, Label: "NoRooms", Capacity: 0,
	})
	o.ID = oschOfferingID
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("seed offering: %v", err)
	}
	start := time.Date(2027, 9, 1, 10, 0, 0, 0, time.UTC)
	w := postSession(t, srv, oschOfferingID, "instructor",
		sessionBodyRoomID("X", "01985e7f-6666-7abc-8def-0000000000ff", start, start.Add(2*time.Hour)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("booking a room_id with Rooms unwired must 503, got %d body=%q", w.Code, w.Body.String())
	}
}
