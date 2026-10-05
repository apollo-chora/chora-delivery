// Package pg application repo tests — S6.1.
//
// Production wiring lands at M12 with pgx; here we exercise the SQL templates
// + RLS contract via a stub Querier that asserts:
//
//  1. rls.ApplySession is called BEFORE any data query.
//  2. Bind args are wired through correctly.
//  3. Conflict-on-natural-key falls through to a SELECT.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

const (
	tenantID = "01970000-0000-7000-8000-000000000001"
	courseID = "01970000-0000-7000-8000-000000000099"
	gcid     = "01970000-0000-7000-9000-000000000001"
)

// -----------------------------------------------------------------------------
// Stub Querier — captures executed SQL for assertions
// -----------------------------------------------------------------------------

type stubRow struct {
	scanFn func(dest ...any) error
}

func (r stubRow) Scan(dest ...any) error { return r.scanFn(dest...) }

type stubRows struct {
	idx     int
	rows    []func(dest ...any) error
	closeOK bool
}

func (r *stubRows) Next() bool {
	if r.idx < len(r.rows) {
		r.idx++
		return true
	}
	return false
}
func (r *stubRows) Scan(dest ...any) error { return r.rows[r.idx-1](dest...) }
func (r *stubRows) Close() error           { r.closeOK = true; return nil }
func (r *stubRows) Err() error             { return nil }

// stubQuerier captures executed SQL + arg bindings.
type stubQuerier struct {
	sqls    []string
	args    [][]any
	rowFn   func(sql string, args ...any) pg.Row
	rowsFn  func(sql string, args ...any) (pg.Rows, error)
	execErr error
	// execErrFor fails only the Execs whose SQL matches, so a test can target the
	// upsert without also breaking the rls.ApplySession SET LOCAL that precedes
	// every query.
	execErrFor func(sql string) error
}

func (s *stubQuerier) Exec(_ context.Context, sql string, args ...any) (rls.CommandTag, error) {
	s.sqls = append(s.sqls, sql)
	s.args = append(s.args, args)
	if s.execErrFor != nil {
		if err := s.execErrFor(sql); err != nil {
			return rls.CommandTag{}, err
		}
	}
	return rls.CommandTag{RowsAffected: 1}, s.execErr
}
func (s *stubQuerier) QueryRow(_ context.Context, sql string, args ...any) pg.Row {
	s.sqls = append(s.sqls, sql)
	s.args = append(s.args, args)
	if s.rowFn == nil {
		return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows") }}
	}
	return s.rowFn(sql, args...)
}
func (s *stubQuerier) Query(_ context.Context, sql string, args ...any) (pg.Rows, error) {
	s.sqls = append(s.sqls, sql)
	s.args = append(s.args, args)
	if s.rowsFn == nil {
		return &stubRows{}, nil
	}
	return s.rowsFn(sql, args...)
}

// stubTxRunner — runs fn with a captured Querier.
type stubTxRunner struct {
	q *stubQuerier
}

func (s *stubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q pg.Querier) error) error {
	return fn(ctx, s.q)
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func TestApplicationRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewApplicationRepo(nil)
	_, _, err := r.SubmitOrGet(context.Background(), application.SubmitInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	})
	if !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented; got %v", err)
	}
}

