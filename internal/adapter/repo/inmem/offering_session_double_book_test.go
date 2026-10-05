// offering_session_double_book_test.go — specs for ratified CHO-2191 / ADR-237:
// the in-memory OfferingSessionRepo mirrors the DB EXCLUDE constraint (no two
// active sessions share a room_id over an overlapping window, per tenant) so the
// handler's 409 path is testable off a live DB. The DB constraint (mig 0052)
// remains the source of truth. Keyed on room_id — NOT the free-text name.
package inmem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	offeringsession "github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
)

// mkSession builds a session booked against roomID (the ratified gate key). A
// blank roomID is a roomless session (exempt from the double-book gate).
func mkSession(t *testing.T, tenant, offering, roomID string, start, end time.Time) *offeringsession.OfferingSession {
	t.Helper()
	s, err := offeringsession.NewOfferingSession(offeringsession.NewOfferingSessionInput{
		TenantID: tenant, OfferingID: offering, Title: "S", RoomID: roomID, StartsAt: start, EndsAt: end,
	})
	if err != nil {
		t.Fatalf("NewOfferingSession: %v", err)
	}
	return s
}

func TestOfferingSessionRepo_DoubleBook_SameRoomOverlap_Rejected(t *testing.T) {
	ctx := context.Background()
	r := inmem.NewOfferingSessionRepo()
	t0 := time.Date(2027, 5, 1, 10, 0, 0, 0, time.UTC)
	if err := r.Save(ctx, mkSession(t, tenantA, "off-1", "Room-101", t0, t0.Add(2*time.Hour))); err != nil {
		t.Fatalf("save a: %v", err)
	}
	// same tenant + room, overlapping window → reject
	b := mkSession(t, tenantA, "off-1", "Room-101", t0.Add(time.Hour), t0.Add(3*time.Hour))
	if err := r.Save(ctx, b); !errors.Is(err, offeringsession.ErrRoomDoubleBooked) {
		t.Fatalf("expected ErrRoomDoubleBooked, got %v", err)
	}
}

// A room double-book is tenant+room+time — NOT scoped to one offering (a
// physical room cannot host two sessions at once, even across offerings).
func TestOfferingSessionRepo_DoubleBook_AcrossOfferings_Rejected(t *testing.T) {
	ctx := context.Background()
	r := inmem.NewOfferingSessionRepo()
	t0 := time.Date(2027, 5, 1, 10, 0, 0, 0, time.UTC)
	if err := r.Save(ctx, mkSession(t, tenantA, "off-1", "Room-101", t0, t0.Add(2*time.Hour))); err != nil {
		t.Fatalf("save a: %v", err)
	}
	b := mkSession(t, tenantA, "off-2", "Room-101", t0.Add(time.Hour), t0.Add(3*time.Hour))
	if err := r.Save(ctx, b); !errors.Is(err, offeringsession.ErrRoomDoubleBooked) {
		t.Fatalf("expected cross-offering room clash rejected, got %v", err)
	}
}

func TestOfferingSessionRepo_DoubleBook_Allowed(t *testing.T) {
	t0 := time.Date(2027, 5, 1, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		s    *offeringsession.OfferingSession
	}{
		{"different room, overlap", mkSession(t, tenantA, "off-1", "Room-202", t0, t0.Add(time.Hour))},
		{"same room, no overlap (after)", mkSession(t, tenantA, "off-1", "Room-101", t0.Add(2*time.Hour), t0.Add(3*time.Hour))},
		{"same room, abutting (end==start)", mkSession(t, tenantA, "off-1", "Room-101", t0.Add(time.Hour), t0.Add(2*time.Hour))},
		{"blank room, overlap", mkSession(t, tenantA, "off-1", "", t0, t0.Add(time.Hour))},
		{"different tenant, same room overlap", mkSession(t, tenantB, "off-1", "Room-101", t0, t0.Add(time.Hour))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := inmem.NewOfferingSessionRepo()
			// occupant: Room-101 for [t0, t0+1h)
			if err := r.Save(context.Background(), mkSession(t, tenantA, "off-1", "Room-101", t0, t0.Add(time.Hour))); err != nil {
				t.Fatalf("occupant: %v", err)
			}
			if err := r.Save(context.Background(), tc.s); err != nil {
				t.Fatalf("expected allowed, got %v", err)
			}
		})
	}
}

func TestOfferingSessionRepo_DoubleBook_ReSaveSelf_OK(t *testing.T) {
	ctx := context.Background()
	r := inmem.NewOfferingSessionRepo()
	t0 := time.Date(2027, 5, 1, 10, 0, 0, 0, time.UTC)
	a := mkSession(t, tenantA, "off-1", "Room-101", t0, t0.Add(time.Hour))
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("re-save self must be OK, got %v", err)
	}
}

func TestOfferingSessionRepo_DoubleBook_SoftDeletedFreesRoom(t *testing.T) {
	ctx := context.Background()
	r := inmem.NewOfferingSessionRepo()
	t0 := time.Date(2027, 5, 1, 10, 0, 0, 0, time.UTC)
	a := mkSession(t, tenantA, "off-1", "Room-101", t0, t0.Add(time.Hour))
	now := t0
	a.DeletedAt = &now
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("save deleted: %v", err)
	}
	b := mkSession(t, tenantA, "off-1", "Room-101", t0, t0.Add(time.Hour))
	if err := r.Save(ctx, b); err != nil {
		t.Fatalf("soft-deleted session must free the room, got %v", err)
	}
}
