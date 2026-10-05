// booking_test.go — unit tests for pg.BookingRepo (HANDOFF_RPLUS §6 follow-up:
// pg-back the Booking aggregate + GET-list).
//
// Mirrors enrollment_test.go: stubs the Querier (shared stubQuerier /
// stubTxRunner / stubRow / stubRows + tenantID / courseID / gcid consts from
// application_test.go) so the SQL surface + RLS contract are exercised without
// a live DB. Live RLS isolation is verified in booking_integration_test.go
// (build tag `integration`).
//
// Guarantees:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL templates match the expected shape (UPSERT ON CONFLICT (id),
//     SELECT ... WHERE deleted_at IS NULL, ORDER BY created_at DESC).
//  3. Nil-tx is fail-loud on writes (Save / ListByTenant → ErrNotImplemented)
//     and degrades on point reads (Get → ok=false).
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

const classID = "01970000-0000-7000-8000-000000000aaa"

// newBooking builds a domain.Booking directly (bypasses NewBooking's class
// capacity reservation) for repo-layer round-trip assertions.
func newBooking(id string) *domain.Booking {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.Booking{
		ID:        id,
		ClassID:   classID,
		CourseID:  courseID,
		TenantID:  tenantID,
		LearnerID: gcid,
		Status:    domain.BookingStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// -----------------------------------------------------------------------------
// Nil-TxRunner — fail-loud on writes, degrade on point reads
// -----------------------------------------------------------------------------

func TestBookingRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewBookingRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newBooking("01970000-0000-7000-9999-000000000001")); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "01970000-0000-7000-9999-000000000001"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

// -----------------------------------------------------------------------------
// Save — RLS-first, idempotent upsert on id
// -----------------------------------------------------------------------------

func TestBookingRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewBookingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	b := newBooking("01970000-0000-7000-9999-000000000002")
	if err := r.Save(ctx, b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + UPSERT); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO bookings") {
		t.Fatalf("expected INSERT INTO bookings; got %q", last)
	}
	if !strings.Contains(last, "ON CONFLICT (id)") {
		t.Fatalf("expected ON CONFLICT (id) idempotent upsert; got %q", last)
	}
	if !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected DO UPDATE on conflict; got %q", last)
	}
}

// -----------------------------------------------------------------------------
// Get — RLS-first; hit rehydrates the aggregate; miss returns ok=false
// -----------------------------------------------------------------------------

func TestBookingRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if len(dest) != 9 {
					return errors.New("scanBooking: expected 9 destinations")
				}
				*(dest[0].(*string)) = "01970000-0000-7000-9999-000000000003"
				*(dest[1].(*string)) = tenantID
				*(dest[2].(*string)) = classID
				*(dest[3].(*string)) = courseID
				*(dest[4].(*string)) = gcid
				*(dest[5].(*string)) = string(domain.BookingStatusConfirmed)
				*(dest[6].(*time.Time)) = now
				*(dest[7].(*time.Time)) = now
				*(dest[8].(**time.Time)) = nil
				return nil
			}}
		},
	}
	r := pg.NewBookingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	b, ok, _ := r.Get(ctx, "01970000-0000-7000-9999-000000000003")
	if !ok || b == nil {
		t.Fatalf("Get: expected hit")
	}
	if b.Status != domain.BookingStatusConfirmed {
		t.Fatalf("Get: status mismatch; got %q", b.Status)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM bookings") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM bookings; got %q", last)
	}
}

func TestBookingRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
		},
	}
	r := pg.NewBookingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if b, ok, _ := r.Get(ctx, "nope"); ok || b != nil {
		t.Fatalf("Get: expected (nil, false); got (%+v, %v)", b, ok)
	}
}

// -----------------------------------------------------------------------------
// ListByTenant — RLS-first; newest-first; soft-delete-filtered
// -----------------------------------------------------------------------------

func TestBookingRepo_ListByTenant_TwoRows(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	older := now.Add(-time.Hour)
	mk := func(id string, ts time.Time) func(dest ...any) error {
		return func(dest ...any) error {
			*(dest[0].(*string)) = id
			*(dest[1].(*string)) = tenantID
			*(dest[2].(*string)) = classID
			*(dest[3].(*string)) = courseID
			*(dest[4].(*string)) = gcid
			*(dest[5].(*string)) = string(domain.BookingStatusPending)
			*(dest[6].(*time.Time)) = ts
			*(dest[7].(*time.Time)) = ts
			*(dest[8].(**time.Time)) = nil
			return nil
		}
	}
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				mk("01970000-0000-7000-9999-00000000000a", now),
				mk("01970000-0000-7000-9999-00000000000b", older),
			}}, nil
		},
	}
	r := pg.NewBookingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenant(ctx, tenantID)
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
	if !strings.Contains(last, "ORDER BY created_at DESC") {
		t.Fatalf("expected ORDER BY created_at DESC; got %q", last)
	}
	if !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete filter; got %q", last)
	}
	if !strings.Contains(last, "tenant_id = $1") {
		t.Fatalf("expected tenant_id = $1 scope; got %q", last)
	}
}
