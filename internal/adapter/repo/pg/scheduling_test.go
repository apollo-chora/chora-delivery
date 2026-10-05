// scheduling_test.go — unit tests for pg.SchedulingRepo (R+ durability sweep
// Wave 2 follow-up). Mirrors exam_test.go / survey_test.go: stubs the Querier
// (shared stubQuerier / stubTxRunner / stubRow / stubRows + tenantID / courseID
// / gcid consts from application_test.go) so the SQL surface + RLS contract are
// exercised without a live DB. ScheduledClass persists as a JSONB snapshot.
//
// Guarantees:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL matches shape (UPSERT ON CONFLICT (id), week filter on iso_year/iso_week,
//     SELECT data … deleted_at IS NULL).
//  3. Nil-tx is fail-loud on writes/lists (ErrNotImplemented) + ok=false on Get.
//  4. JSONB round-trip rehydrates the aggregate; Save extracts iso_year/iso_week.
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
	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

func newSchedClass(t *testing.T, starts, ends time.Time) *scheduling.ScheduledClass {
	t.Helper()
	c, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID: tenantID, CourseID: courseID, InstructorGCID: gcid,
		RoomID: "Room 401", StartsAt: starts, EndsAt: ends, MaxCapacity: 25,
	})
	if err != nil {
		t.Fatalf("NewScheduledClass: %v", err)
	}
	return c
}

func schedJSON(t *testing.T, c *scheduling.ScheduledClass) []byte {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal scheduled_class: %v", err)
	}
	return b
}

func TestSchedulingRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewSchedulingRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	starts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)

	if err := r.Save(ctx, newSchedClass(t, starts, starts.Add(2*time.Hour))); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenantWeek(ctx, tenantID, 2026, 21); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenantWeek: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "x"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestSchedulingRepo_Save_AppliesRLSThenUpsertsWithISOWeek(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSchedulingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	// 2026-05-19 is in ISO week 2026-W21.
	starts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)

	if err := r.Save(ctx, newSchedClass(t, starts, starts.Add(3*time.Hour))); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO scheduled_classes") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert; got %q", last)
	}
	// iso_year=$3, iso_week=$4 must carry the StartsAt ISO week.
	args := q.args[len(q.args)-1]
	if len(args) != 11 || args[2] != 2026 || args[3] != 21 {
		t.Fatalf("expected iso_year=2026 iso_week=21 bound; got %#v", args)
	}
}

func TestSchedulingRepo_Get_Hit_RoundTrips(t *testing.T) {
	t.Parallel()
	starts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC).Truncate(time.Microsecond)
	want := newSchedClass(t, starts, starts.Add(2*time.Hour))
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = schedJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewSchedulingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	c, ok, _ := r.Get(ctx, want.ID)
	if !ok || c == nil {
		t.Fatalf("Get: expected hit")
	}
	if c.ID != want.ID || c.RoomID != "Room 401" || c.MaxCapacity != 25 {
		t.Fatalf("Get: rehydration mismatch; got %+v", c)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM scheduled_classes") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT; got %q", last)
	}
}

func TestSchedulingRepo_ListByTenantWeek_BindsYearWeek(t *testing.T) {
	t.Parallel()
	starts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)
	c := newSchedClass(t, starts, starts.Add(2*time.Hour))
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = schedJSON(t, c); return nil },
			}}, nil
		},
	}
	r := pg.NewSchedulingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenantWeek(ctx, tenantID, 2026, 21)
	if err != nil {
		t.Fatalf("ListByTenantWeek: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 row; got %d", len(out))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "iso_year = $2") || !strings.Contains(last, "iso_week = $3") {
		t.Fatalf("week query must bind iso_year/iso_week; got %q", last)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 3 || args[1] != 2026 || args[2] != 21 {
		t.Fatalf("expected (tenant, 2026, 21) args; got %#v", args)
	}
}
