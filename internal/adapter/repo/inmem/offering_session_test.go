// offering_session_test.go — direct unit tests for the in-memory
// OfferingSessionRepo beyond the double-book gate suite: the Get surface,
// the ListByOffering tenant/offering scoping + StartsAt-then-ID ordering,
// and the nil-safe Save.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	offeringsession "github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
)

func TestOfferingSessionRepo_Get(t *testing.T) {
	t.Parallel()
	r := inmem.NewOfferingSessionRepo()
	ctx := context.Background()

	t0 := time.Date(2027, 5, 1, 10, 0, 0, 0, time.UTC)
	s := mkSession(t, tenantA, "off-1", "Room-101", t0, t0.Add(time.Hour))
	if err := r.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(ctx, s.ID)
	if err != nil || !ok {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != s.ID {
		t.Fatalf("round-trip mismatch")
	}
	if _, ok, _ := r.Get(ctx, "missing"); ok {
		t.Fatal("Get: expected miss for unknown id")
	}
}

func TestOfferingSessionRepo_SaveNilIsNoop(t *testing.T) {
	t.Parallel()
	r := inmem.NewOfferingSessionRepo()
	if err := r.Save(context.Background(), nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	if out, _ := r.ListByOffering(context.Background(), tenantA, "off-1"); len(out) != 0 {
		t.Fatalf("store must stay empty after Save(nil); got %d", len(out))
	}
}

func TestOfferingSessionRepo_ListByOffering_ScopeAndOrder(t *testing.T) {
	t.Parallel()
	r := inmem.NewOfferingSessionRepo()
	ctx := context.Background()

	t0 := time.Date(2027, 5, 1, 10, 0, 0, 0, time.UTC)
	early := mkSession(t, tenantA, "off-1", "Room-101", t0, t0.Add(time.Hour))
	late := mkSession(t, tenantA, "off-1", "Room-202", t0.Add(2*time.Hour), t0.Add(3*time.Hour))
	otherOffering := mkSession(t, tenantA, "off-2", "Room-303", t0, t0.Add(time.Hour))
	otherTenant := mkSession(t, tenantB, "off-1", "Room-404", t0, t0.Add(time.Hour))

	// Saved out of order — the repo sorts by StartsAt then ID.
	for _, s := range []*offeringsession.OfferingSession{late, early, otherOffering, otherTenant} {
		if err := r.Save(ctx, s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := r.ListByOffering(ctx, tenantA, "off-1")
	if err != nil {
		t.Fatalf("ListByOffering: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 (tenant+offering scoped); got %d", len(out))
	}
	if out[0].ID != early.ID || out[1].ID != late.ID {
		t.Fatalf("expected [early late] by StartsAt, got [%s %s]", out[0].ID, out[1].ID)
	}

	// Same StartsAt → ID tiebreak.
	tieA := mkSession(t, tenantA, "off-1", "Room-505", t0, t0.Add(time.Hour))
	tieB := mkSession(t, tenantA, "off-1", "Room-606", t0, t0.Add(time.Hour))
	if tieA.ID == tieB.ID {
		t.Fatal("test bug: sessions must have distinct ids")
	}
	if err := r.Save(ctx, tieA); err != nil {
		t.Fatalf("Save tieA: %v", err)
	}
	if err := r.Save(ctx, tieB); err != nil {
		t.Fatalf("Save tieB: %v", err)
	}
	out, err = r.ListByOffering(ctx, tenantA, "off-1")
	if err != nil {
		t.Fatalf("ListByOffering (tie): %v", err)
	}
	found := map[string]bool{}
	for _, s := range out {
		found[s.ID] = true
	}
	if !found[tieA.ID] || !found[tieB.ID] {
		t.Fatalf("tie sessions missing from the list; got %d items", len(out))
	}

	empty, err := r.ListByOffering(ctx, tenantA, "off-nope")
	if err != nil {
		t.Fatalf("ListByOffering(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected 0; got %d", len(empty))
	}
}
