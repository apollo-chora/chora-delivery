// student_module_progress_extra_unit_test.go — extra unit tests for
// pg.ProgressRepo, topping up student_module_progress_test.go. Covers the 0%
// GetByLearnerModule + ListByModuleIDs read paths (SQL shape, bind args, miss
// / empty-input sentinels, error propagation) and the still-missing Advance
// branches (scope guard, ensure/lock/update wraps, domain reject).
//
// Mirrors student_module_progress_test.go's stub discipline: a stub Querier
// exercises the SQL surface without a live DB — RLS first (SET LOCAL), SQL
// shape via strings.Contains, bind args via captured args, wrapped-cause
// error checks.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// smpxRowID / smpxModuleID are the projection + module ids used by the read
// tests below (distinct from the existing mpt* constants).
const (
	smpxRowID     = "01980000-0000-7000-8000-00000000f101"
	smpxModuleID  = "01980000-0000-7000-8000-000000000d01"
	smpxCourseID2 = "01980000-0000-7000-8000-000000000c02"
)

// smpxProgressRow fills scanProgress's 11 dests for one projection row;
// completedAt / deletedAt stay nil (the common read-path shape).
func smpxProgressRow(dest []any, rowID, tenant, gcid, moduleID, courseID string, isComplete bool) error {
	if len(dest) != 11 {
		return errors.New("scanProgress: expected 11 destinations")
	}
	now := time.Now().UTC()
	*(dest[0].(*string)) = rowID
	*(dest[1].(*string)) = tenant
	*(dest[2].(*string)) = gcid
	*(dest[3].(*string)) = moduleID
	*(dest[4].(*string)) = courseID
	*(dest[5].(*[]byte)) = []byte("[]")
	*(dest[6].(*bool)) = isComplete
	*(dest[7].(**time.Time)) = nil
	*(dest[8].(*time.Time)) = now
	*(dest[9].(*time.Time)) = now
	*(dest[10].(**time.Time)) = nil
	return nil
}

// smpxErrRows wraps stubRows but lets Err() fail — so post-loop rs.Err()
// checks (ListByModuleIDs) are reachable with the shared stub.
type smpxErrRows struct {
	*stubRows
	err error
}

func (r *smpxErrRows) Err() error { return r.err }

// -----------------------------------------------------------------------------
// GetByLearnerModule — SELECT by (tenant, gcid, module); miss is (nil,false)
// -----------------------------------------------------------------------------

func TestProgressRepo_GetByLearnerModule_NilTx_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewProgressRepo(nil, pg.ProgressRepoOptions{})
	if _, _, err := r.GetByLearnerModule(context.Background(), mptTenant, mptGCID, mptRowID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented on nil-tx; got %v", err)
	}
}

func TestProgressRepo_GetByLearnerModule_Happy(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return smpxProgressRow(dest, smpxRowID, mptTenant, mptGCID, smpxModuleID, mptCourse, true)
			}}
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	p, ok, err := r.GetByLearnerModule(ctx, mptTenant, mptGCID, smpxModuleID)
	if err != nil || !ok || p == nil {
		t.Fatalf("GetByLearnerModule: ok=%v err=%v", ok, err)
	}
	if p.ID != smpxRowID || p.ModuleID != smpxModuleID || p.CourseID != mptCourse || p.GCID != mptGCID {
		t.Fatalf("projection fields wrong: %+v", p)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	sel := q.sqls[1]
	if !strings.Contains(sel, "FROM student_module_progress") {
		t.Fatalf("second SQL must SELECT the projection; got %q", sel)
	}
	args := q.args[1]
	if len(args) != 3 {
		t.Fatalf("expected 3 bind args (tenant,gcid,module); got %d (%v)", len(args), args)
	}
	if args[0] != mptTenant || args[1] != mptGCID || args[2] != smpxModuleID {
		t.Fatalf("bind args wrong: %v", args)
	}
}

func TestProgressRepo_GetByLearnerModule_Miss_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	p, ok, err := r.GetByLearnerModule(ctx, mptTenant, mptGCID, smpxModuleID)
	if err != nil {
		t.Fatalf("miss must not error; got %v", err)
	}
	if ok || p != nil {
		t.Fatalf("expected (nil,false) on miss; got (%+v,%v)", p, ok)
	}
}

func TestProgressRepo_GetByLearnerModule_OtherError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("conn closed") }}
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	if _, _, err := r.GetByLearnerModule(ctx, mptTenant, mptGCID, smpxModuleID); err == nil || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("expected a non-no-rows error to propagate; got %v", err)
	}
}

