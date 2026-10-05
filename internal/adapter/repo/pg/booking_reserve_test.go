// booking_reserve_test.go — prepare-smoke unit specs for ADR-236 D2's durable
// capacity gate on pg.BookingRepo.
//
// ReserveSeatAndSave enforces the class-capacity invariant DURABLY and
// cross-pod-correctly: inside ONE serialized txn it (1) applies RLS, (2) takes
// a transaction-scoped advisory lock keyed on the class so concurrent
// reservations for the same class serialise, (3) counts the class's active
// bookings, (4) inserts iff under capacity, else returns ErrClassAtCapacity.
// These stubs assert the SQL surface + ordering without a live DB; the live
// advisory-lock behaviour + concurrency is proven in
// booking_reserve_integration_test.go (build tag `integration`).
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// countRowStub returns a Querier whose single QueryRow (the count) yields n.
func countRowStub(n int) *stubQuerier {
	return &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if len(dest) != 1 {
					return errors.New("count scan: expected 1 destination")
				}
				*(dest[0].(*int)) = n
				return nil
			}}
		},
	}
}

func TestBookingRepo_ReserveSeatAndSave_NilTx(t *testing.T) {
	t.Parallel()
	r := pg.NewBookingRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.ReserveSeatAndSave(ctx, classID, 10, newBooking("01970000-0000-7000-9999-0000000000f1")); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented on nil tx; got %v", err)
	}
}

func TestBookingRepo_ReserveSeatAndSave_UnderCapacity_RLSLockCountInsert(t *testing.T) {
	t.Parallel()
	q := countRowStub(0) // no existing bookings
	r := pg.NewBookingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.ReserveSeatAndSave(ctx, classID, 2, newBooking("01970000-0000-7000-9999-0000000000f2")); err != nil {
		t.Fatalf("ReserveSeatAndSave under capacity: %v", err)
	}

	joined := strings.Join(q.sqls, "\n")
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	if !strings.Contains(joined, "pg_advisory_xact_lock") {
		t.Fatalf("expected a transaction-scoped advisory lock; SQLs=%q", q.sqls)
	}
	if !strings.Contains(joined, "count(") || !strings.Contains(joined, "FROM bookings") || !strings.Contains(joined, "class_id") {
		t.Fatalf("expected a count of the class's bookings; SQLs=%q", q.sqls)
	}
	if !strings.Contains(joined, "INSERT INTO bookings") {
		t.Fatalf("under capacity must INSERT the booking; SQLs=%q", q.sqls)
	}
	// Ordering: RLS -> lock -> count -> insert. The lock must precede the count.
	lockIdx, countIdx, insertIdx := -1, -1, -1
	for i, s := range q.sqls {
		if lockIdx < 0 && strings.Contains(s, "pg_advisory_xact_lock") {
			lockIdx = i
		}
		if countIdx < 0 && strings.Contains(s, "count(") {
			countIdx = i
		}
		if insertIdx < 0 && strings.Contains(s, "INSERT INTO bookings") {
			insertIdx = i
		}
	}
	if !(lockIdx < countIdx && countIdx < insertIdx) {
		t.Fatalf("expected order lock(%d) < count(%d) < insert(%d)", lockIdx, countIdx, insertIdx)
	}
}

func TestBookingRepo_ReserveSeatAndSave_AtCapacity_RejectsNoInsert(t *testing.T) {
	t.Parallel()
	q := countRowStub(2) // already 2 bookings
	r := pg.NewBookingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	err := r.ReserveSeatAndSave(ctx, classID, 2, newBooking("01970000-0000-7000-9999-0000000000f3"))
	if !errors.Is(err, domain.ErrClassAtCapacity) {
		t.Fatalf("expected ErrClassAtCapacity at capacity; got %v", err)
	}
	for _, s := range q.sqls {
		if strings.Contains(s, "INSERT INTO bookings") {
			t.Fatalf("at capacity must NOT INSERT; found %q", s)
		}
	}
}
