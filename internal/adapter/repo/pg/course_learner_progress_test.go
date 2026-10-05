// course_learner_progress_test.go - the CourseLearnerProgress pg adapter's
// production contract (CHO-1827, ASYNC analytics).
//
// The assertion that matters most is the boring one: rls.ApplySession must run
// BEFORE any statement, on every path. course_learner_progress has FORCE RLS and
// the app role is NOBYPASSRLS, so a query without the tenant GUC does not fail -
// it matches NOTHING and reports success. The projection would look wired, the
// Analytics tab would show enrolment-only forever, and no error would ever
// surface. That failure mode is invisible in production and cheap to catch here.
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

const (
	clpTenant = "01980000-0000-7000-8000-000000000001"
	clpGCID   = "01980000-0000-7000-8000-00000000abcd"
	clpCourse = "01980000-0000-7000-8000-000000000c01"
	clpRowID  = "01980000-0000-7000-8000-00000000f001"
	clpPath   = "01980000-0000-7000-8000-00000000d001"
)

var clpT0 = time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)

// clpRowFn scripts the SELECT ... FOR UPDATE result - the pre-state row the
// mutation locks. Column order mirrors courseProgressSelectCols.
func clpRowFn(completed, total int, isComplete bool, lastAdvance *time.Time) func(sql string, args ...any) pg.Row {
	return func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error {
			now := time.Now().UTC().Add(-time.Hour)
			pathID := clpPath
			*(dest[0].(*string)) = clpRowID
			*(dest[1].(*string)) = clpTenant
			*(dest[2].(*string)) = clpGCID
			*(dest[3].(*string)) = clpCourse
			*(dest[4].(**string)) = &pathID
			*(dest[5].(*int)) = completed
			*(dest[6].(*int)) = total
			*(dest[7].(*bool)) = isComplete
			*(dest[8].(**time.Time)) = nil
			*(dest[9].(**time.Time)) = lastAdvance
			*(dest[10].(*time.Time)) = now
			*(dest[11].(*time.Time)) = now
			*(dest[12].(**time.Time)) = nil
			return nil
		}}
	}
}

func clpRepo(q *stubQuerier) *pg.CourseProgressRepo {
	return pg.NewCourseProgressRepo(&stubTxRunner{q: q})
}

// tenantCtx is what production supplies: the SUBSCRIBER puts the tenant on ctx.
func tenantCtx() context.Context {
	return tracing.WithTenantID(context.Background(), clpTenant)
}

func indexOfSQLIn(q *stubQuerier, needle string) int {
	for i, sql := range q.sqls {
		if strings.Contains(sql, needle) {
			return i
		}
	}
	return -1
}

// -----------------------------------------------------------------------------
// THE RLS CONTRACT
// -----------------------------------------------------------------------------

// Every write path must SET the tenant GUC before it touches a table.
func TestCourseProgressRepo_Advance_AppliesRLSSessionFirst(t *testing.T) {
	q := &stubQuerier{rowFn: clpRowFn(2, 10, false, nil)}
	if _, err := clpRepo(q).Advance(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, 5, 10, clpT0); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	guc := indexOfSQLIn(q, "chora.tenant_id")
	if guc != 0 {
		t.Fatalf("rls.ApplySession must be the FIRST statement (a FORCE-RLS write without the "+
			"tenant GUC silently affects 0 rows), got index %d of %v", guc, q.sqls)
	}
	if ins := indexOfSQLIn(q, "INSERT INTO course_learner_progress"); ins < guc {
		t.Fatalf("the ensure INSERT ran BEFORE the RLS session (index %d)", ins)
	}
}

func TestCourseProgressRepo_Complete_AppliesRLSSessionFirst(t *testing.T) {
	q := &stubQuerier{rowFn: clpRowFn(0, 0, false, nil)}
	if _, err := clpRepo(q).Complete(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, clpT0); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if guc := indexOfSQLIn(q, "chora.tenant_id"); guc != 0 {
		t.Fatalf("rls.ApplySession must be first, got %d of %v", guc, q.sqls)
	}
}

