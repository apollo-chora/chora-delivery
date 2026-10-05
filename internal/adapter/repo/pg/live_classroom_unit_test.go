// live_classroom_unit_test.go — unit tests for pg.LiveQuizRepo +
// pg.ClassroomSessionRepo (ADR-168 gap-1 Live Classroom), from OUTSIDE the
// package. Stubs the TxRunner (shared stubQuerier / stubTxRunner / stubRow /
// stubRows + tenantID / gcid consts from application_test.go) so the SQL
// surface + fail-loud guarantees are exercised without a live DB:
//
//  1. nil TxRunner is a WIRING BUG — Save/Get/GetByJoinCode/Mutate return
//     ErrNotImplemented, ListByTenant returns nil (CHO-2184).
//  2. SQL shape: idempotent upsert (ON CONFLICT (id) DO UPDATE), SELECT data
//     ... deleted_at IS NULL, join-code resolve restricted to ACTIVE sessions
//     (ARMED|LIVE), SELECT … FOR UPDATE + upsert inside Mutate's tx.
//  3. NO RLS (see live_classroom.go + migrations/0017_live_classroom.up.sql):
//     these adapters do NOT call rls.ApplySession — the first SQL emitted is
//     the data query itself, never a SET LOCAL.
//  4. The JSONB snapshot rides a single `data []byte` dest per query; fillers
//     write it in order with the marshalled aggregate.
//  5. Error wraps ("pg: upsert live_quiz", "pg: get live_quiz_session by join
//     code", ...) preserve the underlying cause via %w; Mutate maps ANY scan
//     failure of the FOR-UPDATE row to classroom.ErrSessionNotFound.
//
// Real upsert/JSONB round-trip against Cloud SQL is the DSN-gated
// integration_test.go; the JSONB losslessness itself is
// live_classroom_roundtrip_test.go.
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
// lcx helpers — fresh aggregates + JSONB `data []byte` row fillers
// -----------------------------------------------------------------------------

// lcxQuiz builds a PUBLISHED LiveQuiz with stable timestamps.
func lcxQuiz(t *testing.T, id string) *classroom.LiveQuiz {
	t.Helper()
	now := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	return &classroom.LiveQuiz{
		ID:        id,
		TenantID:  tenantID,
		Title:     "Scrum Basics",
		State:     classroom.LiveQuizStatePublished,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// lcxSession builds a LIVE LiveQuizSession with stable timestamps.
func lcxSession(t *testing.T, id string) *classroom.LiveQuizSession {
	t.Helper()
	now := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	return &classroom.LiveQuizSession{
		ID:             id,
		LiveQuizID:     "019e72cc-6108-77a1-a129-4b5fb3a56c11",
		TenantID:       tenantID,
		InstructorGCID: gcid,
		State:          classroom.LiveQuizSessionStateLive,
		JoinCode:       "ABCDEF",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// lcxFillJSON writes the marshalled aggregate into the single `data []byte`
// dest every live-classroom query scans (Get / GetByJoinCode / Mutate /
// ListByTenant all scan exactly one column).
func lcxFillJSON(t *testing.T, v any) func(dest ...any) error {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return func(dest ...any) error {
		*(dest[0].(*[]byte)) = b
		return nil
	}
}

// lcxScanError is a row filler that fails the scan with err.
func lcxScanError(err error) func(dest ...any) error {
	return func(dest ...any) error { return err }
}

// -----------------------------------------------------------------------------
// LiveQuizRepo — nil-tx, Save, Get, ListByTenant
// -----------------------------------------------------------------------------

func TestLiveQuizRepo_NilTx_FailLoud(t *testing.T) {
	t.Parallel()
	r := pg.NewLiveQuizRepo(nil)
	if err := r.Save(&classroom.LiveQuiz{ID: "q-1"}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("nil-tx Save = %v, want ErrNotImplemented", err)
	}
	if _, ok, err := r.Get("q-1"); ok || !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("nil-tx Get = (ok=%v, err=%v), want (false, ErrNotImplemented)", ok, err)
	}
	if got := r.ListByTenant(tenantID); got != nil {
		t.Fatalf("nil-tx ListByTenant = %v, want nil", got)
	}
}

func TestLiveQuizRepo_Save_UpsertShapeAndArgs_NoRLS(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})
	quiz := lcxQuiz(t, "019e72cc-6108-77a1-a129-4b5fb3a56c11")

	if err := r.Save(quiz); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) != 1 {
		t.Fatalf("expected exactly 1 SQL (no SET LOCAL for the no-RLS adapter); got %d: %#v", len(q.sqls), q.sqls)
	}
	if strings.Contains(q.sqls[0], "SET LOCAL") {
		t.Fatalf("LiveQuiz adapter must NOT emit SET LOCAL (tenant isolation is handler-enforced); got %q", q.sqls[0])
	}
	for _, want := range []string{"INSERT INTO live_quizzes", "ON CONFLICT (id)", "DO UPDATE"} {
		if !strings.Contains(q.sqls[0], want) {
			t.Fatalf("upsert SQL missing %q; got %q", want, q.sqls[0])
		}
	}
	args := q.args[0]
	if len(args) != 6 {
		t.Fatalf("expected 6 bind args (id, tenant_id, state, data, created_at, updated_at); got %d: %#v", len(args), args)
	}
	if args[0] != quiz.ID || args[1] != quiz.TenantID || args[2] != string(classroom.LiveQuizStatePublished) {
		t.Fatalf("bind args[0:3] mismatch; got %#v", args[:3])
	}
	var back classroom.LiveQuiz
	if err := json.Unmarshal(args[3].([]byte), &back); err != nil {
		t.Fatalf("data arg must be the marshalled snapshot: %v", err)
	}
	if back.ID != quiz.ID || back.State != quiz.State || back.Title != quiz.Title {
		t.Fatalf("snapshot arg lossy; got %+v", back)
	}
}

