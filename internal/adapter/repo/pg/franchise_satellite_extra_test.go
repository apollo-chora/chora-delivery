// franchise_satellite_extra_test.go — extra pg.FranchiseSatelliteRepo unit
// tests: finish partial statement coverage of franchise_satellite.go.
//
// Extends franchise_satellite_test.go (same pg_test package) — reuses
// newMapping + fsRepoOwner / fsRepoSatellite / fsRepoGCID + fsCtx + the
// shared stubQuerier / stubTxRunner / stubRow / stubRows fixtures.
//
// New helpers/fillers/consts in this file are prefixed `frx` to keep the
// package-level pg_test namespace unique across parallel agents.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/franchise"
)

// frxZeroRowsQuerier is a stubQuerier whose Exec reports ZERO rows affected
// (the stub otherwise hard-codes RowsAffected: 1) — used to exercise the
// Revoke soft-delete miss branch without a live DB.
type frxZeroRowsQuerier struct {
	*stubQuerier
}

func (f frxZeroRowsQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	_, err := f.stubQuerier.Exec(ctx, sql, args...)
	return rls.CommandTag{}, err
}

// -----------------------------------------------------------------------------
// Save — nil guard + non-unique exec error
// -----------------------------------------------------------------------------

func TestFranchiseSatelliteRepo_Save_NilMapping_IsNoOp(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	if err := r.Save(fsCtx(), nil); err != nil {
		t.Fatalf("Save nil: %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("nil mapping must not execute SQL; got %v", q.sqls)
	}
}

func TestFranchiseSatelliteRepo_Save_NonUniqueError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO franchise_satellite") {
			return errors.New("connection lost")
		}
		return nil
	}}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	err := r.Save(fsCtx(), newMapping(t))
	if err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
	if errors.Is(err, franchise.ErrDuplicateMapping) {
		t.Fatalf("a non-23505 error must NOT map to ErrDuplicateMapping; got %v", err)
	}
	if !strings.Contains(err.Error(), "pg: insert franchise_satellite") {
		t.Fatalf("insert error must be wrapped with context; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Get — infra (non-"no rows") scan error is LOUD
// -----------------------------------------------------------------------------

func TestFranchiseSatelliteRepo_Get_InfraError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return errors.New("conn reset") }}
	}}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	_, ok, err := r.Get(fsCtx(), "any-id")
	if err == nil {
		t.Fatalf("expected an infra error to surface (CHO-2184: not a miss)")
	}
	if ok {
		t.Fatalf("ok must stay false alongside the error")
	}
	if !strings.Contains(err.Error(), "pg: get franchise_satellite") {
		t.Fatalf("get error must be wrapped with context; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListByOwner — query + scan error branches
// -----------------------------------------------------------------------------

func TestFranchiseSatelliteRepo_ListByOwner_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("connection reset by peer")
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, wantErr
	}}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	if _, err := r.ListByOwner(fsCtx(), fsRepoOwner); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v; got %v", wantErr, err)
	}
}

func TestFranchiseSatelliteRepo_ListByOwner_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	if _, err := r.ListByOwner(fsCtx(), fsRepoOwner); err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
}

// -----------------------------------------------------------------------------
// Revoke — exec error wrap + zero-rows-affected (soft-delete miss)
// -----------------------------------------------------------------------------

func TestFranchiseSatelliteRepo_Revoke_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "UPDATE franchise_satellite") {
			return errors.New("write failed")
		}
		return nil
	}}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	_, err := r.Revoke(fsCtx(), "some-id", time.Now().UTC())
	if err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: revoke franchise_satellite") {
		t.Fatalf("revoke error must be wrapped with context; got %v", err)
	}
}

func TestFranchiseSatelliteRepo_Revoke_ZeroRowsAffected_ReturnsFalse(t *testing.T) {
	t.Parallel()
	// RowsAffected=0 → the soft-delete matched no live row → ok=false (the
	// idempotent re-revoke case), nil error.
	q := frxZeroRowsQuerier{stubQuerier: &stubQuerier{}}
	r := pg.NewFranchiseSatelliteRepo(&anyTxRunner{q: q})
	ok, err := r.Revoke(fsCtx(), "already-revoked", time.Now().UTC())
	if err != nil {
		t.Fatalf("Revoke zero rows: %v", err)
	}
	if ok {
		t.Fatal("Revoke zero rows: ok must be false when no live row matched")
	}
}

func TestFranchiseSatelliteRepo_Get_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	if _, _, err := r.Get(context.Background(), fsRepoOwner); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestFranchiseSatelliteRepo_ListByOwner_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	if _, err := r.ListByOwner(context.Background(), fsRepoOwner); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestFranchiseSatelliteRepo_Revoke_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	if _, err := r.Revoke(context.Background(), fsRepoOwner, time.Now()); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestFranchiseSatelliteRepo_Save_UniqueViolation_MessageForm(t *testing.T) {
	t.Parallel()
	// The "duplicate key value" (non-SQLSTATE) form is also classified as a
	// duplicate by isFranchiseUniqueViolation.
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO franchise_satellite") {
			return errors.New("duplicate key value violates unique constraint")
		}
		return nil
	}}
	r := pg.NewFranchiseSatelliteRepo(&stubTxRunner{q: q})
	if err := r.Save(fsCtx(), newMapping(t)); !errors.Is(err, franchise.ErrDuplicateMapping) {
		t.Fatalf("Save: want ErrDuplicateMapping for the message-form duplicate; got %v", err)
	}
}