func TestCourseProgressRepo_ListByCourseIDs_AppliesRLSSessionFirst(t *testing.T) {
	q := &stubQuerier{}
	if _, err := clpRepo(q).ListByCourseIDs(tenantCtx(), clpTenant, []string{clpCourse}); err != nil {
		t.Fatalf("ListByCourseIDs: %v", err)
	}
	if guc := indexOfSQLIn(q, "chora.tenant_id"); guc != 0 {
		t.Fatalf("rls.ApplySession must be first on the READ path too, got %d of %v", guc, q.sqls)
	}
}

func TestCourseProgressRepo_GetByLearnerCourse_AppliesRLSSessionFirst(t *testing.T) {
	q := &stubQuerier{rowFn: clpRowFn(3, 10, false, nil)}
	if _, _, err := clpRepo(q).GetByLearnerCourse(tenantCtx(), clpTenant, clpGCID, clpCourse); err != nil {
		t.Fatalf("GetByLearnerCourse: %v", err)
	}
	if guc := indexOfSQLIn(q, "chora.tenant_id"); guc != 0 {
		t.Fatalf("rls.ApplySession must be first, got %d of %v", guc, q.sqls)
	}
}

// -----------------------------------------------------------------------------
// The write path: ensure → lock → mutate → persist
// -----------------------------------------------------------------------------

func TestCourseProgressRepo_Advance_EnsuresThenLocksThenUpdates(t *testing.T) {
	q := &stubQuerier{rowFn: clpRowFn(2, 10, false, nil)}
	changed, err := clpRepo(q).Advance(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, 5, 10, clpT0)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !changed {
		t.Fatal("changed: want true (2/10 → 5/10)")
	}
	ensure := indexOfSQLIn(q, "INSERT INTO course_learner_progress")
	lock := indexOfSQLIn(q, "FOR UPDATE")
	update := indexOfSQLIn(q, "UPDATE course_learner_progress")
	if ensure < 0 || lock < 0 || update < 0 {
		t.Fatalf("want ensure+lock+update, got %v", q.sqls)
	}
	if !(ensure < lock && lock < update) {
		t.Fatalf("order must be ensure → lock → update, got %d/%d/%d", ensure, lock, update)
	}
	// The ensure must be idempotent, or a concurrent first event 23505s.
	if !strings.Contains(q.sqls[ensure], "ON CONFLICT") {
		t.Fatalf("the ensure INSERT must be ON CONFLICT DO NOTHING, got %q", q.sqls[ensure])
	}
	// The persisted counts are the aggregate's, not the raw arguments.
	args := q.args[update]
	if args[0] != 5 || args[1] != 10 {
		t.Fatalf("UPDATE must persist 5/10, got %v/%v", args[0], args[1])
	}
}

// An unchanged aggregate must not issue a pointless UPDATE.
func TestCourseProgressRepo_Advance_NoUpdateWhenUnchanged(t *testing.T) {
	q := &stubQuerier{rowFn: clpRowFn(5, 10, false, nil)}
	changed, err := clpRepo(q).Advance(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, 5, 10, clpT0)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if changed {
		t.Fatal("changed: want false on an identical redelivery")
	}
	if i := indexOfSQLIn(q, "UPDATE course_learner_progress"); i >= 0 {
		t.Fatal("an identical redelivery must not UPDATE")
	}
}

// The stored watermark must fence a stale redelivery at the DB layer too.
func TestCourseProgressRepo_Advance_StaleEventWritesNothing(t *testing.T) {
	newer := clpT0.Add(time.Minute)
	q := &stubQuerier{rowFn: clpRowFn(8, 10, false, &newer)}
	changed, err := clpRepo(q).Advance(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, 2, 10, clpT0)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if changed {
		t.Fatal("changed: want false for a stale event")
	}
	if i := indexOfSQLIn(q, "UPDATE course_learner_progress"); i >= 0 {
		t.Fatal("a stale event must not rewind the row")
	}
}

func TestCourseProgressRepo_Complete_PersistsCompletion(t *testing.T) {
	q := &stubQuerier{rowFn: clpRowFn(10, 10, false, nil)}
	changed, err := clpRepo(q).Complete(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, clpT0)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !changed {
		t.Fatal("changed: want true")
	}
	update := indexOfSQLIn(q, "UPDATE course_learner_progress")
	if update < 0 {
		t.Fatalf("want an UPDATE, got %v", q.sqls)
	}
	if isComplete, ok := q.args[update][2].(bool); !ok || !isComplete {
		t.Fatalf("UPDATE must persist is_complete=true, got %v", q.args[update][2])
	}
}

