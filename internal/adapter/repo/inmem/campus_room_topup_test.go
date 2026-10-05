// campus_room_topup_test.go — branch top-up for CampusRepo + RoomRepo: the
// nil-safe Save and the CreatedAt-tie ID tiebreak in ListByTenant (the
// existing suites only exercised distinct CreatedAt values).
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
)

func TestCampusRepo_SaveNilIsNoop(t *testing.T) {
	t.Parallel()
	r := inmem.NewCampusRepo()
	if err := r.Save(context.Background(), nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	if rows, _ := r.ListByTenant(context.Background(), campusTenantA); len(rows) != 0 {
		t.Fatalf("store must stay empty after Save(nil); got %d", len(rows))
	}
}

func TestCampusRepo_ListByTenant_CreatedAtTieIDOrder(t *testing.T) {
	t.Parallel()
	r := inmem.NewCampusRepo()
	ctx := context.Background()

	tie := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	c1 := mustCampus(t, campusTenantA, "Alpha")
	c1.CreatedAt = tie
	c2 := mustCampus(t, campusTenantA, "Beta")
	c2.CreatedAt = tie
	if c1.ID == c2.ID {
		t.Fatal("test bug: campuses must have distinct ids")
	}
	// Saved out of order — the tie must fall back to ID ascending.
	if err := r.Save(ctx, c2); err != nil {
		t.Fatalf("Save c2: %v", err)
	}
	if err := r.Save(ctx, c1); err != nil {
		t.Fatalf("Save c1: %v", err)
	}

	rows, err := r.ListByTenant(ctx, campusTenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2; got %d", len(rows))
	}
	if rows[0].ID > rows[1].ID {
		t.Fatalf("expected ID-ascending tie order, got [%s %s]", rows[0].ID, rows[1].ID)
	}
}

func TestRoomRepo_ListByTenant_CreatedAtTieIDOrder(t *testing.T) {
	t.Parallel()
	r := inmem.NewRoomRepo()
	ctx := context.Background()

	tie := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	rm1 := newRoom(t, tenantA, "Alpha", 10)
	rm1.CreatedAt = tie
	rm2 := newRoom(t, tenantA, "Beta", 10)
	rm2.CreatedAt = tie
	if rm1.ID == rm2.ID {
		t.Fatal("test bug: rooms must have distinct ids")
	}
	// Saved out of order — the tie must fall back to ID ascending.
	if err := r.Save(ctx, rm2); err != nil {
		t.Fatalf("Save rm2: %v", err)
	}
	if err := r.Save(ctx, rm1); err != nil {
		t.Fatalf("Save rm1: %v", err)
	}

	rows, err := r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2; got %d", len(rows))
	}
	if rows[0].ID > rows[1].ID {
		t.Fatalf("expected ID-ascending tie order, got [%s %s]", rows[0].ID, rows[1].ID)
	}
}
