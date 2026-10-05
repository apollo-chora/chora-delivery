// Package scheduling owns scheduled-class extensions for the Content Delivery
// domain — see `class.go`. These tests pin the contract per .claude/rules/
// development-execution.md TDD enforcement.
package scheduling_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

const (
	tenantA  = "01970000-0000-7000-8000-000000000001"
	courseA  = "01970000-0000-7000-7000-000000000001"
	teacher1 = "01970000-0000-7000-9000-000000000001"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// -----------------------------------------------------------------------------
// NewScheduledClass — happy + sad
// -----------------------------------------------------------------------------

func TestNewScheduledClass_TableDriven(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T11:00:00Z")

	cases := []struct {
		name     string
		tenant   string
		course   string
		instr    string
		roomID   string
		starts   time.Time
		ends     time.Time
		capacity int
		wantErr  error
	}{
		{"happy", tenantA, courseA, teacher1, "rm-1", starts, ends, 25, nil},
		{"missing tenant", "", courseA, teacher1, "rm-1", starts, ends, 25, scheduling.ErrInvalidArgument},
		{"missing course", tenantA, "", teacher1, "rm-1", starts, ends, 25, scheduling.ErrInvalidArgument},
		{"missing instructor", tenantA, courseA, "", "rm-1", starts, ends, 25, scheduling.ErrInvalidArgument},
		{"non-positive capacity", tenantA, courseA, teacher1, "rm-1", starts, ends, 0, scheduling.ErrInvalidArgument},
		{"end before start", tenantA, courseA, teacher1, "rm-1", ends, starts, 25, scheduling.ErrInvalidArgument},
		{"end equals start", tenantA, courseA, teacher1, "rm-1", starts, starts, 25, scheduling.ErrInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cls, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
				TenantID: c.tenant, CourseID: c.course, InstructorGCID: c.instr,
				RoomID: c.roomID, StartsAt: c.starts, EndsAt: c.ends, MaxCapacity: c.capacity,
			})
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("got err %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if cls == nil || cls.ID == "" {
				t.Fatalf("nil class or empty id")
			}
			if cls.TenantID != c.tenant {
				t.Errorf("tenant: got %s want %s", cls.TenantID, c.tenant)
			}
			if cls.CourseID != c.course {
				t.Errorf("course: got %s want %s", cls.CourseID, c.course)
			}
			if cls.InstructorGCID != c.instr {
				t.Errorf("instructor: got %s want %s", cls.InstructorGCID, c.instr)
			}
			if cls.MaxCapacity != c.capacity {
				t.Errorf("capacity: got %d want %d", cls.MaxCapacity, c.capacity)
			}
			if !cls.StartsAt.Equal(c.starts.UTC()) {
				t.Errorf("starts_at: got %s want %s", cls.StartsAt, c.starts.UTC())
			}
			// UUIDv7: 36-char dash-formatted with version nibble 7.
			if !strings.Contains(cls.ID, "-") {
				t.Errorf("id not dash-formatted: %s", cls.ID)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Reschedule
// -----------------------------------------------------------------------------

func TestReschedule(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T11:00:00Z")
	cls, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID: tenantA, CourseID: courseA, InstructorGCID: teacher1,
		RoomID: "rm-1", StartsAt: starts, EndsAt: ends, MaxCapacity: 25,
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	prevUpdated := cls.UpdatedAt
	time.Sleep(time.Millisecond)

	newStarts := mustTime(t, "2026-06-02T13:00:00Z")
	newEnds := mustTime(t, "2026-06-02T15:00:00Z")
	if err := cls.Reschedule(newStarts, newEnds, "rm-2", ""); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if !cls.StartsAt.Equal(newStarts.UTC()) {
		t.Errorf("starts_at not updated")
	}
	if !cls.EndsAt.Equal(newEnds.UTC()) {
		t.Errorf("ends_at not updated")
	}
	// CHO-2299: the booking is keyed on RoomID now, so the reschedule target
	// "rm-2" is the stable key, not the display name.
	if cls.RoomID != "rm-2" {
		t.Errorf("room booking not updated")
	}
	if !cls.UpdatedAt.After(prevUpdated) {
		t.Errorf("updated_at not advanced")
	}

	// Reschedule rejects bad ranges.
	if err := cls.Reschedule(newEnds, newStarts, "rm-3", ""); !errors.Is(err, scheduling.ErrInvalidArgument) {
		t.Errorf("reschedule(end<start): got %v want ErrInvalidArgument", err)
	}
}

// -----------------------------------------------------------------------------
// SoftDelete
// -----------------------------------------------------------------------------

func TestSoftDelete_Idempotent(t *testing.T) {
	starts := mustTime(t, "2026-06-01T09:00:00Z")
	ends := mustTime(t, "2026-06-01T11:00:00Z")
	cls, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID: tenantA, CourseID: courseA, InstructorGCID: teacher1,
		RoomID: "rm-1", StartsAt: starts, EndsAt: ends, MaxCapacity: 25,
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	cls.SoftDelete()
	first := *cls.DeletedAt
	cls.SoftDelete() // second call no-op (preserves first stamp)
	if !cls.DeletedAt.Equal(first) {
		t.Errorf("soft delete not idempotent: %v != %v", cls.DeletedAt, first)
	}
}

// -----------------------------------------------------------------------------
// ISO week computation
// -----------------------------------------------------------------------------

func TestISOWeek(t *testing.T) {
	cases := []struct {
		name string
		t    string
		want string
	}{
		{"early jan", "2026-01-05T09:00:00Z", "2026-W02"},
		{"mid year", "2026-06-15T09:00:00Z", "2026-W25"},
		{"late dec rollover", "2026-12-31T09:00:00Z", "2026-W53"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scheduling.ISOWeek(mustTime(t, c.t))
			if got != c.want {
				t.Errorf("ISOWeek(%s): got %s want %s", c.t, got, c.want)
			}
		})
	}
}

func TestParseISOWeek(t *testing.T) {
	cases := []struct {
		in       string
		wantYear int
		wantWeek int
		wantErr  bool
	}{
		{"2026-W02", 2026, 2, false},
		{"2026-W25", 2026, 25, false},
		{"2026-W53", 2026, 53, false},
		{"bogus", 0, 0, true},
		{"2026-WAA", 0, 0, true},
		{"2026-W00", 0, 0, true},
		{"2026-W54", 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			y, wk, err := scheduling.ParseISOWeek(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("ParseISOWeek(%s) want err, got %d-W%d", c.in, y, wk)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if y != c.wantYear || wk != c.wantWeek {
				t.Errorf("got %d-W%d want %d-W%d", y, wk, c.wantYear, c.wantWeek)
			}
		})
	}
}

func TestInISOWeek(t *testing.T) {
	cls, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID: tenantA, CourseID: courseA, InstructorGCID: teacher1, RoomID: "rm-1",
		StartsAt: mustTime(t, "2026-06-15T09:00:00Z"),
		EndsAt:   mustTime(t, "2026-06-15T11:00:00Z"), MaxCapacity: 25,
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !scheduling.InISOWeek(cls, 2026, 25) {
		t.Errorf("expected class in 2026-W25")
	}
	if scheduling.InISOWeek(cls, 2026, 24) {
		t.Errorf("expected class NOT in 2026-W24")
	}
	if scheduling.InISOWeek(cls, 2025, 25) {
		t.Errorf("expected class NOT in 2025-W25")
	}
}