func TestLiveQuizRepo_Save_NilQuiz_NoOp(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})
	if err := r.Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("Save(nil) must not execute any SQL; got %#v", q.sqls)
	}
}

func TestLiveQuizRepo_Save_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New("backend down")}
	r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})
	err := r.Save(lcxQuiz(t, "q-1"))
	if err == nil {
		t.Fatalf("expected an error on failed upsert")
	}
	if !strings.Contains(err.Error(), "pg: upsert live_quiz") || !strings.Contains(err.Error(), "backend down") {
		t.Fatalf("upsert error must wrap the cause with context; got %v", err)
	}
}

func TestLiveQuizRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	quiz := lcxQuiz(t, "q-1")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxFillJSON(t, quiz)}
		},
	}
	r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})

	out, ok, err := r.Get("q-1")
	if err != nil || !ok || out == nil {
		t.Fatalf("Get = (ok=%v, err=%v), want a hit", ok, err)
	}
	if out.ID != quiz.ID || out.State != quiz.State || out.Title != quiz.Title {
		t.Fatalf("Get rehydration mismatch; got %+v", out)
	}
	if len(q.sqls) != 1 || strings.Contains(q.sqls[0], "SET LOCAL") {
		t.Fatalf("Get must execute exactly the SELECT (no RLS); got %#v", q.sqls)
	}
	sql := q.sqls[0]
	if !strings.Contains(sql, "SELECT data FROM live_quizzes") || !strings.Contains(sql, "WHERE id = $1") || !strings.Contains(sql, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM live_quizzes; got %q", sql)
	}
	if len(q.args[0]) != 1 || q.args[0][0] != "q-1" {
		t.Fatalf("expected id bound as $1; got %#v", q.args)
	}
}

func TestLiveQuizRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxScanError(errors.New("no rows in result set"))}
		},
	}
	r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})

	if out, ok, err := r.Get("nope"); ok || out != nil || err != nil {
		t.Fatalf("Get on absent id = (%+v, ok=%v, err=%v), want (nil, false, nil)", out, ok, err)
	}
}

func TestLiveQuizRepo_Get_ScanError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxScanError(errors.New("conn closed"))}
		},
	}
	r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})

	if _, ok, err := r.Get("q-1"); ok || err == nil {
		t.Fatalf("Get hard scan error must be LOUD; got (ok=%v, err=%v)", ok, err)
	} else if !strings.Contains(err.Error(), "pg: get live_quiz") || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("scan error must wrap the cause with context; got %v", err)
	}
}

func TestLiveQuizRepo_Get_UnmarshalError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte("{not json")
				return nil
			}}
		},
	}
	r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})

	if _, ok, err := r.Get("q-1"); ok || err == nil {
		t.Fatalf("Get on corrupt snapshot must be LOUD; got (ok=%v, err=%v)", ok, err)
	} else if !strings.Contains(err.Error(), "pg: unmarshal live_quiz") {
		t.Fatalf("unmarshal error must carry context; got %v", err)
	}
}

