// course_prerequisite_unit_test.go — unit tests for pg.CoursePrerequisiteRepo.
//
// Exercises the Course Prerequisite DAG edge persistence (ADR-226) against a
// stub Querier, no live DB. Guarantees:
//  1. rls.ApplySession runs BEFORE every data statement (each method);
//  2. SQL templates match the expected shape (INSERT … ON CONFLICT DO UPDATE
//     revives soft-deleted edges, "soft-delete" UPDATE, tenant-scoped SELECTs
//     with deleted_at IS NULL + deterministic ORDER BY);
//  3. bind-arg order matches the $N placeholders;
//  4. nil-tx returns ErrNotImplemented (fail-loud);
//  5. empty tenant / course inputs return the dedicated sentinels
//     ErrPrereqMissingTenant / ErrPrereqMissingCourse;
//  6. error paths are returned without being folded into "not found".
//
// Live RLS isolation is covered by the integration-tagged tests.
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

// ppreqFillCourseRow writes ListForCourse's 2 destinations (prerequisite id,
// kind). ppreqFillTenantRow writes ListForTenant's 3 (course, prerequisite, kind).
func ppreqFillCourseRow(dest []any, prereqID, kind string) error {
	if len(dest) != 2 {
		return errors.New("ListForCourse scan: expected 2 destinations")
	}
	*(dest[0].(*string)) = prereqID
	*(dest[1].(*string)) = kind
	return nil
}

func ppreqFillTenantRow(dest []any, courseID, prereqID, kind string) error {
	if len(dest) != 3 {
		return errors.New("ListForTenant scan: expected 3 destinations")
	}
	*(dest[0].(*string)) = courseID
	*(dest[1].(*string)) = prereqID
	*(dest[2].(*string)) = kind
	return nil
}

// -----------------------------------------------------------------------------
// Nil-TxRunner — fail-loud
// -----------------------------------------------------------------------------

func TestPrereqRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCoursePrerequisiteRepo(nil)
	ctx := context.Background()
	edge := domain.PrerequisiteEdge{
		CourseID: courseID, PrerequisiteCourseID: gcid, Kind: domain.PrereqKindHardGate,
	}

	if err := r.Upsert(ctx, tenantID, edge); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Upsert: expected ErrNotImplemented; got %v", err)
	}
	if err := r.Remove(ctx, tenantID, courseID, gcid); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Remove: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListForCourse(ctx, tenantID, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListForCourse: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListForTenant(ctx, tenantID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListForTenant: expected ErrNotImplemented; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Input validation — dedicated sentinels
// -----------------------------------------------------------------------------

func TestPrereqRepo_Sentinels_MissingTenantAndCourse(t *testing.T) {
	t.Parallel()
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: &stubQuerier{}})
	ctx := context.Background()

	if err := r.Upsert(ctx, "  ", domain.PrerequisiteEdge{CourseID: courseID, PrerequisiteCourseID: gcid, Kind: domain.PrereqKindHardGate}); !errors.Is(err, pg.ErrPrereqMissingTenant) {
		t.Fatalf("Upsert: expected ErrPrereqMissingTenant; got %v", err)
	}
	if err := r.Upsert(ctx, tenantID, domain.PrerequisiteEdge{CourseID: "", PrerequisiteCourseID: gcid, Kind: domain.PrereqKindHardGate}); !errors.Is(err, pg.ErrPrereqMissingCourse) {
		t.Fatalf("Upsert: expected ErrPrereqMissingCourse; got %v", err)
	}
	if err := r.Upsert(ctx, tenantID, domain.PrerequisiteEdge{CourseID: courseID, PrerequisiteCourseID: "", Kind: domain.PrereqKindHardGate}); !errors.Is(err, pg.ErrPrereqMissingCourse) {
		t.Fatalf("Upsert: expected ErrPrereqMissingCourse; got %v", err)
	}
	if err := r.Remove(ctx, "", courseID, gcid); !errors.Is(err, pg.ErrPrereqMissingTenant) {
		t.Fatalf("Remove: expected ErrPrereqMissingTenant; got %v", err)
	}
	if _, err := r.ListForCourse(ctx, "", courseID); !errors.Is(err, pg.ErrPrereqMissingTenant) {
		t.Fatalf("ListForCourse: expected ErrPrereqMissingTenant; got %v", err)
	}
	if _, err := r.ListForTenant(ctx, " "); !errors.Is(err, pg.ErrPrereqMissingTenant) {
		t.Fatalf("ListForTenant: expected ErrPrereqMissingTenant; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Upsert — INSERT … ON CONFLICT DO UPDATE SET (revive + kind update)
// -----------------------------------------------------------------------------

func TestPrereqRepo_Upsert_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	edge := domain.PrerequisiteEdge{
		CourseID: courseID, PrerequisiteCourseID: gcid, Kind: domain.PrereqKindAdvisory,
	}

	if err := r.Upsert(ctx, tenantID, edge); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	upsert := q.sqls[1]
	for _, frag := range []string{
		"INSERT INTO course_prerequisites",
		"ON CONFLICT (tenant_id, course_id, prerequisite_course_id) DO UPDATE SET",
		"kind       = EXCLUDED.kind",
		"deleted_at = NULL",
	} {
		if !strings.Contains(upsert, frag) {
			t.Fatalf("upsert must contain %q; got %q", frag, upsert)
		}
	}
	if len(q.args) < 2 {
		t.Fatalf("expected SET LOCAL + upsert args; got %d", len(q.args))
	}
	args := q.args[1]
	if len(args) != 6 {
		t.Fatalf("upsert must bind 6 args (id, tenant, course, prereq, kind, now); got %d", len(args))
	}
	if id, ok := args[0].(string); !ok || id == "" {
		t.Fatalf("upsert arg 0 must be a fresh edge id; got %v", args[0])
	}
	if args[1] != tenantID || args[2] != courseID || args[3] != gcid {
		t.Fatalf("upsert tenant/course/prereq args wrong: %v", args)
	}
	if args[4] != string(domain.PrereqKindAdvisory) {
		t.Fatalf("upsert kind arg wrong: %v", args[4])
	}
	if _, ok := args[5].(time.Time); !ok {
		t.Fatalf("upsert arg 5 must be the now timestamp; got %v", args[5])
	}
}

func TestPrereqRepo_Upsert_ExecError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "INSERT INTO course_prerequisites") {
				return errors.New("duplicate key")
			}
			return nil
		},
	}
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: q})
	ctx := context.Background()
	edge := domain.PrerequisiteEdge{
		CourseID: courseID, PrerequisiteCourseID: gcid, Kind: domain.PrereqKindHardGate,
	}

	err := r.Upsert(ctx, tenantID, edge)
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("upsert error must propagate; got %v", err)
	}
}

