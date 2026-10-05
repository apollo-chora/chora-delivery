// sitting_repos_test.go — unit tests for the W4 Brick-B pg repos (SittingRepo +
// InvigilatorRepo + IncidentRepo). Reuses the shared stubQuerier / stubTxRunner
// / stubRow / stubRows + tenantID / gcid consts (application_test.go) so the SQL
// surface + RLS contract are exercised without a live DB. Each aggregate is a
// JSONB snapshot (exam.go pattern).
//
// Guarantees per repo:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL shape (sitting/invig = UPSERT ON CONFLICT (id); incident = plain
//     INSERT, append-only; scoped SELECT ... deleted_at IS NULL ORDER BY id).
//  3. Nil-tx fails loud on writes/list (ErrNotImplemented) + degrades on point
//     reads (ok=false).
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

const (
	sbExamPG = "01970000-0000-7000-8000-0000000000f1"
	sbSitPG  = "01970000-0000-7000-8000-0000000000f2"
)

func jsonBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// -----------------------------------------------------------------------------
// SittingRepo
// -----------------------------------------------------------------------------

func newSitting(id string, state exam.ExamSittingState) *exam.ExamSitting {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &exam.ExamSitting{
		ID: id, TenantID: tenantID, ExamID: sbExamPG, RoomID: "room-a",
		StartsAt: now, EndsAt: now.Add(time.Hour), Capacity: 10,
		State: state, CreatedAt: now, UpdatedAt: now,
	}
}

func TestSittingRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewSittingRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newSitting(sbSitPG, exam.ExamSittingStateScheduled)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: want ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByExam(ctx, tenantID, sbExamPG); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByExam: want ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, sbSitPG); ok {
		t.Fatalf("Get: want ok=false on nil-tx")
	}
}

func TestSittingRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSittingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newSitting(sbSitPG, exam.ExamSittingStateOpen)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO exam_sittings") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert into exam_sittings; got %q", last)
	}
}

func TestSittingRepo_Get_Hit_And_ListSQL(t *testing.T) {
	t.Parallel()
	want := newSitting(sbSitPG, exam.ExamSittingStateInProgress)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { *(dest[0].(*[]byte)) = jsonBytes(t, want); return nil }}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = jsonBytes(t, want); return nil },
			}}, nil
		},
	}
	r := pg.NewSittingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, ok, _ := r.Get(ctx, sbSitPG)
	if !ok || got.ID != want.ID || got.State != exam.ExamSittingStateInProgress {
		t.Fatalf("Get: rehydration mismatch; ok=%v got=%+v", ok, got)
	}
	if _, err := r.ListByExam(ctx, tenantID, sbExamPG); err != nil {
		t.Fatalf("ListByExam: %v", err)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM exam_sittings") || !strings.Contains(last, "tenant_id = $1") ||
		!strings.Contains(last, "exam_id = $2") || !strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant+exam scoped, soft-delete-filtered, ordered list; got %q", last)
	}
}

// -----------------------------------------------------------------------------
// InvigilatorRepo
// -----------------------------------------------------------------------------

func newInvig(id string, rank exam.InvigilatorRank) *exam.ExamInvigilator {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &exam.ExamInvigilator{
		ID: id, TenantID: tenantID, SittingID: sbSitPG, InvigilatorGCID: gcid,
		Rank: rank, AssignedAt: now, UpdatedAt: now,
	}
}

func TestInvigilatorRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewInvigilatorRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newInvig("01970000-0000-7000-9999-a00000000001", exam.InvigilatorRankChief)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: want ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListBySitting(ctx, tenantID, sbSitPG); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListBySitting: want ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "x"); ok {
		t.Fatalf("Get: want ok=false on nil-tx")
	}
}

func TestInvigilatorRepo_Save_UpsertsWithRankColumn(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewInvigilatorRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newInvig("01970000-0000-7000-9999-a00000000002", exam.InvigilatorRankChief)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO exam_invigilators") || !strings.Contains(last, "ON CONFLICT (id)") {
		t.Fatalf("expected upsert into exam_invigilators; got %q", last)
	}
	// rank must be a bound extracted column so the partial-unique chief index keys off it.
	args := q.args[len(q.args)-1]
	foundRank := false
	for _, a := range args {
		if s, ok := a.(string); ok && s == string(exam.InvigilatorRankChief) {
			foundRank = true
		}
	}
	if !foundRank {
		t.Fatalf("rank must be bound as an extracted column; args=%#v", args)
	}
}

