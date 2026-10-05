// Package rostering owns class-roster aggregation. Tests here pin the
// invariants per .claude/rules/development-execution.md TDD enforcement
// and .claude/rules/ddd-enforcement.md soft-delete + cross-domain UUID
// reference rules.
package rostering_test

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/domain/rostering"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	classA  = "01970000-0000-7000-7000-000000000001"
	gcid1   = "01970000-0000-7000-9000-000000000001"
	gcid2   = "01970000-0000-7000-9000-000000000002"
	gcid3   = "01970000-0000-7000-9000-000000000003"
)

// -----------------------------------------------------------------------------
// NewRoster
// -----------------------------------------------------------------------------

func TestNewRoster_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		tenant  string
		class   string
		cap     int
		wantErr error
	}{
		{"happy", tenantA, classA, 30, nil},
		{"missing tenant", "", classA, 30, rostering.ErrInvalidArgument},
		{"missing class", tenantA, "", 30, rostering.ErrInvalidArgument},
		{"non-positive capacity", tenantA, classA, 0, rostering.ErrInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := rostering.NewRoster(c.tenant, c.class, c.cap)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("got err %v want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if r == nil || r.ID == "" {
				t.Fatalf("nil roster or empty id")
			}
			if r.ClassID != c.class || r.TenantID != c.tenant {
				t.Errorf("class/tenant mismatch")
			}
			if got := r.Size(); got != 0 {
				t.Errorf("new roster Size() = %d, want 0", got)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// AssignBulk — happy + duplicates collapsed + capacity guard
// -----------------------------------------------------------------------------

func TestAssignBulk_DeduplicatesAndEnforcesCapacity(t *testing.T) {
	r, err := rostering.NewRoster(tenantA, classA, 2)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Bulk assign 3 unique learners → second batch + duplicates ignored.
	added, err := r.AssignBulk([]string{gcid1, gcid2, gcid1})
	if err != nil {
		t.Fatalf("first assign: %v", err)
	}
	if len(added) != 2 {
		t.Fatalf("first added: got %d want 2", len(added))
	}
	if r.Size() != 2 {
		t.Errorf("size after bulk: got %d want 2", r.Size())
	}

	// Re-add gcid2 → no-op, returns empty added slice.
	added, err = r.AssignBulk([]string{gcid2})
	if err != nil {
		t.Fatalf("re-assign: %v", err)
	}
	if len(added) != 0 {
		t.Errorf("re-assign added: got %d want 0", len(added))
	}

	// gcid3 would push past capacity=2 → ErrRosterAtCapacity.
	_, err = r.AssignBulk([]string{gcid3})
	if !errors.Is(err, rostering.ErrRosterAtCapacity) {
		t.Errorf("capacity exceed: got %v want ErrRosterAtCapacity", err)
	}
}

func TestAssignBulk_RejectsEmptyGCID(t *testing.T) {
	r, _ := rostering.NewRoster(tenantA, classA, 5)
	if _, err := r.AssignBulk([]string{""}); !errors.Is(err, rostering.ErrInvalidArgument) {
		t.Errorf("empty gcid: got %v want ErrInvalidArgument", err)
	}
	if _, err := r.AssignBulk([]string{"   "}); !errors.Is(err, rostering.ErrInvalidArgument) {
		t.Errorf("blank gcid: got %v want ErrInvalidArgument", err)
	}
}

// -----------------------------------------------------------------------------
// Unassign
// -----------------------------------------------------------------------------

func TestUnassign(t *testing.T) {
	r, _ := rostering.NewRoster(tenantA, classA, 5)
	if _, err := r.AssignBulk([]string{gcid1, gcid2}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if removed := r.Unassign(gcid1); !removed {
		t.Errorf("unassign known: got false want true")
	}
	if removed := r.Unassign(gcid1); removed {
		t.Errorf("unassign-after-remove: got true want false")
	}
	if r.Size() != 1 {
		t.Errorf("size after unassign: got %d want 1", r.Size())
	}
}

// -----------------------------------------------------------------------------
// Has + List
// -----------------------------------------------------------------------------

func TestHasAndList(t *testing.T) {
	r, _ := rostering.NewRoster(tenantA, classA, 5)
	r.AssignBulk([]string{gcid1, gcid2}) //nolint:errcheck
	if !r.Has(gcid1) {
		t.Errorf("has gcid1: false")
	}
	if r.Has("unknown") {
		t.Errorf("has unknown: true")
	}
	list := r.List()
	if len(list) != 2 {
		t.Fatalf("list len: got %d want 2", len(list))
	}
	// List must be a copy — mutating it must not affect roster.
	list[0] = "tampered"
	if r.Has("tampered") {
		t.Errorf("List() returned shared slice — mutation leaked into roster")
	}
}

// -----------------------------------------------------------------------------
// SoftDelete
// -----------------------------------------------------------------------------

func TestRosterSoftDelete(t *testing.T) {
	r, _ := rostering.NewRoster(tenantA, classA, 5)
	r.SoftDelete()
	if r.DeletedAt == nil {
		t.Fatalf("DeletedAt nil after SoftDelete")
	}
	first := *r.DeletedAt
	r.SoftDelete()
	if !r.DeletedAt.Equal(first) {
		t.Errorf("SoftDelete not idempotent")
	}
}
