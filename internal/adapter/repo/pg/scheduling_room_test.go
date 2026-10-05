// scheduling_room_test.go - the extracted room_id + ends_at columns and the
// double-book translation (CHO-2299, closing ADR-237 O3).
//
// WHY THIS EXISTS. Mig 0059 adds an EXCLUDE gate keyed on scheduled_classes
// (tenant_id, room_id, tstzrange(starts_at, ends_at)). A gate on columns that
// nothing WRITES can never fire: the repo previously persisted only
// id/tenant_id/iso_year/iso_week/starts_at/data, so both gate columns would have
// stayed NULL forever and every row would have sat in the partial predicate's
// exempt branch. These tests pin the write path and the error translation.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

func newRoomedClass(t *testing.T) *scheduling.ScheduledClass {
	t.Helper()
	starts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)
	c, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID:       tenantID,
		CourseID:       courseID,
		InstructorGCID: gcid,
		RoomID:         "01985e7f-6666-7abc-8def-000000000401",
		Room:           "Lab A",
		StartsAt:       starts,
		EndsAt:         starts.Add(2 * time.Hour),
		MaxCapacity:    25,
	})
	if err != nil {
		t.Fatalf("NewScheduledClass: %v", err)
	}
	return c
}

// The gate reads extracted columns, so the upsert must write them.
func TestSchedulingRepo_Save_WritesRoomIDAndEndsAtColumns(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSchedulingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	c := newRoomedClass(t)

	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "room_id") {
		t.Fatalf("upsert must persist room_id or the EXCLUDE gate can never fire; got %q", last)
	}
	if !strings.Contains(last, "ends_at") {
		t.Fatalf("upsert must persist ends_at or the gate's range side is always NULL; got %q", last)
	}
	// Both must also be REFRESHED on conflict, else a reschedule silently keeps
	// the old window and the gate guards a stale range.
	if !strings.Contains(last, "room_id    = EXCLUDED.room_id") &&
		!strings.Contains(last, "room_id = EXCLUDED.room_id") {
		t.Fatalf("ON CONFLICT must refresh room_id; got %q", last)
	}
	if !strings.Contains(last, "ends_at    = EXCLUDED.ends_at") &&
		!strings.Contains(last, "ends_at = EXCLUDED.ends_at") {
		t.Fatalf("ON CONFLICT must refresh ends_at; got %q", last)
	}
}

// A roomless class must bind NULL, not "", so it lands in the gate's exempt
// branch rather than failing the uuid cast.
func TestSchedulingRepo_Save_RoomlessBindsNull(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSchedulingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	starts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)
	c, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID: tenantID, CourseID: courseID, InstructorGCID: gcid,
		StartsAt: starts, EndsAt: starts.Add(time.Hour), MaxCapacity: 10,
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	args := q.args[len(q.args)-1]
	var sawNil bool
	for _, a := range args {
		if a == nil {
			sawNil = true
		}
		if a == "" {
			t.Fatalf("a blank room_id must bind NULL, never an empty string (22P02); args=%#v", args)
		}
	}
	if !sawNil {
		t.Fatalf("expected a NULL bind for the roomless room_id; args=%#v", args)
	}
}

// A real double-book must surface as the DOMAIN sentinel so the handler can map
// it to 409 without matching on driver text.
func TestSchedulingRepo_Save_TranslatesExclusionViolation(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO scheduled_classes") {
			return &pgconn.PgError{Code: "23P01"}
		}
		return nil
	}}
	r := pg.NewSchedulingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	err := r.Save(ctx, newRoomedClass(t))
	if !errors.Is(err, scheduling.ErrRoomDoubleBooked) {
		t.Fatalf("SQLSTATE 23P01 must translate to ErrRoomDoubleBooked; got %v", err)
	}
}

// Any OTHER driver error must stay loud and must NOT be mistaken for a clash.
func TestSchedulingRepo_Save_OtherPgErrorIsNotADoubleBook(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO scheduled_classes") {
			return &pgconn.PgError{Code: "23505"} // unique_violation
		}
		return nil
	}}
	r := pg.NewSchedulingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	err := r.Save(ctx, newRoomedClass(t))
	if err == nil {
		t.Fatalf("a non-exclusion driver error must still fail loudly")
	}
	if errors.Is(err, scheduling.ErrRoomDoubleBooked) {
		t.Fatalf("only 23P01 is a double-book; 23505 must not be reported as one")
	}
}