// A corrupt completed_content_item_ids JSONB must surface scanProgress's
// unmarshal wrap (not be swallowed as a miss — the miss route only maps
// isNoRows errors).
func TestProgressRepo_GetByLearnerModule_BadCompletedJSON_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				// Mirror smpxProgressRow but inject corrupt JSONB at dest[5].
				if err := smpxProgressRow(dest, smpxRowID, mptTenant, mptGCID, smpxModuleID, mptCourse, false); err != nil {
					return err
				}
				*(dest[5].(*[]byte)) = []byte("{not-json")
				return nil
			}}
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	_, _, err := r.GetByLearnerModule(ctx, mptTenant, mptGCID, smpxModuleID)
	if err == nil || !strings.Contains(err.Error(), "unmarshal completed_content_item_ids") {
		t.Fatalf("expected the unmarshal wrap on corrupt JSONB; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListByModuleIDs — single ANY($2::uuid[]) query, row loop, empty-input sentinel
// -----------------------------------------------------------------------------

func TestProgressRepo_ListByModuleIDs_NilTx_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewProgressRepo(nil, pg.ProgressRepoOptions{})
	if _, err := r.ListByModuleIDs(context.Background(), mptTenant, []string{smpxModuleID}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented on nil-tx; got %v", err)
	}
}

func TestProgressRepo_ListByModuleIDs_EmptyInput_NoSQL(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	out, err := r.ListByModuleIDs(ctx, mptTenant, nil)
	if err != nil {
		t.Fatalf("empty input must not error; got %v", err)
	}
	if out == nil || len(out) != 0 {
		t.Fatalf("expected an empty non-nil slice; got %#v", out)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("empty input must not execute SQL; got %d", len(q.sqls))
	}
}

func TestProgressRepo_ListByModuleIDs_Happy(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					return smpxProgressRow(dest, smpxRowID, mptTenant, mptGCID, smpxModuleID, mptCourse, false)
				},
				func(dest ...any) error {
					return smpxProgressRow(dest, "01980000-0000-7000-8000-00000000f102", mptTenant, "01980000-0000-7000-8000-00000000abce", "01980000-0000-7000-8000-000000000d02", mptCourse, true)
				},
			}}, nil
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	ids := []string{smpxModuleID, "01980000-0000-7000-8000-000000000d02"}
	out, err := r.ListByModuleIDs(ctx, mptTenant, ids)
	if err != nil {
		t.Fatalf("ListByModuleIDs: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 projections; got %d", len(out))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	sel := q.sqls[1]
	if !strings.Contains(sel, "module_id = ANY($2::uuid[])") {
		t.Fatalf("select must use a single ANY($2::uuid[]) query; got %q", sel)
	}
	args := q.args[1]
	if len(args) != 2 {
		t.Fatalf("expected 2 bind args (tenant, moduleIDs); got %d (%v)", len(args), args)
	}
	gotIDs, ok := args[1].([]string)
	if !ok || len(gotIDs) != 2 {
		t.Fatalf("expected the []string module_ids slice bound at $2; got %T %v", args[1], args[1])
	}
	if gotIDs[0] != ids[0] || gotIDs[1] != ids[1] {
		t.Fatalf("module_ids slice mismatch: %v", gotIDs)
	}
}

func TestProgressRepo_ListByModuleIDs_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("cohort query conn closed")
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	if _, err := r.ListByModuleIDs(ctx, mptTenant, []string{smpxModuleID}); err == nil || !strings.Contains(err.Error(), "cohort query conn closed") {
		t.Fatalf("expected the SELECT error to propagate; got %v", err)
	}
}

func TestProgressRepo_ListByModuleIDs_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad progress row bytes") },
			}}, nil
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	if _, err := r.ListByModuleIDs(ctx, mptTenant, []string{smpxModuleID}); err == nil || !strings.Contains(err.Error(), "bad progress row bytes") {
		t.Fatalf("expected the row scan error to propagate; got %v", err)
	}
}

// The post-loop rs.Err() check — reachable only with an Err-overriding stub.
func TestProgressRepo_ListByModuleIDs_RowsErr_Propagates(t *testing.T) {
	t.Parallel()
	boom := errors.New("rows iteration failed")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &smpxErrRows{stubRows: &stubRows{}, err: boom}, nil
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	_, err := r.ListByModuleIDs(ctx, mptTenant, []string{smpxModuleID})
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the post-loop rs.Err() error to propagate; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Advance — the branches student_module_progress_test.go leaves open
// -----------------------------------------------------------------------------

func TestProgressRepo_Advance_NilTx_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewProgressRepo(nil, pg.ProgressRepoOptions{})
	if _, err := r.Advance(context.Background(), mptTenant, mptGCID, mptRowID, mptCourse, mptItem1, nil); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented on nil-tx; got %v", err)
	}
}

