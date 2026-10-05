// live_poll_unit_test.go — unit tests for pg.LivePollRepo (ADR-168 gap-1b),
// from OUTSIDE the package. Stubs the TxRunner (shared stubQuerier /
// stubTxRunner / stubRow / stubRows + tenantID / gcid consts from
// application_test.go) so the SQL surface + fail-loud guarantees are exercised
// without a live DB:
//
//  1. nil TxRunner is a WIRING BUG — Save/Get return ErrNotImplemented,
//     ListByTenant returns nil (CHO-2184).
//  2. SQL shape: idempotent upsert (ON CONFLICT (id) DO UPDATE), SELECT data
//     ... deleted_at IS NULL, tenant-scoped ordered list.
//  3. NO RLS (see live_poll.go + migrations/0018_live_poll.up.sql): the adapter
//     does NOT call rls.ApplySession — the first SQL emitted is the data
//     query itself, never a SET LOCAL.
//  4. The JSONB snapshot rides the single `data []byte` dest; fillers write it
//     with the marshalled aggregate. LivePoll's custom Marshal/Unmarshal keeps
//     the unexported `voters` set lossless through the adapter's snapshot.
//  5. Error wraps ("pg: upsert live_poll", "pg: get live_poll", ...) preserve
//     the underlying cause via %w.
//
// Real upsert/JSONB round-trip against Cloud SQL is the DSN-gated
// integration_test.go; the voters-losslessness itself is
// live_poll_roundtrip_test.go.
package pg_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/classroom"
)

// -----------------------------------------------------------------------------
// lpx helpers — fresh aggregates + JSONB `data []byte` row fillers
// -----------------------------------------------------------------------------

// lpxVotedPoll builds an OPEN LivePoll with one recorded vote — the unexported
// `voters` set must survive the adapter's json.Marshal/Unmarshal snapshot.
func lpxVotedPoll(t *testing.T) *classroom.LivePoll {
	t.Helper()
	p, err := classroom.NewLivePoll(classroom.NewLivePollInput{
		TenantID:       tenantID,
		InstructorGCID: gcid,
		Question:       "How was the pace?",
		Options:        []string{"Too slow", "Just right"},
	})
	if err != nil {
		t.Fatalf("NewLivePoll: %v", err)
	}
	now := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	if err := p.Open(now); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := p.CastVote("019e2f93-d586-71b5-8c3d-e2b0d0d50320", "Just right", now.Add(time.Second)); err != nil {
		t.Fatalf("CastVote: %v", err)
	}
	return p
}

// lpxPollJSON marshals a poll the way the `data []byte` column rides it.
func lpxPollJSON(t *testing.T, p *classroom.LivePoll) []byte {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal live poll: %v", err)
	}
	return b
}

func lpxScanError(err error) func(dest ...any) error {
	return func(dest ...any) error { return err }
}

// -----------------------------------------------------------------------------
// LivePollRepo — nil-tx, Save, Get, ListByTenant
// -----------------------------------------------------------------------------

func TestLivePollRepo_NilTx_FailLoud(t *testing.T) {
	t.Parallel()
	r := pg.NewLivePollRepo(nil)
	if err := r.Save(&classroom.LivePoll{ID: "p-1"}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("nil-tx Save = %v, want ErrNotImplemented", err)
	}
	if _, ok, err := r.Get("p-1"); ok || !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("nil-tx Get = (ok=%v, err=%v), want (false, ErrNotImplemented)", ok, err)
	}
	if got := r.ListByTenant(tenantID); got != nil {
		t.Fatalf("nil-tx ListByTenant = %v, want nil", got)
	}
}

