// rostering_test.go — direct unit tests for the in-memory RosterRepo
// (indexed by class_id AND roster_id). Pins upsert, GetByClass/Get hits and
// misses.
package inmem_test

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/rostering"
)

func mkRoster(id, classID string) *rostering.Roster {
	now := time.Now().UTC()
	return &rostering.Roster{
		ID:          id,
		TenantID:    tenantA,
		ClassID:     classID,
		MaxCapacity: 20,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestRosterRepo_SaveGetByClassAndGet(t *testing.T) {
	r := inmem.NewRosterRepo()
	rs := mkRoster("rost-1", "class-1")
	r.Save(rs)

	got, ok := r.GetByClass("class-1")
	if !ok || got.ID != rs.ID {
		t.Fatalf("GetByClass: ok=%v id=%s", ok, got.ID)
	}
	got2, ok := r.Get(rs.ID)
	if !ok || got2.ID != rs.ID || got2.ClassID != "class-1" {
		t.Fatalf("Get: ok=%v %+v", ok, got2)
	}

	// Upsert: a re-save on the same class replaces the class index entry with
	// the new aggregate. byID is append-only (an id maps to one roster), so the
	// retired id still resolves to the OLD aggregate while the class index
	// points at the replacement.
	repl := mkRoster("rost-2", "class-1")
	r.Save(repl)
	if got, ok := r.GetByClass("class-1"); !ok || got.ID != "rost-2" {
		t.Fatalf("GetByClass after upsert: ok=%v id=%s (want rost-2)", ok, got.ID)
	}
	if got, ok := r.Get("rost-2"); !ok || got.ClassID != "class-1" {
		t.Fatalf("byID index after upsert: ok=%v class=%s", ok, got.ClassID)
	}
}

func TestRosterRepo_Misses(t *testing.T) {
	r := inmem.NewRosterRepo()
	if _, ok := r.GetByClass("absent-class"); ok {
		t.Fatal("GetByClass unknown: want ok=false")
	}
	if _, ok := r.Get("absent-id"); ok {
		t.Fatal("Get unknown: want ok=false")
	}
}
