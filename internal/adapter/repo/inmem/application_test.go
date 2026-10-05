// Package inmem application repo tests — S6.1.
//
// Idempotent submit invariant: re-firing submit with same (course_id, gcid)
// returns the existing application_id (NOT a duplicate row, NOT a 409).
//
// Listing scoped by tenant_id + gcid (the learner's own applications),
// ordered newest-first.
//
// TDD strict: RED before GREEN.
package inmem_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	tenantB = "01970000-0000-7000-8000-000000000002"
	courseA = "01970000-0000-7000-8000-000000000099"
	courseB = "01970000-0000-7000-8000-000000000098"
	gcidA   = "01970000-0000-7000-9000-000000000001"
	gcidB   = "01970000-0000-7000-9000-000000000002"
)

// -----------------------------------------------------------------------------
// SubmitOrGet — idempotent insert
// -----------------------------------------------------------------------------

func TestApplicationRepo_SubmitOrGet_NewApplication(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()

	app, created, err := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantA,
		CourseID: courseA,
		GCID:     gcidA,
	})
	if err != nil {
		t.Fatalf("SubmitOrGet: %v", err)
	}
	if !created {
		t.Fatalf("expected created=true on first submit")
	}
	if app.Status != application.StatusSubmitted {
		t.Fatalf("expected status=submitted post-submit; got %s", app.Status)
	}
	if app.ID == "" {
		t.Fatalf("expected non-empty id")
	}
}

func TestApplicationRepo_SubmitOrGet_Idempotent_SameCourseGCID(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()

	first, _, err := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	if err != nil {
		t.Fatalf("first SubmitOrGet: %v", err)
	}

	second, created, err := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	if err != nil {
		t.Fatalf("second SubmitOrGet: %v", err)
	}
	if created {
		t.Fatalf("expected created=false on second submit")
	}
	if first.ID != second.ID {
		t.Fatalf("expected same id on idempotent submit; got %s vs %s", first.ID, second.ID)
	}
}

func TestApplicationRepo_SubmitOrGet_TenantIsolated(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()

	a, _, _ := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})
	b, _, _ := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantB, CourseID: courseA, GCID: gcidA,
	})
	if a.ID == b.ID {
		t.Fatalf("cross-tenant submits must be distinct rows; got %s twice", a.ID)
	}
}

// -----------------------------------------------------------------------------
// Get + Save
// -----------------------------------------------------------------------------

func TestApplicationRepo_Save_GetByID_TenantScoped(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()

	app, _, _ := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantA, CourseID: courseA, GCID: gcidA,
	})

	got, ok, err := r.Get(ctx, tenantA, app.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if got.ID != app.ID {
		t.Fatalf("Get: id mismatch")
	}

	// Cross-tenant Get must return ok=false (RLS-equivalent at adapter level).
	_, ok, err = r.Get(ctx, tenantB, app.ID)
	if err != nil {
		t.Fatalf("Get cross-tenant: %v", err)
	}
	if ok {
		t.Fatalf("cross-tenant Get must return ok=false")
	}
}

// -----------------------------------------------------------------------------
// List by GCID — paginated
// -----------------------------------------------------------------------------

func TestApplicationRepo_ListByGCID_FiltersTenantAndGCID(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()

	_, _, _ = r.SubmitOrGet(ctx, application.SubmitInput{TenantID: tenantA, CourseID: courseA, GCID: gcidA})
	_, _, _ = r.SubmitOrGet(ctx, application.SubmitInput{TenantID: tenantA, CourseID: courseB, GCID: gcidA})
	_, _, _ = r.SubmitOrGet(ctx, application.SubmitInput{TenantID: tenantA, CourseID: courseA, GCID: gcidB})
	_, _, _ = r.SubmitOrGet(ctx, application.SubmitInput{TenantID: tenantB, CourseID: courseA, GCID: gcidA})

	items, total, err := r.ListByGCID(ctx, tenantA, gcidA, 0, 10)
	if err != nil {
		t.Fatalf("ListByGCID: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 apps for tenantA+gcidA; got %d", total)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items; got %d", len(items))
	}
	for _, it := range items {
		if it.TenantID != tenantA || it.GCID != gcidA {
			t.Fatalf("scope leak: %s %s", it.TenantID, it.GCID)
		}
	}
}

func TestApplicationRepo_ListByGCID_RespectsLimit(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()
	courses := []string{
		"01970000-0000-7000-8000-000000000099",
		"01970000-0000-7000-8000-000000000098",
		"01970000-0000-7000-8000-000000000097",
		"01970000-0000-7000-8000-000000000096",
		"01970000-0000-7000-8000-000000000095",
	}
	for _, c := range courses {
		_, _, _ = r.SubmitOrGet(ctx, application.SubmitInput{TenantID: tenantA, CourseID: c, GCID: gcidA})
	}
	items, total, err := r.ListByGCID(ctx, tenantA, gcidA, 0, 2)
	if err != nil {
		t.Fatalf("ListByGCID: %v", err)
	}
	if total != 5 {
		t.Fatalf("total: got %d want 5", total)
	}
	if len(items) != 2 {
		t.Fatalf("limit=2: got %d", len(items))
	}
}

// -----------------------------------------------------------------------------
// Save — persists state changes
// -----------------------------------------------------------------------------

func TestApplicationRepo_Save_PersistsTransition(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()

	app, _, _ := r.SubmitOrGet(ctx, application.SubmitInput{TenantID: tenantA, CourseID: courseA, GCID: gcidA})
	if err := app.Transition(application.StatusUnderReview); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := r.Save(ctx, app); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, _ := r.Get(ctx, tenantA, app.ID)
	if !ok {
		t.Fatalf("post-Save Get failed")
	}
	if got.Status != application.StatusUnderReview {
		t.Fatalf("Save did not persist status; got %s", got.Status)
	}
}

func TestApplicationRepo_Save_NilRejected(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	if err := r.Save(context.Background(), nil); err == nil {
		t.Fatalf("expected error on nil Save")
	}
}

func TestApplicationRepo_SubmitOrGet_RejectsBlanks(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()
	cases := []application.SubmitInput{
		{CourseID: courseA, GCID: gcidA},
		{TenantID: tenantA, GCID: gcidA},
		{TenantID: tenantA, CourseID: courseA},
	}
	for _, c := range cases {
		if _, _, err := r.SubmitOrGet(ctx, c); err == nil {
			t.Fatalf("expected error for input %+v", c)
		}
	}
}

func TestApplicationRepo_Get_UnknownReturnsOkFalse(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	got, ok, err := r.Get(context.Background(), tenantA, "does-not-exist")
	if err != nil {
		t.Fatalf("Get unknown: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("expected ok=false / nil for unknown id")
	}
}

func TestApplicationRepo_ListByGCID_OffsetExceedsTotal(t *testing.T) {
	t.Parallel()
	r := inmem.NewApplicationRepo()
	ctx := context.Background()
	_, _, _ = r.SubmitOrGet(ctx, application.SubmitInput{TenantID: tenantA, CourseID: courseA, GCID: gcidA})
	items, total, err := r.ListByGCID(ctx, tenantA, gcidA, 10, 5)
	if err != nil {
		t.Fatalf("ListByGCID: %v", err)
	}
	if total != 1 {
		t.Fatalf("total: got %d", total)
	}
	if len(items) != 0 {
		t.Fatalf("offset>total should return empty; got %d", len(items))
	}
}