func TestLiveQuizRepo_ListByTenant_Rows(t *testing.T) {
	t.Parallel()
	a := lcxQuiz(t, "019e72cc-6108-77a1-a129-4b5fb3a56caa")
	b := lcxQuiz(t, "019e72cc-6108-77a1-a129-4b5fb3a56cbb")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				lcxFillJSON(t, a), lcxFillJSON(t, b),
			}}, nil
		},
	}
	r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})

	out := r.ListByTenant(tenantID)
	if len(out) != 2 {
		t.Fatalf("expected 2 quizzes; got %d", len(out))
	}
	if out[0].ID != a.ID || out[1].ID != b.ID {
		t.Fatalf("ListByTenant rehydration mismatch; got %+v", out)
	}
	if len(q.sqls) != 1 || strings.Contains(q.sqls[0], "SET LOCAL") {
		t.Fatalf("ListByTenant must execute exactly the SELECT (no RLS); got %#v", q.sqls)
	}
	sql := q.sqls[0]
	if !strings.Contains(sql, "SELECT data FROM live_quizzes") || !strings.Contains(sql, "tenant_id = $1") ||
		!strings.Contains(sql, "deleted_at IS NULL") || !strings.Contains(sql, "ORDER BY id") {
		t.Fatalf("expected tenant-scoped, soft-delete-filtered, ordered list; got %q", sql)
	}
	if len(q.args[0]) != 1 || q.args[0][0] != tenantID {
		t.Fatalf("expected tenant_id bound as $1; got %#v", q.args)
	}
}

func TestLiveQuizRepo_ListByTenant_QueryError_ReturnsNil(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("conn closed")
		},
	}
	r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})
	if out := r.ListByTenant(tenantID); out != nil {
		t.Fatalf("ListByTenant on query error = %v, want nil (errors are swallowed by design)", out)
	}
}

func TestLiveQuizRepo_ListByTenant_ScanAndParseErrors_ReturnPartial(t *testing.T) {
	t.Parallel()
	{
		// Scan failure on the first row → nothing accumulated.
		q := &stubQuerier{
			rowsFn: func(sql string, args ...any) (pg.Rows, error) {
				return &stubRows{rows: []func(dest ...any) error{
					lcxScanError(errors.New("bad row bytes")),
				}}, nil
			},
		}
		r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})
		if out := r.ListByTenant(tenantID); out != nil {
			t.Fatalf("ListByTenant on scan error = %v, want nil", out)
		}
	}
	{
		// Unmarshal failure on the first row → nothing accumulated.
		q := &stubQuerier{
			rowsFn: func(sql string, args ...any) (pg.Rows, error) {
				return &stubRows{rows: []func(dest ...any) error{
					func(dest ...any) error {
						*(dest[0].(*[]byte)) = []byte("{bad")
						return nil
					},
				}}, nil
			},
		}
		r := pg.NewLiveQuizRepo(&stubTxRunner{q: q})
		if out := r.ListByTenant(tenantID); out != nil {
			t.Fatalf("ListByTenant on unmarshal error = %v, want nil", out)
		}
	}
}

// -----------------------------------------------------------------------------
// ClassroomSessionRepo — nil-tx, Save, Get, GetByJoinCode, Mutate, ListByTenant
// -----------------------------------------------------------------------------

func TestClassroomSessionRepo_NilTx_FailLoud(t *testing.T) {
	t.Parallel()
	r := pg.NewClassroomSessionRepo(nil)
	if err := r.Save(&classroom.LiveQuizSession{ID: "s-1"}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("nil-tx Save = %v, want ErrNotImplemented", err)
	}
	if _, ok, err := r.Get("s-1"); ok || !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("nil-tx Get = (ok=%v, err=%v), want (false, ErrNotImplemented)", ok, err)
	}
	if _, ok, err := r.GetByJoinCode(tenantID, "ABCDEF"); ok || !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("nil-tx GetByJoinCode = (ok=%v, err=%v), want (false, ErrNotImplemented)", ok, err)
	}
	if err := r.Mutate("s-1", func(*classroom.LiveQuizSession) error { return nil }); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("nil-tx Mutate = %v, want ErrNotImplemented", err)
	}
	if got := r.ListByTenant(tenantID); got != nil {
		t.Fatalf("nil-tx ListByTenant = %v, want nil", got)
	}
}