// An already-complete row must not be re-stamped by a redelivery.
func TestCourseProgressRepo_Complete_IdempotentNoUpdate(t *testing.T) {
	q := &stubQuerier{rowFn: clpRowFn(10, 10, true, nil)}
	changed, err := clpRepo(q).Complete(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, clpT0)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if changed {
		t.Fatal("changed: want false when already complete")
	}
	if i := indexOfSQLIn(q, "UPDATE course_learner_progress"); i >= 0 {
		t.Fatal("a repeat completion must not UPDATE")
	}
}

// -----------------------------------------------------------------------------
// Fail-loud guards
// -----------------------------------------------------------------------------

func TestCourseProgressRepo_RefusesBlankScope(t *testing.T) {
	r := clpRepo(&stubQuerier{})
	if _, err := r.Advance(tenantCtx(), "", clpGCID, clpCourse, clpPath, 1, 10, clpT0); !errors.Is(err, pg.ErrCourseProgressMissingScope) {
		t.Fatalf("blank tenant: want ErrCourseProgressMissingScope, got %v", err)
	}
	if _, err := r.Complete(tenantCtx(), clpTenant, "", clpCourse, clpPath, clpT0); !errors.Is(err, pg.ErrCourseProgressMissingScope) {
		t.Fatalf("blank gcid: want ErrCourseProgressMissingScope, got %v", err)
	}
	if _, _, err := r.GetByLearnerCourse(tenantCtx(), clpTenant, clpGCID, ""); !errors.Is(err, pg.ErrCourseProgressMissingScope) {
		t.Fatalf("blank course: want ErrCourseProgressMissingScope, got %v", err)
	}
	if _, err := r.ListByCourseIDs(tenantCtx(), "", []string{clpCourse}); !errors.Is(err, pg.ErrCourseProgressMissingScope) {
		t.Fatalf("blank tenant list: want ErrCourseProgressMissingScope, got %v", err)
	}
}

// A nil TxRunner must fail loud, never pretend to persist.
func TestCourseProgressRepo_NilRunnerIsNotImplemented(t *testing.T) {
	r := pg.NewCourseProgressRepo(nil)
	if _, err := r.Advance(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, 1, 10, clpT0); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
	if _, err := r.Complete(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, clpT0); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
	if _, _, err := r.GetByLearnerCourse(tenantCtx(), clpTenant, clpGCID, clpCourse); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
	if _, err := r.ListByCourseIDs(tenantCtx(), clpTenant, []string{clpCourse}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}

// A domain violation must surface (→ NACK), not be persisted.
func TestCourseProgressRepo_Advance_DomainViolationSurfaces(t *testing.T) {
	q := &stubQuerier{rowFn: clpRowFn(2, 10, false, nil)}
	if _, err := clpRepo(q).Advance(tenantCtx(), clpTenant, clpGCID, clpCourse, clpPath, 11, 10, clpT0); err == nil {
		t.Fatal("completed > total must surface an error, got nil")
	}
	if i := indexOfSQLIn(q, "UPDATE course_learner_progress"); i >= 0 {
		t.Fatal("an invalid advance must not UPDATE")
	}
}

// An empty course list must not issue a query at all.
func TestCourseProgressRepo_ListByCourseIDs_EmptyIsNoQuery(t *testing.T) {
	q := &stubQuerier{}
	rows, err := clpRepo(q).ListByCourseIDs(tenantCtx(), clpTenant, nil)
	if err != nil {
		t.Fatalf("ListByCourseIDs(nil): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want 0 rows, got %d", len(rows))
	}
	if len(q.sqls) != 0 {
		t.Fatalf("an empty course list must issue no SQL, got %v", q.sqls)
	}
}

// A genuine miss is (nil,false,nil) - distinct from a failure.
func TestCourseProgressRepo_GetByLearnerCourse_MissIsNotAnError(t *testing.T) {
	// The pgx driver's miss, as the repo's isNoRows recognises it.
	q := &stubQuerier{rowFn: func(_ string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
	}}
	p, ok, err := clpRepo(q).GetByLearnerCourse(tenantCtx(), clpTenant, clpGCID, clpCourse)
	if err != nil {
		t.Fatalf("a miss must not be an error, got %v", err)
	}
	if ok || p != nil {
		t.Fatalf("want (nil,false), got (%v,%v)", p, ok)
	}
}
