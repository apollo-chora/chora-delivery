// course_extra_test.go — extra pg.CourseRepo unit tests: finish partial
// statement coverage of course.go.
//
// Extends course_test.go (same pg_test package) — reuses the shared
// stubQuerier / stubTxRunner / stubRow / stubRows fixtures + the tenantID /
// courseID / gcid / instructorGCID consts.
//
// New helpers/fillers/consts in this file are prefixed `corsx` to keep the
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

// corsxFillCourse fills a Scan dest shaped like scanCourse (13 cols) with a
// caller-chosen deleted_at value (nil → SQL NULL).
func corsxFillCourse(id string, deletedAt *time.Time) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) != 13 {
			return errors.New("scanCourse: expected 13 destinations")
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		*(dest[0].(*string)) = id
		*(dest[1].(*string)) = tenantID
		*(dest[2].(*string)) = instructorGCID
		*(dest[3].(*string)) = "Title"
		*(dest[4].(*string)) = "desc"
		*(dest[5].(*[]string)) = []string{}
		*(dest[6].(*bool)) = true
		*(dest[7].(*int64)) = 0
		*(dest[8].(*bool)) = false
		*(dest[9].(*int)) = 30
		*(dest[10].(*time.Time)) = now
		*(dest[11].(*time.Time)) = now
		*(dest[12].(**time.Time)) = deletedAt
		return nil
	}
}

// -----------------------------------------------------------------------------
// Save — missing instructor / exec error / RLS-session failure
// -----------------------------------------------------------------------------

func TestCourseRepo_Save_RejectsMissingInstructor(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	c, err := domain.NewCourse(tenantID, "no instructor", []string{}, 10)
	if err != nil {
		t.Fatalf("NewCourse: %v", err)
	}
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, c); !errors.Is(err, pg.ErrCourseMissingInstructor) {
		t.Fatalf("expected ErrCourseMissingInstructor; got %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("no SQL must execute on missing instructor; got %v", q.sqls)
	}
}

func TestCourseRepo_Save_ExecError_WrapsUpsertContext(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO courses") {
			return errors.New("connection lost")
		}
		return nil
	}}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	c, err := domain.NewCourse(tenantID, "upsert fail", []string{}, 30)
	if err != nil {
		t.Fatalf("NewCourse: %v", err)
	}
	c.InstructorGCID = instructorGCID
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err = r.Save(ctx, c)
	if err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: upsert course") {
		t.Fatalf("exec error must be wrapped with context; got %v", err)
	}
	if !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("wrapped error must preserve the cause; got %v", err)
	}
}

func TestCourseRepo_Save_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	c, err := domain.NewCourse(tenantID, "bare ctx", []string{}, 10)
	if err != nil {
		t.Fatalf("NewCourse: %v", err)
	}
	c.InstructorGCID = instructorGCID
	if err := r.Save(context.Background(), c); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Get — deleted_at hydration + RLS-session failure
// -----------------------------------------------------------------------------

func TestCourseRepo_Get_HydratesDeletedAt(t *testing.T) {
	t.Parallel()
	deleted := time.Now().UTC().Add(-time.Hour)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: corsxFillCourse(courseID, &deleted)}
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	c, ok, err := r.Get(ctx, tenantID, courseID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if c.DeletedAt == nil || !c.DeletedAt.Equal(deleted) {
		t.Fatalf("deleted_at must hydrate onto the aggregate; got %v", c.DeletedAt)
	}
}

func TestCourseRepo_Get_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	if _, _, err := r.Get(context.Background(), tenantID, courseID); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListByTenant — row-scan error + RLS-session failure
// -----------------------------------------------------------------------------

func TestCourseRepo_ListByTenant_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.ListByTenant(ctx, tenantID, 0, 10); err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
}

func TestCourseRepo_ListByTenant_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	if _, _, err := r.ListByTenant(context.Background(), tenantID, 0, 10); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListPublic — row-scan + default-limit branches
// -----------------------------------------------------------------------------

func TestCourseRepo_ListPublic_ReturnsScannedRows(t *testing.T) {
	t.Parallel()
	// The list SELECT is the only Query call — return one course row so the
	// scanCourse loop body runs (the empty-rows path was already covered).
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			corsxFillCourse(courseID, nil),
		}}, nil
	}}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	items, total, err := r.ListPublic(ctx, 0, 10)
	if err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	if len(items) != 1 || total != 1 {
		t.Fatalf("expected 1 item; got %d / %d", len(items), total)
	}
	if items[0].ID != courseID {
		t.Fatalf("item id: got %q", items[0].ID)
	}
}

func TestCourseRepo_ListPublic_ZeroLimit_DefaultsTo50(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{}, nil
	}}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.ListPublic(ctx, 0, 0); err != nil {
		t.Fatalf("ListPublic zero limit: %v", err)
	}
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 2 {
		t.Fatalf("list query must bind (limit, offset); got %d", len(lastArgs))
	}
	if n, _ := lastArgs[0].(int); n != 50 {
		t.Fatalf("zero limit must default to 50; got %v", lastArgs[0])
	}
}

func TestCourseRepo_ListPublic_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, errors.New("query timeout")
	}}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.ListPublic(ctx, 0, 10); err == nil {
		t.Fatalf("expected the query error to propagate")
	}
}

func TestCourseRepo_ListPublic_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.ListPublic(ctx, 0, 10); err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
}

func TestCourseRepo_ListByTenant_ZeroLimit_DefaultsTo50(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{}, nil
	}}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.ListByTenant(ctx, tenantID, 0, 0); err != nil {
		t.Fatalf("ListByTenant zero limit: %v", err)
	}
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 3 {
		t.Fatalf("list query must bind (tenant, limit, offset); got %d", len(lastArgs))
	}
	if n, _ := lastArgs[1].(int); n != 50 {
		t.Fatalf("zero limit must default to 50; got %v", lastArgs[1])
	}
}

func TestCourseRepo_ListByTenant_RowsIteratorError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &corsxErrRows{stubRows: &stubRows{rows: []func(dest ...any) error{
			corsxFillCourse(courseID, nil),
		}}}, nil
	}}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.ListByTenant(ctx, tenantID, 0, 10); err == nil {
		t.Fatalf("expected the rows.Err() failure to propagate")
	}
}

// corsxErrRows is a stubRows whose Err() reports an iteration failure —
// exercises the `return rows.Err()` guard body without a live DB.
type corsxErrRows struct {
	*stubRows
}

func (r *corsxErrRows) Err() error { return errors.New("iteration failed") }

func TestCourseRepo_ListPublic_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	if _, _, err := r.ListPublic(context.Background(), 0, 10); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}
