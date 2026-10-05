// offering_repo_test.go — direct unit tests for the in-memory OfferingRepo
// (R+ four-delivery-mode W1). Pins round-trip persistence, tenant +
// soft-delete scoping, ID ordering, and the domain-engine Search delegate.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func mustOffering(t *testing.T, id, tenantID string, deleted bool) *delivery.Offering {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{"01970000-0000-7000-8000-000000000099"},
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "offering-" + id,
		Capacity:     20,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	if deleted {
		now := time.Now().UTC()
		o.DeletedAt = &now
	}
	return o
}

func TestOfferingRepo_SaveGet(t *testing.T) {
	t.Parallel()
	r := inmem.NewOfferingRepo()
	ctx := context.Background()

	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	o := mustOffering(t, "off-1", tenantA, false)
	if err := r.Save(ctx, o); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(ctx, o.ID)
	if err != nil || !ok {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != o.ID {
		t.Fatalf("round-trip mismatch")
	}
	if _, ok, _ := r.Get(ctx, "missing"); ok {
		t.Fatal("Get: expected miss for unknown id")
	}
}

func TestOfferingRepo_Get_SoftDeletedStillResolves(t *testing.T) {
	t.Parallel()
	r := inmem.NewOfferingRepo()
	ctx := context.Background()
	o := mustOffering(t, "off-2", tenantA, true)
	if err := r.Save(ctx, o); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Get is a raw id lookup — the soft-deleted row still resolves by id;
	// ListByTenant/Search apply the deleted_at filter.
	if _, ok, _ := r.Get(ctx, o.ID); !ok {
		t.Fatal("soft-deleted offering must still resolve by id (filtering is a list concern)")
	}
}

func TestOfferingRepo_ListByTenant_FiltersAndSorts(t *testing.T) {
	t.Parallel()
	r := inmem.NewOfferingRepo()
	ctx := context.Background()
	for _, o := range []*delivery.Offering{
		mustOffering(t, "off-a1", tenantA, false),
		mustOffering(t, "off-a2", tenantA, false),
		mustOffering(t, "off-b1", tenantB, false),
		mustOffering(t, "off-gone", tenantA, true), // soft-deleted
	} {
		if err := r.Save(ctx, o); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 (tenant + soft-delete filtered); got %d", len(out))
	}
	if out[0].ID != "off-a1" || out[1].ID != "off-a2" {
		t.Fatalf("expected [off-a1 off-a2] sorted by ID, got [%s %s]", out[0].ID, out[1].ID)
	}
}

// Search snapshots every row and delegates to the pure domain engine
// (delivery.SearchOfferings) — tenant + soft-delete scoping and the query
// filters all live in that engine.
func TestOfferingRepo_Search(t *testing.T) {
	t.Parallel()
	r := inmem.NewOfferingRepo()
	ctx := context.Background()

	keep := mustOffering(t, "off-search-1", tenantA, false)
	keep.Label = "Intro to Robotics"
	excluded := mustOffering(t, "off-search-2", tenantA, false)
	excluded.Label = "Project Management"
	otherTenant := mustOffering(t, "off-search-3", tenantB, false)
	otherTenant.Label = "Intro to Robotics"
	gone := mustOffering(t, "off-search-4", tenantA, true) // soft-deleted
	if err := r.Save(ctx, keep); err != nil {
		t.Fatalf("Save keep: %v", err)
	}
	if err := r.Save(ctx, excluded); err != nil {
		t.Fatalf("Save excluded: %v", err)
	}
	if err := r.Save(ctx, otherTenant); err != nil {
		t.Fatalf("Save other tenant: %v", err)
	}
	if err := r.Save(ctx, gone); err != nil {
		t.Fatalf("Save gone: %v", err)
	}

	page, err := r.Search(ctx, delivery.OfferingQuery{
		TenantID: tenantA,
		Q:        "robotics",
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != keep.ID {
		t.Fatalf("expected exactly the matching tenantA active row; got %d items", len(page.Items))
	}
	if page.TotalEstimate != 1 {
		t.Fatalf("expected TotalEstimate=1, got %d", page.TotalEstimate)
	}

	// Empty store search still returns a valid page, never an error.
	empty := inmem.NewOfferingRepo()
	page, err = empty.Search(ctx, delivery.OfferingQuery{TenantID: tenantA})
	if err != nil {
		t.Fatalf("Search(empty): %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("empty search must return zero items; got %d", len(page.Items))
	}
}
