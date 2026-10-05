// campus_test.go - prepare-smoke tests for pg.CampusRepo (CHO-2293).
//
// Mirrors room_test.go: stubs the Querier (shared stubQuerier / stubTxRunner /
// stubRow / stubRows + tenantID from application_test.go) so the SQL surface and
// the RLS contract are exercised without a live DB.
//
// Campus was the LAST in-memory binding in chora-delivery serving production
// reads and writes, and it was not even registered with the durability guard, so
// the boot log read in_memory=0 while campuses died on every pod restart. Like
// Room, Campus persists as REAL queryable columns, not a JSONB snapshot.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

func newTestCampus(t *testing.T) *campusops.Campus {
	t.Helper()
	c, err := campusops.NewCampus(campusops.NewCampusInput{
		TenantID:  tenantID,
		Name:      "MTM Singapore, Bras Basah",
		AddressL1: "123 Bras Basah Rd",
		City:      "Singapore",
		Country:   "SG",
	})
	if err != nil {
		t.Fatalf("NewCampus: %v", err)
	}
	return c
}

// An unwired repo is a WIRING BUG, not an empty database (CHO-2184).
func TestCampusRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewCampusRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newTestCampus(t)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, err := r.GetForTenant(ctx, tenantID, "x"); ok || !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("GetForTenant: expected (false, ErrNotImplemented); got ok=%v err=%v", ok, err)
	}
}

func TestCampusRepo_Save_AppliesRLSThenUpsertsRealColumns(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCampusRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	c := newTestCampus(t)

	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO campuses") ||
		!strings.Contains(last, "ON CONFLICT (id)") ||
		!strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert; got %q", last)
	}
	// args: id, tenant_id, name, address_l1, address_l2, city, country,
	// created_at, updated_at, deleted_at = 10 real columns.
	args := q.args[len(q.args)-1]
	if len(args) != 10 {
		t.Fatalf("expected 10 bind args; got %d (%#v)", len(args), args)
	}
	if args[0] != c.ID || args[1] != tenantID {
		t.Fatalf("expected id=$1 tenant=$2; got id=%v tenant=%v", args[0], args[1])
	}
	if args[2] != "MTM Singapore, Bras Basah" {
		t.Fatalf("expected name=$3; got %v", args[2])
	}
	if args[6] != "SG" {
		t.Fatalf("expected country=$7; got %v", args[6])
	}
	if args[9] != nil {
		t.Fatalf("nil deleted_at must bind NULL; got %#v", args[9])
	}
}

// Fail-loud: an infra/exec failure must surface, never be swallowed into a fake
// success (the engineering standard).
func TestCampusRepo_Save_PropagatesExecError(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New("boom: connection reset")}
	r := pg.NewCampusRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newTestCampus(t)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Save must surface the exec error loudly; got %v", err)
	}
}

func TestCampusRepo_GetForTenant_Hit_RoundTrips(t *testing.T) {
	t.Parallel()
	want := newTestCampus(t)
	want.CreatedAt = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	want.UpdatedAt = want.CreatedAt
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*string)) = want.ID
				*(dest[1].(*string)) = tenantID
				*(dest[2].(*string)) = want.Name
				*(dest[6].(*string)) = want.Country
				*(dest[7].(*time.Time)) = want.CreatedAt
				*(dest[8].(*time.Time)) = want.UpdatedAt
				return nil
			}}
		},
	}
	r := pg.NewCampusRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, ok, err := r.GetForTenant(ctx, tenantID, want.ID)
	if err != nil || !ok || got == nil {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != want.ID || got.Name != want.Name || got.Country != "SG" {
		t.Fatalf("rehydration mismatch; got %+v", got)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM campuses") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT; got %q", last)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
}

// A dead DB read must NOT read as an absent row (CHO-2184). ok=false with a nil
// error is reserved for a GENUINE miss.
func TestCampusRepo_GetForTenant_InfraError_IsLoudNotAMiss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("boom: RLS policy violation")
			}}
		},
	}
	r := pg.NewCampusRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, ok, err := r.GetForTenant(ctx, tenantID, "any-id")
	if err == nil {
		t.Fatalf("infra failure must surface as an error, not a miss; got ok=%v got=%v", ok, got)
	}
	if ok {
		t.Fatalf("infra failure must report ok=false; got ok=true")
	}
}

// Belt-and-braces: even if RLS is somehow bypassed, never leak a cross-tenant row.
func TestCampusRepo_GetForTenant_OtherTenantRow_IsMiss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*string)) = "01985e7f-6666-7abc-8def-0000000000f9"
				*(dest[1].(*string)) = "99999999-9999-7999-8999-999999999999" // foreign tenant
				*(dest[2].(*string)) = "Foreign Campus"
				*(dest[6].(*string)) = "SG"
				return nil
			}}
		},
	}
	r := pg.NewCampusRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, ok, err := r.GetForTenant(ctx, tenantID, "01985e7f-6666-7abc-8def-0000000000f9")
	if err != nil {
		t.Fatalf("cross-tenant row is a miss, not an error; got %v", err)
	}
	if ok || got != nil {
		t.Fatalf("must never leak a cross-tenant row; got ok=%v row=%+v", ok, got)
	}
}

func TestCampusRepo_ListByTenant_AppliesRLSAndScopes(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCampusRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByTenant(ctx, tenantID); err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM campuses") ||
		!strings.Contains(last, "tenant_id = $1") ||
		!strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected tenant-scoped soft-delete-filtered SELECT; got %q", last)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 1 || args[0] != tenantID {
		t.Fatalf("expected tenant bind arg; got %#v", args)
	}
}
