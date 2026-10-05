// application_extra_test.go — extra pg.ApplicationRepo unit tests: finish
// partial statement coverage of application.go.
//
// Extends application_test.go (same pg_test package) — reuses the shared
// stubQuerier / stubTxRunner / stubRow / stubRows fixtures + the byIDAppRow /
// twoHistoryRows helpers + tenantID / courseID / gcid consts. These two
// helpers are reused, never redefined.
//
// New helpers/fillers/consts in this file are prefixed `ax2` to keep the
// package-level pg_test namespace unique across parallel agents.
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

// ax2FullAppRow fills ALL 19 scanApplication destinations — the idempotent
// rehydration path that sets every optional pointer (class_id, the six
// lifecycle timestamps, the payment/invoice/Singpass refs, decision reasons)
// plus a funding_lines JSON payload.
func ax2FullAppRow() func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) != 19 {
			return errors.New("scanApplication: expected 19 destinations")
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if v, ok := dest[0].(*string); ok {
			*v = "app-full"
		}
		if v, ok := dest[1].(*string); ok {
			*v = tenantID
		}
		if v, ok := dest[2].(*string); ok {
			*v = courseID
		}
		cls := "cls-1"
		if v, ok := dest[3].(**string); ok {
			*v = &cls
		}
		if v, ok := dest[4].(*string); ok {
			*v = gcid
		}
		if v, ok := dest[5].(*string); ok {
			*v = string(application.StatusUnderReview)
		}
		offer := now.Add(24 * time.Hour)
		if v, ok := dest[6].(**time.Time); ok {
			*v = &offer
		}
		accepted := now.Add(-time.Hour)
		if v, ok := dest[7].(**time.Time); ok {
			*v = &accepted
		}
		paid := now.Add(-30 * time.Minute)
		if v, ok := dest[8].(**time.Time); ok {
			*v = &paid
		}
		enrolled := now.Add(-time.Hour)
		if v, ok := dest[9].(**time.Time); ok {
			*v = &enrolled
		}
		withdrawnAt := now.Add(-2 * time.Hour)
		if v, ok := dest[10].(**time.Time); ok {
			*v = &withdrawnAt
		}
		pi := "pi_123"
		if v, ok := dest[11].(**string); ok {
			*v = &pi
		}
		inv := "inv_456"
		if v, ok := dest[12].(**string); ok {
			*v = &inv
		}
		sss := "ss_789"
		if v, ok := dest[13].(**string); ok {
			*v = &sss
		}
		rej := "funding shortfall"
		if v, ok := dest[14].(**string); ok {
			*v = &rej
		}
		wdr := "learner request"
		if v, ok := dest[15].(**string); ok {
			*v = &wdr
		}
		if v, ok := dest[16].(*[]byte); ok {
			*v = []byte(`[{"type":"SSG","max_amount_sgd_cents":5000,"eligible":true}]`)
		}
		if v, ok := dest[17].(*time.Time); ok {
			*v = now
		}
		if v, ok := dest[18].(*time.Time); ok {
			*v = now
		}
		return nil
	}
}

// -----------------------------------------------------------------------------
// SubmitOrGet — created-path history INSERT failure + lookup miss
// -----------------------------------------------------------------------------

func TestApplicationRepo_SubmitOrGet_CreatedPath_HistoryInsertError(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			// INSERT … RETURNING returns the inserted application_id.
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*string); ok {
					*ptr = args[0].(string)
				}
				return nil
			}}
		},
		execErrFor: func(sql string) error {
			if strings.Contains(sql, "INSERT INTO application_state_history") {
				return errors.New("history write failed")
			}
			return nil
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, _, err := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	})
	if err == nil {
		t.Fatalf("expected the history INSERT error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: insert history") {
		t.Fatalf("history error must be wrapped with context; got %v", err)
	}
	if !strings.Contains(err.Error(), "history write failed") {
		t.Fatalf("wrapped error must preserve the cause; got %v", err)
	}
}

