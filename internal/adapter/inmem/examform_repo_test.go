// examform_repo_test.go — direct unit tests for the W4 Brick-1 in-memory
// doubles (ExamFormRepo + ExamResultRepo). These also round-trip through the
// HTTP handler tests, but a direct suite keeps the adapter honestly covered
// in-package and pins tenant scoping + write-once semantics.
package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

const (
	itTenantA = "019e2f93-d586-71b5-8c3d-e2b0d0d5aa01"
	itTenantB = "019e2f93-d586-71b5-8c3d-e2b0d0d5aa02"
	itExam1   = "019e2f93-d586-71b5-8c3d-e2b0d0d5ee01"
	itExam2   = "019e2f93-d586-71b5-8c3d-e2b0d0d5ee02"
	itForm1   = "019e2f93-d586-71b5-8c3d-e2b0d0d5ff01"
)

func mkForm(id, tenant, examID string, deleted bool) *exam.ExamForm {
	f := &exam.ExamForm{
		ID:         id,
		TenantID:   tenant,
		ExamID:     examID,
		ItemBankID: "bank",
		State:      exam.ExamFormStateAssembled,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if deleted {
		d := time.Now().UTC()
		f.DeletedAt = &d
	}
	return f
}

func TestInmemExamFormRepo_SaveGetList(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamFormRepo()
	ctx := context.Background()

	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("nil save: %v", err)
	}
	_ = r.Save(ctx, mkForm(itForm1, itTenantA, itExam1, false))
	_ = r.Save(ctx, mkForm("f2", itTenantA, itExam1, false))
	_ = r.Save(ctx, mkForm("f3", itTenantA, itExam2, false)) // different exam
	_ = r.Save(ctx, mkForm("f4", itTenantB, itExam1, false)) // different tenant
	_ = r.Save(ctx, mkForm("f5", itTenantA, itExam1, true))  // soft-deleted

	if got, ok, _ := r.Get(ctx, itForm1); !ok || got.ID != itForm1 {
		t.Fatalf("Get hit failed: %+v ok=%v", got, ok)
	}
	if _, ok, _ := r.Get(ctx, "missing"); ok {
		t.Fatal("Get miss should be ok=false")
	}

	list, err := r.ListByExam(ctx, itTenantA, itExam1)
	if err != nil {
		t.Fatalf("ListByExam: %v", err)
	}
	// f1 + f2 only — different exam, different tenant, and soft-deleted excluded.
	if len(list) != 2 {
		t.Fatalf("ListByExam len=%d want 2 (tenant+exam scoped, soft-delete filtered)", len(list))
	}
}

func mkResult(id, tenant, formID string, outcome exam.Outcome) *exam.ExamResult {
	return &exam.ExamResult{
		ID:           id,
		TenantID:     tenant,
		ExamID:       itExam1,
		ExamFormID:   formID,
		CandidateRef: "cand",
		RawScore:     70,
		MaxScore:     100,
		Outcome:      outcome,
		ScoredAt:     time.Now().UTC(),
		CreatedAt:    time.Now().UTC(),
	}
}

func TestInmemExamResultRepo_WriteOnceAndScope(t *testing.T) {
	t.Parallel()
	r := inmem.NewExamResultRepo()
	ctx := context.Background()

	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("nil save: %v", err)
	}
	_ = r.Save(ctx, mkResult("r1", itTenantA, itForm1, exam.OutcomePass))
	// write-once: a second Save with the same id must NOT overwrite the outcome.
	_ = r.Save(ctx, mkResult("r1", itTenantA, itForm1, exam.OutcomeFail))
	got, ok, _ := r.Get(ctx, "r1")
	if !ok || got.Outcome != exam.OutcomePass {
		t.Fatalf("write-once violated: outcome=%v ok=%v", got.Outcome, ok)
	}

	_ = r.Save(ctx, mkResult("r2", itTenantA, itForm1, exam.OutcomeFail))
	_ = r.Save(ctx, mkResult("r3", itTenantA, "other-form", exam.OutcomePass)) // different form
	_ = r.Save(ctx, mkResult("r4", itTenantB, itForm1, exam.OutcomePass))      // different tenant

	list, err := r.ListByForm(ctx, itTenantA, itForm1)
	if err != nil {
		t.Fatalf("ListByForm: %v", err)
	}
	if len(list) != 2 { // r1 + r2 only
		t.Fatalf("ListByForm len=%d want 2 (tenant+form scoped)", len(list))
	}
}
