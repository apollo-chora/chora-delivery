// exam_repo_test.go — direct unit tests for the in-memory ExamRepo
// (W4 Brick-1 / M12 add-only track). Pins round-trip persistence, tenant +
// soft-delete scoping of the catalogue list, and ID ordering.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

func mkExam(id, tenantID string, deleted bool) *exam.Exam {
	e := &exam.Exam{
		ID:       id,
		TenantID: tenantID,
		Title:    "exam " + id,
	}
	if deleted {
		now := time.Now().UTC()
		e.DeletedAt = &now
	}
	return e
}

func TestExamRepo_SaveGet(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamRepo()
	ctx := context.Background()

	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	e := mkExam("exam-1", tenantA, false)
	if err := r.Save(ctx, e); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(ctx, e.ID)
	if err != nil || !ok {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != e.ID {
		t.Fatalf("round-trip mismatch")
	}
	if _, ok, _ := r.Get(ctx, "missing"); ok {
		t.Fatal("Get: expected miss for unknown id")
	}
}

func TestExamRepo_Get_SoftDeletedStillResolves(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamRepo()
	ctx := context.Background()
	e := mkExam("exam-2", tenantA, true)
	if err := r.Save(ctx, e); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Get is a raw id lookup (no tenant/soft-delete filter) — the row
	// resolves; the ListByTenant catalogue applies the deleted_at filter.
	if _, ok, _ := r.Get(ctx, e.ID); !ok {
		t.Fatal("soft-deleted exam must still resolve by id (filtering is a list concern)")
	}
}

func TestExamRepo_ListByTenant_FiltersAndSorts(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamRepo()
	ctx := context.Background()
	for _, e := range []*exam.Exam{
		mkExam("exam-a1", tenantA, false),
		mkExam("exam-a2", tenantA, false),
		mkExam("exam-b1", tenantB, false),
		mkExam("exam-gone", tenantA, true), // soft-deleted
	} {
		if err := r.Save(ctx, e); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 (tenant-scoped + soft-delete filtered); got %d", len(out))
	}
	if out[0].ID != "exam-a1" || out[1].ID != "exam-a2" {
		t.Fatalf("expected [exam-a1 exam-a2] sorted by ID, got [%s %s]", out[0].ID, out[1].ID)
	}

	empty, err := r.ListByTenant(ctx, tenantB)
	if err != nil {
		t.Fatalf("ListByTenant(tenantB): %v", err)
	}
	if len(empty) != 1 {
		t.Fatalf("tenantB should see exactly its own exam; got %d", len(empty))
	}
}
