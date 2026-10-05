// Package inmem_test exercises the in-memory repository adapters with
// emphasis on tenant filtering, soft-delete invisibility, and pagination
// math edge cases.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/skillsfutures"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	tenantB = "01970000-0000-7000-8000-000000000002"
	gcidA   = "01970000-0000-7000-9000-000000000001"
)

func TestCourseRepo_SaveAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewCourseRepo()
	c, _ := domain.NewCourse(tenantA, "x", nil, 1)
	if err := repo.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := repo.Get(ctx, tenantA, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("Get: expected hit")
	}
	if got.ID != c.ID {
		t.Fatalf("Get: ID mismatch")
	}
	if _, ok, _ := repo.Get(ctx, tenantA, "nope"); ok {
		t.Fatalf("Get: expected miss")
	}
}

// ADR-236 D1 — the pre-widening Get(ctx, id) had NO tenant parameter at all
// (caller-discipline isolation only): any caller holding a course_id could
// read another tenant's course out of the shared in-memory map. Get is now
// tenant-scoped like pg.CourseRepo.Get(ctx, tenantID, courseID) — a
// cross-tenant lookup MUST report a genuine miss (ok=false), never the
// other tenant's row.
func TestCourseRepo_Get_CrossTenant_ReturnsMiss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewCourseRepo()
	c, _ := domain.NewCourse(tenantA, "tenantA's course", nil, 1)
	if err := repo.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := repo.Get(ctx, tenantB, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("cross-tenant leak: tenantB read tenantA's course: ok=%v got=%+v", ok, got)
	}
}

// Get must exclude soft-deleted rows — mirrors pg.CourseRepo.Get's
// `deleted_at IS NULL` filter and this package's own
// InMemCourseCJ2Store.Get (course_cj2_port.go), which already does this.
func TestCourseRepo_Get_SoftDeleted_ReturnsMiss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewCourseRepo()
	c, _ := domain.NewCourse(tenantA, "x", nil, 1)
	if err := repo.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	c.SoftDelete()
	if err := repo.Save(ctx, c); err != nil {
		t.Fatalf("Save (soft-delete): %v", err)
	}
	if _, ok, _ := repo.Get(ctx, tenantA, c.ID); ok {
		t.Fatalf("Get: expected miss on soft-deleted course")
	}
}

func TestCourseRepo_ListByTenant_FiltersByTenantAndSoftDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewCourseRepo()
	a1, _ := domain.NewCourse(tenantA, "a1", nil, 1)
	a2, _ := domain.NewCourse(tenantA, "a2", nil, 1)
	b1, _ := domain.NewCourse(tenantB, "b1", nil, 1)
	_ = repo.Save(ctx, a1)
	_ = repo.Save(ctx, a2)
	_ = repo.Save(ctx, b1)
	a2.SoftDelete()
	_ = repo.Save(ctx, a2)

	out, total, err := repo.ListByTenant(ctx, tenantA, 0, 50)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if total != 1 || len(out) != 1 {
		t.Fatalf("ListByTenant: expected total=1,len=1; got total=%d,len=%d", total, len(out))
	}
	if out[0].ID != a1.ID {
		t.Fatalf("ListByTenant: expected a1, got %q", out[0].ID)
	}
}

func TestCourseRepo_ListByTenant_PagingEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewCourseRepo()
	for i := 0; i < 3; i++ {
		c, _ := domain.NewCourse(tenantA, "x", nil, 1)
		_ = repo.Save(ctx, c)
		// UUIDv7 carries unix_ts_ms; force monotonic spread so sort
		// produces stable ordering even on fast machines.
		time.Sleep(2 * time.Millisecond)
	}
	// offset > total: should return empty slice, total=3
	out, total, err := repo.ListByTenant(ctx, tenantA, 100, 10)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if total != 3 || len(out) != 0 {
		t.Fatalf("offset>total: expected len=0 total=3, got len=%d total=%d", len(out), total)
	}
	// limit smaller than total
	out, _, err = repo.ListByTenant(ctx, tenantA, 0, 2)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("limit=2: expected 2 items, got %d", len(out))
	}
	// offset + limit overflow
	out, _, err = repo.ListByTenant(ctx, tenantA, 2, 50)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("offset=2,limit=50: expected 1 item, got %d", len(out))
	}
}

