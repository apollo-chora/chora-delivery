// enrollment_listbycourse_test.go — unit tests for pg.EnrollmentRepo.ListByCourse.
//
// ADD-ONLY companion to enrollment_test.go for the R+ M4 course-Roster READ
// VIEW. ListByCourse is the by-COURSE iterator (sibling of the existing
// by-GCID ListByGCID) that makes *EnrollmentRepo satisfy
// domain.EnrollmentListByCoursePort so cmd/server/main.go::wireRosters wires
// the GET /api/v1/rosters/{courseId} route in production (pg DSN set).
//
// Mirrors enrollment_test.go: stubs the Querier so the SQL surface is
// exercised without a live DB (shared stubQuerier / stubRows / stubRow /
// stubTxRunner + tenantID / courseID / gcid consts live in application_test.go,
// same pg_test package). Live RLS isolation is verified in
// enrollment_integration_test.go (build tag `integration`).
//
// What these tests guarantee:
//
//  1. rls.ApplySession runs BEFORE the data query (SET LOCAL chora.tenant_id
//     is the first SQL) — tenant isolation is enforced at the SQL boundary,
//     not in Go.
//  2. The SQL template is the by-course SELECT shape: SELECT the enrolment
//     columns FROM course_enrollments WHERE tenant_id = $1 AND course_id = $2
//     AND deleted_at IS NULL (soft-delete-aware).
//  3. Argument order matches the SQL placeholders ($1 tenant, $2 course).
//  4. Nil-tx returns ErrNotImplemented (fail-loud, matches the other repo
//     methods + Course / Application repos).
//  5. Multi-row hits scan into the returned slice; an empty course returns a
//     nil/empty slice with no error (NOT a fabricated row).
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
)

// -----------------------------------------------------------------------------
// Nil-TxRunner — fail-loud per [[no-stubs-real-wiring]]
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_ListByCourse_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewEnrollmentRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByCourse(ctx, tenantID, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByCourse: expected ErrNotImplemented; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListByCourse — applies RLS first, then the by-course SELECT
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_ListByCourse_AppliesRLSThenByCourseSelect(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	older := now.Add(-90 * time.Minute)

	const learnerA = "01970000-0000-7000-9000-00000000000a"
	const learnerB = "01970000-0000-7000-9000-00000000000b"

	var boundArgs []any
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			boundArgs = args
			return &stubRows{
				rows: []func(dest ...any) error{
					func(dest ...any) error {
						*(dest[0].(*string)) = "01970000-0000-7000-9999-eeeeeeeeeeee"
						*(dest[1].(*string)) = tenantID
						*(dest[2].(*string)) = courseID
						*(dest[3].(*string)) = learnerA
						*(dest[4].(*time.Time)) = older
						*(dest[5].(**time.Time)) = nil
						return nil
					},
					func(dest ...any) error {
						*(dest[0].(*string)) = "01970000-0000-7000-9999-ffffffffffff"
						*(dest[1].(*string)) = tenantID
						*(dest[2].(*string)) = courseID
						*(dest[3].(*string)) = learnerB
						*(dest[4].(*time.Time)) = now
						*(dest[5].(**time.Time)) = nil
						return nil
					},
				},
			}, nil
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 learners for the course; got %d", len(out))
	}
	// Every returned row must belong to the requested (tenant, course).
	for i, e := range out {
		if e.TenantID != tenantID || e.CourseID != courseID {
			t.Fatalf("row %d: cross-tenant/cross-course leak; got tenant=%s course=%s", i, e.TenantID, e.CourseID)
		}
		if e.GCID == "" {
			t.Fatalf("row %d: empty gcid — roster shape needs the learner gcid", i)
		}
	}

	// RLS must be applied before the user query.
	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + SELECT); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id (RLS before read); got %q", q.sqls[0])
	}

	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM course_enrollments") {
		t.Fatalf("expected SELECT FROM course_enrollments; got %q", last)
	}
	if !strings.Contains(last, "course_id = $2") {
		t.Fatalf("expected by-course filter course_id = $2; got %q", last)
	}
	if !strings.Contains(last, "tenant_id = $1") {
		t.Fatalf("expected tenant filter tenant_id = $1; got %q", last)
	}
	if !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete filter deleted_at IS NULL; got %q", last)
	}

	// Argument order must match the placeholders: $1 tenant, $2 course.
	if len(boundArgs) != 2 {
		t.Fatalf("expected 2 bound args (tenant, course); got %d (%v)", len(boundArgs), boundArgs)
	}
	if boundArgs[0] != tenantID {
		t.Fatalf("arg $1 must be tenantID; got %v", boundArgs[0])
	}
	if boundArgs[1] != courseID {
		t.Fatalf("arg $2 must be courseID; got %v", boundArgs[1])
	}
}

// -----------------------------------------------------------------------------
// ListByCourse — empty course yields an empty slice, NOT an error / placeholder
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_ListByCourse_EmptyCourse_ReturnsEmptyNoError(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			// Zero rows — a course with no enrolments. The stubRows
			// zero-value iterates zero times.
			return &stubRows{}, nil
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByCourse(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("ListByCourse (empty): unexpected error %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("empty course must return zero learners; got %d", len(out))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS even on the empty path; got %q", q.sqls[0])
	}
}

// -----------------------------------------------------------------------------
// ListByCourse — Query error bubbles up (fail loud, not a silent empty roster)
// -----------------------------------------------------------------------------

func TestEnrollmentRepo_ListByCourse_QueryError_Bubbles(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("boom: connection reset")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, sentinel
		},
	}
	r := pg.NewEnrollmentRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, err := r.ListByCourse(ctx, tenantID, courseID)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the Query error to bubble; got %v", err)
	}
}
