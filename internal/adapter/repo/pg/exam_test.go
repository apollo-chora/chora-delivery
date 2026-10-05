// exam_test.go — unit tests for pg.ExamRepo (R+ durability sweep).
//
// Mirrors booking_test.go: stubs the Querier (shared stubQuerier / stubTxRunner
// / stubRow / stubRows + tenantID const from application_test.go) so the SQL
// surface + RLS contract are exercised without a live DB. The Exam aggregate is
// persisted as a JSONB snapshot (live_poll.go pattern), so reads stub a single
// `data []byte` column carrying the marshalled aggregate.
//
// Guarantees:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL matches shape (UPSERT ON CONFLICT (id), SELECT data ... deleted_at
//     IS NULL, ORDER BY id).
//  3. Nil-tx is fail-loud on writes (Save / ListByTenant → ErrNotImplemented)
//     and degrades on point reads (Get → ok=false).
//  4. JSONB round-trip rehydrates the aggregate.
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

func newExam(id string, state exam.ExamState) *exam.Exam {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &exam.Exam{
		ID:        id,
		TenantID:  tenantID,
		CourseID:  courseID,
		State:     state,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func examJSON(t *testing.T, e *exam.Exam) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal exam: %v", err)
	}
	return b
}

func TestExamRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewExamRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newExam("01970000-0000-7000-9999-e00000000001", exam.ExamStateDraft)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "01970000-0000-7000-9999-e00000000001"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestExamRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewExamRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newExam("01970000-0000-7000-9999-e00000000002", exam.ExamStateScheduled)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO exams") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert into exams; got %q", last)
	}
}

func TestExamRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	want := newExam("01970000-0000-7000-9999-e00000000003", exam.ExamStateOpen)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = examJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewExamRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	e, ok, _ := r.Get(ctx, want.ID)
	if !ok || e == nil {
		t.Fatalf("Get: expected hit")
	}
	if e.ID != want.ID || e.State != exam.ExamStateOpen {
		t.Fatalf("Get: rehydration mismatch; got %+v", e)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM exams") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM exams; got %q", last)
	}
}

func TestExamRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewExamRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if e, ok, _ := r.Get(ctx, "nope"); ok || e != nil {
		t.Fatalf("Get: expected (nil, false); got (%+v, %v)", e, ok)
	}
}

func TestExamRepo_ListByTenant_TwoRows(t *testing.T) {
	t.Parallel()
	a := newExam("01970000-0000-7000-9999-e000000000aa", exam.ExamStateDraft)
	b := newExam("01970000-0000-7000-9999-e000000000bb", exam.ExamStateGraded)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = examJSON(t, a); return nil },
				func(dest ...any) error { *(dest[0].(*[]byte)) = examJSON(t, b); return nil },
			}}, nil
		},
	}
	r := pg.NewExamRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rows; got %d", len(out))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant-scoped, soft-delete-filtered, ordered list; got %q", last)
	}
}
