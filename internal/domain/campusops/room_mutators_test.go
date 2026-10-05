// room_mutators_test.go - CHO-2332: Room.Rename + Room.SetCapacity domain
// guards. These are the Edit-side mutators; they reuse the SAME invariants
// NewRoom enforces (non-blank trimmed name, capacity strictly > 0) and bump
// UpdatedAt so the durable Save writes a fresh updated_at.
package campusops_test

import (
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

// newTestRoom builds a valid Room with a known-past UpdatedAt so mutator tests
// can assert the timestamp advanced.
func newTestRoom(t *testing.T) *campusops.Room {
	t.Helper()
	rm, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantA,
		Name:     "Lab A",
		Capacity: 30,
	})
	if err != nil {
		t.Fatalf("NewRoom: %v", err)
	}
	rm.UpdatedAt = time.Now().UTC().Add(-time.Hour) // known past
	return rm
}

func TestRoom_Rename_Succeeds(t *testing.T) {
	t.Parallel()
	rm := newTestRoom(t)
	before := rm.UpdatedAt
	if err := rm.Rename("Lab B"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if rm.Name != "Lab B" {
		t.Fatalf("name: want Lab B, got %q", rm.Name)
	}
	if !rm.UpdatedAt.After(before) {
		t.Fatalf("UpdatedAt must advance: before=%s after=%s", before, rm.UpdatedAt)
	}
}

func TestRoom_Rename_TrimsWhitespace(t *testing.T) {
	t.Parallel()
	rm := newTestRoom(t)
	if err := rm.Rename("  Studio 5  "); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if rm.Name != "Studio 5" {
		t.Fatalf("name must be trimmed: got %q", rm.Name)
	}
}

func TestRoom_Rename_RejectsBlank(t *testing.T) {
	t.Parallel()
	for _, blank := range []string{"", "   ", "\t\n"} {
		rm := newTestRoom(t)
		before := rm.UpdatedAt
		err := rm.Rename(blank)
		if err == nil {
			t.Fatalf("Rename(%q): expected error", blank)
		}
		if !errors.Is(err, campusops.ErrInvalidArgument) {
			t.Fatalf("Rename(%q): want ErrInvalidArgument, got %v", blank, err)
		}
		if rm.Name != "Lab A" {
			t.Fatalf("Rename(%q): name must be unchanged on reject, got %q", blank, rm.Name)
		}
		if !rm.UpdatedAt.Equal(before) {
			t.Fatalf("Rename(%q): UpdatedAt must not move on reject", blank)
		}
	}
}

func TestRoom_SetCapacity_Succeeds(t *testing.T) {
	t.Parallel()
	rm := newTestRoom(t)
	before := rm.UpdatedAt
	if err := rm.SetCapacity(45); err != nil {
		t.Fatalf("SetCapacity: %v", err)
	}
	if rm.Capacity != 45 {
		t.Fatalf("capacity: want 45, got %d", rm.Capacity)
	}
	if !rm.UpdatedAt.After(before) {
		t.Fatalf("UpdatedAt must advance: before=%s after=%s", before, rm.UpdatedAt)
	}
}

func TestRoom_SetCapacity_RejectsNonPositive(t *testing.T) {
	t.Parallel()
	for _, bad := range []int{0, -1, -30} {
		rm := newTestRoom(t)
		before := rm.UpdatedAt
		err := rm.SetCapacity(bad)
		if err == nil {
			t.Fatalf("SetCapacity(%d): expected error", bad)
		}
		if !errors.Is(err, campusops.ErrInvalidArgument) {
			t.Fatalf("SetCapacity(%d): want ErrInvalidArgument, got %v", bad, err)
		}
		if rm.Capacity != 30 {
			t.Fatalf("SetCapacity(%d): capacity must be unchanged on reject, got %d", bad, rm.Capacity)
		}
		if !rm.UpdatedAt.Equal(before) {
			t.Fatalf("SetCapacity(%d): UpdatedAt must not move on reject", bad)
		}
	}
}
