// franchise_satellite_test.go: unit tests for pg.FranchiseSatelliteRepo
// (ADR-192 D1 mapping, chora_delivery.franchise_satellite, CHO-2230).
//
// Reuses the shared stubQuerier / stubTxRunner / stubRow(s) from
// application_test.go (same package). The mapping table is FLAT-COLUMN on
// purpose: the future exam_owner_rollup policy subqueries
// "SELECT satellite_tenant_id ... WHERE owner_tenant_id = ..." in SQL, so a
// JSONB snapshot would put the policy's key inside a document.
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
	"github.com/apollo-chora/chora-delivery/internal/domain/franchise"
)

const (
	fsRepoOwner     = "019e2f93-d586-71b5-8c3d-e2b0d0d50001"
	fsRepoSatellite = "019e2f93-d586-71b5-8c3d-e2b0d0d50002"
	fsRepoGCID      = "019e2f93-d586-71b5-8c3d-e2b0d0d5ad01"
)

func newMapping(t *testing.T) *franchise.FranchiseSatellite {
	t.Helper()
	m, err := franchise.New(fsRepoOwner, fsRepoSatellite, fsRepoGCID)
	if err != nil {
		t.Fatalf("franchise.New: %v", err)
	}
	return m
}

func fsCtx() context.Context {
	return tracing.WithTenantID(context.Background(), fsRepoOwner)
}

func TestFranchiseSatelliteRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewFranchiseSatelliteRepo(nil)
	ctx := fsCtx()
	if err := r.Save(ctx, newMapping(t)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: want ErrNotImplemented, got %v", err)
	}
	if _, _, err := r.Get(ctx, "id"); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Get: want ErrNotImplemented, got %v", err)
	}
	if _, err := r.ListByOwner(ctx, fsRepoOwner); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByOwner: want ErrNotImplemented, got %v", err)
	}
	if _, err := r.Revoke(ctx, "id", time.Now()); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Revoke: want ErrNotImplemented, got %v", err)
	}
}

func TestFranchiseSatelliteRepo_Save_AppliesSessionAndInserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	m := newMapping(t)
	if err := r.Save(fsCtx(), m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("want session + insert, got %d statements: %v", len(q.sqls), q.sqls)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("first statement must apply the RLS session, got %q", q.sqls[0])
	}
	insert := q.sqls[len(q.sqls)-1]
	if !strings.Contains(insert, "INSERT INTO franchise_satellite") {
		t.Errorf("insert SQL wrong: %q", insert)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 6 {
		t.Fatalf("insert args=%d want 6 (%v)", len(args), args)
	}
	if args[0] != m.ID || args[1] != m.OwnerTenantID || args[2] != m.SatelliteTenantID || args[3] != m.CreatedByGCID {
		t.Errorf("insert args wrong: %v", args)
	}
}

func TestFranchiseSatelliteRepo_Save_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	// A bare ctx has no tenant: ApplySession MUST refuse (never a silent
	// unscoped write). The RLS-ctx trap battery.
	if err := r.Save(context.Background(), newMapping(t)); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("Save bare ctx: want ErrNoTenantContext, got %v", err)
	}
}

// failOnQuerier fails ONLY the statement containing failContains, so the
// rls.ApplySession SET LOCAL that precedes the insert still succeeds.
type failOnQuerier struct {
	*stubQuerier
	failContains string
	err          error
}

func (f *failOnQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	if strings.Contains(sql, f.failContains) {
		return rls.CommandTag{}, f.err
	}
	return f.stubQuerier.Exec(ctx, sql, args...)
}

// anyTxRunner runs fn against an arbitrary Querier (stubTxRunner is pinned to
// *stubQuerier).
type anyTxRunner struct{ q pg.Querier }

func (a *anyTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q pg.Querier) error) error {
	return fn(ctx, a.q)
}

func TestFranchiseSatelliteRepo_Save_UniqueViolation_IsDuplicateMapping(t *testing.T) {
	t.Parallel()
	q := &failOnQuerier{
		stubQuerier:  &stubQuerier{},
		failContains: "INSERT INTO franchise_satellite",
		err:          errors.New(`ERROR: duplicate key value violates unique constraint "uq_franchise_satellite_live" (SQLSTATE 23505)`),
	}
	r := pg.NewFranchiseSatelliteRepo(&anyTxRunner{q: q})
	err := r.Save(fsCtx(), newMapping(t))
	if !errors.Is(err, franchise.ErrDuplicateMapping) {
		t.Fatalf("Save: want ErrDuplicateMapping, got %v", err)
	}
}

func TestFranchiseSatelliteRepo_Get_ScansRow(t *testing.T) {
	t.Parallel()
	m := newMapping(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*string)) = m.ID
				*(dest[1].(*string)) = m.OwnerTenantID
				*(dest[2].(*string)) = m.SatelliteTenantID
				*(dest[3].(*string)) = m.CreatedByGCID
				*(dest[4].(*time.Time)) = now
				*(dest[5].(*time.Time)) = now
				return nil
			}}
		},
	}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	got, ok, err := r.Get(fsCtx(), m.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.OwnerTenantID != m.OwnerTenantID || got.SatelliteTenantID != m.SatelliteTenantID {
		t.Errorf("Get: %+v", got)
	}
	sel := q.sqls[len(q.sqls)-1]
	if !strings.Contains(sel, "deleted_at IS NULL") {
		t.Errorf("Get SQL must filter soft-deleted rows: %q", sel)
	}
}

func TestFranchiseSatelliteRepo_Get_NoRows_IsGenuineMiss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	_, ok, err := r.Get(fsCtx(), "absent")
	if err != nil {
		t.Fatalf("Get miss: err=%v (a genuine miss is not an error)", err)
	}
	if ok {
		t.Fatal("Get miss: ok must be false")
	}
}

func TestFranchiseSatelliteRepo_ListByOwner(t *testing.T) {
	t.Parallel()
	m := newMapping(t)
	now := time.Now().UTC()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					*(dest[0].(*string)) = m.ID
					*(dest[1].(*string)) = m.OwnerTenantID
					*(dest[2].(*string)) = m.SatelliteTenantID
					*(dest[3].(*string)) = m.CreatedByGCID
					*(dest[4].(*time.Time)) = now
					*(dest[5].(*time.Time)) = now
					return nil
				},
			}}, nil
		},
	}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	items, err := r.ListByOwner(fsCtx(), fsRepoOwner)
	if err != nil {
		t.Fatalf("ListByOwner: %v", err)
	}
	if len(items) != 1 || items[0].SatelliteTenantID != fsRepoSatellite {
		t.Fatalf("ListByOwner: %+v", items)
	}
}

func TestFranchiseSatelliteRepo_Revoke_RowsAffected(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	ok, err := r.Revoke(fsCtx(), "some-id", time.Now().UTC())
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !ok {
		t.Fatal("Revoke: want ok=true when a row was updated")
	}
	upd := q.sqls[len(q.sqls)-1]
	if !strings.Contains(upd, "UPDATE franchise_satellite") || !strings.Contains(upd, "deleted_at") {
		t.Errorf("Revoke SQL must be a soft-delete UPDATE: %q", upd)
	}
	if strings.Contains(strings.ToUpper(upd), "DELETE FROM") {
		t.Errorf("Revoke must NEVER hard-delete: %q", upd)
	}
}
