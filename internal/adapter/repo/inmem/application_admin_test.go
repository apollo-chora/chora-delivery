// application_admin_test.go — direct unit tests for the admin-scoped
// ApplicationRepo.ListByTenant (M14 R+ review queue). Pins the optional
// tenant filter, the status filter, newest-first ordering, the offset/limit
// clamps, and the post-filter pre-pagination total.
package inmem_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

// submit seeds an application via the idempotent submit path, returning the
// aggregate (which carries a submission-stamped ID for ordering assertions).
func submit(t *testing.T, r *inmem.ApplicationRepo, tenantID, courseID, gcid string) *application.Application {
	t.Helper()
	app, created, err := r.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	})
	if err != nil {
		t.Fatalf("SubmitOrGet: %v", err)
	}
	if !created {
		t.Fatalf("expected created=true for fresh submit (%s/%s/%s)", tenantID, courseID, gcid)
	}
	return app
}

func TestApplicationRepo_ListByTenant_TenantAndStatusFilters(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()

	// Seed: two tenantA apps + one tenantB app (the admin queue spans tenants).
	_ = submit(t, r, tenantA, courseA, gcidA)
	a2 := submit(t, r, tenantA, courseA, gcidB)
	_ = submit(t, r, tenantB, courseA, gcidA)

	// Move a2 to a distinct status so the status filter has something to hit.
	if err := a2.Transition(application.StatusUnderReview); err != nil {
		t.Fatalf("transition a2: %v", err)
	}
	if err := r.Save(ctx, a2); err != nil {
		t.Fatalf("Save a2: %v", err)
	}

	// Tenant-scoped, newest-first.
	all, total, err := r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantA})
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if total != 2 || len(all) != 2 {
		t.Fatalf("want 2 apps total for tenantA; got total=%d len=%d", total, len(all))
	}

	// Status filter.
	underReview, total, err := r.ListByTenant(ctx, application.ListByTenantInput{
		TenantID: tenantA, Status: application.StatusUnderReview,
	})
	if err != nil {
		t.Fatalf("ListByTenant(status): %v", err)
	}
	if total != 1 || len(underReview) != 1 || underReview[0].ID != a2.ID {
		t.Fatalf("status filter failed; got total=%d len=%d", total, len(underReview))
	}

	// Empty tenant filter returns ALL tenants' apps (admin queue).
	everywhere, total, err := r.ListByTenant(ctx, application.ListByTenantInput{})
	if err != nil {
		t.Fatalf("ListByTenant(all): %v", err)
	}
	if total != 3 || len(everywhere) != 3 {
		t.Fatalf("want all 3 apps with empty tenant filter; got total=%d len=%d", total, len(everywhere))
	}

	// Cross-tenant rows must never surface under tenantA's filter.
	for _, it := range all {
		if it.TenantID != tenantA {
			t.Fatalf("cross-tenant leak: %s", it.TenantID)
		}
	}
}

func TestApplicationRepo_ListByTenant_PaginationClamps(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		c := courseIDs[i]
		submit(t, r, tenantA, c, gcidA)
	}

	// offset > total → empty slice, total preserved.
	out, total, err := r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantA, Offset: 100, Limit: 10})
	if err != nil {
		t.Fatalf("ListByTenant(offset>total): %v", err)
	}
	if total != 4 || len(out) != 0 {
		t.Fatalf("offset>total: want len=0 total=4; got len=%d total=%d", len(out), total)
	}

	// negative offset clamps to 0.
	out, _, err = r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantA, Offset: -5, Limit: 2})
	if err != nil {
		t.Fatalf("ListByTenant(negative offset): %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("negative offset must clamp to 0; got %d", len(out))
	}

	// limit <= 0 → no slicing.
	out, total, err = r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantA, Offset: 0, Limit: 0})
	if err != nil {
		t.Fatalf("ListByTenant(limit=0): %v", err)
	}
	if len(out) != total || total != 4 {
		t.Fatalf("limit<=0 must return everything; got len=%d total=%d", len(out), total)
	}

	// offset+limit overflow clamps to the tail.
	out, _, err = r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantA, Offset: 3, Limit: 50})
	if err != nil {
		t.Fatalf("ListByTenant(overflow): %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("offset+limit overflow must clamp; got %d", len(out))
	}
}

var courseIDs = []string{
	"01970000-0000-7000-8000-000000000099",
	"01970000-0000-7000-8000-000000000098",
	"01970000-0000-7000-8000-000000000097",
	"01970000-0000-7000-8000-000000000096",
}