func TestClassroomSessionRepo_Save_UpsertShapeAndArgs_NoRLS(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	s := lcxSession(t, "019e72d1-dede-7aaf-93d9-14040876f4ff")

	if err := r.Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) != 1 {
		t.Fatalf("expected exactly 1 SQL (no SET LOCAL for the no-RLS adapter); got %d: %#v", len(q.sqls), q.sqls)
	}
	if strings.Contains(q.sqls[0], "SET LOCAL") {
		t.Fatalf("Session adapter must NOT emit SET LOCAL; got %q", q.sqls[0])
	}
	for _, want := range []string{"INSERT INTO live_quiz_sessions", "ON CONFLICT (id)", "DO UPDATE"} {
		if !strings.Contains(q.sqls[0], want) {
			t.Fatalf("upsert SQL missing %q; got %q", want, q.sqls[0])
		}
	}
	args := q.args[0]
	if len(args) != 7 {
		t.Fatalf("expected 7 bind args (id, tenant_id, live_quiz_id, state, data, created_at, updated_at); got %d: %#v", len(args), args)
	}
	if args[0] != s.ID || args[1] != s.TenantID || args[2] != s.LiveQuizID || args[3] != string(classroom.LiveQuizSessionStateLive) {
		t.Fatalf("bind args[0:4] mismatch; got %#v", args[:4])
	}
	var back classroom.LiveQuizSession
	if err := json.Unmarshal(args[4].([]byte), &back); err != nil {
		t.Fatalf("data arg must be the marshalled snapshot: %v", err)
	}
	if back.ID != s.ID || back.JoinCode != "ABCDEF" {
		t.Fatalf("snapshot arg lossy; got %+v", back)
	}
}

func TestClassroomSessionRepo_Save_NilSession_NoOp(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	if err := r.Save(nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("Save(nil) must not execute any SQL; got %#v", q.sqls)
	}
}

func TestClassroomSessionRepo_Save_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErr: errors.New("backend down")}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	err := r.Save(lcxSession(t, "s-1"))
	if err == nil {
		t.Fatalf("expected an error on failed upsert")
	}
	if !strings.Contains(err.Error(), "pg: upsert live_quiz_session") || !strings.Contains(err.Error(), "backend down") {
		t.Fatalf("upsert error must wrap the cause with context; got %v", err)
	}
}

func TestClassroomSessionRepo_Get_Hit(t *testing.T) {
	t.Parallel()
	s := lcxSession(t, "s-1")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxFillJSON(t, s)}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})

	out, ok, err := r.Get("s-1")
	if err != nil || !ok || out == nil {
		t.Fatalf("Get = (ok=%v, err=%v), want a hit", ok, err)
	}
	if out.ID != s.ID || out.State != s.State || out.JoinCode != "ABCDEF" {
		t.Fatalf("Get rehydration mismatch; got %+v", out)
	}
	sql := q.sqls[0]
	if !strings.Contains(sql, "SELECT data FROM live_quiz_sessions") || !strings.Contains(sql, "WHERE id = $1") || !strings.Contains(sql, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM live_quiz_sessions; got %q", sql)
	}
	if len(q.args[0]) != 1 || q.args[0][0] != "s-1" {
		t.Fatalf("expected id bound as $1; got %#v", q.args)
	}
}

func TestClassroomSessionRepo_Get_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxScanError(errors.New("no rows in result set"))}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	if out, ok, err := r.Get("nope"); ok || out != nil || err != nil {
		t.Fatalf("Get on absent id = (%+v, ok=%v, err=%v), want (nil, false, nil)", out, ok, err)
	}
}

func TestClassroomSessionRepo_Get_UnmarshalError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte("{bad")
				return nil
			}}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	if _, ok, err := r.Get("s-1"); ok || err == nil {
		t.Fatalf("Get on corrupt snapshot must be LOUD; got (ok=%v, err=%v)", ok, err)
	} else if !strings.Contains(err.Error(), "pg: unmarshal live_quiz_session") {
		t.Fatalf("unmarshal error must carry context; got %v", err)
	}
}