func TestApplicationRepo_SubmitOrGet_CallsApplySessionFirst(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			// Stub: INSERT … RETURNING returns the inserted application_id.
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*string); ok {
					*ptr = args[0].(string) // application_id arg
				}
				return nil
			}}
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewApplicationRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	app, created, err := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	})
	if err != nil {
		t.Fatalf("SubmitOrGet: %v", err)
	}
	if !created {
		t.Fatalf("expected created=true on fresh insert")
	}
	if app == nil {
		t.Fatalf("nil application")
	}
	if len(q.sqls) < 3 {
		t.Fatalf("expected at least 3 SQLs (SET LOCAL + app INSERT + initial history INSERT); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	var sawAppInsert, sawHistInsert bool
	for _, s := range q.sqls {
		if strings.Contains(s, "INSERT INTO applications") {
			sawAppInsert = true
		}
		if strings.Contains(s, "INSERT INTO application_state_history") {
			sawHistInsert = true
		}
	}
	if !sawAppInsert {
		t.Fatalf("expected an INSERT INTO applications; got %#v", q.sqls)
	}
	// Created path must persist the initial Draft->Submitted row in the same tx.
	if !sawHistInsert {
		t.Fatalf("created SubmitOrGet must also insert the initial history row; got %#v", q.sqls)
	}
}

func TestApplicationRepo_SubmitOrGet_ConflictPath_FallsThrough(t *testing.T) {
	t.Parallel()
	// Stub: INSERT returns no rows (ON CONFLICT DO NOTHING). Lookup-by-natural-key
	// then returns an existing application_id, and Get returns the row.
	calls := 0
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			calls++
			switch {
			case strings.Contains(sql, "INSERT INTO applications"):
				return stubRow{scanFn: func(dest ...any) error {
					return errors.New("no rows in result set")
				}}
			case strings.Contains(sql, "WHERE tenant_id") && strings.Contains(sql, "course_id"):
				return stubRow{scanFn: func(dest ...any) error {
					if ptr, ok := dest[0].(*string); ok {
						*ptr = "existing-app-id"
					}
					return nil
				}}
			case strings.Contains(sql, "SELECT") && strings.Contains(sql, "application_id"):
				return stubRow{scanFn: func(dest ...any) error {
					// Populate the 19 scan dests (id, tenant_id, course_id, …).
					if v, ok := dest[0].(*string); ok {
						*v = "existing-app-id"
					}
					if v, ok := dest[1].(*string); ok {
						*v = tenantID
					}
					if v, ok := dest[2].(*string); ok {
						*v = courseID
					}
					if v, ok := dest[4].(*string); ok {
						*v = gcid
					}
					if v, ok := dest[5].(*string); ok {
						*v = "submitted"
					}
					return nil
				}}
			}
			return stubRow{scanFn: func(dest ...any) error { return errors.New("unmatched stub") }}
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewApplicationRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	app, created, err := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	})
	if err != nil {
		t.Fatalf("conflict path: %v", err)
	}
	if created {
		t.Fatalf("expected created=false on conflict")
	}
	if app == nil || app.ID != "existing-app-id" {
		t.Fatalf("expected fall-through to load existing-app-id; got %+v", app)
	}
}

func TestApplicationRepo_Save_AppliesSessionThenUpdatesAndAppendsHistory(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewApplicationRepo(tx)

	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	})
	_ = app.Transition(application.StatusSubmitted)
	_ = app.Transition(application.StatusUnderReview)

	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, app); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// First SQL should be SET LOCAL chora.tenant_id (RLS); then UPDATE; then
	// 2× INSERT INTO application_state_history (one per transition).
	if len(q.sqls) < 4 {
		t.Fatalf("expected at least 4 SQLs (SET LOCAL + UPDATE + 2× history INSERT); got %d", len(q.sqls))
	}
	updateFound := false
	historyCount := 0
	for _, s := range q.sqls {
		if strings.Contains(s, "UPDATE applications SET") {
			updateFound = true
		}
		if strings.Contains(s, "INSERT INTO application_state_history") {
			historyCount++
		}
	}
	if !updateFound {
		t.Fatalf("expected UPDATE applications statement")
	}
	if historyCount != 2 {
		t.Fatalf("expected 2 history INSERTs; got %d", historyCount)
	}
}

func TestApplicationRepo_Get_NoRow_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewApplicationRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	app, ok, err := r.Get(ctx, tenantID, "nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok || app != nil {
		t.Fatalf("expected ok=false / nil for unknown id")
	}
}

func TestApplicationRepo_ListByGCID_ReturnsRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{
				rows: []func(dest ...any) error{
					func(dest ...any) error {
						if v, ok := dest[0].(*string); ok {
							*v = "app-1"
						}
						if v, ok := dest[1].(*string); ok {
							*v = tenantID
						}
						if v, ok := dest[2].(*string); ok {
							*v = courseID
						}
						if v, ok := dest[4].(*string); ok {
							*v = gcid
						}
						if v, ok := dest[5].(*string); ok {
							*v = "submitted"
						}
						return nil
					},
				},
			}, nil
		},
	}
	tx := &stubTxRunner{q: q}
	r := pg.NewApplicationRepo(tx)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	items, total, err := r.ListByGCID(ctx, tenantID, gcid, 0, 10)
	if err != nil {
		t.Fatalf("ListByGCID: %v", err)
	}
	if len(items) != 1 || total != 1 {
		t.Fatalf("expected 1 item; got %d / %d", len(items), total)
	}
}

