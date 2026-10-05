// course_test.go — unit tests for the pgx-backed CourseRepo.
//
// Mirrors application_test.go. Stubs the Querier so the SQL surface is
// exercised without a live DB. Production wiring runs the same code
// against pgxpool via the cmd/server bootstrap; the live RLS isolation
// is verified separately in integration_test.go.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// instructorGCID is reused across CourseRepo unit tests. tenantID + courseID
// + gcid constants are defined in application_test.go (same package).
const instructorGCID = "01970000-0000-7000-a000-000000000001"

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func TestCourseRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCourseRepo(nil)
	if err := r.Save(context.Background(), &domain.Course{}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, _, err := r.Get(context.Background(), tenantID, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Get: expected ErrNotImplemented; got %v", err)
	}
	if _, _, err := r.ListByTenant(context.Background(), tenantID, 0, 10); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
}

func TestCourseRepo_Save_RejectsNil(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)

	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.Save(ctx, nil)
	if !errors.Is(err, pg.ErrInvalidCourse) {
		t.Fatalf("expected ErrInvalidCourse on nil; got %v", err)
	}
}

func TestCourseRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)

	c, err := domain.NewCourse(tenantID, "RED test course", []string{}, 30)
	if err != nil {
		t.Fatalf("NewCourse: %v", err)
	}
	c.InstructorGCID = instructorGCID
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + UPSERT); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO courses") {
		t.Fatalf("expected INSERT INTO courses; got %q", last)
	}
	if !strings.Contains(last, "ON CONFLICT (course_id) DO UPDATE") {
		t.Fatalf("expected UPSERT on conflict (course_id); got %q", last)
	}
}

func TestCourseRepo_Get_NoRow_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	c, ok, err := r.Get(ctx, tenantID, "nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok || c != nil {
		t.Fatalf("expected ok=false / nil for unknown id")
	}
	// First SQL must be SET LOCAL chora.tenant_id.
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
}

func TestCourseRepo_Get_HappyPath(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				// 13 columns per scanCourse signature.
				if v, ok := dest[0].(*string); ok {
					*v = courseID
				}
				if v, ok := dest[1].(*string); ok {
					*v = tenantID
				}
				if v, ok := dest[2].(*string); ok {
					*v = "instructor-gcid"
				}
				if v, ok := dest[3].(*string); ok {
					*v = "Title"
				}
				if v, ok := dest[4].(*string); ok {
					*v = "desc"
				}
				if v, ok := dest[5].(*[]string); ok {
					*v = []string{}
				}
				if v, ok := dest[6].(*bool); ok {
					*v = true
				}
				if v, ok := dest[7].(*int64); ok {
					*v = 0
				}
				if v, ok := dest[8].(*bool); ok {
					*v = false
				}
				if v, ok := dest[9].(*int); ok {
					*v = 30
				}
				return nil
			}}
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	c, ok, err := r.Get(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || c == nil {
		t.Fatalf("expected ok=true / non-nil")
	}
	if c.ID != courseID || c.TenantID != tenantID || c.MaxCapacity != 30 {
		t.Fatalf("scanned course mismatched: %+v", c)
	}
}

func TestCourseRepo_ListByTenant_ReturnsRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{
				rows: []func(dest ...any) error{
					func(dest ...any) error {
						if v, ok := dest[0].(*string); ok {
							*v = "course-1"
						}
						if v, ok := dest[1].(*string); ok {
							*v = tenantID
						}
						if v, ok := dest[3].(*string); ok {
							*v = "T"
						}
						if v, ok := dest[5].(*[]string); ok {
							*v = nil
						}
						if v, ok := dest[9].(*int); ok {
							*v = 10
						}
						return nil
					},
				},
			}, nil
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	items, total, err := r.ListByTenant(ctx, tenantID, 0, 10)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(items) != 1 || total != 1 {
		t.Fatalf("expected 1 item; got %d / %d", len(items), total)
	}
	// First SQL is SET LOCAL chora.tenant_id; second SELECT.
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	if !strings.Contains(q.sqls[1], "FROM courses") || !strings.Contains(q.sqls[1], "deleted_at IS NULL") {
		t.Fatalf("expected SELECT … FROM courses with soft-delete filter; got %q", q.sqls[1])
	}
}

// ADR-236 D1 — new coverage: a query failure on the SELECT (not just the
// no-live-DB / no-rows paths already covered above) must propagate as a
// non-nil error, never be swallowed into an empty-but-ok result. This is
// the contract callers newly rely on now that ListByTenant is wired behind
// cmd/server's pool gate + threaded through the HTTP handler's error branch
// (handlers.go coursesHandler GET) instead of being unreachable dead code.
func TestCourseRepo_ListByTenant_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("pg: connection reset by peer")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, wantErr
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	items, total, err := r.ListByTenant(ctx, tenantID, 0, 10)
	if err == nil {
		t.Fatalf("ListByTenant: expected the query error to propagate, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("ListByTenant: expected wrapped %v; got %v", wantErr, err)
	}
	if items != nil || total != 0 {
		t.Fatalf("ListByTenant: expected a zero-value result alongside the error; got items=%v total=%d", items, total)
	}
}

func TestCourseRepo_ListPublic_FiltersOnPublicAndDeleted(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, _, err := r.ListPublic(ctx, 0, 10); err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	if !strings.Contains(q.sqls[1], "public = TRUE") || !strings.Contains(q.sqls[1], "deleted_at IS NULL") {
		t.Fatalf("expected public + soft-delete filter; got %q", q.sqls[1])
	}
}

func TestSQLCourseTemplates_AreExported(t *testing.T) {
	t.Parallel()
	if !strings.Contains(pg.SQLUpsertCourse, "INSERT INTO courses") {
		t.Fatalf("SQLUpsertCourse malformed")
	}
	if !strings.Contains(pg.SQLUpsertCourse, "ON CONFLICT (course_id) DO UPDATE") {
		t.Fatalf("SQLUpsertCourse missing UPSERT clause")
	}
	if !strings.Contains(pg.SQLSelectCourseByID, "FROM courses") {
		t.Fatalf("SQLSelectCourseByID malformed")
	}
	if !strings.Contains(pg.SQLListCoursesByTenant, "FROM courses") {
		t.Fatalf("SQLListCoursesByTenant malformed")
	}
	if !strings.Contains(pg.SQLListPublicCourses, "public = TRUE") {
		t.Fatalf("SQLListPublicCourses missing public filter")
	}
}
