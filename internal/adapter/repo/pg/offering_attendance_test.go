// offering_attendance_test.go — unit tests for pg.OfferingAttendanceRepo.
// Stubs the Querier (shared stubs + tenantID from application_test.go) so the
// SQL surface + RLS + upsert-on-natural-key contract are exercised without a
// live DB.
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
	"github.com/apollo-chora/chora-delivery/internal/domain/attendance"
	offeringattendance "github.com/apollo-chora/chora-delivery/internal/domain/offering_attendance"
)

const oattTestSessionID = "01985e7f-7777-7abc-8def-000000000501"

func newTestMark(t *testing.T) *offeringattendance.Record {
	t.Helper()
	rec, err := offeringattendance.NewRecord(offeringattendance.NewRecordInput{
		TenantID:  tenantID,
		SessionID: oattTestSessionID,
		GCID:      gcid,
		Status:    attendance.StatusPresent,
		Source:    attendance.SourceManual,
		Now:       time.Date(2026, 9, 1, 9, 5, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	return rec
}

func markJSON(t *testing.T, rec *offeringattendance.Record) []byte {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal attendance_record: %v", err)
	}
	return b
}

func TestOfferingAttendanceRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewOfferingAttendanceRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Upsert(ctx, newTestMark(t)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Upsert: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListBySession(ctx, tenantID, oattTestSessionID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListBySession: expected ErrNotImplemented; got %v", err)
	}
}

func TestOfferingAttendanceRepo_Upsert_AppliesRLSThenOnConflict(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewOfferingAttendanceRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Upsert(ctx, newTestMark(t)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO offering_attendance_records") ||
		!strings.Contains(last, "ON CONFLICT (tenant_id, session_id, gcid)") ||
		!strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected upsert on natural key; got %q", last)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 6 || args[1] != tenantID || args[2] != oattTestSessionID || args[3] != gcid {
		t.Fatalf("expected (id,tenant,session,gcid,recorded_at,data) args; got %#v", args)
	}
}

func TestOfferingAttendanceRepo_ListBySession_BindsTenantSession(t *testing.T) {
	t.Parallel()
	rec := newTestMark(t)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = markJSON(t, rec); return nil },
			}}, nil
		},
	}
	r := pg.NewOfferingAttendanceRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListBySession(ctx, tenantID, oattTestSessionID)
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(out) != 1 || out[0].GCID != gcid {
		t.Fatalf("expected 1 rehydrated row; got %+v", out)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "tenant_id = $1") || !strings.Contains(last, "session_id = $2") || !strings.Contains(last, "ORDER BY gcid") {
		t.Fatalf("list query must bind tenant/session + order by gcid; got %q", last)
	}
}