func TestInvigilatorRepo_ListBySitting_SQL(t *testing.T) {
	t.Parallel()
	a := newInvig("01970000-0000-7000-9999-a0000000000a", exam.InvigilatorRankInvigilator)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = jsonBytes(t, a); return nil },
			}}, nil
		},
	}
	r := pg.NewInvigilatorRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	out, err := r.ListBySitting(ctx, tenantID, sbSitPG)
	if err != nil || len(out) != 1 {
		t.Fatalf("ListBySitting: err=%v len=%d", err, len(out))
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM exam_invigilators") || !strings.Contains(last, "tenant_id = $1") ||
		!strings.Contains(last, "sitting_id = $2") || !strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant+sitting scoped, soft-delete-filtered, ordered list; got %q", last)
	}
}

// -----------------------------------------------------------------------------
// IncidentRepo (append-only — plain INSERT, no ON CONFLICT)
// -----------------------------------------------------------------------------

func newIncidentPG(id string) *exam.IncidentReport {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &exam.IncidentReport{
		ID: id, TenantID: tenantID, SittingID: sbSitPG, ReportedByGCID: gcid,
		Kind: exam.IncidentKindDeviceViolation, Narrative: "smartwatch", OccurredAt: now, CreatedAt: now,
	}
}

func TestIncidentRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewIncidentRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Append(ctx, newIncidentPG("01970000-0000-7000-9999-b00000000001")); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Append: want ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListBySitting(ctx, tenantID, sbSitPG); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListBySitting: want ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "x"); ok {
		t.Fatalf("Get: want ok=false on nil-tx")
	}
}

func TestIncidentRepo_Append_IsAppendOnlyInsert(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewIncidentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Append(ctx, newIncidentPG("01970000-0000-7000-9999-b00000000002")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO incident_reports") {
		t.Fatalf("expected INSERT INTO incident_reports; got %q", last)
	}
	// Append-only: NEVER an UPDATE path.
	if strings.Contains(last, "ON CONFLICT") || strings.Contains(last, "DO UPDATE") {
		t.Fatalf("incident_reports is append-only — must not UPDATE; got %q", last)
	}
}

func TestIncidentRepo_ListBySitting_SQL(t *testing.T) {
	t.Parallel()
	a := newIncidentPG("01970000-0000-7000-9999-b0000000000a")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = jsonBytes(t, a); return nil },
			}}, nil
		},
	}
	r := pg.NewIncidentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	out, err := r.ListBySitting(ctx, tenantID, sbSitPG)
	if err != nil || len(out) != 1 {
		t.Fatalf("ListBySitting: err=%v len=%d", err, len(out))
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM incident_reports") || !strings.Contains(last, "tenant_id = $1") ||
		!strings.Contains(last, "sitting_id = $2") || !strings.Contains(last, "ORDER BY") {
		t.Fatalf("expected tenant+sitting scoped, ordered list; got %q", last)
	}
}

func TestInvigilatorRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	want := newInvig("01970000-0000-7000-9999-a0000000000f", exam.InvigilatorRankTechnicalSupport)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { *(dest[0].(*[]byte)) = jsonBytes(t, want); return nil }}
		},
	}
	r := pg.NewInvigilatorRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	got, ok, _ := r.Get(ctx, want.ID)
	if !ok || got.ID != want.ID || got.Rank != exam.InvigilatorRankTechnicalSupport {
		t.Fatalf("Get: rehydration mismatch; ok=%v got=%+v", ok, got)
	}
	if !strings.Contains(q.sqls[len(q.sqls)-1], "FROM exam_invigilators") || !strings.Contains(q.sqls[len(q.sqls)-1], "deleted_at IS NULL") {
		t.Fatalf("Get SQL shape; got %q", q.sqls[len(q.sqls)-1])
	}
}

func TestIncidentRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	want := newIncidentPG("01970000-0000-7000-9999-b0000000000f")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { *(dest[0].(*[]byte)) = jsonBytes(t, want); return nil }}
		},
	}
	r := pg.NewIncidentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	got, ok, _ := r.Get(ctx, want.ID)
	if !ok || got.ID != want.ID || got.Kind != exam.IncidentKindDeviceViolation {
		t.Fatalf("Get: rehydration mismatch; ok=%v got=%+v", ok, got)
	}
	if !strings.Contains(q.sqls[len(q.sqls)-1], "FROM incident_reports") {
		t.Fatalf("Get SQL shape; got %q", q.sqls[len(q.sqls)-1])
	}
}

// Compile-time assertions that the pg repos satisfy the domain ports.
var (
	_ exam.ExamSittingStore     = (*pg.SittingRepo)(nil)
	_ exam.ExamInvigilatorStore = (*pg.InvigilatorRepo)(nil)
	_ exam.IncidentReportStore  = (*pg.IncidentRepo)(nil)
)