func TestBookingRepo_SaveAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewBookingRepo()
	b, _ := domain.NewBookingForClass("class-1", "course-1", tenantA, gcidA)
	if err := repo.Save(ctx, b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, _ := repo.Get(ctx, b.ID)
	if !ok {
		t.Fatalf("Get: expected hit")
	}
	if got.ID != b.ID {
		t.Fatalf("Get: ID mismatch")
	}
	if _, ok, _ := repo.Get(ctx, "nope"); ok {
		t.Fatalf("Get: expected miss")
	}
}

// TestBookingRepo_ListByTenant verifies tenant filtering + soft-delete
// invisibility for the GET /api/bookings list (HANDOFF_RPLUS §6 follow-up).
func TestBookingRepo_ListByTenant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewBookingRepo()

	// Two bookings in tenantA (one soft-deleted) + one in tenantB.
	mk := func(tenant string) *domain.Booking {
		b, _ := domain.NewBookingForClass("class-1", "course-1", tenant, gcidA)
		return b
	}
	live := mk(tenantA)
	gone := mk(tenantA)
	gone.SoftDelete()
	other := mk(tenantB)
	for _, b := range []*domain.Booking{live, gone, other} {
		if err := repo.Save(ctx, b); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := repo.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 active tenantA booking; got %d", len(out))
	}
	if out[0].ID != live.ID {
		t.Fatalf("expected live booking %s; got %s", live.ID, out[0].ID)
	}
}

// -----------------------------------------------------------------------------
// SkillsFuturesRepo — RED→GREEN coverage for tenant filtering + state filtering.
// -----------------------------------------------------------------------------

func newSFClaim(tenantID, gcid string) *skillsfutures.SkillsFuturesClaim {
	c, _ := skillsfutures.NewClaim(skillsfutures.NewClaimInput{
		TenantID:             tenantID,
		GCID:                 gcid,
		CourseID:             "01970000-0000-7000-9000-000000000003",
		NRICHash:             "sha256:0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa",
		RequestedAmountCents: 50000,
	})
	return c
}

func TestSkillsFuturesRepo_SaveAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewSkillsFuturesRepo()
	c := newSFClaim(tenantA, gcidA)
	_ = repo.Save(ctx, c)
	got, ok, _ := repo.Get(ctx, c.ID)
	if !ok {
		t.Fatalf("Get: expected hit")
	}
	if got.ID != c.ID {
		t.Fatalf("Get: ID mismatch")
	}
	if _, ok, _ := repo.Get(ctx, "missing"); ok {
		t.Fatalf("Get: expected miss")
	}
}

func TestSkillsFuturesRepo_ListByTenant_FiltersByTenant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewSkillsFuturesRepo()
	_ = repo.Save(ctx, newSFClaim(tenantA, gcidA))
	time.Sleep(2 * time.Millisecond) // monotonic UUIDv7 spread
	_ = repo.Save(ctx, newSFClaim(tenantA, gcidA))
	_ = repo.Save(ctx, newSFClaim(tenantB, gcidA))

	out, _ := repo.ListByTenant(ctx, tenantA, "")
	if len(out) != 2 {
		t.Fatalf("ListByTenant(A): expected 2 got %d", len(out))
	}
	for _, c := range out {
		if c.TenantID != tenantA {
			t.Errorf("cross-tenant leak: %q", c.TenantID)
		}
	}
}

func TestSkillsFuturesRepo_ListByTenant_FiltersByState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := inmem.NewSkillsFuturesRepo()
	c1 := newSFClaim(tenantA, gcidA)
	c2 := newSFClaim(tenantA, gcidA)
	_ = c2.Reject("admin-gcid", "missing docs")
	_ = repo.Save(ctx, c1)
	_ = repo.Save(ctx, c2)

	pending, _ := repo.ListByTenant(ctx, tenantA, skillsfutures.ClaimStatePending)
	if len(pending) != 1 || pending[0].ID != c1.ID {
		t.Fatalf("PENDING filter: got %d items (want 1 with c1.ID)", len(pending))
	}
	rejected, _ := repo.ListByTenant(ctx, tenantA, skillsfutures.ClaimStateRejected)
	if len(rejected) != 1 || rejected[0].ID != c2.ID {
		t.Fatalf("REJECTED filter: got %d items (want 1 with c2.ID)", len(rejected))
	}
	all, _ := repo.ListByTenant(ctx, tenantA, "")
	if len(all) != 2 {
		t.Fatalf("no state filter: got %d items (want 2)", len(all))
	}
}

