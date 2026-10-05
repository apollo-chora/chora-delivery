// Package inmem Room repo tests — CHO-2191 SP1.
//
// RoomStore contract: Save upserts; GetForTenant is tenant-scoped +
// soft-delete-aware (ok=false on absent / cancelled / other-tenant row);
// ListByTenant returns a tenant's active rooms, CreatedAt-ordered.
//
// Reuses the inmem_test package tenantA / tenantB constants (application_test.go).
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

func newRoom(t *testing.T, tenant, name string, cap int) *campusops.Room {
	t.Helper()
	rm, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenant, Name: name, Capacity: cap,
	})
	if err != nil {
		t.Fatalf("NewRoom: %v", err)
	}
	return rm
}

func TestRoomRepo_SaveGet_RoundTrip(t *testing.T) {
	t.Parallel()
	r := inmem.NewRoomRepo()
	ctx := context.Background()
	rm := newRoom(t, tenantA, "Lab A", 30)

	if err := r.Save(ctx, rm); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.GetForTenant(ctx, tenantA, rm.ID)
	if err != nil {
		t.Fatalf("GetForTenant: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("expected hit")
	}
	if got.ID != rm.ID || got.Name != "Lab A" || got.Capacity != 30 {
		t.Fatalf("round-trip mismatch; got %+v", got)
	}
}

func TestRoomRepo_Save_NilRoom_NoOp(t *testing.T) {
	t.Parallel()
	r := inmem.NewRoomRepo()
	if err := r.Save(context.Background(), nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	list, _ := r.ListByTenant(context.Background(), tenantA)
	if len(list) != 0 {
		t.Fatalf("nil save must not add a row; got %d", len(list))
	}
}

func TestRoomRepo_Get_MissOnUnknownID(t *testing.T) {
	t.Parallel()
	r := inmem.NewRoomRepo()
	_, ok, err := r.GetForTenant(context.Background(), tenantA, "01970000-0000-7000-8000-0000000000ff")
	if err != nil {
		t.Fatalf("GetForTenant: unexpected err %v", err)
	}
	if ok {
		t.Fatalf("expected miss on unknown id")
	}
}

func TestRoomRepo_TenantIsolation(t *testing.T) {
	t.Parallel()
	r := inmem.NewRoomRepo()
	ctx := context.Background()
	roomA := newRoom(t, tenantA, "A-room", 10)
	roomB := newRoom(t, tenantB, "B-room", 10)
	if err := r.Save(ctx, roomA); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := r.Save(ctx, roomB); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	// Tenant A cannot GET tenant B's room.
	if _, ok, _ := r.GetForTenant(ctx, tenantA, roomB.ID); ok {
		t.Fatalf("tenant A must NOT see tenant B's room")
	}
	// ListByTenant only returns the caller's own rooms.
	listA, err := r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(listA) != 1 || listA[0].ID != roomA.ID {
		t.Fatalf("tenant A list must be [A-room]; got %+v", listA)
	}
}

func TestRoomRepo_ListByTenant_HidesSoftDeleted_AndOrders(t *testing.T) {
	t.Parallel()
	r := inmem.NewRoomRepo()
	ctx := context.Background()

	early := newRoom(t, tenantA, "Early", 5)
	early.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := newRoom(t, tenantA, "Late", 5)
	late.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	gone := newRoom(t, tenantA, "Gone", 5)
	gone.SoftDelete()

	// Save the LATE one first — list must still order Early→Late by CreatedAt.
	for _, rm := range []*campusops.Room{late, early, gone} {
		if err := r.Save(ctx, rm); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	list, err := r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("soft-deleted room must be hidden; want 2, got %d (%+v)", len(list), list)
	}
	if list[0].Name != "Early" || list[1].Name != "Late" {
		t.Fatalf("order: want [Early, Late]; got [%s, %s]", list[0].Name, list[1].Name)
	}
}

func TestRoomRepo_Get_SoftDeletedHidden(t *testing.T) {
	t.Parallel()
	r := inmem.NewRoomRepo()
	ctx := context.Background()
	rm := newRoom(t, tenantA, "Doomed", 5)
	if err := r.Save(ctx, rm); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rm.SoftDelete()
	if err := r.Save(ctx, rm); err != nil {
		t.Fatalf("Save (soft-deleted): %v", err)
	}
	if _, ok, _ := r.GetForTenant(ctx, tenantA, rm.ID); ok {
		t.Fatalf("soft-deleted room must be hidden from GetForTenant")
	}
}