func TestPrereqRepo_Upsert_RlsFailure_ReturnsWrappedError(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "SET LOCAL") {
				return errors.New("conn closed")
			}
			return nil
		},
	}
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: q})
	ctx := context.Background()
	edge := domain.PrerequisiteEdge{
		CourseID: courseID, PrerequisiteCourseID: gcid, Kind: domain.PrereqKindHardGate,
	}

	err := r.Upsert(ctx, tenantID, edge)
	if err == nil || !strings.Contains(err.Error(), "SET LOCAL chora.tenant_id failed") {
		t.Fatalf("RLS error must be wrapped with context; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Remove — soft-delete UPDATE (idempotent, deleted_at IS NULL guard)
// -----------------------------------------------------------------------------

func TestPrereqRepo_Remove_AppliesRLSThenSoftDeletes(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	if err := r.Remove(ctx, tenantID, courseID, gcid); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	del := q.sqls[1]
	for _, frag := range []string{
		"UPDATE course_prerequisites",
		"SET deleted_at = $4, updated_at = $4",
		"WHERE tenant_id = $1 AND course_id = $2 AND prerequisite_course_id = $3",
		"deleted_at IS NULL",
	} {
		if !strings.Contains(del, frag) {
			t.Fatalf("soft-delete must contain %q; got %q", frag, del)
		}
	}
	if len(q.args) < 2 || len(q.args[1]) != 4 ||
		q.args[1][0] != tenantID || q.args[1][1] != courseID || q.args[1][2] != gcid {
		t.Fatalf("soft-delete args wrong: %v", q.args[1])
	}
	if _, ok := q.args[1][3].(time.Time); !ok {
		t.Fatalf("soft-delete arg 3 must be the now timestamp; got %v", q.args[1][3])
	}
}

func TestPrereqRepo_Remove_ExecError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		execErr: errors.New("conn closed"),
	}
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	err := r.Remove(ctx, tenantID, courseID, gcid)
	if err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("Remove error must propagate; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListForCourse — active prerequisites of one course
// -----------------------------------------------------------------------------

func TestPrereqRepo_ListForCourse_ReturnsRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return ppreqFillCourseRow(dest, gcid, string(domain.PrereqKindHardGate)) },
				func(dest ...any) error {
					return ppreqFillCourseRow(dest, "01970000-0000-7000-b000-0000000000c2", string(domain.PrereqKindAdvisory))
				},
			}}, nil
		},
	}
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListForCourse(ctx, tenantID, courseID)
	if err != nil {
		t.Fatalf("ListForCourse: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 prerequisites; got %d", len(out))
	}
	if out[0].PrerequisiteCourseID != gcid || out[0].Kind != domain.PrereqKindHardGate {
		t.Fatalf("row 0 wrong: %+v", out[0])
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	sel := q.sqls[1]
	for _, frag := range []string{
		"SELECT prerequisite_course_id, kind",
		"FROM course_prerequisites",
		"WHERE tenant_id = $1 AND course_id = $2",
		"deleted_at IS NULL",
		"ORDER BY prerequisite_course_id ASC",
	} {
		if !strings.Contains(sel, frag) {
			t.Fatalf("ListForCourse SELECT must contain %q; got %q", frag, sel)
		}
	}
	if len(q.args) < 2 || len(q.args[1]) != 2 || q.args[1][0] != tenantID || q.args[1][1] != courseID {
		t.Fatalf("ListForCourse args wrong: %v", q.args[1])
	}
}

func TestPrereqRepo_ListForCourse_RowScanError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.ListForCourse(ctx, tenantID, courseID)
	if err == nil || !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("row-scan error must propagate; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListForTenant — every active edge in the tenant (cycle-detection graph read)
// -----------------------------------------------------------------------------

func TestPrereqRepo_ListForTenant_ReturnsEdges(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					return ppreqFillTenantRow(dest, courseID, gcid, string(domain.PrereqKindHardGate))
				},
			}}, nil
		},
	}
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	out, err := r.ListForTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 edge; got %d", len(out))
	}
	if out[0].CourseID != courseID || out[0].PrerequisiteCourseID != gcid {
		t.Fatalf("edge wrong: %+v", out[0])
	}
	sel := q.sqls[1]
	for _, frag := range []string{
		"SELECT course_id, prerequisite_course_id, kind",
		"ORDER BY course_id ASC, prerequisite_course_id ASC",
	} {
		if !strings.Contains(sel, frag) {
			t.Fatalf("ListForTenant SELECT must contain %q; got %q", frag, sel)
		}
	}
	if len(q.args) < 2 || len(q.args[1]) != 1 || q.args[1][0] != tenantID {
		t.Fatalf("ListForTenant args wrong: %v", q.args[1])
	}
}

func TestPrereqRepo_ListForTenant_QueryError_Returned(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("conn closed")
		},
	}
	r := pg.NewCoursePrerequisiteRepo(&stubTxRunner{q: q})
	ctx := context.Background()

	_, err := r.ListForTenant(ctx, tenantID)
	if err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("Query error must propagate; got %v", err)
	}
}
