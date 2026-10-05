// examresult_test.go — unit tests for pg.ExamResultRepo (W4 Brick-1).
//
// The durable exam_results row is the source of truth for a candidate's
// outcome, so Save is a write-once idempotent INSERT (ON CONFLICT DO NOTHING —
// a scored result is immutable, never overwritten). Reuses the shared stubs +
// tenantID const from application_test.go.
package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

const (
	examResultID   = "019e2f93-d586-71b5-8c3d-e2b0d0d5a001"
	examResultForm = "019e2f93-d586-71b5-8c3d-e2b0d0d5f001"
	examResultCand = "019e2f93-d586-71b5-8c3d-e2b0d0d5c001"
)

func newResult(id string, outcome exam.Outcome) *exam.ExamResult {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &exam.ExamResult{
		ID:           id,
		TenantID:     tenantID,
		ExamID:       examFormExam,
		ExamFormID:   examResultForm,
		CandidateRef: examResultCand,
		RawScore:     72,
		MaxScore:     100,
		Outcome:      outcome,
		ScoredAt:     now,
		CreatedAt:    now,
	}
}

func resultJSON(t *testing.T, r *exam.ExamResult) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return b
}

func TestExamResultRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewExamResultRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newResult(examResultID, exam.OutcomePass)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByForm(ctx, tenantID, examResultForm); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByForm: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, examResultID); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestExamResultRepo_Save_AppliesRLSThenInsertsWriteOnce(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewExamResultRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newResult(examResultID, exam.OutcomeFail)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO exam_results") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO NOTHING") {
		t.Fatalf("expected write-once insert into exam_results; got %q", last)
	}
}

func TestExamResultRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	want := newResult(examResultID, exam.OutcomePass)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = resultJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewExamResultRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, ok, _ := r.Get(ctx, want.ID)
	if !ok || got == nil {
		t.Fatalf("Get: expected hit")
	}
	if got.ID != want.ID || got.Outcome != exam.OutcomePass || got.MaxScore != 100 {
		t.Fatalf("Get: rehydration mismatch; got %+v", got)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM exam_results") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM exam_results; got %q", last)
	}
}

func TestExamResultRepo_ListByForm_TwoRows(t *testing.T) {
	t.Parallel()
	a := newResult(examResultID, exam.OutcomePass)
	b := newResult("019e2f93-d586-71b5-8c3d-e2b0d0d5a002", exam.OutcomeFail)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = resultJSON(t, a); return nil },
				func(dest ...any) error { *(dest[0].(*[]byte)) = resultJSON(t, b); return nil },
			}}, nil
		},
	}
	r := pg.NewExamResultRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByForm(ctx, tenantID, examResultForm)
	if err != nil {
		t.Fatalf("ListByForm: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rows; got %d", len(out))
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "exam_form_id = $2") ||
		!strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant+form-scoped, soft-delete-filtered, ordered list; got %q", last)
	}
}