func TestApplicationRepo_SubmitOrGet_ConflictLookupMiss_ReturnsNilFalseNil(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			if strings.Contains(sql, "INSERT INTO applications") {
				// Conflict — ON CONFLICT DO NOTHING yields no RETURNING row.
				return stubRow{scanFn: func(dest ...any) error {
					return errors.New("no rows in result set")
				}}
			}
			// Natural-key lookup: scans an EMPTY id → genuine miss.
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*string); ok {
					*ptr = ""
				}
				return nil
			}}
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	app, created, err := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	})
	if err != nil {
		t.Fatalf("conflict miss: %v", err)
	}
	if created || app != nil {
		t.Fatalf("expected (nil, false) on empty natural-key lookup; got app=%+v created=%v", app, created)
	}
}

// -----------------------------------------------------------------------------
// Get — history load error + reason-bearing history rows
// -----------------------------------------------------------------------------

func TestApplicationRepo_Get_HistoryLoadError_Propagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("history query failed")
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: byIDAppRow("offer_made")}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "application_state_history") {
				return nil, wantErr
			}
			return &stubRows{}, nil
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, _, err := r.Get(ctx, tenantID, "app-1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the history query error; got %v", err)
	}
}

func TestApplicationRepo_Get_RehydratesReason(t *testing.T) {
	t.Parallel()
	reason := "admin note"
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: byIDAppRow("offer_made")}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if !strings.Contains(sql, "application_state_history") {
				return &stubRows{}, nil
			}
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					if v, ok := dest[0].(*string); ok {
						*v = "draft"
					}
					if v, ok := dest[1].(*string); ok {
						*v = "submitted"
					}
					if v, ok := dest[2].(**string); ok {
						*v = &reason
					}
					if v, ok := dest[3].(*time.Time); ok {
						*v = time.Now().UTC()
					}
					return nil
				},
			}}, nil
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	app, ok, err := r.Get(ctx, tenantID, "app-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	hist := app.History()
	if len(hist) != 1 || hist[0].Reason != reason {
		t.Fatalf("history reason must round-trip; got %+v", hist)
	}
}

// -----------------------------------------------------------------------------
// Save — exec error / bare ctx / no-op history (rehydrated aggregate)
// -----------------------------------------------------------------------------

func TestApplicationRepo_Save_ExecError_WrapsUpdateContext(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "UPDATE applications") {
			return errors.New("write failed")
		}
		return nil
	}}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	})
	_ = app.Transition(application.StatusSubmitted)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.Save(ctx, app)
	if err == nil {
		t.Fatalf("expected the UPDATE error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: update application") {
		t.Fatalf("UPDATE error must be wrapped with context; got %v", err)
	}
}

func TestApplicationRepo_Save_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	app, _ := application.NewApplication(application.NewApplicationInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	})
	if err := r.Save(context.Background(), app); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("no SQL must execute without a tenant session; got %v", q.sqls)
	}
}

func TestApplicationRepo_Save_RehydratedNoNewTransitions_InsertsNoHistory(t *testing.T) {
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
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	app, ok, err := pg.NewApplicationRepo(&stubTxRunner{q: getQ}).Get(ctx, tenantID, "app-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	// No new transition → PendingHistory is empty → Save must be write-only
	// (UPDATE) with ZERO history INSERTs (no re-digit of rehydrated rows).
	saveQ := &stubQuerier{}
	if err := pg.NewApplicationRepo(&stubTxRunner{q: saveQ}).Save(ctx, app); err != nil {
		t.Fatalf("Save: %v", err)
	}
	sawUpdate := false
	for _, s := range saveQ.sqls {
		if strings.Contains(s, "INSERT INTO application_state_history") {
			t.Fatalf("no-op Save must not insert history rows; got %q", s)
		}
		if strings.Contains(s, "UPDATE applications") {
			sawUpdate = true
		}
	}
	if !sawUpdate {
		t.Fatalf("Save must still persist the UPDATE; got %v", saveQ.sqls)
	}
}

// -----------------------------------------------------------------------------
// list methods — query + scan error branches
// -----------------------------------------------------------------------------

func TestApplicationRepo_ListByGCID_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("connection reset by peer")
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, wantErr
	}}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	items, total, err := r.ListByGCID(ctx, tenantID, gcid, 0, 10)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v; got %v", wantErr, err)
	}
	if items != nil || total != 0 {
		t.Fatalf("expected zero-value result alongside the error; got %d / %d", len(items), total)
	}
}

func TestApplicationRepo_ListByGCID_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.ListByGCID(ctx, tenantID, gcid, 0, 10); err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
}