func TestLivePollRepo_Save_UpsertShapeAndArgs_NoRLS_LosslessVoters(t *testing.T) {
	t.Parallel()
	poll := lpxVotedPoll(t)
	q := &stubQuerier{}
	r := pg.NewLivePollRepo(&stubTxRunner{q: q})

	if err := r.Save(poll); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) != 1 {
		t.Fatalf("expected exactly 1 SQL (no SET LOCAL for the no-RLS adapter); got %d: %#v", len(q.sqls), q.sqls)
	}
	if strings.Contains(q.sqls[0], "SET LOCAL") {
		t.Fatalf("LivePoll adapter must NOT emit SET LOCAL; got %q", q.sqls[0])
	}
	for _, want := range []string{"INSERT INTO live_polls", "ON CONFLICT (id)", "DO UPDATE"} {
		if !strings.Contains(q.sqls[0], want) {
			t.Fatalf("upsert SQL missing %q; got %q", want, q.sqls[0])
		}
	}
	args := q.args[0]
	if len(args) != 6 {
		t.Fatalf("expected 6 bind args (id, tenant_id, state, data, created_at, updated_at); got %d: %#v", len(args), args)
	}
	if args[0] != poll.ID || args[1] != poll.TenantID || args[2] != string(classroom.LivePollStateOpen) {
		t.Fatalf("bind args[0:3] mismatch; got %#v", args[:3])
	}
	// The snapshot must round-trip the UNEXPORTED voter set: the adapter only
	// calls json.Marshal/Unmarshal, so losslessness rides LivePoll's own
	// MarshalJSON/UnmarshalJSON.
	var back classroom.LivePoll
	if err := json.Unmarshal(args[3].([]byte), &back); err != nil {
		t.Fatalf("data arg must be the marshalled snapshot: %v", err)
	}
	if back.ID != poll.ID || back.State != poll.State || back.TotalVotes() != 1 {
		t.Fatalf("snapshot arg lossy (voters dropped?); got %+v (votes=%d)", back, back.TotalVotes())
	}
	// The rehydrated set still enforces first-vote-wins.
	dupErr := back.CastVote("019e2f93-d586-71b5-8c3d-e2b0d0d50320", "Too slow", time.Now().UTC())
	if !errors.Is(dupErr, classroom.ErrLivePollDuplicateVote) {
		t.Fatalf("snapshot arg did not rehydrate voters — duplicate vote allowed: %v", dupErr)
	}
}

func TestLivePollRepo_Save_NilPoll_NoOp(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewLivePollRepo(&stubTxRunner{q: q})
	if err := r.Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("Save(nil) must not execute any SQL; got %#v", q.sqls)
	}
}

func TestLivePollRepo_Save_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New("backend down")}
	r := pg.NewLivePollRepo(&stubTxRunner{q: q})
	err := r.Save(lpxVotedPoll(t))
	if err == nil {
		t.Fatalf("expected an error on failed upsert")
	}
	if !strings.Contains(err.Error(), "pg: upsert live_poll") || !strings.Contains(err.Error(), "backend down") {
		t.Fatalf("upsert error must wrap the cause with context; got %v", err)
	}
}

func TestLivePollRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	poll := lpxVotedPoll(t)
	raw := lpxPollJSON(t, poll)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = raw
				return nil
			}}
		},
	}
	r := pg.NewLivePollRepo(&stubTxRunner{q: q})

	out, ok, err := r.Get(poll.ID)
	if err != nil || !ok || out == nil {
		t.Fatalf("Get = (ok=%v, err=%v), want a hit", ok, err)
	}
	if out.ID != poll.ID || out.State != poll.State || out.Question != poll.Question {
		t.Fatalf("Get rehydration mismatch; got %+v", out)
	}
	if len(q.sqls) != 1 || strings.Contains(q.sqls[0], "SET LOCAL") {
		t.Fatalf("Get must execute exactly the SELECT (no RLS); got %#v", q.sqls)
	}
	sql := q.sqls[0]
	if !strings.Contains(sql, "SELECT data FROM live_polls") || !strings.Contains(sql, "WHERE id = $1") || !strings.Contains(sql, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM live_polls; got %q", sql)
	}
	if len(q.args[0]) != 1 || q.args[0][0] != poll.ID {
		t.Fatalf("expected id bound as $1; got %#v", q.args)
	}
}

func TestLivePollRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lpxScanError(errors.New("no rows in result set"))}
		},
	}
	r := pg.NewLivePollRepo(&stubTxRunner{q: q})
	if out, ok, err := r.Get("nope"); ok || out != nil || err != nil {
		t.Fatalf("Get on absent id = (%+v, ok=%v, err=%v), want (nil, false, nil)", out, ok, err)
	}
}

func TestLivePollRepo_Get_ScanError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lpxScanError(errors.New("conn closed"))}
		},
	}
	r := pg.NewLivePollRepo(&stubTxRunner{q: q})
	if _, ok, err := r.Get("p-1"); ok || err == nil {
		t.Fatalf("Get hard scan error must be LOUD; got (ok=%v, err=%v)", ok, err)
	} else if !strings.Contains(err.Error(), "pg: get live_poll") || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("scan error must wrap the cause with context; got %v", err)
	}
}

func TestLivePollRepo_Get_UnmarshalError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte("{not json")
				return nil
			}}
		},
	}
	r := pg.NewLivePollRepo(&stubTxRunner{q: q})
	if _, ok, err := r.Get("p-1"); ok || err == nil {
		t.Fatalf("Get on corrupt snapshot must be LOUD; got (ok=%v, err=%v)", ok, err)
	} else if !strings.Contains(err.Error(), "pg: unmarshal live_poll") {
		t.Fatalf("unmarshal error must carry context; got %v", err)
	}
}

func TestLivePollRepo_ListByTenant_Rows(t *testing.T) {
	t.Parallel()
	a := lpxVotedPoll(t)
	b := lpxVotedPoll(t)
	rawA := lpxPollJSON(t, a)
	rawB := lpxPollJSON(t, b)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = rawA; return nil },
				func(dest ...any) error { *(dest[0].(*[]byte)) = rawB; return nil },
			}}, nil
		},
	}
	r := pg.NewLivePollRepo(&stubTxRunner{q: q})

	out := r.ListByTenant(tenantID)
	if len(out) != 2 {
		t.Fatalf("expected 2 polls; got %d", len(out))
	}
	if out[0].ID != a.ID || out[1].ID != b.ID {
		t.Fatalf("ListByTenant rehydration mismatch; got %+v", out)
	}
	if len(q.sqls) != 1 || strings.Contains(q.sqls[0], "SET LOCAL") {
		t.Fatalf("ListByTenant must execute exactly the SELECT (no RLS); got %#v", q.sqls)
	}
	sql := q.sqls[0]
	if !strings.Contains(sql, "SELECT data FROM live_polls") || !strings.Contains(sql, "tenant_id = $1") ||
		!strings.Contains(sql, "deleted_at IS NULL") || !strings.Contains(sql, "ORDER BY id") {
		t.Fatalf("expected tenant-scoped, soft-delete-filtered, ordered list; got %q", sql)
	}
	if len(q.args[0]) != 1 || q.args[0][0] != tenantID {
		t.Fatalf("expected tenant_id bound as $1; got %#v", q.args)
	}
}

func TestLivePollRepo_ListByTenant_Errors_ReturnNil(t *testing.T) {
	t.Parallel()
	{
		q := &stubQuerier{
			rowsFn: func(sql string, args ...any) (pg.Rows, error) {
				return nil, errors.New("conn closed")
			},
		}
		r := pg.NewLivePollRepo(&stubTxRunner{q: q})
		if out := r.ListByTenant(tenantID); out != nil {
			t.Fatalf("ListByTenant on query error = %v, want nil", out)
		}
	}
	{
		q := &stubQuerier{
			rowsFn: func(sql string, args ...any) (pg.Rows, error) {
				return &stubRows{rows: []func(dest ...any) error{
					lpxScanError(errors.New("bad row bytes")),
				}}, nil
			},
		}
		r := pg.NewLivePollRepo(&stubTxRunner{q: q})
		if out := r.ListByTenant(tenantID); out != nil {
			t.Fatalf("ListByTenant on scan error = %v, want nil", out)
		}
	}
}
