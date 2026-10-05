// scheduling_test.go — direct unit tests for the in-memory SchedulingRepo.
// Pins round-trip persistence, the nil-safe Save, tenant + soft-delete
// scoping, the ISO-week filter, and ID ordering.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

func mustScheduledClass(t *testing.T, id, tenantID string, startsAt time.Time) *scheduling.ScheduledClass {
	t.Helper()
	c, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID:       tenantID,
		CourseID:       "01970000-0000-7000-8000-000000000099",
		InstructorGCID: "01970000-0000-7000-9000-000000000001",
		StartsAt:       startsAt,
		EndsAt:         startsAt.Add(2 * time.Hour),
		MaxCapacity:    20,
	})
	if err != nil {
		t.Fatalf("NewScheduledClass: %v", err)
	}
	c.ID = id
	return c
}

func TestSchedulingRepo_SaveGet(t *testing.T) {
	t.Parallel()
	r := inmem.NewSchedulingRepo()
	ctx := context.Background()

	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	c := mustScheduledClass(t, "class-1", tenantA, time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC))
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(ctx, c.ID)
	if err != nil || !ok {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != c.ID {
		t.Fatalf("round-trip mismatch")
	}
	if _, ok, _ := r.Get(ctx, "missing"); ok {
		t.Fatal("Get: expected miss for unknown id")
	}
}

func TestSchedulingRepo_ListByTenantWeek(t *testing.T) {
	t.Parallel()
	r := inmem.NewSchedulingRepo()
	ctx := context.Background()

	inWeek := mustScheduledClass(t, "sc-w2", tenantA, time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)) // ISO week of 2026-06-15
	inWeek2 := mustScheduledClass(t, "sc-w1", tenantA, time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC)) // same ISO week
	otherWeek := mustScheduledClass(t, "sc-w3", tenantA, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	otherTenant := mustScheduledClass(t, "sc-w4", tenantB, time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC))
	gone := mustScheduledClass(t, "sc-w5", tenantA, time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC))
	now := time.Now().UTC()
	gone.DeletedAt = &now

	for _, c := range []*scheduling.ScheduledClass{inWeek, inWeek2, otherWeek, otherTenant, gone} {
		if err := r.Save(ctx, c); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	year, week := inWeek.StartsAt.UTC().ISOWeek()
	out, err := r.ListByTenantWeek(ctx, tenantA, year, week)
	if err != nil {
		t.Fatalf("ListByTenantWeek: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected the two in-week tenantA classes; got %d items", len(out))
	}
	// Sorted by ID ascending (UUIDv7 ⇒ creation order).
	if out[0].ID != "sc-w1" || out[1].ID != "sc-w2" {
		t.Fatalf("expected [sc-w1 sc-w2] sorted by ID, got [%s %s]", out[0].ID, out[1].ID)
	}

	// A week with no classes returns an empty slice.
	empty, err := r.ListByTenantWeek(ctx, tenantA, 2020, 1)
	if err != nil {
		t.Fatalf("ListByTenantWeek(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected 0; got %d", len(empty))
	}
}

func TestSchedulingRepo_ListByTenant_FiltersAndSorts(t *testing.T) {
	t.Parallel()
	r := inmem.NewSchedulingRepo()
	ctx := context.Background()

	a1 := mustScheduledClass(t, "sc-a1", tenantA, time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC))
	a2 := mustScheduledClass(t, "sc-a2", tenantA, time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC))
	b1 := mustScheduledClass(t, "sc-b1", tenantB, time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC))
	gone := mustScheduledClass(t, "sc-gone", tenantA, time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC))
	now := time.Now().UTC()
	gone.DeletedAt = &now

	// Saved out of order — the repo sorts by ID.
	for _, c := range []*scheduling.ScheduledClass{a2, a1, b1, gone} {
		if err := r.Save(ctx, c); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 (tenant-scoped + soft-delete filtered); got %d", len(out))
	}
	if out[0].ID != "sc-a1" || out[1].ID != "sc-a2" {
		t.Fatalf("expected [sc-a1 sc-a2] sorted by ID, got [%s %s]", out[0].ID, out[1].ID)
	}

	tenantBOut, err := r.ListByTenant(ctx, tenantB)
	if err != nil {
		t.Fatalf("ListByTenant(tenantB): %v", err)
	}
	if len(tenantBOut) != 1 || tenantBOut[0].ID != "sc-b1" {
		t.Fatalf("tenantB must see only its own class; got %d", len(tenantBOut))
	}
}
