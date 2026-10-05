// class_room_test.go - ScheduledClass adopts the ratified room_id booking
// (CHO-2299, closing ADR-237 O3).
//
// ADR-236 D2 designates ScheduledClass and OfferingSession as the two sanctioned
// durable scheduled-meeting models. ADR-237 gave OfferingSession a room_id and a
// DB-enforced double-book EXCLUDE; ScheduledClass was left on a free-text string,
// so the ratified no-double-book invariant is evadable by booking through
// /r/scheduling. These tests move the aggregate onto the same stable key.
//
// Note NewScheduledClass previously performed NO room validation whatsoever:
// room was required only by the browser's canCreate gate, so a direct API call
// with room:"" was accepted. The domain now owns that rule.
package scheduling_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

const (
	classTenant = "01970000-0000-7000-8000-000000000001"
	classCourse = "01970000-0000-7000-8000-0000000000c1"
	classGCID   = "01970000-0000-7000-8000-0000000000g1"
	classRoomID = "01985e7f-6666-7abc-8def-0000000000f9"
)

func classWindow() (time.Time, time.Time) {
	s := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	return s, s.Add(2 * time.Hour)
}

func TestNewScheduledClass_CarriesRoomIDAndDisplayName(t *testing.T) {
	t.Parallel()
	s, e := classWindow()
	c, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID:       classTenant,
		CourseID:       classCourse,
		InstructorGCID: classGCID,
		RoomID:         classRoomID,
		Room:           "Lab A",
		StartsAt:       s,
		EndsAt:         e,
		MaxCapacity:    20,
	})
	if err != nil {
		t.Fatalf("NewScheduledClass: %v", err)
	}
	if c.RoomID != classRoomID {
		t.Fatalf("RoomID must be persisted as the stable key; got %q", c.RoomID)
	}
	if c.Room != "Lab A" {
		t.Fatalf("display name must round-trip; got %q", c.Room)
	}
}

// The whole point of the story: the typo-bypass must not be reachable through
// this aggregate either.
func TestNewScheduledClass_RejectsFreeTextRoomWithoutRoomID(t *testing.T) {
	t.Parallel()
	s, e := classWindow()
	_, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID:       classTenant,
		CourseID:       classCourse,
		InstructorGCID: classGCID,
		Room:           "MTM HQ Room 401", // free text, no room_id
		StartsAt:       s,
		EndsAt:         e,
		MaxCapacity:    20,
	})
	if !errors.Is(err, campusops.ErrFreeTextRoom) {
		t.Fatalf("a free-text room with no room_id must be rejected with ErrFreeTextRoom; got %v", err)
	}
}

// Roomless stays legitimate and exempt from the gate.
func TestNewScheduledClass_RoomlessIsAllowed(t *testing.T) {
	t.Parallel()
	s, e := classWindow()
	c, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID:       classTenant,
		CourseID:       classCourse,
		InstructorGCID: classGCID,
		StartsAt:       s,
		EndsAt:         e,
		MaxCapacity:    20,
	})
	if err != nil {
		t.Fatalf("a roomless class must be allowed; got %v", err)
	}
	if c.RoomID != "" {
		t.Fatalf("roomless must leave RoomID blank so it is exempt from the EXCLUDE; got %q", c.RoomID)
	}
}

// Pre-existing guards must survive the constructor signature change.
func TestNewScheduledClass_KeepsExistingGuards(t *testing.T) {
	t.Parallel()
	s, e := classWindow()
	base := scheduling.NewScheduledClassInput{
		TenantID:       classTenant,
		CourseID:       classCourse,
		InstructorGCID: classGCID,
		RoomID:         classRoomID,
		StartsAt:       s,
		EndsAt:         e,
		MaxCapacity:    20,
	}
	cases := map[string]func(scheduling.NewScheduledClassInput) scheduling.NewScheduledClassInput{
		"blank tenant": func(i scheduling.NewScheduledClassInput) scheduling.NewScheduledClassInput { i.TenantID = ""; return i },
		"blank course": func(i scheduling.NewScheduledClassInput) scheduling.NewScheduledClassInput { i.CourseID = ""; return i },
		"blank instructor": func(i scheduling.NewScheduledClassInput) scheduling.NewScheduledClassInput {
			i.InstructorGCID = ""
			return i
		},
		"zero capacity": func(i scheduling.NewScheduledClassInput) scheduling.NewScheduledClassInput {
			i.MaxCapacity = 0
			return i
		},
		"ends before starts": func(i scheduling.NewScheduledClassInput) scheduling.NewScheduledClassInput {
			i.EndsAt = i.StartsAt.Add(-time.Hour)
			return i
		},
	}
	for name, mutate := range cases {
		if _, err := scheduling.NewScheduledClass(mutate(base)); err == nil {
			t.Fatalf("%s: expected a guard failure, got nil", name)
		}
	}
}

// Reschedule moves the booking, so it must enforce the same rule.
func TestReschedule_RejectsFreeTextRoomWithoutRoomID(t *testing.T) {
	t.Parallel()
	s, e := classWindow()
	c, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID: classTenant, CourseID: classCourse, InstructorGCID: classGCID,
		RoomID: classRoomID, Room: "Lab A", StartsAt: s, EndsAt: e, MaxCapacity: 20,
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	err = c.Reschedule(s.Add(time.Hour), e.Add(time.Hour), "", "Some Other Room")
	if !errors.Is(err, campusops.ErrFreeTextRoom) {
		t.Fatalf("Reschedule must reject a free-text room with no room_id; got %v", err)
	}
	if c.RoomID != classRoomID || c.Room != "Lab A" {
		t.Fatalf("a rejected Reschedule must not mutate the aggregate; got %q/%q", c.RoomID, c.Room)
	}
}

func TestReschedule_MovesTheBooking(t *testing.T) {
	t.Parallel()
	s, e := classWindow()
	c, _ := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID: classTenant, CourseID: classCourse, InstructorGCID: classGCID,
		RoomID: classRoomID, Room: "Lab A", StartsAt: s, EndsAt: e, MaxCapacity: 20,
	})
	other := "01985e7f-6666-7abc-8def-0000000000fa"
	if err := c.Reschedule(s.Add(time.Hour), e.Add(time.Hour), other, "Hall B"); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	if c.RoomID != other || c.Room != "Hall B" {
		t.Fatalf("Reschedule must move the booking; got %q/%q", c.RoomID, c.Room)
	}
}
