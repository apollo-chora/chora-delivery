// skillsfutures_test.go — unit tests for pg.SkillsFuturesRepo (R+ durability
// sweep).
//
// Mirrors exam_test.go: stubs the Querier (shared stubQuerier / stubTxRunner /
// stubRow / stubRows + tenantID / courseID / gcid consts from
// application_test.go) so the SQL surface + RLS contract are exercised without
// a live DB. The SkillsFuturesClaim aggregate is persisted as a JSONB snapshot
// (exam.go pattern), so reads stub a single `data []byte` column carrying the
// marshalled aggregate.
//
// Guarantees:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL matches shape (UPSERT ON CONFLICT (id), SELECT data ... deleted_at
//     IS NULL, ORDER BY id; ?state= filter adds AND state = $2).
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
	"github.com/apollo-chora/chora-delivery/internal/domain/skillsfutures"
)

func newClaim(id string, state skillsfutures.ClaimState) *skillsfutures.SkillsFuturesClaim {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &skillsfutures.SkillsFuturesClaim{
		ID:                   id,
		TenantID:             tenantID,
		GCID:                 gcid,
		CourseID:             courseID,
		NRICHash:             "sha256-deadbeef",
		RequestedAmountCents: 50000,
		State:                state,
		SubmittedAt:          now,
	}
}

func claimJSON(t *testing.T, c *skillsfutures.SkillsFuturesClaim) []byte {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal claim: %v", err)
	}
	return b
}

func TestSkillsFuturesRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewSkillsFuturesRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newClaim("01970000-0000-7000-9999-f00000000001", skillsfutures.ClaimStatePending)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID, ""); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "01970000-0000-7000-9999-f00000000001"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestSkillsFuturesRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSkillsFuturesRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newClaim("01970000-0000-7000-9999-f00000000002", skillsfutures.ClaimStateApproved)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO skillsfutures_claims") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert into skillsfutures_claims; got %q", last)
	}
}

func TestSkillsFuturesRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	want := newClaim("01970000-0000-7000-9999-f00000000003", skillsfutures.ClaimStatePending)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = claimJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewSkillsFuturesRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	c, ok, _ := r.Get(ctx, want.ID)
	if !ok || c == nil {
		t.Fatalf("Get: expected hit")
	}
	if c.ID != want.ID || c.State != skillsfutures.ClaimStatePending {
		t.Fatalf("Get: rehydration mismatch; got %+v", c)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM skillsfutures_claims") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM skillsfutures_claims; got %q", last)
	}
}

func TestSkillsFuturesRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewSkillsFuturesRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if c, ok, _ := r.Get(ctx, "nope"); ok || c != nil {
		t.Fatalf("Get: expected (nil, false); got (%+v, %v)", c, ok)
	}
}

func TestSkillsFuturesRepo_ListByTenant_TwoRows(t *testing.T) {
	t.Parallel()
	a := newClaim("01970000-0000-7000-9999-f00000000aa", skillsfutures.ClaimStatePending)
	b := newClaim("01970000-0000-7000-9999-f00000000bb", skillsfutures.ClaimStateApproved)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = claimJSON(t, a); return nil },
				func(dest ...any) error { *(dest[0].(*[]byte)) = claimJSON(t, b); return nil },
			}}, nil
		},
	}
	r := pg.NewSkillsFuturesRepo(&stubTxRunner{q: q})
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
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant-scoped, soft-delete-filtered, ordered list; got %q", last)
	}
	if strings.Contains(last, "state = $2") {
		t.Fatalf("empty state filter must NOT add a state predicate; got %q", last)
	}
}

func TestSkillsFuturesRepo_ListByTenant_StateFiltered(t *testing.T) {
	t.Parallel()
	a := newClaim("01970000-0000-7000-9999-f00000000cc", skillsfutures.ClaimStatePending)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = claimJSON(t, a); return nil },
			}}, nil
		},
	}
	r := pg.NewSkillsFuturesRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenant(ctx, tenantID, skillsfutures.ClaimStatePending)
	if err != nil {
		t.Fatalf("ListByTenant(state): %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 row; got %d", len(out))
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "state = $2") || !strings.Contains(last, "ORDER BY id") {
		t.Fatalf("expected tenant + state-filtered, ordered list; got %q", last)
	}
}
