// booking_reserve_test.go — RED specs for ADR-236 D2: the durable-shaped
// capacity gate on the in-memory BookingRepo.
//
// ReserveSeatAndSave(ctx, classID, maxCapacity, b) is the drop-in for the
// retired Class.reserveSeat mutex: it counts the class's active bookings and
// inserts iff under capacity, atomically. The in-memory adapter serialises via
// its own mutex; the pg adapter serialises via a transaction-scoped advisory
// lock (booking_reserve_test.go in repo/pg). The concurrency test proves the
// gate holds — exactly maxCapacity reservations win under a stampede.
package inmem_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const reserveClassID = "01970000-0000-7000-8000-0000000000c1"

func mkPendingBooking(id, classID string) *domain.Booking {
	now := time.Now().UTC()
	return &domain.Booking{
		ID:        id,
		ClassID:   classID,
		CourseID:  "01970000-0000-7000-8000-0000000000c0",
		TenantID:  tenantA,
		LearnerID: gcidA,
		Status:    domain.BookingStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestBookingRepo_ReserveSeatAndSave_UnderCapacity(t *testing.T) {
	ctx := context.Background()
	r := inmem.NewBookingRepo()

	b := mkPendingBooking("01970000-0000-7000-9000-0000000000a1", reserveClassID)
	if err := r.ReserveSeatAndSave(ctx, reserveClassID, 2, b); err != nil {
		t.Fatalf("ReserveSeatAndSave: %v", err)
	}
	got, ok, err := r.Get(ctx, b.ID)
	if err != nil || !ok || got == nil {
		t.Fatalf("expected the reserved booking to be stored; ok=%v err=%v", ok, err)
	}
}

func TestBookingRepo_ReserveSeatAndSave_AtCapacity_Rejects(t *testing.T) {
	ctx := context.Background()
	r := inmem.NewBookingRepo()

	// Fill the 2 seats.
	for i, id := range []string{
		"01970000-0000-7000-9000-0000000000b1",
		"01970000-0000-7000-9000-0000000000b2",
	} {
		if err := r.ReserveSeatAndSave(ctx, reserveClassID, 2, mkPendingBooking(id, reserveClassID)); err != nil {
			t.Fatalf("seed booking %d: %v", i, err)
		}
	}
	// The 3rd must be rejected — and NOT stored.
	third := mkPendingBooking("01970000-0000-7000-9000-0000000000b3", reserveClassID)
	err := r.ReserveSeatAndSave(ctx, reserveClassID, 2, third)
	if !errors.Is(err, domain.ErrClassAtCapacity) {
		t.Fatalf("expected ErrClassAtCapacity, got %v", err)
	}
	if _, ok, _ := r.Get(ctx, third.ID); ok {
		t.Fatalf("rejected booking must NOT be stored")
	}
}

// A different class is unaffected by a full class's count.
func TestBookingRepo_ReserveSeatAndSave_CountIsPerClass(t *testing.T) {
	ctx := context.Background()
	r := inmem.NewBookingRepo()
	otherClass := "01970000-0000-7000-8000-0000000000c2"

	if err := r.ReserveSeatAndSave(ctx, reserveClassID, 1, mkPendingBooking("01970000-0000-7000-9000-0000000000d1", reserveClassID)); err != nil {
		t.Fatalf("class1 seed: %v", err)
	}
	// otherClass has its own capacity — a booking here must succeed.
	if err := r.ReserveSeatAndSave(ctx, otherClass, 1, mkPendingBooking("01970000-0000-7000-9000-0000000000d2", otherClass)); err != nil {
		t.Fatalf("otherClass booking must succeed (separate count): %v", err)
	}
}

// Adversarial: many concurrent reservations on a capacity-M class must let
// EXACTLY M win — the gate never over-books under a stampede.
func TestBookingRepo_ReserveSeatAndSave_ConcurrentStampede(t *testing.T) {
	ctx := context.Background()
	r := inmem.NewBookingRepo()

	const capacity = 5
	const attempts = 40

	var wg sync.WaitGroup
	var mu sync.Mutex
	won, full := 0, 0
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := mkPendingBooking(newTestUUID(i), reserveClassID)
			err := r.ReserveSeatAndSave(ctx, reserveClassID, capacity, b)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, domain.ErrClassAtCapacity):
				full++
			default:
				t.Errorf("unexpected err: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if won != capacity {
		t.Fatalf("over/under-booked: %d reservations won, want exactly %d", won, capacity)
	}
	if full != attempts-capacity {
		t.Fatalf("expected %d capacity rejections, got %d", attempts-capacity, full)
	}
	// The store must hold exactly `capacity` active bookings for the class.
	list, err := r.ListByTenant(ctx, tenantA)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	n := 0
	for _, b := range list {
		if b.ClassID == reserveClassID {
			n++
		}
	}
	if n != capacity {
		t.Fatalf("store holds %d bookings for the class, want %d", n, capacity)
	}
}

// newTestUUID builds a distinct valid-ish id per goroutine index.
func newTestUUID(i int) string {
	const hex = "0123456789abcdef"
	return "01970000-0000-7000-9000-0000000e" + string([]byte{hex[(i>>4)&0xf], hex[i&0xf]}) + "00"
}