func TestClassroomSessionRepo_GetByJoinCode_Hit(t *testing.T) {
	t.Parallel()
	s := lcxSession(t, "s-1")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxFillJSON(t, s)}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})

	out, ok, err := r.GetByJoinCode(tenantID, "ABCDEF")
	if err != nil || !ok || out == nil {
		t.Fatalf("GetByJoinCode = (ok=%v, err=%v), want a hit", ok, err)
	}
	if out.ID != s.ID || out.JoinCode != "ABCDEF" {
		t.Fatalf("GetByJoinCode rehydration mismatch; got %+v", out)
	}
	sql := q.sqls[0]
	for _, want := range []string{
		"SELECT data FROM live_quiz_sessions",
		"WHERE tenant_id = $1",
		"data->>'join_code' = $2",
		"state IN ('ARMED','LIVE')",
		"deleted_at IS NULL",
		"ORDER BY created_at DESC",
		"LIMIT 1",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("join-code resolve SQL missing %q; got %q", want, sql)
		}
	}
	if len(q.args[0]) != 2 || q.args[0][0] != tenantID || q.args[0][1] != "ABCDEF" {
		t.Fatalf("expected (tenant_id, join_code) bound as $1/$2; got %#v", q.args)
	}
}

func TestClassroomSessionRepo_GetByJoinCode_EmptyCode_IsMiss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})

	out, ok, err := r.GetByJoinCode(tenantID, "")
	if out != nil || ok || err != nil {
		t.Fatalf("empty join code must be a clean miss; got (%+v, ok=%v, err=%v)", out, ok, err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("empty join code must not hit the DB; got %#v", q.sqls)
	}
}

func TestClassroomSessionRepo_GetByJoinCode_Miss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxScanError(errors.New("no rows in result set"))}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	if out, ok, err := r.GetByJoinCode(tenantID, "ZZZZZZ"); ok || out != nil || err != nil {
		t.Fatalf("miss must be (nil, false, nil); got (%+v, ok=%v, err=%v)", out, ok, err)
	}
}

func TestClassroomSessionRepo_GetByJoinCode_UnmarshalError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte("{bad")
				return nil
			}}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	if _, ok, err := r.GetByJoinCode(tenantID, "ABCDEF"); ok || err == nil {
		t.Fatalf("join-code resolve on corrupt snapshot must be LOUD; got (ok=%v, err=%v)", ok, err)
	} else if !strings.Contains(err.Error(), "pg: unmarshal live_quiz_session") {
		t.Fatalf("unmarshal error must carry context; got %v", err)
	}
}

func TestClassroomSessionRepo_GetByJoinCode_ScanError_Loud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxScanError(errors.New("conn closed"))}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	if _, ok, err := r.GetByJoinCode(tenantID, "ABCDEF"); ok || err == nil {
		t.Fatalf("join-code scan error must be LOUD; got (ok=%v, err=%v)", ok, err)
	} else if !strings.Contains(err.Error(), "pg: get live_quiz_session by join code") || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("join-code error must wrap the cause with context; got %v", err)
	}
}

func TestClassroomSessionRepo_Mutate_Success_FORUPDATEThenUpsert(t *testing.T) {
	t.Parallel()
	s := lcxSession(t, "s-1")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxFillJSON(t, s)}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})

	err := r.Mutate("s-1", func(cur *classroom.LiveQuizSession) error {
		cur.State = classroom.LiveQuizSessionStateClosed
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(q.sqls) != 2 {
		t.Fatalf("expected 2 SQLs (FOR UPDATE + upsert); got %d: %#v", len(q.sqls), q.sqls)
	}
	if !strings.Contains(q.sqls[0], "SELECT data FROM live_quiz_sessions") || !strings.Contains(q.sqls[0], "FOR UPDATE") {
		t.Fatalf("Mutate must load under SELECT … FOR UPDATE; got %q", q.sqls[0])
	}
	if len(q.args[0]) != 1 || q.args[0][0] != "s-1" {
		t.Fatalf("expected id bound on the FOR-UPDATE select; got %#v", q.args)
	}
	if !strings.Contains(q.sqls[1], "INSERT INTO live_quiz_sessions") || !strings.Contains(q.sqls[1], "ON CONFLICT (id)") {
		t.Fatalf("Mutate must re-persist via the idempotent upsert; got %q", q.sqls[1])
	}
	upsertArgs := q.args[1]
	if len(upsertArgs) != 7 {
		t.Fatalf("expected 7 bind args on the upsert; got %d: %#v", len(upsertArgs), upsertArgs)
	}
	var back classroom.LiveQuizSession
	if err := json.Unmarshal(upsertArgs[4].([]byte), &back); err != nil {
		t.Fatalf("upsert data arg must be the mutated snapshot: %v", err)
	}
	if back.State != classroom.LiveQuizSessionStateClosed {
		t.Fatalf("mutated state must be persisted; got %+v", back)
	}
}

func TestClassroomSessionRepo_Mutate_AnyScanError_IsSessionNotFound(t *testing.T) {
	t.Parallel()
	for _, scanErr := range []error{
		errors.New("no rows in result set"), // genuine miss
		errors.New("conn closed"),           // hard infra failure — still mapped (by design)
	} {
		q := &stubQuerier{
			rowFn: func(sql string, args ...any) pg.Row {
				return stubRow{scanFn: lcxScanError(scanErr)}
			},
		}
		r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
		err := r.Mutate("s-1", func(*classroom.LiveQuizSession) error { return nil })
		if !errors.Is(err, classroom.ErrSessionNotFound) {
			t.Fatalf("Mutate scan failure (%v) must map to ErrSessionNotFound; got %v", scanErr, err)
		}
		if len(q.sqls) != 1 {
			t.Fatalf("no upsert may run when the row is missing; got %#v", q.sqls)
		}
	}
}

func TestClassroomSessionRepo_Mutate_FnError_AbortsBeforeUpsert(t *testing.T) {
	t.Parallel()
	s := lcxSession(t, "s-1")
	sentinel := errors.New("fn exploded")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxFillJSON(t, s)}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})

	err := r.Mutate("s-1", func(*classroom.LiveQuizSession) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("Mutate must propagate the fn error verbatim; got %v", err)
	}
	if len(q.sqls) != 1 {
		t.Fatalf("fn error must abort before the upsert; got %d SQLs: %#v", len(q.sqls), q.sqls)
	}
}