func TestApplicationRepo_ListByTenant_NoFilter_AppliesRLSThenCountThenList(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			// The only QueryRow in ListByTenant is the COUNT.
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*int); ok {
					*ptr = 3
				}
				return nil
			}}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					if v, ok := dest[0].(*string); ok {
						*v = "app-1"
					}
					if v, ok := dest[1].(*string); ok {
						*v = tenantID
					}
					if v, ok := dest[2].(*string); ok {
						*v = courseID
					}
					if v, ok := dest[4].(*string); ok {
						*v = gcid
					}
					if v, ok := dest[5].(*string); ok {
						*v = "submitted"
					}
					return nil
				},
			}}, nil
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	items, total, err := r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantID, Offset: 0, Limit: 50})
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected total=3 (COUNT); got %d", total)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 page row; got %d", len(items))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	// Should NOT bind a status filter on the no-filter path.
	for _, s := range q.sqls {
		if strings.Contains(s, "status = $2") {
			t.Fatalf("no-filter ListByTenant must not bind status; got %q", s)
		}
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "ORDER BY application_id DESC") {
		t.Fatalf("expected newest-first list; got %q", last)
	}
}

func TestApplicationRepo_ListByTenant_StatusFilter_BindsStatus(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*int); ok {
					*ptr = 0
				}
				return nil
			}}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, _, err := r.ListByTenant(ctx, application.ListByTenantInput{
		TenantID: tenantID, Status: application.StatusUnderReview, Offset: 0, Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	foundStatusList := false
	for _, s := range q.sqls {
		if strings.Contains(s, "status = $2") {
			foundStatusList = true
		}
	}
	if !foundStatusList {
		t.Fatalf("status-filtered ListByTenant must bind status = $2; got %#v", q.sqls)
	}
	// The status arg must be the under_review domain value.
	sawStatusArg := false
	for _, a := range q.args {
		for _, v := range a {
			if v == string(application.StatusUnderReview) {
				sawStatusArg = true
			}
		}
	}
	if !sawStatusArg {
		t.Fatalf("expected under_review bound as a query arg; got %#v", q.args)
	}
}

func TestApplicationRepo_ListByTenant_NilTx(t *testing.T) {
	t.Parallel()
	r := pg.NewApplicationRepo(nil)
	if _, _, err := r.ListByTenant(context.Background(), application.ListByTenantInput{TenantID: tenantID}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented on nil-tx; got %v", err)
	}
}

// CHO-2337: ListByTenant errors MUST carry which step failed so the admin
// handler's 500 log names the failing operation (COUNT vs list vs row-scan),
// which is what makes an intermittent 500 diagnosable. The underlying cause
// must be preserved (wrapped with %w), not replaced.

func TestApplicationRepo_ListByTenant_CountError_WrappedWithContext(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			// The only QueryRow in ListByTenant is the COUNT, so fail it.
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("conn closed")
			}}
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, _, err := r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantID, Limit: 50})
	if err == nil {
		t.Fatalf("expected an error when COUNT fails")
	}
	if !strings.Contains(err.Error(), "count applications") {
		t.Fatalf("COUNT error must be wrapped with context; got %v", err)
	}
	if !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("wrapped error must preserve the underlying cause; got %v", err)
	}
}

func TestApplicationRepo_ListByTenant_RowScanError_WrappedWithContext(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			// COUNT succeeds so we reach the page-row scan.
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*int); ok {
					*ptr = 1
				}
				return nil
			}}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	_, _, err := r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantID, Limit: 50})
	if err == nil {
		t.Fatalf("expected an error when a page row fails to scan")
	}
	if !strings.Contains(err.Error(), "scan application") {
		t.Fatalf("row-scan error must be wrapped with context; got %v", err)
	}
	if !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("wrapped error must preserve the underlying cause; got %v", err)
	}
}

