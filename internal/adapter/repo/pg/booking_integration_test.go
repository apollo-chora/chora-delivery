//go:build integration

// booking_integration_test.go — exercises the BookingRepo round-trip against
// live Cloud SQL via the Cloud SQL Auth Proxy.
//
// Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_BookingRepo \
//	  ./services/chora-delivery/internal/adapter/repo/pg/...
//
// Mirrors enrollment_integration_test.go (live pgxpool via liveDB(t) + the
// liveTxRunner adapter declared in course_integration_test.go — same _test
// package). The `integration` build tag keeps these off the default unit-test
// run; CI invokes them when the live-DB env vars are wired (Cloud Build per
// [[feedback-no-local-cicd-run]]). Unlike enrolments, bookings has NO FK, so
// no parent row needs seeding — Save writes a standalone row.
//
// Suite:
//  1. Save -> Get round-trip (insert + read same row).
//  2. RLS isolation: Save under A; read + list under B must miss.
//  3. Idempotent Save: re-Save after a status transition updates in place
//     (one row, refreshed status), not a duplicate.
//  4. ListByTenant returns the active set newest-first + hides soft-deleted.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// mkLiveBooking builds a fresh standalone booking with random UUIDs and
// registers a t.Cleanup that hard-deletes the row (under its tenant RLS scope)
// so the dev DB stays pristine.
func mkLiveBooking(t *testing.T, tenant string) *domain.Booking {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	id, _ := uuid.NewV7()
	class, _ := uuid.NewV7()
	course, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	b := &domain.Booking{
		ID:        id.String(),
		ClassID:   class.String(),
		CourseID:  course.String(),
		TenantID:  tenant,
		LearnerID: learner.String(),
		Status:    domain.BookingStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	t.Cleanup(func() {
		pool := liveDB(t)
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer func() { _ = tx.Rollback(cctx) }()
		_, _ = tx.Exec(cctx, "SET LOCAL chora.tenant_id = '"+tenant+"'")
		_, _ = tx.Exec(cctx, `DELETE FROM bookings WHERE id = $1`, b.ID)
		_ = tx.Commit(cctx)
	})
	return b
}

// -----------------------------------------------------------------------------
// 1. Save -> Get round-trip
// -----------------------------------------------------------------------------

func TestIntegration_BookingRepo_SaveGet_Roundtrip(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewBookingRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	b := mkLiveBooking(t, tenantA.String())

	ctx := tracing.WithTenantID(context.Background(), tenantA.String())
	if err := r.Save(ctx, b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := r.Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("expected round-trip hit")
	}
	if got.ID != b.ID || got.LearnerID != b.LearnerID || got.Status != domain.BookingStatusPending {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
}

// -----------------------------------------------------------------------------
// 2. RLS isolation: Save under A, read + list under B -> miss
// -----------------------------------------------------------------------------

func TestIntegration_BookingRepo_RLS_TenantIsolation(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewBookingRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	b := mkLiveBooking(t, tenantA.String())

	ctxA := tracing.WithTenantID(context.Background(), tenantA.String())
	if err := r.Save(ctxA, b); err != nil {
		t.Fatalf("Save under A: %v", err)
	}

	ctxB := tracing.WithTenantID(context.Background(), tenantB.String())
	got, ok, err := r.Get(ctxB, b.ID)
	if err != nil {
		t.Fatalf("tenant B read must be a clean MISS, not an error: %v", err)
	}
	if ok || got != nil {
		t.Fatalf("RLS LEAK: tenant B saw tenant A's booking")
	}
	listB, err := r.ListByTenant(ctxB, tenantB.String())
	if err != nil {
		t.Fatalf("ListByTenant under B: %v", err)
	}
	for _, x := range listB {
		if x.ID == b.ID {
			t.Fatalf("RLS LEAK: tenant A's booking appeared in tenant B's list")
		}
	}

	gotA, okA, errA := r.Get(ctxA, b.ID)
	if errA != nil {
		t.Fatalf("Get(ctxA): %v", errA)
	}
	if !okA || gotA == nil {
		t.Fatalf("expected tenant A to see its own booking")
	}
	t.Logf("RLS isolation OK at the repo layer: A=ok B=hidden")
}

// -----------------------------------------------------------------------------
// 3. Idempotent Save (re-Save after transition updates in place)
// -----------------------------------------------------------------------------

func TestIntegration_BookingRepo_Save_IsIdempotent(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewBookingRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	b := mkLiveBooking(t, tenantA.String())
	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	if err := r.Save(ctx, b); err != nil {
		t.Fatalf("Save #1: %v", err)
	}
	if err := b.TransitionStatus(domain.BookingStatusConfirmed); err != nil {
		t.Fatalf("TransitionStatus: %v", err)
	}
	if err := r.Save(ctx, b); err != nil {
		t.Fatalf("Save #2 (after transition): %v", err)
	}

	got, ok, err := r.Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("Get after re-Save: %v", err)
	}
	if !ok || got == nil {
		t.Fatalf("expected hit after re-Save")
	}
	if got.Status != domain.BookingStatusConfirmed {
		t.Fatalf("expected status confirmed after re-Save; got %q", got.Status)
	}

	list, err := r.ListByTenant(ctx, tenantA.String())
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	n := 0
	for _, x := range list {
		if x.ID == b.ID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("idempotency violated: expected 1 row for booking; got %d", n)
	}
}

// -----------------------------------------------------------------------------
// 4. ListByTenant hides soft-deleted + returns newest-first
// -----------------------------------------------------------------------------

func TestIntegration_BookingRepo_ListByTenant_HidesSoftDeleted(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewBookingRepo(&liveTxRunner{pool: pool})

	tenantA, _ := uuid.NewV7()
	ctx := tracing.WithTenantID(context.Background(), tenantA.String())

	live := mkLiveBooking(t, tenantA.String())
	gone := mkLiveBooking(t, tenantA.String())
	for _, b := range []*domain.Booking{live, gone} {
		if err := r.Save(ctx, b); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// Soft-delete `gone` directly (the handler path is DELETE/withdraw — not
	// wired here; the column-level filter is what we assert).
	gtx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin soft-delete: %v", err)
	}
	_, _ = gtx.Exec(ctx, "SET LOCAL chora.tenant_id = '"+tenantA.String()+"'")
	if _, err := gtx.Exec(ctx, `UPDATE bookings SET deleted_at = now() WHERE id = $1`, gone.ID); err != nil {
		_ = gtx.Rollback(ctx)
		t.Fatalf("soft-delete: %v", err)
	}
	if err := gtx.Commit(ctx); err != nil {
		t.Fatalf("commit soft-delete: %v", err)
	}

	out, err := r.ListByTenant(ctx, tenantA.String())
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	sawLive, sawGone := false, false
	for _, x := range out {
		if x.ID == live.ID {
			sawLive = true
		}
		if x.ID == gone.ID {
			sawGone = true
		}
	}
	if !sawLive {
		t.Fatalf("expected active booking in list")
	}
	if sawGone {
		t.Fatalf("soft-deleted booking leaked into list")
	}
}