func TestClassroomSessionRepo_Mutate_UnmarshalError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte("{bad")
				return nil
			}}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	err := r.Mutate("s-1", func(*classroom.LiveQuizSession) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "pg: unmarshal live_quiz_session") {
		t.Fatalf("Mutate snapshot unmarshal error must carry context; got %v", err)
	}
}

func TestClassroomSessionRepo_Mutate_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	s := lcxSession(t, "s-1")
	q := &stubQuerier{
		execErr: errors.New("backend down"),
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: lcxFillJSON(t, s)}
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
	err := r.Mutate("s-1", func(*classroom.LiveQuizSession) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "pg: upsert live_quiz_session") || !strings.Contains(err.Error(), "backend down") {
		t.Fatalf("Mutate upsert error must wrap the cause with context; got %v", err)
	}
}

func TestClassroomSessionRepo_ListByTenant_Rows(t *testing.T) {
	t.Parallel()
	a := lcxSession(t, "019e72d1-dede-7aaf-93d9-14040876f4aa")
	b := lcxSession(t, "019e72d1-dede-7aaf-93d9-14040876f4bb")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				lcxFillJSON(t, a), lcxFillJSON(t, b),
			}}, nil
		},
	}
	r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})

	out := r.ListByTenant(tenantID)
	if len(out) != 2 {
		t.Fatalf("expected 2 sessions; got %d", len(out))
	}
	if out[0].ID != a.ID || out[1].ID != b.ID {
		t.Fatalf("ListByTenant rehydration mismatch; got %+v", out)
	}
	sql := q.sqls[0]
	if !strings.Contains(sql, "SELECT data FROM live_quiz_sessions") || !strings.Contains(sql, "tenant_id = $1") ||
		!strings.Contains(sql, "deleted_at IS NULL") || !strings.Contains(sql, "ORDER BY id") {
		t.Fatalf("expected tenant-scoped, soft-delete-filtered, ordered list; got %q", sql)
	}
	if len(q.args[0]) != 1 || q.args[0][0] != tenantID {
		t.Fatalf("expected tenant_id bound as $1; got %#v", q.args)
	}
}

func TestClassroomSessionRepo_ListByTenant_Errors_ReturnNil(t *testing.T) {
	t.Parallel()
	{
		q := &stubQuerier{
			rowsFn: func(sql string, args ...any) (pg.Rows, error) {
				return nil, errors.New("conn closed")
			},
		}
		r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
		if out := r.ListByTenant(tenantID); out != nil {
			t.Fatalf("ListByTenant on query error = %v, want nil", out)
		}
	}
	{
		q := &stubQuerier{
			rowsFn: func(sql string, args ...any) (pg.Rows, error) {
				return &stubRows{rows: []func(dest ...any) error{
					lcxScanError(errors.New("bad row bytes")),
				}}, nil
			},
		}
		r := pg.NewClassroomSessionRepo(&stubTxRunner{q: q})
		if out := r.ListByTenant(tenantID); out != nil {
			t.Fatalf("ListByTenant on scan error = %v, want nil", out)
		}
	}
}
