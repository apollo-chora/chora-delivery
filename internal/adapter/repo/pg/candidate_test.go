// candidate_test.go — unit tests for pg.CandidateRepo (ADR-190 D2 exam
// candidate admission).
//
// Mirrors exam_test.go: reuses the shared stubQuerier / stubTxRunner / stubRow
// / stubRows + tenantID / gcid consts from application_test.go so the SQL
// surface + RLS contract are exercised without a live DB. The Candidate
// aggregate is persisted as a JSONB snapshot (exam.go pattern), so reads stub a
// single `data []byte` column carrying the marshalled aggregate.
//
// Guarantees:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL matches shape (UPSERT ON CONFLICT (id); SELECT data ... deleted_at IS
//     NULL; GetByExamAndGCID filters tenant_id+exam_id+gcid; ListByExam orders
//     by id).
//  3. Nil-tx is fail-loud on writes (Save / ListByExam → ErrNotImplemented) and
//     degrades on point reads (GetByExamAndGCID → ok=false).
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

const candExamID = "01970000-0000-7000-8000-0000000000ee"

func newCandidate(id string, state exam.CandidateState, vs exam.VerificationStatus) *exam.Candidate {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &exam.Candidate{
		ID:                 id,
		TenantID:           tenantID,
		ExamID:             candExamID,
		GCID:               gcid,
		State:              state,
		VerificationStatus: vs,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func candidateJSON(t *testing.T, c *exam.Candidate) []byte {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	return b
}

func TestCandidateRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewCandidateRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newCandidate("01970000-0000-7000-9999-c00000000001", exam.CandidateStateAllocated, exam.VerificationStatusUnverified)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByExam(ctx, tenantID, candExamID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByExam: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.GetByExamAndGCID(ctx, tenantID, candExamID, gcid); ok {
		t.Fatalf("GetByExamAndGCID: expected ok=false on nil-tx")
	}
}

func TestCandidateRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCandidateRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newCandidate("01970000-0000-7000-9999-c00000000002", exam.CandidateStateIDVerified, exam.VerificationStatusVerified)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO exam_candidates") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert into exam_candidates; got %q", last)
	}
	// Extracted columns must be bound (tenant_id, exam_id, gcid, state,
	// verification_status) so RLS + roster listing key off real columns.
	args := q.args[len(q.args)-1]
	if len(args) < 8 {
		t.Fatalf("upsert must bind id+tenant+exam+gcid+state+verification_status+timestamps; got %d args", len(args))
	}
}

func TestCandidateRepo_GetByExamAndGCID_Hit(t *testing.T) {
	t.Parallel()
	want := newCandidate("01970000-0000-7000-9999-c00000000003", exam.CandidateStateIDVerified, exam.VerificationStatusVerified)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = candidateJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewCandidateRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	c, ok, _ := r.GetByExamAndGCID(ctx, tenantID, candExamID, gcid)
	if !ok || c == nil {
		t.Fatalf("GetByExamAndGCID: expected hit")
	}
	if c.ID != want.ID || c.State != exam.CandidateStateIDVerified || c.VerificationStatus != exam.VerificationStatusVerified {
		t.Fatalf("rehydration mismatch; got %+v", c)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM exam_candidates") ||
		!strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "exam_id = $2") ||
		!strings.Contains(last, "gcid = $3") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected tenant+exam+gcid scoped, soft-delete-filtered SELECT; got %q", last)
	}
}

func TestCandidateRepo_GetByExamAndGCID_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewCandidateRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if c, ok, _ := r.GetByExamAndGCID(ctx, tenantID, candExamID, gcid); ok || c != nil {
		t.Fatalf("expected (nil, false); got (%+v, %v)", c, ok)
	}
}

func TestCandidateRepo_ListByExam_TwoRows(t *testing.T) {
	t.Parallel()
	a := newCandidate("01970000-0000-7000-9999-c00000000aaa", exam.CandidateStateAllocated, exam.VerificationStatusUnverified)
	b := newCandidate("01970000-0000-7000-9999-c00000000bbb", exam.CandidateStateAdmitted, exam.VerificationStatusVerified)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = candidateJSON(t, a); return nil },
				func(dest ...any) error { *(dest[0].(*[]byte)) = candidateJSON(t, b); return nil },
			}}, nil
		},
	}
	r := pg.NewCandidateRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByExam(ctx, tenantID, candExamID)
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
		t.Fatalf("expected tenant+exam scoped, soft-delete-filtered, ordered list; got %q", last)
	}
}
