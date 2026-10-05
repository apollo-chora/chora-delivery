// certification_unit_test.go — unit tests for pg.CertificationRepo.
//
// Exercises the SQL surface of the append-only certifications store (CHO-2157)
// with a stub Querier, no live DB. Guarantees:
//  1. rls.ApplySession runs BEFORE any data query (every method);
//  2. SQL templates match the expected shape (INSERT … ON CONFLICT DO NOTHING
//     RETURNING, tenant-scoped SELECTs, list ORDER BY issued_at DESC);
//  3. bind-arg order matches the $N placeholders;
//  4. nil-tx returns ErrNotImplemented (fail-loud);
//  5. zero-row scans map to ErrCertAlreadyIssued (Issue) / (nil,false) (Get);
//  6. error paths are returned / wrapped without being folded into "not found".
//
// Live RLS isolation + real append-only enforcement are covered by
// certification_integration_test.go (build tag `integration`).
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// certFillRow writes scanCertification's 8 destinations, in order:
// id, tenant_id, course_id, gcid, accomplishments ([]string), score (*int16),
// signature_hash, issued_at. score is discarded by the repo, so it is left nil.
func certFillRow(dest []any, id, tenant, course, gcid, hash string, acc []string) error {
	if len(dest) != 8 {
		return errors.New("scanCertification: expected 8 destinations")
	}
	*(dest[0].(*string)) = id
	*(dest[1].(*string)) = tenant
	*(dest[2].(*string)) = course
	*(dest[3].(*string)) = gcid
	*(dest[4].(*[]string)) = acc
	*(dest[5].(**int16)) = nil
	*(dest[6].(*string)) = hash
	*(dest[7].(*time.Time)) = time.Now().UTC()
	return nil
}

// -----------------------------------------------------------------------------
// Nil-TxRunner — fail-loud
// -----------------------------------------------------------------------------

func TestCertificationRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCertificationRepo(nil)
	ctx := context.Background()

	if _, err := r.IssueCtx(ctx, tenantID, gcid, courseID, nil, nil); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("IssueCtx: expected ErrNotImplemented; got %v", err)
	}
	if _, _, err := r.GetCtx(ctx, tenantID, "cert-1"); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("GetCtx: expected ErrNotImplemented; got %v", err)
	}
	if _, _, err := r.GetByLearnerCourseCtx(ctx, tenantID, gcid, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("GetByLearnerCourseCtx: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenantCtx(ctx, tenantID, "", ""); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenantCtx: expected ErrNotImplemented; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Input validation
// -----------------------------------------------------------------------------

func TestCertificationRepo_Fields_RejectEmptyTenant(t *testing.T) {
	t.Parallel()
	r := pg.NewCertificationRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()

	// IssueCtx has no explicit tenant guard — the domain constructor owns it.
	if _, err := r.IssueCtx(ctx, "  ", gcid, courseID, nil, nil); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("IssueCtx: expected ErrInvalidArgument; got %v", err)
	}
	if _, _, err := r.GetCtx(ctx, "", "cert-1"); err == nil || !strings.Contains(err.Error(), "tenantID required") {
		t.Fatalf("GetCtx: expected tenantID required; got %v", err)
	}
	if _, _, err := r.GetByLearnerCourseCtx(ctx, "", gcid, courseID); err == nil || !strings.Contains(err.Error(), "tenantID required") {
		t.Fatalf("GetByLearnerCourseCtx: expected tenantID required; got %v", err)
	}
	if _, err := r.ListByTenantCtx(ctx, "", "", ""); err == nil || !strings.Contains(err.Error(), "tenantID required") {
		t.Fatalf("ListByTenantCtx: expected tenantID required; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// IssueCtx — INSERT … ON CONFLICT DO NOTHING RETURNING
// -----------------------------------------------------------------------------

func TestCertificationRepo_IssueCtx_AppliesRLSThenInserts(t *testing.T) {
	t.Parallel()
	score := 95
	acc := []string{"Completed the assessment"}
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return certFillRow(dest, "cert-1", tenantID, courseID, gcid, strings.Repeat("ab", 32), acc)
			}}
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	cert, err := r.IssueCtx(ctx, tenantID, gcid, courseID, &score, acc)
	if err != nil {
		t.Fatalf("IssueCtx: %v", err)
	}
	if cert == nil || cert.ID != "cert-1" {
		t.Fatalf("returned certificate wrong: %+v", cert)
	}
	if cert.LearnerID != gcid || cert.CourseID != courseID {
		t.Fatalf("certificate mapping wrong: %+v", cert)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	insert := q.sqls[1]
	for _, frag := range []string{"INSERT INTO certifications", "ON CONFLICT (course_id, gcid) DO NOTHING", "RETURNING"} {
		if !strings.Contains(insert, frag) {
			t.Fatalf("certification INSERT must contain %q; got %q", frag, insert)
		}
	}
	if len(q.args) < 2 {
		t.Fatalf("expected SET LOCAL + INSERT args; got %d", len(q.args))
	}
	args := q.args[1]
	if len(args) != 8 {
		t.Fatalf("INSERT must bind 8 args; got %d", len(args))
	}
	if args[1] != tenantID || args[2] != courseID || args[3] != gcid {
		t.Fatalf("INSERT tenant/course/gcid args wrong: %v", args)
	}
	if args[5] != &score {
		t.Fatalf("INSERT must bind the caller's score pointer; got %v", args[5])
	}
	if hash, ok := args[6].(string); !ok || len(hash) != 64 {
		t.Fatalf("INSERT must bind a 64-char signature hash; got %v", args[6])
	}
}

func TestCertificationRepo_IssueCtx_Conflict_ReturnsErrCertAlreadyIssued(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.IssueCtx(ctx, tenantID, gcid, courseID, nil, nil)
	if !errors.Is(err, domain.ErrCertAlreadyIssued) {
		t.Fatalf("zero-row RETURNING must map to ErrCertAlreadyIssued; got %v", err)
	}
}

func TestCertificationRepo_IssueCtx_RlsFailure_ReturnsWrappedError(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "SET LOCAL") {
				return errors.New("conn closed")
			}
			return nil
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.IssueCtx(ctx, tenantID, gcid, courseID, nil, nil)
	if err == nil {
		t.Fatalf("expected RLS failure to surface")
	}
	if !strings.Contains(err.Error(), "SET LOCAL chora.tenant_id failed") {
		t.Fatalf("RLS error must be wrapped with context; got %v", err)
	}
	if !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("wrapped error must preserve the underlying cause; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// GetCtx — SELECT by id + tenant
// -----------------------------------------------------------------------------

func TestCertificationRepo_GetCtx_ReturnsCertificate(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return certFillRow(dest, "cert-1", tenantID, courseID, gcid, "hash-1", []string{"a"})
			}}
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	cert, ok, err := r.GetCtx(ctx, tenantID, "cert-1")
	if err != nil || !ok || cert == nil {
		t.Fatalf("GetCtx: ok=%v err=%v cert=%v", ok, err, cert)
	}
	if cert.ID != "cert-1" || cert.TenantID != tenantID {
		t.Fatalf("certificate wrong: %+v", cert)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	sel := q.sqls[1]
	for _, frag := range []string{"SELECT", "FROM certifications", "WHERE certification_id = $1", "AND tenant_id = $2"} {
		if !strings.Contains(sel, frag) {
			t.Fatalf("by-id SELECT must contain %q; got %q", frag, sel)
		}
	}
	if len(q.args) < 2 || len(q.args[1]) != 2 || q.args[1][0] != "cert-1" || q.args[1][1] != tenantID {
		t.Fatalf("by-id SELECT args wrong: %v", q.args[1])
	}
}

func TestCertificationRepo_GetCtx_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	cert, ok, err := r.GetCtx(ctx, tenantID, "nope")
	if err != nil {
		t.Fatalf("GetCtx miss must not error; got %v", err)
	}
	if ok || cert != nil {
		t.Fatalf("expected (nil,false); got (%+v,%v)", cert, ok)
	}
}

func TestCertificationRepo_GetCtx_ScanError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("bad row bytes")
			}}
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	_, _, err := r.GetCtx(ctx, tenantID, "cert-1")
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("scan error must propagate; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// GetByLearnerCourseCtx — the idempotency / "do they hold one?" read
// -----------------------------------------------------------------------------

func TestCertificationRepo_GetByLearnerCourse_ReturnsCertificate(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return certFillRow(dest, "cert-1", tenantID, courseID, gcid, "hash-1", []string{"a"})
			}}
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	cert, ok, err := r.GetByLearnerCourseCtx(ctx, tenantID, gcid, courseID)
	if err != nil || !ok || cert == nil {
		t.Fatalf("GetByLearnerCourseCtx: ok=%v err=%v cert=%v", ok, err, cert)
	}
	if cert.LearnerID != gcid || cert.CourseID != courseID {
		t.Fatalf("certificate wrong: %+v", cert)
	}
	sel := q.sqls[1]
	for _, frag := range []string{"FROM certifications", "WHERE tenant_id = $1", "AND gcid = $2", "AND course_id = $3"} {
		if !strings.Contains(sel, frag) {
			t.Fatalf("by-learner-course SELECT must contain %q; got %q", frag, sel)
		}
	}
	if len(q.args) < 2 || len(q.args[1]) != 3 ||
		q.args[1][0] != tenantID || q.args[1][1] != gcid || q.args[1][2] != courseID {
		t.Fatalf("by-learner-course SELECT args wrong: %v", q.args[1])
	}
}

