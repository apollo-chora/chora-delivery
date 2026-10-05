// room_test.go — prepare-smoke tests for pg.RoomRepo (CHO-2191 SP1). Mirrors
// offering_session_test.go: stubs the Querier (shared stubQuerier / stubTxRunner
// / stubRow / stubRows + tenantID from application_test.go) so the SQL surface +
// RLS contract are exercised without a live DB. Unlike OfferingSession, Room
// persists as REAL columns (no JSONB snapshot) — the room_id-keyed gate (SP2)
// queries them.
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

func newTestRoom(t *testing.T) *campusops.Room {
	t.Helper()
	rm, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantID,
		Name:     "Lab A",
		Capacity: 30,
	})
	if err != nil {
		t.Fatalf("NewRoom: %v", err)
	}
	return rm
}

func TestRoomRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewRoomRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newTestRoom(t)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, err := r.GetForTenant(ctx, tenantID, "x"); ok || !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("GetForTenant: expected (false, ErrNotImplemented); got ok=%v err=%v", ok, err)
	}
}

func TestRoomRepo_Save_AppliesRLSThenUpsertsRealColumns(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewRoomRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	rm := newTestRoom(t) // blank campus_id + branch_id

	if err := r.Save(ctx, rm); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO rooms") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert; got %q", last)
	}
	// args: id, tenant_id, campus_id, branch_id, name, capacity, created_at,
	// updated_at, deleted_at — 9 real columns.
	args := q.args[len(q.args)-1]
	if len(args) != 9 {
		t.Fatalf("expected 9 bind args; got %d (%#v)", len(args), args)
	}
	if args[0] != rm.ID || args[1] != tenantID {
		t.Fatalf("expected id=$1 tenant=$2; got id=%v tenant=%v", args[0], args[1])
	}
	// Blank campus_id + branch_id + deleted_at must bind as NULL (nil), not "".
	if args[2] != nil {
		t.Fatalf("blank campus_id must bind NULL; got %#v", args[2])
	}
	if args[3] != nil {
		t.Fatalf("blank branch_id must bind NULL; got %#v", args[3])
	}
	if args[4] != "Lab A" || args[5] != 30 {
		t.Fatalf("expected name=$5 capacity=$6; got name=%v capacity=%v", args[4], args[5])
	}
	if args[8] != nil {
		t.Fatalf("nil deleted_at must bind NULL; got %#v", args[8])
	}
}

func TestRoomRepo_Save_BindsCampusWhenSet(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewRoomRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	rm := newTestRoom(t)
	rm.CampusID = courseID // any UUID string; assert it flows through as a value
	rm.BranchID = gcid

	if err := r.Save(ctx, rm); err != nil {
		t.Fatalf("Save: %v", err)
	}
	args := q.args[len(q.args)-1]
	if args[2] != courseID {
		t.Fatalf("set campus_id must bind value; got %#v", args[2])
	}
	if args[3] != gcid {
		t.Fatalf("set branch_id must bind value; got %#v", args[3])
	}
}

// Fail-loud contract: an infra/exec failure must surface from Save, never be
// swallowed into a fake success (CHO-2184 / the engineering standard).
func TestRoomRepo_Save_PropagatesExecError(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New("boom: connection reset")}
	r := pg.NewRoomRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newTestRoom(t)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Save must surface the exec error loudly; got %v", err)
	}
}

func TestRoomRepo_GetForTenant_Hit_RoundTrips(t *testing.T) {
	t.Parallel()
	want := newTestRoom(t)
	want.CreatedAt = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	want.UpdatedAt = want.CreatedAt
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*string)) = want.ID
				*(dest[1].(*string)) = tenantID
				*(dest[4].(*string)) = want.Name
				*(dest[5].(*int)) = want.Capacity
				*(dest[6].(*time.Time)) = want.CreatedAt
				*(dest[7].(*time.Time)) = want.UpdatedAt
				return nil
			}}
		},
	}
	r := pg.NewRoomRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, ok, err := r.GetForTenant(ctx, tenantID, want.ID)
	if err != nil || !ok || got == nil {
		t.Fatalf("Get: expected hit; ok=%v err=%v", ok, err)
	}
	if got.ID != want.ID || got.Name != "Lab A" || got.Capacity != 30 {
		t.Fatalf("rehydration mismatch; got %+v", got)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM rooms") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT; got %q", last)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
}

// Belt-and-braces: even if RLS is somehow bypassed and a row for ANOTHER tenant
// comes back, GetForTenant must report a miss (never leak cross-tenant).
func TestRoomRepo_GetForTenant_OtherTenantRow_IsMiss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*string)) = "01985e7f-6666-7abc-8def-0000000000f9"
				*(dest[1].(*string)) = "99999999-9999-7999-8999-999999999999" // foreign tenant
				*(dest[4].(*string)) = "Foreign"
				*(dest[5].(*int)) = 10
				return nil
			}}
		},
	}
	r := pg.NewRoomRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, ok, err := r.GetForTenant(ctx, tenantID, "01985e7f-6666-7abc-8def-0000000000f9"); ok || err != nil {
		t.Fatalf("cross-tenant row must be a miss; ok=%v err=%v", ok, err)
	}
}

func TestRoomRepo_ListByTenant_BindsTenant(t *testing.T) {
	t.Parallel()
	want := newTestRoom(t)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					*(dest[0].(*string)) = want.ID
					*(dest[1].(*string)) = tenantID
					*(dest[4].(*string)) = want.Name
					*(dest[5].(*int)) = want.Capacity
					return nil
				},
			}}, nil
		},
	}
	r := pg.NewRoomRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 1 || out[0].ID != want.ID {
		t.Fatalf("expected 1 rehydrated row; got %+v", out)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "deleted_at IS NULL") || !strings.Contains(last, "ORDER BY created_at") {
		t.Fatalf("list query must bind tenant + filter soft-delete + order by created_at; got %q", last)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 1 || args[0] != tenantID {
		t.Fatalf("expected (tenant) arg; got %#v", args)
	}
}
