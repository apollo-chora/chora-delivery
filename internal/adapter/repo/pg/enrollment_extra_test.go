// enrollment_extra_test.go — extra pg.EnrollmentRepo unit tests: finish
// partial statement coverage of enrollment.go.
//
// Extends enrollment_test.go (same pg_test package) — reuses the shared
// stubQuerier / stubTxRunner / stubRow / stubRows fixtures from
// application_test.go + the tenantID / courseID / gcid consts.
//
// New helpers/fillers/consts in this file are prefixed `enx` to keep the
// package-level pg_test namespace unique across parallel agents.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// enxFillEnrollment fills a Scan dest shaped like scanEnrollment (9 cols) with
// the given values; nil ptrs write the nullable columns as SQL NULL.
func enxFillEnrollment(id string, status string) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) != 9 {
			return errors.New("scanEnrollment: expected 9 destinations")
		}
		*(dest[0].(*string)) = id
		*(dest[1].(*string)) = tenantID
		*(dest[2].(*string)) = courseID
		*(dest[3].(*string)) = gcid
		*(dest[4].(*time.Time)) = time.Now().UTC().Truncate(time.Microsecond)
		*(dest[5].(**time.Time)) = nil
		*(dest[6].(*string)) = status
		*(dest[7].(**time.Time)) = nil
		*(dest[8].(**bool)) = nil
		return nil
	}
}

// -----------------------------------------------------------------------------
// Register — the conflict-fallback lookup-error wrap
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_Register_ConflictLookupError_Wrapped(t *testing.T) {
	t.Parallel()
	var queryRowCalls int
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			queryRowCalls++
			if queryRowCalls == 1 {
				// INSERT … DO NOTHING conflict — RETURNING empty.
				return stubRow{scanFn: func(dest ...any) error {
					return errors.New("no rows in result set")
				}}
			}
			// Natural-key fallback SELECT fails (infra) — must be wrapped with
			// the "conflict but natural-key lookup empty" context, not surfaced
			// raw.
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("conn reset")
			}}
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, err := r.Register(ctx, tenantID, courseID, gcid)
	if err == nil {
		t.Fatalf("expected the lookup error to propagate")
	}
	if !strings.Contains(err.Error(), "conflict but natural-key lookup empty") {
		t.Fatalf("lookup error must be wrapped with conflict context; got %v", err)
	}
	if !strings.Contains(err.Error(), "conn reset") {
		t.Fatalf("wrapped error must preserve the underlying cause; got %v", err)
	}
	if len(q.sqls) < 3 {
		t.Fatalf("expected SET LOCAL + INSERT + natural-key SELECT; got %d", len(q.sqls))
	}
}

// -----------------------------------------------------------------------------
// Get — miss path + RLS-session failure
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_Get_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	enr, ok, err := r.Get(ctx, "absent")
	if err != nil {
		t.Fatalf("Get miss: err=%v (a genuine miss is not an error)", err)
	}
	if ok || enr != nil {
		t.Fatalf("expected (nil, false); got (%+v, %v)", enr, ok)
	}
}

func TestEnrollmentRepo_Get_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	// No tenant on ctx → ApplySession refuses before the SELECT.
	if _, _, err := r.Get(context.Background(), "id"); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestEnrollmentRepo_GetByCourseAndGCID_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	if _, _, err := r.GetByCourseAndGCID(context.Background(), tenantID, courseID, gcid); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListByGCID / ListByCourse — query- + scan-error propagation
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_ListByGCID_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("connection reset by peer")
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, wantErr
	}}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	out, err := r.ListByGCID(ctx, tenantID, gcid)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v; got %v", wantErr, err)
	}
	if out != nil {
		t.Fatalf("expected nil slice alongside the error; got %v", out)
	}
}

func TestEnrollmentRepo_ListByGCID_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListByGCID(ctx, tenantID, gcid); err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
}

func TestEnrollmentRepo_ListByCourse_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("connection reset by peer")
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, wantErr
	}}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListByCourse(ctx, tenantID, courseID); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v; got %v", wantErr, err)
	}
}

func TestEnrollmentRepo_ListByCourse_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListByCourse(ctx, tenantID, courseID); err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
}

// -----------------------------------------------------------------------------
// CountByCourse — count-scan error branch
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_CountByCourse_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return errors.New("conn closed") }}
	}}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.CountByCourse(ctx, tenantID, courseID); err == nil {
		t.Fatalf("expected the count-scan error to propagate")
	}
}

// -----------------------------------------------------------------------------
// MarkCompleted — nil-enrollment guard + exec-error propagation
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_MarkCompleted_NilEnrollment_ReturnsError(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.MarkCompleted(ctx, nil)
	if err == nil {
		t.Fatalf("expected an error for a nil enrollment")
	}
	if !strings.Contains(err.Error(), "non-nil enrollment") {
		t.Fatalf("nil-enrollment error must name the requirement; got %v", err)
	}
}

func TestEnrollmentRepo_MarkCompleted_ExecError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "UPDATE course_enrollments") {
			return errors.New("write failed")
		}
		return nil
	}}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	e, _ := domain.NewEnrollment(tenantID, courseID, gcid)
	if err := e.Complete(true, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := r.MarkCompleted(ctx, e); err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
}

// -----------------------------------------------------------------------------
// Cancel — RLS-session failure + exec-error propagation
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_Cancel_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	if err := r.Cancel(context.Background(), tenantID, "eid-1"); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestEnrollmentRepo_Cancel_ExecError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "UPDATE course_enrollments") {
			return errors.New("write failed")
		}
		return nil
	}}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Cancel(ctx, tenantID, "eid-1"); err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
}

func TestEnrollmentRepo_GetByCourseAndGCID_HappyPath_ScansRow(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: enxFillEnrollment("01970000-0000-7000-9999-bb0000000001", "active")}
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	enr, ok, err := r.GetByCourseAndGCID(ctx, tenantID, courseID, gcid)
	if err != nil || !ok {
		t.Fatalf("GetByCourseAndGCID: ok=%v err=%v", ok, err)
	}
	if enr.ID != "01970000-0000-7000-9999-bb0000000001" || enr.CourseID != courseID || enr.GCID != gcid {
		t.Fatalf("natural-key scan mismatch: %+v", enr)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("natural-key SELECT must be soft-delete-aware; got %q", last)
	}
}

func TestEnrollmentRepo_Register_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	if _, err := r.Register(context.Background(), tenantID, courseID, gcid); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

// enx bare-context battery: every remaining method must refuse a missing RLS
// session (ApplySession error body) instead of querying unscoped.

func TestEnrollmentRepo_ListByGCID_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	if _, err := r.ListByGCID(context.Background(), tenantID, gcid); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestEnrollmentRepo_ListByCourse_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	if _, err := r.ListByCourse(context.Background(), tenantID, courseID); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestEnrollmentRepo_CountByCourse_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	if _, err := r.CountByCourse(context.Background(), tenantID, courseID); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestEnrollmentRepo_MarkCompleted_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	e, _ := domain.NewEnrollment(tenantID, courseID, gcid)
	if err := r.MarkCompleted(context.Background(), e); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}
