// port_test.go - behaviour of the in-memory ProgressPort. It is the store every
// handler/subscriber test runs against, so a defect here would go green
// everywhere else while hiding a real disagreement with the pg adapter: both
// must load-or-create, scope by tenant, and exclude soft-deleted rows.
package courseprogress_test

import (
	"context"
	"testing"
	"time"

	cp "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
)

const cpOther = "22222222-2222-7222-8222-222222222222" // a second tenant

func newStore() (*cp.InMemProgressStore, context.Context) {
	return cp.NewInMemProgressStore(), context.Background()
}

// -----------------------------------------------------------------------------
// Advance: load-or-create + persist
// -----------------------------------------------------------------------------

func TestInMemStore_AdvanceCreatesThenUpdatesOneRow(t *testing.T) {
	s, ctx := newStore()
	changed, err := s.Advance(ctx, cpTenant, cpGCID, cpCourse, cpPath, 2, 10, cpT0)
	if err != nil || !changed {
		t.Fatalf("first Advance: changed=%v err=%v", changed, err)
	}
	if changed, err = s.Advance(ctx, cpTenant, cpGCID, cpCourse, cpPath, 6, 10, cpT0.Add(time.Minute)); err != nil || !changed {
		t.Fatalf("second Advance: changed=%v err=%v", changed, err)
	}
	// Must be ONE row that moved, not two rows racing.
	rows, err := s.ListByCourseIDs(ctx, cpTenant, []string{cpCourse})
	if err != nil {
		t.Fatalf("ListByCourseIDs: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 projection per (tenant,gcid,course), got %d", len(rows))
	}
	if rows[0].CompletedAtoms != 6 {
		t.Fatalf("CompletedAtoms: want 6, got %d", rows[0].CompletedAtoms)
	}
}

func TestInMemStore_AdvanceIdempotentRedeliveryDoesNotChange(t *testing.T) {
	s, ctx := newStore()
	if _, err := s.Advance(ctx, cpTenant, cpGCID, cpCourse, cpPath, 2, 10, cpT0); err != nil {
		t.Fatalf("first: %v", err)
	}
	changed, err := s.Advance(ctx, cpTenant, cpGCID, cpCourse, cpPath, 2, 10, cpT0)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if changed {
		t.Fatal("changed: want false on an identical redelivery")
	}
}

func TestInMemStore_AdvanceRejectsInvalidCounts(t *testing.T) {
	s, ctx := newStore()
	if _, err := s.Advance(ctx, cpTenant, cpGCID, cpCourse, cpPath, 11, 10, cpT0); err == nil {
		t.Fatal("want error when completed exceeds total, got nil")
	}
}

func TestInMemStore_AdvanceRejectsBlankScope(t *testing.T) {
	s, ctx := newStore()
	if _, err := s.Advance(ctx, "", cpGCID, cpCourse, cpPath, 1, 10, cpT0); err == nil {
		t.Fatal("want error on a blank tenant, got nil")
	}
}

// -----------------------------------------------------------------------------
// Complete: must load-or-create (completed.v1 can arrive with no prior advance)
// -----------------------------------------------------------------------------

func TestInMemStore_CompleteCreatesRowWithNoPriorAdvance(t *testing.T) {
	s, ctx := newStore()
	changed, err := s.Complete(ctx, cpTenant, cpGCID, cpCourse, cpPath, cpT0)
	if err != nil || !changed {
		t.Fatalf("Complete: changed=%v err=%v", changed, err)
	}
	p, ok, err := s.GetByLearnerCourse(ctx, cpTenant, cpGCID, cpCourse)
	if err != nil || !ok {
		t.Fatalf("projection missing: ok=%v err=%v", ok, err)
	}
	if !p.IsComplete {
		t.Fatal("IsComplete: want true - a completion with no prior advance must still land")
	}
	if p.ProgressFraction() != 1.0 {
		t.Fatalf("ProgressFraction: want 1.0, got %v", p.ProgressFraction())
	}
}

func TestInMemStore_CompleteIsIdempotent(t *testing.T) {
	s, ctx := newStore()
	if _, err := s.Complete(ctx, cpTenant, cpGCID, cpCourse, cpPath, cpT0); err != nil {
		t.Fatalf("first: %v", err)
	}
	changed, err := s.Complete(ctx, cpTenant, cpGCID, cpCourse, cpPath, cpT0.Add(time.Hour))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if changed {
		t.Fatal("changed: want false on a repeat completion")
	}
}

func TestInMemStore_CompleteRejectsBlankScope(t *testing.T) {
	s, ctx := newStore()
	if _, err := s.Complete(ctx, cpTenant, "", cpCourse, cpPath, cpT0); err == nil {
		t.Fatal("want error on a blank gcid, got nil")
	}
}

// -----------------------------------------------------------------------------
// Reads
// -----------------------------------------------------------------------------

func TestInMemStore_GetByLearnerCourse_MissIsNotAnError(t *testing.T) {
	s, ctx := newStore()
	p, ok, err := s.GetByLearnerCourse(ctx, cpTenant, cpGCID, cpCourse)
	if err != nil {
		t.Fatalf("a genuine miss must not be an error, got %v", err)
	}
	if ok || p != nil {
		t.Fatalf("want (nil,false), got (%v,%v)", p, ok)
	}
}

