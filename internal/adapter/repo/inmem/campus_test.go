// campus_test.go - dev-fallback adapter tests for inmem.CampusRepo (CHO-2293).
//
// Mirrors room_test.go. This adapter is the CHORA_DB_DSN-unset fallback only;
// production wires pg.CampusRepo. The durability guard must classify THIS one as
// IN_MEMORY, which is the negative control that proves the guard can still
// report the bad state after the promotion.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

const (
	campusTenantA = "01970000-0000-7000-8000-00000000000a"
	campusTenantB = "01970000-0000-7000-8000-00000000000b"
)

func mustCampus(t *testing.T, tenantID, name string) *campusops.Campus {
	t.Helper()
	c, err := campusops.NewCampus(campusops.NewCampusInput{
		TenantID: tenantID,
		Name:     name,
		Country:  "SG",
	})
	if err != nil {
		t.Fatalf("NewCampus: %v", err)
	}
	return c
}

func TestInmemCampusRepo_SaveGetRoundTrip(t *testing.T) {
	t.Parallel()
	r := inmem.NewCampusRepo()
	ctx := context.Background()
	c := mustCampus(t, campusTenantA, "Bras Basah")

	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.GetForTenant(ctx, campusTenantA, c.ID)
	if err != nil || !ok || got == nil {
		t.Fatalf("GetForTenant: expected hit; ok=%v err=%v", ok, err)
	}
	if got.Name != "Bras Basah" {
		t.Fatalf("round-trip mismatch; got %+v", got)
	}
}

func TestInmemCampusRepo_ScopedByTenant(t *testing.T) {
	t.Parallel()
	r := inmem.NewCampusRepo()
	ctx := context.Background()
	c := mustCampus(t, campusTenantA, "Bras Basah")
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, ok, _ := r.GetForTenant(ctx, campusTenantB, c.ID); ok {
		t.Fatalf("tenant B must not resolve tenant A's campus")
	}
	rows, err := r.ListByTenant(ctx, campusTenantB)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("tenant B must see 0 rows; got %d", len(rows))
	}
}

func TestInmemCampusRepo_SoftDeletedIsFilteredOut(t *testing.T) {
	t.Parallel()
	r := inmem.NewCampusRepo()
	ctx := context.Background()
	c := mustCampus(t, campusTenantA, "Retired Campus")
	c.SoftDelete()
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, ok, _ := r.GetForTenant(ctx, campusTenantA, c.ID); ok {
		t.Fatalf("soft-deleted campus must read as a miss")
	}
	rows, _ := r.ListByTenant(ctx, campusTenantA)
	if len(rows) != 0 {
		t.Fatalf("soft-deleted campus must not appear in the list; got %d", len(rows))
	}
}

func TestInmemCampusRepo_ListOrderedByCreatedAtThenID(t *testing.T) {
	t.Parallel()
	r := inmem.NewCampusRepo()
	ctx := context.Background()
	older := mustCampus(t, campusTenantA, "Older")
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := mustCampus(t, campusTenantA, "Newer")
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// Saved out of order on purpose: ordering is the repo's job, not the caller's.
	if err := r.Save(ctx, newer); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := r.Save(ctx, older); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rows, err := r.ListByTenant(ctx, campusTenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows; got %d", len(rows))
	}
	if rows[0].Name != "Older" || rows[1].Name != "Newer" {
		t.Fatalf("expected CreatedAt order (Older, Newer); got %q, %q", rows[0].Name, rows[1].Name)
	}
}

// The empty list must be a non-nil empty slice so the HTTP layer renders [] and
// never null.
func TestInmemCampusRepo_EmptyListIsNotNil(t *testing.T) {
	t.Parallel()
	r := inmem.NewCampusRepo()
	rows, err := r.ListByTenant(context.Background(), campusTenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if rows == nil {
		t.Fatalf("empty list must be non-nil so the DTO renders [] not null")
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows; got %d", len(rows))
	}
}