// SQL constants reachable from outside — surface them so consumers (Cloud Build
// CI lint) can grep without breaking encapsulation.
func TestSQLTemplates_AreExported(t *testing.T) {
	t.Parallel()
	if !strings.Contains(pg.SQLInsertApplication, "INSERT INTO applications") {
		t.Fatalf("InsertApplication SQL malformed")
	}
	if !strings.Contains(pg.SQLUpdateApplicationStatus, "UPDATE applications") {
		t.Fatalf("UpdateApplicationStatus SQL malformed")
	}
	if !strings.Contains(pg.SQLInsertHistoryEntry, "application_state_history") {
		t.Fatalf("InsertHistoryEntry SQL malformed")
	}
}

// byIDAppRow stubs the SQLSelectApplicationByID scan (19 cols), seeding the
// key fields + a caller-chosen status so Get can rehydrate + transition.
func byIDAppRow(status string) func(dest ...any) error {
	return func(dest ...any) error {
		if v, ok := dest[0].(*string); ok {
			*v = "app-1"
		}
		if v, ok := dest[1].(*string); ok {
			*v = tenantID
		}
		if v, ok := dest[2].(*string); ok {
			*v = courseID
		}
		if v, ok := dest[4].(*string); ok {
			*v = gcid
		}
		if v, ok := dest[5].(*string); ok {
			*v = status
		}
		return nil
	}
}

// twoHistoryRows stubs SQLListHistoryByApplication returning 2 transitions.
func twoHistoryRows() *stubRows {
	mk := func(from, to string) func(dest ...any) error {
		return func(dest ...any) error {
			if v, ok := dest[0].(*string); ok {
				*v = from
			}
			if v, ok := dest[1].(*string); ok {
				*v = to
			}
			// dest[2] is reason (**string) — leave nil; dest[3] is *time.Time.
			if v, ok := dest[3].(*time.Time); ok {
				*v = time.Now().UTC()
			}
			return nil
		}
	}
	return &stubRows{rows: []func(dest ...any) error{
		mk("draft", "submitted"),
		mk("submitted", "under_review"),
	}}
}

func TestApplicationRepo_Get_RehydratesHistory(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: byIDAppRow("offer_made")}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "application_state_history") {
				return twoHistoryRows(), nil
			}
			return &stubRows{}, nil
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	app, ok, err := r.Get(ctx, tenantID, "app-1")
	if err != nil || !ok || app == nil {
		t.Fatalf("Get: ok=%v err=%v app=%v", ok, err, app)
	}
	if got := app.History(); len(got) != 2 {
		t.Fatalf("expected rehydrated history len 2; got %d", len(got))
	}
	// Rehydrated rows are durable → nothing pending.
	if got := app.PendingHistory(); len(got) != 0 {
		t.Fatalf("expected 0 pending after rehydrate; got %d", len(got))
	}
	// A history SELECT must have run.
	sawHistSelect := false
	for _, s := range q.sqls {
		if strings.Contains(s, "FROM application_state_history") {
			sawHistSelect = true
		}
	}
	if !sawHistSelect {
		t.Fatalf("Get must SELECT application_state_history; got %#v", q.sqls)
	}
}

func TestApplicationRepo_GetThenSave_InsertsOnlyNewTransition(t *testing.T) {
	t.Parallel()
	getQ := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: byIDAppRow("offer_made")}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "application_state_history") {
				return twoHistoryRows(), nil
			}
			return &stubRows{}, nil
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: getQ})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	app, ok, err := r.Get(ctx, tenantID, "app-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	// One new transition on top of the rehydrated (persisted) trail.
	if err := app.Transition(application.StatusAccepted); err != nil {
		t.Fatalf("transition Accepted: %v", err)
	}

	// Save with a FRESH querier so we count only Save's history inserts.
	saveQ := &stubQuerier{}
	if err := pg.NewApplicationRepo(&stubTxRunner{q: saveQ}).Save(ctx, app); err != nil {
		t.Fatalf("Save: %v", err)
	}
	histInserts := 0
	for _, s := range saveQ.sqls {
		if strings.Contains(s, "INSERT INTO application_state_history") {
			histInserts++
		}
	}
	if histInserts != 1 {
		t.Fatalf("Save must insert ONLY the 1 new transition (not re-insert the 2 rehydrated); got %d", histInserts)
	}
	// After a successful Save the new row is marked persisted.
	if got := app.PendingHistory(); len(got) != 0 {
		t.Fatalf("expected 0 pending after Save; got %d", len(got))
	}
}
