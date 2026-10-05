// examresult_repo_test.go — additional coverage for the in-memory
// ExamResultRepo beyond examform_repo_test.go: the Get miss path and the
// ListByExam surface (tenant + exam scoping, soft-delete exclusion, and
// most-recent-first ordering by ScoredAt — CHO-2104).
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

func mkScoredResult(id, tenantID, examID, formID string, scoredAt time.Time) *exam.ExamResult {
	return &exam.ExamResult{
		ID:           id,
		TenantID:     tenantID,
		ExamID:       examID,
		ExamFormID:   formID,
		CandidateRef: "cand-" + id,
		RawScore:     80,
		MaxScore:     100,
		Outcome:      exam.OutcomePass,
		ScoredAt:     scoredAt,
		CreatedAt:    scoredAt,
	}
}

func TestInmemExamResultRepo_GetMiss(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamResultRepo()
	if got, ok, err := r.Get(context.Background(), "absent"); ok || err != nil || got != nil {
		t.Fatalf("Get unknown: ok=%v err=%v got=%v", ok, err, got)
	}
}

func TestInmemExamResultRepo_ListByExam(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamResultRepo()
	ctx := context.Background()

	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	older := mkScoredResult("r-old", itTenantA, itExam1, itForm1, base)
	// Save the NEWER one first — ordering is the repo's job, not the caller's.
	newer := mkScoredResult("r-new", itTenantA, itExam1, itForm1, base.Add(time.Hour))
	otherExam := mkScoredResult("r-other-exam", itTenantA, itExam2, itForm1, base.Add(2*time.Hour))
	otherTenant := mkScoredResult("r-other-tenant", itTenantB, itExam1, itForm1, base.Add(3*time.Hour))
	gone := mkScoredResult("r-gone", itTenantA, itExam1, itForm1, base.Add(4*time.Hour))
	d := base.Add(5 * time.Hour)
	gone.DeletedAt = &d

	for _, res := range []*exam.ExamResult{newer, older, otherExam, otherTenant, gone} {
		if err := r.Save(ctx, res); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	out, err := r.ListByExam(ctx, itTenantA, itExam1)
	if err != nil {
		t.Fatalf("ListByExam: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 (tenant+exam scoped, soft-delete filtered); got %d", len(out))
	}
	// Most-recent first by ScoredAt (CHO-2104).
	if out[0].ID != "r-new" || out[1].ID != "r-old" {
		t.Fatalf("expected [r-new r-old] by ScoredAt desc, got [%s %s]", out[0].ID, out[1].ID)
	}
}