// -----------------------------------------------------------------------------
// LiveQuizRepo — R+ M9 wave-5. Tenant scoping + UUIDv7 ID ordering.
// -----------------------------------------------------------------------------

const instructorA = "01970000-0000-7000-9000-000000000010"

func newDraftLiveQuiz(t *testing.T, tenantID, instructorGCID, title string) *classroom.LiveQuiz {
	t.Helper()
	q, err := classroom.NewLiveQuiz(classroom.NewLiveQuizInput{
		TenantID:       tenantID,
		InstructorGCID: instructorGCID,
		Title:          title,
		CourseID:       "course-cspo",
	})
	if err != nil {
		t.Fatalf("NewLiveQuiz: %v", err)
	}
	return q
}

func TestLiveQuizRepo_SaveAndGet(t *testing.T) {
	t.Parallel()
	repo := inmem.NewLiveQuizRepo()
	q := newDraftLiveQuiz(t, tenantA, instructorA, "Sprint Planning")
	repo.Save(q)
	got, ok, _ := repo.Get(q.ID)
	if !ok {
		t.Fatalf("Get: expected hit")
	}
	if got.ID != q.ID {
		t.Fatalf("Get: ID mismatch")
	}
	if _, ok, _ := repo.Get("missing"); ok {
		t.Fatalf("Get: expected miss for unknown id")
	}
}

func TestLiveQuizRepo_SaveNilIsNoop(t *testing.T) {
	t.Parallel()
	repo := inmem.NewLiveQuizRepo()
	repo.Save(nil) // must not panic; must leave the store empty
	if got := repo.ListByTenant(tenantA); len(got) != 0 {
		t.Fatalf("expected empty list after Save(nil); got %d", len(got))
	}
}

func TestLiveQuizRepo_ListByTenant_FiltersByTenant(t *testing.T) {
	t.Parallel()
	repo := inmem.NewLiveQuizRepo()
	repo.Save(newDraftLiveQuiz(t, tenantA, instructorA, "A1"))
	time.Sleep(2 * time.Millisecond) // monotonic UUIDv7 spread
	repo.Save(newDraftLiveQuiz(t, tenantA, instructorA, "A2"))
	repo.Save(newDraftLiveQuiz(t, tenantB, instructorA, "B1"))

	out := repo.ListByTenant(tenantA)
	if len(out) != 2 {
		t.Fatalf("ListByTenant(A): expected 2 got %d", len(out))
	}
	for _, q := range out {
		if q.TenantID != tenantA {
			t.Errorf("cross-tenant leak: tenant=%q", q.TenantID)
		}
	}
}

func TestLiveQuizRepo_ListByTenant_SortedByID(t *testing.T) {
	t.Parallel()
	repo := inmem.NewLiveQuizRepo()
	q1 := newDraftLiveQuiz(t, tenantA, instructorA, "First")
	repo.Save(q1)
	time.Sleep(2 * time.Millisecond)
	q2 := newDraftLiveQuiz(t, tenantA, instructorA, "Second")
	repo.Save(q2)
	time.Sleep(2 * time.Millisecond)
	q3 := newDraftLiveQuiz(t, tenantA, instructorA, "Third")
	repo.Save(q3)

	out := repo.ListByTenant(tenantA)
	if len(out) != 3 {
		t.Fatalf("expected 3 got %d", len(out))
	}
	if out[0].ID != q1.ID || out[1].ID != q2.ID || out[2].ID != q3.ID {
		t.Fatalf("expected UUIDv7 creation-order; got [%s, %s, %s] want [%s, %s, %s]",
			out[0].ID, out[1].ID, out[2].ID, q1.ID, q2.ID, q3.ID)
	}
}

func TestLiveQuizRepo_ListByTenant_EmptyWhenNoMatches(t *testing.T) {
	t.Parallel()
	repo := inmem.NewLiveQuizRepo()
	repo.Save(newDraftLiveQuiz(t, tenantB, instructorA, "Other tenant only"))
	out := repo.ListByTenant(tenantA)
	if len(out) != 0 {
		t.Fatalf("expected 0 items got %d (cross-tenant leak)", len(out))
	}
}
