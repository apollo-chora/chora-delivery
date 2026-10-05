// candidate_repo_test.go — direct unit tests for the in-memory CandidateRepo
// (ADR-190 D2). Pins (tenant, exam, gcid) resolution, soft-delete exclusion
// for the active-candidate lookup, and the ListByExam tenant/exam scoping +
// ID ordering.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

const (
	candExam = "019e2f93-d586-71b5-8c3d-e2b0d0d5ee01"
	candGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d5ee02"
)

func mkCandidate(id, tenant, gcID string) *exam.Candidate {
	return &exam.Candidate{
		ID:       id,
		TenantID: tenant,
		ExamID:   candExam,
		GCID:     gcID,
	}
}

func TestCandidateRepo_SaveNilIsNoop(t *testing.T) {
	t.Parallel()
	r := inmem.NewCandidateRepo()
	if err := r.Save(context.Background(), nil); err != nil {
		t.Fatalf("Save(nil) must be a safe no-op; got %v", err)
	}
	if out, err := r.ListByExam(context.Background(), tenantA, candExam); err != nil || len(out) != 0 {
		t.Fatalf("store must stay empty after Save(nil); len=%d err=%v", len(out), err)
	}
}

func TestCandidateRepo_GetByExamAndGCID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := inmem.NewCandidateRepo()
	c := mkCandidate("cand-1", tenantA, candGCID)
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := r.GetByExamAndGCID(ctx, tenantA, candExam, candGCID)
	if err != nil || !ok {
		t.Fatalf("GetByExamAndGCID: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != c.ID {
		t.Fatalf("expected candidate %s, got %s", c.ID, got.ID)
	}

	// Wrong tenant / exam / gcid are all genuine misses.
	if _, ok, _ := r.GetByExamAndGCID(ctx, tenantB, candExam, candGCID); ok {
		t.Fatal("cross-tenant must be a miss")
	}
	if _, ok, _ := r.GetByExamAndGCID(ctx, tenantA, "other-exam", candGCID); ok {
		t.Fatal("wrong exam must be a miss")
	}
	if _, ok, _ := r.GetByExamAndGCID(ctx, tenantA, candExam, "other-gcid"); ok {
		t.Fatal("wrong gcid must be a miss")
	}
}

func TestCandidateRepo_GetByExamAndGCID_SoftDeletedIsMiss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := inmem.NewCandidateRepo()
	c := mkCandidate("cand-2", tenantA, candGCID)
	now := time.Now().UTC()
	c.DeletedAt = &now
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok, _ := r.GetByExamAndGCID(ctx, tenantA, candExam, candGCID); ok {
		t.Fatal("soft-deleted candidate must resolve as a miss (active-only)")
	}
}

func TestCandidateRepo_ListByExam_FiltersAndSorts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := inmem.NewCandidateRepo()

	a1 := mkCandidate("cand-a1", tenantA, candGCID)
	a2 := mkCandidate("cand-a2", tenantA, "019e2f93-d586-71b5-8c3d-e2b0d0d5ee03")
	b1 := mkCandidate("cand-b1", tenantB, candGCID)       // other tenant
	other := mkCandidate("cand-x1", tenantA, candGCID)    // other exam below
	other.ExamID = "019e2f93-d586-71b5-8c3d-e2b0d0d5ee09" // (different exam)
	gone := mkCandidate("cand-gone", tenantA, candGCID)
	now := time.Now().UTC()
	gone.DeletedAt = &now

	for _, c := range []*exam.Candidate{a1, a2, b1, other, gone} {
		if err := r.Save(ctx, c); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := r.ListByExam(ctx, tenantA, candExam)
	if err != nil {
		t.Fatalf("ListByExam: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 (tenant+exam scoped, soft-deleted excluded); got %d", len(out))
	}
	// Sorted by ID ascending (UUIDv7 ⇒ allocation order).
	if out[0].ID != "cand-a1" || out[1].ID != "cand-a2" {
		t.Fatalf("expected [cand-a1 cand-a2], got [%s %s]", out[0].ID, out[1].ID)
	}

	empty, err := r.ListByExam(ctx, tenantB, candExam)
	if err != nil {
		t.Fatalf("ListByExam(tenantB): %v", err)
	}
	// tenantB sees exactly its own candidate (cand-b1); tenantA's rows never
	// surface cross-tenant.
	if len(empty) != 1 || empty[0].ID != "cand-b1" {
		t.Fatalf("tenantB must see only its own candidate; got %d items", len(empty))
	}
}