// Tenant isolation is enforced in-process here; the pg adapter defers to RLS.
// Both must agree, or a test that passes in-mem would mask a cross-tenant leak.
func TestInMemStore_IsTenantScoped(t *testing.T) {
	s, ctx := newStore()
	if _, err := s.Advance(ctx, cpTenant, cpGCID, cpCourse, cpPath, 5, 10, cpT0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok, _ := s.GetByLearnerCourse(ctx, cpOther, cpGCID, cpCourse); ok {
		t.Fatal("another tenant must NOT see this projection")
	}
	rows, err := s.ListByCourseIDs(ctx, cpOther, []string{cpCourse})
	if err != nil {
		t.Fatalf("ListByCourseIDs: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("another tenant must list 0 rows, got %d", len(rows))
	}
}

func TestInMemStore_ListByCourseIDs_FiltersToRequestedCourses(t *testing.T) {
	s, ctx := newStore()
	const otherCourse = "01985e7f-5555-7abc-8def-000000000c02"
	if _, err := s.Advance(ctx, cpTenant, cpGCID, cpCourse, cpPath, 5, 10, cpT0); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if _, err := s.Advance(ctx, cpTenant, cpGCID, otherCourse, cpPath, 1, 10, cpT0); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	rows, err := s.ListByCourseIDs(ctx, cpTenant, []string{cpCourse})
	if err != nil {
		t.Fatalf("ListByCourseIDs: %v", err)
	}
	if len(rows) != 1 || rows[0].CourseID != cpCourse {
		t.Fatalf("want only the requested course, got %+v", rows)
	}
}

func TestInMemStore_ListByCourseIDs_EmptyRequestIsEmpty(t *testing.T) {
	s, ctx := newStore()
	rows, err := s.ListByCourseIDs(ctx, cpTenant, nil)
	if err != nil {
		t.Fatalf("ListByCourseIDs(nil): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want 0 rows, got %d", len(rows))
	}
}

func TestInMemStore_ListByCourseIDs_SortedByCourseThenGCID(t *testing.T) {
	s, ctx := newStore()
	const courseB = "01985e7f-5555-7abc-8def-000000000c02"
	for _, seed := range []struct{ gcid, course string }{
		{"g2", courseB}, {"g1", courseB}, {"g2", cpCourse}, {"g1", cpCourse},
	} {
		if _, err := s.Advance(ctx, cpTenant, seed.gcid, seed.course, cpPath, 1, 10, cpT0); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	rows, err := s.ListByCourseIDs(ctx, cpTenant, []string{cpCourse, courseB})
	if err != nil {
		t.Fatalf("ListByCourseIDs: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 rows, got %d", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		if cur.CourseID < prev.CourseID ||
			(cur.CourseID == prev.CourseID && cur.GCID < prev.GCID) {
			t.Fatalf("rows not sorted by (course,gcid): %s/%s before %s/%s",
				prev.CourseID, prev.GCID, cur.CourseID, cur.GCID)
		}
	}
}

// -----------------------------------------------------------------------------
// Soft delete
// -----------------------------------------------------------------------------

func TestInMemStore_SoftDeletedRowIsExcludedAndRecreated(t *testing.T) {
	s, ctx := newStore()
	if _, err := s.Advance(ctx, cpTenant, cpGCID, cpCourse, cpPath, 5, 10, cpT0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p, _, err := s.GetByLearnerCourse(ctx, cpTenant, cpGCID, cpCourse)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	p.SoftDelete()

	if _, ok, _ := s.GetByLearnerCourse(ctx, cpTenant, cpGCID, cpCourse); ok {
		t.Fatal("a soft-deleted projection must not be returned")
	}
	rows, err := s.ListByCourseIDs(ctx, cpTenant, []string{cpCourse})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a soft-deleted projection must not be listed, got %d", len(rows))
	}
	// A later event must start a FRESH projection rather than resurrect the
	// deleted one (soft-delete is never undone by a write).
	if _, err := s.Advance(ctx, cpTenant, cpGCID, cpCourse, cpPath, 1, 10, cpT0.Add(time.Hour)); err != nil {
		t.Fatalf("post-delete advance: %v", err)
	}
	fresh, ok, err := s.GetByLearnerCourse(ctx, cpTenant, cpGCID, cpCourse)
	if err != nil || !ok {
		t.Fatalf("a fresh projection must exist: ok=%v err=%v", ok, err)
	}
	if fresh.ID == p.ID {
		t.Fatal("the soft-deleted row was resurrected; want a NEW projection")
	}
	if fresh.CompletedAtoms != 1 {
		t.Fatalf("fresh CompletedAtoms: want 1, got %d", fresh.CompletedAtoms)
	}
}

func TestSoftDelete_IsIdempotent(t *testing.T) {
	p := mustNew(t)
	p.SoftDelete()
	first := *p.DeletedAt
	p.SoftDelete()
	if !p.DeletedAt.Equal(first) {
		t.Fatalf("SoftDelete must keep the FIRST stamp, got %v want %v", p.DeletedAt, first)
	}
	if p.IsActive() {
		t.Fatal("IsActive: want false after SoftDelete")
	}
}

func TestIsActive_TrueWhenFresh(t *testing.T) {
	if !mustNew(t).IsActive() {
		t.Fatal("IsActive: want true for a fresh projection")
	}
}
