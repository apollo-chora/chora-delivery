// wbl_test.go — unit tests for pg.WblRepo (R+ durability sweep).
//
// Mirrors exam_test.go: stubs the Querier (shared stubQuerier / stubTxRunner /
// stubRow / stubRows + tenantID / courseID consts from application_test.go) so
// the SQL surface + RLS contract are exercised without a live DB. The Placement
// aggregate is persisted as a JSONB snapshot (exam.go pattern), so reads stub a
// single `data []byte` column carrying the marshalled aggregate.
//
// Guarantees:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL matches shape (UPSERT ON CONFLICT (id), SELECT data ... deleted_at
//     IS NULL, ORDER BY id; default list excludes WITHDRAWN; explicit-state
//     list filters state = $2).
//  3. Nil-tx is fail-loud on writes (Save / ListByTenant → ErrNotImplemented)
//     and degrades on point reads (Get → ok=false).
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
	wbl "github.com/apollo-chora/chora-delivery/internal/domain/wbl"
)

func newWblPlacement(id string, state wbl.PlacementState) *wbl.Placement {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &wbl.Placement{
		ID:              id,
		TenantID:        tenantID,
		GCID:            gcid,
		CourseID:        courseID,
		HostOrgName:     "Acme Industries",
		SupervisorName:  "Pat Lee",
		SupervisorEmail: "pat.lee@acme.example",
		StartDate:       now,
		EndDate:         now.Add(72 * time.Hour),
		HoursRequired:   120,
		HoursCompleted:  0,
		State:           state,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func wblPlacementJSON(t *testing.T, p *wbl.Placement) []byte {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal wbl placement: %v", err)
	}
	return b
}

func TestWblRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewWblRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newWblPlacement("01970000-0000-7000-9999-f00000000001", wbl.PlacementStateScheduled)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID, ""); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "01970000-0000-7000-9999-f00000000001"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestWblRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewWblRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newWblPlacement("01970000-0000-7000-9999-f00000000002", wbl.PlacementStateInProgress)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO wbl_placements") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert into wbl_placements; got %q", last)
	}
}

func TestWblRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	want := newWblPlacement("01970000-0000-7000-9999-f00000000003", wbl.PlacementStateInProgress)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = wblPlacementJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewWblRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	p, ok, _ := r.Get(ctx, want.ID)
	if !ok || p == nil {
		t.Fatalf("Get: expected hit")
	}
	if p.ID != want.ID || p.State != wbl.PlacementStateInProgress || p.HostOrgName != want.HostOrgName {
		t.Fatalf("Get: rehydration mismatch; got %+v", p)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM wbl_placements") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM wbl_placements; got %q", last)
	}
}

func TestWblRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewWblRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if p, ok, _ := r.Get(ctx, "nope"); ok || p != nil {
		t.Fatalf("Get: expected (nil, false); got (%+v, %v)", p, ok)
	}
}

func TestWblRepo_ListByTenant_DefaultExcludesWithdrawn(t *testing.T) {
	t.Parallel()
	a := newWblPlacement("01970000-0000-7000-9999-f00000000aa", wbl.PlacementStateScheduled)
	b := newWblPlacement("01970000-0000-7000-9999-f00000000bb", wbl.PlacementStateCompleted)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = wblPlacementJSON(t, a); return nil },
				func(dest ...any) error { *(dest[0].(*[]byte)) = wblPlacementJSON(t, b); return nil },
			}}, nil
		},
	}
	r := pg.NewWblRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenant(ctx, tenantID, "")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rows; got %d", len(out))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "state <> 'WITHDRAWN'") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant-scoped, WITHDRAWN-excluded, ordered list; got %q", last)
	}
	// The default branch binds only the tenant arg.
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 1 || lastArgs[0] != tenantID {
		t.Fatalf("expected single tenant arg; got %#v", lastArgs)
	}
}

func TestWblRepo_ListByTenant_ExplicitStateFilters(t *testing.T) {
	t.Parallel()
	a := newWblPlacement("01970000-0000-7000-9999-f00000000cc", wbl.PlacementStateWithdrawn)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = wblPlacementJSON(t, a); return nil },
			}}, nil
		},
	}
	r := pg.NewWblRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenant(ctx, tenantID, wbl.PlacementStateWithdrawn)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 1 || out[0].State != wbl.PlacementStateWithdrawn {
		t.Fatalf("expected 1 WITHDRAWN row; got %+v", out)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "state = $2") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant + state-filtered, ordered list; got %q", last)
	}
	// The explicit-state branch binds tenant + state args.
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 2 || lastArgs[0] != tenantID || lastArgs[1] != string(wbl.PlacementStateWithdrawn) {
		t.Fatalf("expected (tenant, state) args; got %#v", lastArgs)
	}
}
