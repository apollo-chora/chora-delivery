package offering_session

import (
	"errors"
	"testing"
	"time"
)

func validInput() NewOfferingSessionInput {
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	return NewOfferingSessionInput{
		TenantID:       "11111111-1111-7111-8111-111111111111",
		OfferingID:     "01985e7f-6666-7abc-8def-0000000000f1",
		Title:          "Week 1 — Intro",
		RoomID:         "01985e7f-6666-7abc-8def-0000000000aa",
		Room:           "Room 204",
		InstructorGCID: "00000000-0000-7000-8000-000000001999",
		StartsAt:       start,
		EndsAt:         start.Add(2 * time.Hour),
	}
}

func TestNewOfferingSession_OK(t *testing.T) {
	s, err := NewOfferingSession(validInput())
	if err != nil {
		t.Fatalf("NewOfferingSession: %v", err)
	}
	if s.ID == "" {
		t.Fatal("ID not generated")
	}
	if s.Title != "Week 1 — Intro" || s.Room != "Room 204" {
		t.Fatalf("fields: got title=%q room=%q", s.Title, s.Room)
	}
	if s.RoomID != "01985e7f-6666-7abc-8def-0000000000aa" {
		t.Fatalf("RoomID not carried: got %q", s.RoomID)
	}
	if !s.StartsAt.Equal(validInput().StartsAt.UTC()) {
		t.Fatalf("StartsAt normalisation: got %v", s.StartsAt)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		t.Fatal("timestamps not stamped")
	}
	if s.DeletedAt != nil {
		t.Fatal("DeletedAt should be nil on a fresh session")
	}
}

func TestNewOfferingSession_OptionalRoomInstructor(t *testing.T) {
	in := validInput()
	in.RoomID = ""
	in.Room = ""
	in.InstructorGCID = ""
	s, err := NewOfferingSession(in)
	if err != nil {
		t.Fatalf("room+instructor optional: %v", err)
	}
	if s.RoomID != "" || s.Room != "" || s.InstructorGCID != "" {
		t.Fatalf("want empty room_id/room/instructor (roomless), got %q/%q/%q", s.RoomID, s.Room, s.InstructorGCID)
	}
}

// A roomless session (no room_id) is valid — the double-book gate is partial on
// room_id IS NOT NULL, so an unbooked session is exempt.
func TestNewOfferingSession_RoomIDTrimmed(t *testing.T) {
	in := validInput()
	in.RoomID = "  01985e7f-6666-7abc-8def-0000000000bb  "
	s, err := NewOfferingSession(in)
	if err != nil {
		t.Fatalf("NewOfferingSession: %v", err)
	}
	if s.RoomID != "01985e7f-6666-7abc-8def-0000000000bb" {
		t.Fatalf("RoomID must be trimmed, got %q", s.RoomID)
	}
}

func TestNewOfferingSession_Guards(t *testing.T) {
	cases := map[string]func(*NewOfferingSessionInput){
		"missing tenant":     func(in *NewOfferingSessionInput) { in.TenantID = "" },
		"missing offering":   func(in *NewOfferingSessionInput) { in.OfferingID = "  " },
		"missing title":      func(in *NewOfferingSessionInput) { in.Title = "" },
		"ends before starts": func(in *NewOfferingSessionInput) { in.EndsAt = in.StartsAt.Add(-time.Hour) },
		"ends equals starts": func(in *NewOfferingSessionInput) { in.EndsAt = in.StartsAt },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mutate(&in)
			if _, err := NewOfferingSession(in); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("want ErrInvalidArgument, got %v", err)
			}
		})
	}
}
