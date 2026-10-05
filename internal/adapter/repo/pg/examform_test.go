// examform_test.go — unit tests for pg.ExamFormRepo (W4 Brick-1).
//
// Mirrors exam_test.go: reuses the shared stubQuerier / stubTxRunner / stubRow /
// stubRows + tenantID const from application_test.go so the SQL surface + RLS
// contract are exercised without a live DB. The ExamForm aggregate is persisted
// as a JSONB snapshot (the 0020_exams / live_poll.go pattern), so reads stub a
// single `data []byte` column carrying the marshalled aggregate.
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
	examFormID    = "019e2f93-d586-71b5-8c3d-e2b0d0d5f001"
	examFormExam  = "019e2f93-d586-71b5-8c3d-e2b0d0d5e001"
	examFormBank  = "019e2f93-d586-71b5-8c3d-e2b0d0d5b001"
	examFormExam2 = "019e2f93-d586-71b5-8c3d-e2b0d0d5e002"
)

func newForm(id, exam2 string, state exam.ExamFormState) *exam.ExamForm {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &exam.ExamForm{
		ID:         id,
		TenantID:   tenantID,
		ExamID:     exam2,
		ItemBankID: examFormBank,
		Items:      []exam.PinnedItem{{ItemID: "it-1", AtomRevisionID: "rev-1", Position: 0}},
		State:      state,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func formJSON(t *testing.T, f *exam.ExamForm) []byte {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal form: %v", err)
	}
	return b
}

func TestExamFormRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewExamFormRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newForm(examFormID, examFormExam, exam.ExamFormStateDraft)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByExam(ctx, tenantID, examFormExam); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByExam: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, examFormID); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestExamFormRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewExamFormRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newForm(examFormID, examFormExam, exam.ExamFormStateAssembled)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO exam_forms") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert into exam_forms; got %q", last)
	}
}

func TestExamFormRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	want := newForm(examFormID, examFormExam, exam.ExamFormStateExposed)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = formJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewExamFormRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	f, ok, _ := r.Get(ctx, want.ID)
	if !ok || f == nil {
		t.Fatalf("Get: expected hit")
	}
	if f.ID != want.ID || f.State != exam.ExamFormStateExposed || len(f.Items) != 1 {
		t.Fatalf("Get: rehydration mismatch; got %+v", f)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM exam_forms") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM exam_forms; got %q", last)
	}
}

func TestExamFormRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewExamFormRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if f, ok, _ := r.Get(ctx, "nope"); ok || f != nil {
		t.Fatalf("Get: expected (nil, false); got (%+v, %v)", f, ok)
	}
}

func TestExamFormRepo_ListByExam_TwoRows(t *testing.T) {
	t.Parallel()
	a := newForm(examFormID, examFormExam, exam.ExamFormStateDraft)
	b := newForm("019e2f93-d586-71b5-8c3d-e2b0d0d5f002", examFormExam, exam.ExamFormStateRetired)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = formJSON(t, a); return nil },
				func(dest ...any) error { *(dest[0].(*[]byte)) = formJSON(t, b); return nil },
			}}, nil
		},
	}
	r := pg.NewExamFormRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByExam(ctx, tenantID, examFormExam)
	if err != nil {
		t.Fatalf("ListByExam: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rows; got %d", len(out))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "exam_id = $2") ||
		!strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant+exam-scoped, soft-delete-filtered, ordered list; got %q", last)
	}
}
