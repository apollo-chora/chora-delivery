// offering_session_test.go — unit tests for pg.OfferingSessionRepo. Mirrors
// scheduling_test.go: stubs the Querier (shared stubQuerier / stubTxRunner /
// stubRow / stubRows + tenantID from application_test.go) so the SQL surface +
// RLS contract are exercised without a live DB. OfferingSession persists as a
// JSONB snapshot keyed by id (extracted cols: tenant_id, offering_id, starts_at).
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
	offeringsession "github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
)

const oschTestOfferingID = "01985e7f-6666-7abc-8def-0000000000f1"

func newTestSession(t *testing.T) *offeringsession.OfferingSession {
	t.Helper()
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC).Truncate(time.Microsecond)
	s, err := offeringsession.NewOfferingSession(offeringsession.NewOfferingSessionInput{
		TenantID:       tenantID,
		OfferingID:     oschTestOfferingID,
		Title:          "Week 1",
		RoomID:         "01985e7f-6666-7abc-8def-0000000000aa",
		Room:           "Room 204",
		InstructorGCID: gcid,
		StartsAt:       start,
		EndsAt:         start.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("NewOfferingSession: %v", err)
	}
	return s
}

func sessionJSON(t *testing.T, s *offeringsession.OfferingSession) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal offering_session: %v", err)
	}
	return b
}

func TestOfferingSessionRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewOfferingSessionRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, newTestSession(t)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByOffering(ctx, tenantID, oschTestOfferingID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByOffering: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "x"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestOfferingSessionRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewOfferingSessionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	s := newTestSession(t)

	if err := r.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO offering_sessions") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert; got %q", last)
	}
	// args: id, tenant_id, offering_id, starts_at, ends_at, room_id, data,
	// created_at, updated_at, deleted_at (ends_at + room_id extracted for the
	// ratified no-double-book EXCLUDE gate, mig 0052 / CHO-2191).
	args := q.args[len(q.args)-1]
	if len(args) != 10 {
		t.Fatalf("expected 10 bind args; got %d (%#v)", len(args), args)
	}
	if args[1] != tenantID || args[2] != oschTestOfferingID {
		t.Fatalf("expected (tenant, offering) extracted; got tenant=%v offering=%v", args[1], args[2])
	}
	if args[4] != s.EndsAt {
		t.Fatalf("expected ends_at ($5) = %v; got %v", s.EndsAt, args[4])
	}
	// room_id ($6) is bound via nullStr — a non-blank RoomID round-trips as the
	// string (NULL when roomless). The slice-1 `room` string is no longer bound.
	if args[5] != s.RoomID {
		t.Fatalf("expected room_id ($6) = %q; got %v", s.RoomID, args[5])
	}
}

func TestOfferingSessionRepo_Get_Hit_RoundTrips(t *testing.T) {
	t.Parallel()
	want := newTestSession(t)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = sessionJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewOfferingSessionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	s, ok, _ := r.Get(ctx, want.ID)
	if !ok || s == nil {
		t.Fatalf("Get: expected hit")
	}
	if s.ID != want.ID || s.Room != "Room 204" || s.Title != "Week 1" {
		t.Fatalf("Get: rehydration mismatch; got %+v", s)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM offering_sessions") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT; got %q", last)
	}
}

func TestOfferingSessionRepo_ListByOffering_BindsTenantOffering(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = sessionJSON(t, s); return nil },
			}}, nil
		},
	}
	r := pg.NewOfferingSessionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByOffering(ctx, tenantID, oschTestOfferingID)
	if err != nil {
		t.Fatalf("ListByOffering: %v", err)
	}
	if len(out) != 1 || out[0].ID != s.ID {
		t.Fatalf("expected 1 rehydrated row; got %+v", out)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "offering_id = $2") || !strings.Contains(last, "ORDER BY starts_at") {
		t.Fatalf("list query must bind tenant/offering + order by starts_at; got %q", last)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 2 || args[0] != tenantID || args[1] != oschTestOfferingID {
		t.Fatalf("expected (tenant, offering) args; got %#v", args)
	}
}