func TestCertificationRepo_GetByLearnerCourse_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	cert, ok, err := r.GetByLearnerCourseCtx(ctx, tenantID, gcid, courseID)
	if err != nil {
		t.Fatalf("miss must not error; got %v", err)
	}
	if ok || cert != nil {
		t.Fatalf("expected (nil,false); got (%+v,%v)", cert, ok)
	}
}

// -----------------------------------------------------------------------------
// ListByTenant — tenant-scoped list with optional learner/course filters
// -----------------------------------------------------------------------------

func TestCertificationRepo_ListByTenant_ReturnsRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					return certFillRow(dest, "cert-1", tenantID, courseID, gcid, "hash-1", []string{"a"})
				},
				func(dest ...any) error {
					return certFillRow(dest, "cert-2", tenantID, courseID, gcid, "hash-2", []string{"a", "b"})
				},
			}}, nil
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenantCtx(ctx, tenantID, "", "")
	if err != nil {
		t.Fatalf("ListByTenantCtx: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 certificates; got %d", len(out))
	}
	if out[1].ID != "cert-2" || len(out[1].Accomplishments) != 2 {
		t.Fatalf("row 2 not rehydrated: %+v", out[1])
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	list := q.sqls[1]
	for _, frag := range []string{"FROM certifications", "WHERE tenant_id = $1", "ORDER BY issued_at DESC"} {
		if !strings.Contains(list, frag) {
			t.Fatalf("list SELECT must contain %q; got %q", frag, list)
		}
	}
	// Unfiltered list still binds the two optional-filter placeholders as ''.
	if len(q.args) < 2 || len(q.args[1]) != 3 ||
		q.args[1][0] != tenantID || q.args[1][1] != "" || q.args[1][2] != "" {
		t.Fatalf("list args wrong: %v", q.args[1])
	}
}

func TestCertificationRepo_ListByTenant_QueryError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("conn closed")
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.ListByTenantCtx(ctx, tenantID, gcid, courseID)
	if err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("Query error must propagate; got %v", err)
	}
}

func TestCertificationRepo_ListByTenant_RowScanError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewCertificationRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.ListByTenantCtx(ctx, tenantID, gcid, courseID)
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("row-scan error must propagate; got %v", err)
	}
}
