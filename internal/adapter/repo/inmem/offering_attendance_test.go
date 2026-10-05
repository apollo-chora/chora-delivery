// offering_attendance_test.go — direct unit tests for the in-memory
// OfferingAttendanceRepo. Pins the natural-key upsert (re-mark replaces the
// prior mark), the nil-safe Upsert, and the tenant/session-scoped listing
// ordered by GCID.
package inmem_test

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/attendance"
	offeratt "github.com/apollo-chora/chora-delivery/internal/domain/offering_attendance"
)

const (
	attSession1 = "01970000-0000-7000-8000-0000000000a1"
	attSession2 = "01970000-0000-7000-8000-0000000000a2"
)

func mustMark(t *testing.T, tenantID, sessionID, gcid string, status attendance.Status) *offeratt.Record {
	t.Helper()
	rec, err := offeratt.NewRecord(offeratt.NewRecordInput{
		TenantID:  tenantID,
		SessionID: sessionID,
		GCID:      gcid,
		Status:    status,
	})
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	return rec
}

func TestOfferingAttendanceRepo_UpsertAndList(t *testing.T) {
	t.Parallel()
	r := inmem.NewOfferingAttendanceRepo()
	ctx := context.Background()

	if err := r.Upsert(ctx, nil); err != nil {
		t.Fatalf("Upsert(nil) must be a safe no-op; got %v", err)
	}
	if err := r.Upsert(ctx, mustMark(t, tenantA, attSession1, gcidA, attendance.StatusPresent)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := r.Upsert(ctx, mustMark(t, tenantA, attSession1, gcidB, attendance.StatusAbsent)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// Other session + other tenant rows must not leak into the listing.
	if err := r.Upsert(ctx, mustMark(t, tenantA, attSession2, gcidA, attendance.StatusPresent)); err != nil {
		t.Fatalf("Upsert other session: %v", err)
	}
	if err := r.Upsert(ctx, mustMark(t, tenantB, attSession1, gcidA, attendance.StatusPresent)); err != nil {
		t.Fatalf("Upsert other tenant: %v", err)
	}

	out, err := r.ListBySession(ctx, tenantA, attSession1)
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 marks (tenant+session scoped); got %d", len(out))
	}
	// Ordered by GCID ascending (gcidA < gcidB).
	if out[0].GCID != gcidA || out[1].GCID != gcidB {
		t.Fatalf("expected [%s %s] by GCID, got [%s %s]", gcidA, gcidB, out[0].GCID, out[1].GCID)
	}

	empty, err := r.ListBySession(ctx, tenantA, "no-such-session")
	if err != nil {
		t.Fatalf("ListBySession(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected 0; got %d", len(empty))
	}
}

// A re-mark on the same (tenant, session, gcid) natural key replaces the
// prior mark — latest wins.
func TestOfferingAttendanceRepo_Upsert_ReMarkReplaces(t *testing.T) {
	t.Parallel()
	r := inmem.NewOfferingAttendanceRepo()
	ctx := context.Background()

	if err := r.Upsert(ctx, mustMark(t, tenantA, attSession1, gcidA, attendance.StatusPresent)); err != nil {
		t.Fatalf("Upsert present: %v", err)
	}
	late := mustMark(t, tenantA, attSession1, gcidA, attendance.StatusAbsent)
	if err := r.Upsert(ctx, late); err != nil {
		t.Fatalf("Upsert absent: %v", err)
	}

	out, err := r.ListBySession(ctx, tenantA, attSession1)
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("re-mark must not create a second row; got %d", len(out))
	}
	if out[0].GCID != gcidA || out[0].Status != attendance.StatusAbsent {
		t.Fatalf("re-mark must replace the mark (latest wins); got status=%s gcid=%s", out[0].Status, out[0].GCID)
	}
}