func TestProgressRepo_Advance_EmptyScope_ReturnsErrProgressMissingScope(t *testing.T) {
	t.Parallel()
	r := pg.NewProgressRepo(&stubTxRunner{q: &stubQuerier{}}, pg.ProgressRepoOptions{})
	ctx := context.Background()

	if _, err := r.Advance(ctx, " ", mptGCID, mptRowID, mptCourse, mptItem1, nil); !errors.Is(err, pg.ErrProgressMissingScope) {
		t.Fatalf("empty tenant: expected ErrProgressMissingScope; got %v", err)
	}
	if _, err := r.Advance(ctx, mptTenant, "", mptRowID, mptCourse, mptItem1, nil); !errors.Is(err, pg.ErrProgressMissingScope) {
		t.Fatalf("empty gcid: expected ErrProgressMissingScope; got %v", err)
	}
	if _, err := r.Advance(ctx, mptTenant, mptGCID, "", mptCourse, mptItem1, nil); !errors.Is(err, pg.ErrProgressMissingScope) {
		t.Fatalf("empty module id: expected ErrProgressMissingScope; got %v", err)
	}
}

func TestProgressRepo_Advance_EnsureRowExecError_Wrapped(t *testing.T) {
	t.Parallel()
	m := mptModule(t, []string{mptItem1}, nil)
	boom := errors.New("ensure insert constraint violation")
	q := &stubQuerier{
		rowFn: mptProgressRowFn(m, nil, false, nil),
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "INSERT INTO student_module_progress") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	changed, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, mptItem1, m)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the ensure INSERT failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: ensure progress row") {
		t.Fatalf("expected the wrap to name the ensure; got %v", err)
	}
	if changed {
		t.Fatalf("changed must stay false when the tx aborts")
	}
}

func TestProgressRepo_Advance_LockRowScanError_Wrapped(t *testing.T) {
	t.Parallel()
	m := mptModule(t, []string{mptItem1}, nil)
	boom := errors.New("lock row unreadable")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return boom }}
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	changed, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, mptItem1, m)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the lock SELECT failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: lock progress row") {
		t.Fatalf("expected the wrap to name the lock; got %v", err)
	}
	if changed {
		t.Fatalf("changed must stay false when the tx aborts")
	}
}

// Recording a content item the module does not group must surface the domain
// reject (fail-loud) before any UPDATE.
func TestProgressRepo_Advance_NonMemberItem_DomainReject(t *testing.T) {
	t.Parallel()
	m := mptModule(t, []string{mptItem1}, nil)
	q := &stubQuerier{rowFn: mptProgressRowFn(m, nil, false, nil)}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	changed, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, "01970000-0000-7000-9000-00000000dead", m)
	if !errors.Is(err, moduleprogress.ErrItemNotInModule) {
		t.Fatalf("expected moduleprogress.ErrItemNotInModule; got %v", err)
	}
	if changed {
		t.Fatalf("changed must stay false on a rejected completion")
	}
	for _, s := range q.sqls {
		if strings.Contains(s, "UPDATE student_module_progress") {
			t.Fatalf("a rejected completion must not reach the UPDATE; sqls=%v", q.sqls)
		}
	}
}

func TestProgressRepo_Advance_UpdateProgressExecError_Wrapped(t *testing.T) {
	t.Parallel()
	m := mptModule(t, []string{mptItem1}, nil)
	boom := errors.New("update progress deadlock")
	q := &stubQuerier{
		rowFn: mptProgressRowFn(m, nil, false, nil),
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "UPDATE student_module_progress") {
				return boom
			}
			return nil
		},
	}
	r := pg.NewProgressRepo(&stubTxRunner{q: q}, pg.ProgressRepoOptions{})
	ctx := tracing.WithTenantID(context.Background(), mptTenant)

	changed, err := r.Advance(ctx, mptTenant, mptGCID, m.ID, mptCourse, mptItem1, m)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected the UPDATE failure wrapped with cause; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: update progress") {
		t.Fatalf("expected the wrap to name the update; got %v", err)
	}
	if changed {
		t.Fatalf("changed must stay false when the tx aborts")
	}
}

// -----------------------------------------------------------------------------
// Scan branches not reachable without code change (documented, not tested):
//   - enqueueCompletedEvent's CompletedAt==nil guard, envelope Validate and the
//     two json.Marshal wraps are defensive — Advance always stamps CompletedAt
//     on the false→true edge and cgcenvelope.Build output always validates +
//     marshals ([]string / string-keyed maps cannot fail Marshal).
//   - marshalRequiredItemIDs on []string never fails (shared with module.go).
// -----------------------------------------------------------------------------
