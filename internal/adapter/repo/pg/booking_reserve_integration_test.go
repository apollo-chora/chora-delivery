//go:build integration

// booking_reserve_integration_test.go — proves ADR-236 D2's durable capacity
// gate holds against LIVE Cloud SQL under a concurrent stampede. The unit
// smoke test (booking_reserve_test.go) asserts the SQL surface; only a real
// Postgres exercises the transaction-scoped advisory lock that serialises
// concurrent reservations for the same class across connections (== across
// pods). Run:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_BookingRepo_ReserveSeat \
//	  ./services/chora-delivery/internal/adapter/repo/pg/...
package pg_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// TestIntegration_BookingRepo_ReserveSeat_ConcurrentStampede fires many
// concurrent reservations at ONE class with capacity M and asserts exactly M
// win — the advisory lock must prevent any over-book.
func TestIntegration_BookingRepo_ReserveSeat_ConcurrentStampede(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewBookingRepo(&liveTxRunner{pool: pool})

	tenant, _ := uuid.NewV7()
	class, _ := uuid.NewV7()
	tenantID := tenant.String()
	classID := class.String()
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	// Clean every row for this class at the end (winners only, but a class_id
	// sweep is simplest + idempotent).
	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer func() { _ = tx.Rollback(cctx) }()
		_, _ = tx.Exec(cctx, "SET LOCAL chora.tenant_id = '"+tenantID+"'")
		_, _ = tx.Exec(cctx, `DELETE FROM bookings WHERE class_id = $1`, classID)
		_ = tx.Commit(cctx)
	})

	const capacity = 3
	const attempts = 15

	var wg sync.WaitGroup
	var mu sync.Mutex
	won, full := 0, 0
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, _ := uuid.NewV7()
			learner, _ := uuid.NewV7()
			now := time.Now().UTC().Truncate(time.Microsecond)
			b := &domain.Booking{
				ID:        id.String(),
				ClassID:   classID,
				CourseID:  classID, // course id irrelevant to the gate
				TenantID:  tenantID,
				LearnerID: learner.String(),
				Status:    domain.BookingStatusPending,
				CreatedAt: now,
				UpdatedAt: now,
			}
			err := r.ReserveSeatAndSave(ctx, classID, capacity, b)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, domain.ErrClassAtCapacity):
				full++
			default:
				t.Errorf("unexpected reserve err: %v", err)
			}
		}()
	}
	wg.Wait()

	if won != capacity {
		t.Fatalf("OVER/UNDER-BOOK: %d reservations committed, want exactly %d", won, capacity)
	}
	if full != attempts-capacity {
		t.Fatalf("expected %d capacity rejections, got %d", attempts-capacity, full)
	}

	// Confirm the durable row count matches capacity.
	list, err := r.ListByTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	n := 0
	for _, b := range list {
		if b.ClassID == classID {
			n++
		}
	}
	if n != capacity {
		t.Fatalf("durable store holds %d bookings for the class, want %d", n, capacity)
	}
	t.Logf("advisory-lock gate OK: %d/%d reservations won on a capacity-%d class", won, attempts, capacity)
}