func TestApplicationRepo_ListByTenant_ApplySessionError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	// Bare ctx: the RLS session fails and the error must name the failing step.
	_, _, err := r.ListByTenant(context.Background(), application.ListByTenantInput{TenantID: tenantID})
	if err == nil {
		t.Fatalf("expected an error on a bare-context list")
	}
	if !strings.Contains(err.Error(), "pg: list applications: apply rls session") {
		t.Fatalf("RLS failure must be wrapped with the failing step; got %v", err)
	}
}

func TestApplicationRepo_ListByTenant_ListQueryError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*int); ok {
					*ptr = 1
				}
				return nil
			}}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, errors.New("list query failed")
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, _, err := r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantID, Limit: 50})
	if err == nil {
		t.Fatalf("expected the list query error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: list applications by tenant") {
		t.Fatalf("list query error must be wrapped with context; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// scanApplication — the full rehydration path (every optional column set)
// -----------------------------------------------------------------------------

func TestApplicationRepo_Get_HydratesEveryOptionalField(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: ax2FullAppRow()}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil // empty history trail
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	app, ok, err := r.Get(ctx, tenantID, "app-full")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if app.ClassID != "cls-1" {
		t.Fatalf("ClassID: got %q", app.ClassID)
	}
	if app.OfferExpiresAt.IsZero() || app.AcceptedAt.IsZero() || app.PaidAt.IsZero() ||
		app.EnrolledAt.IsZero() || app.WithdrawnAt.IsZero() {
		t.Fatalf("lifecycle timestamps must hydrate; got %+v", app)
	}
	if app.StripePaymentIntentID != "pi_123" || app.InvoiceID != "inv_456" || app.SingpassSessionID != "ss_789" {
		t.Fatalf("payment refs must hydrate; got %+v", app)
	}
	if app.RejectedReason != "funding shortfall" || app.WithdrawnReason != "learner request" {
		t.Fatalf("decision reasons must hydrate; got %+v", app)
	}
	if len(app.FundingLines) != 1 || app.FundingLines[0].MaxAmountSGDCents != 5000 || !app.FundingLines[0].Eligible {
		t.Fatalf("funding_lines JSON must unmarshal; got %+v", app.FundingLines)
	}
}

// ax2ErrRows is a stubRows whose Err() reports an iteration failure —
// exercises the `rows.Err()` guard bodies without a live DB.
type ax2ErrRows struct {
	*stubRows
}

func (r *ax2ErrRows) Err() error { return errors.New("iteration failed") }

func TestApplicationRepo_Get_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	if _, _, err := r.Get(context.Background(), tenantID, "app-1"); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestApplicationRepo_Get_HistoryScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: byIDAppRow("offer_made")}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if !strings.Contains(sql, "application_state_history") {
				return &stubRows{}, nil
			}
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.Get(ctx, tenantID, "app-1"); err == nil {
		t.Fatalf("expected the history row-scan error to propagate")
	}
}

func TestApplicationRepo_ListByGCID_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	if _, _, err := r.ListByGCID(context.Background(), tenantID, gcid, 0, 10); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestApplicationRepo_ListByTenant_RowsIteratorError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*int); ok {
					*ptr = 1
				}
				return nil
			}}
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &ax2ErrRows{stubRows: &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					// One scannable application row, then an iteration failure.
					if ptr, ok := dest[0].(*string); ok {
						*ptr = "app-1"
					}
					return nil
				},
			}}}, nil
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, _, err := r.ListByTenant(ctx, application.ListByTenantInput{TenantID: tenantID, Limit: 50})
	if err == nil {
		t.Fatalf("expected the rows.Err() failure to propagate")
	}
	if !strings.Contains(err.Error(), "pg: iterate application rows") {
		t.Fatalf("iter error must be wrapped; got %v", err)
	}
}

func TestApplicationRepo_SubmitOrGet_ConflictLookupError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			if strings.Contains(sql, "INSERT INTO applications") {
				return stubRow{scanFn: func(dest ...any) error {
					return errors.New("no rows in result set")
				}}
			}
			// Natural-key lookup SELECT fails (infra) — must propagate, not
			// masquerade as a miss.
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("conn reset")
			}}
		},
	}
	r := pg.NewApplicationRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.SubmitOrGet(ctx, application.SubmitInput{
		TenantID: tenantID, CourseID: courseID, GCID: gcid,
	}); err == nil {
		t.Fatalf("expected the natural-key lookup error to propagate")
	}
}
