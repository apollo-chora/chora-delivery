// survey_extra_test.go — extra pg.SurveyRepo unit tests: finish partial
// statement coverage of survey.go.
//
// Extends survey_test.go (same pg_test package) — reuses newSurvey +
// surveyJSON + the shared stubQuerier / stubTxRunner / stubRow / stubRows
// fixtures + the tenantID / gcid consts.
//
// New helpers/fillers/consts in this file are prefixed `sux` to keep the
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
	"github.com/apollo-chora/chora-delivery/internal/domain/survey"
)

// suxSurveyID is reused across the survey extra tests.
const suxSurveyID = "01970000-0000-7000-9999-5000000eeee1"

// -----------------------------------------------------------------------------
// Save — nil guard / exec-error wrap / bare-ctx RLS failure
// -----------------------------------------------------------------------------

func TestSurveyRepo_Save_NilSurvey_IsNoOp(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	if err := r.Save(tracing.WithTenantID(context.Background(), tenantID), nil); err != nil {
		t.Fatalf("Save nil: %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("nil survey must not execute SQL; got %v", q.sqls)
	}
}

func TestSurveyRepo_Save_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO surveys") {
			return errors.New("write failed")
		}
		return nil
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.Save(ctx, newSurvey(t, suxSurveyID, survey.SurveyStateDraft))
	if err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: upsert survey") {
		t.Fatalf("exec error must be wrapped with context; got %v", err)
	}
}

func TestSurveyRepo_Save_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	err := r.Save(context.Background(), newSurvey(t, suxSurveyID, survey.SurveyStateDraft))
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Get — genuine-miss / infra-loud / unmarshal-error branches
// -----------------------------------------------------------------------------

func TestSurveyRepo_Get_NoRows_IsGenuineMiss(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return errors.New("no rows in result set") }}
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	s, ok, err := r.Get(ctx, "absent")
	if err != nil {
		t.Fatalf("Get miss: err=%v (a genuine miss is not an error)", err)
	}
	if ok || s != nil {
		t.Fatalf("expected (nil, false); got (%+v, %v)", s, ok)
	}
}

func TestSurveyRepo_Get_InfraError_IsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return errors.New("conn reset") }}
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, _, err := r.Get(ctx, suxSurveyID)
	if err == nil {
		t.Fatalf("an infra failure must be LOUD, not a silent miss (CHO-2184)")
	}
	if !strings.Contains(err.Error(), "pg: get survey") {
		t.Fatalf("get error must be wrapped with context; got %v", err)
	}
}

func TestSurveyRepo_Get_BadJSON_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error {
			*(dest[0].(*[]byte)) = []byte(`{not json`)
			return nil
		}}
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, _, err := r.Get(ctx, suxSurveyID)
	if err == nil {
		t.Fatalf("expected the unmarshal error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: unmarshal survey") {
		t.Fatalf("unmarshal error must be wrapped; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ListByTenant — query / scan / unmarshal error branches
// -----------------------------------------------------------------------------

func TestSurveyRepo_ListByTenant_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("connection reset by peer")
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, wantErr
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListByTenant(ctx, tenantID, ""); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v; got %v", wantErr, err)
	}
}

func TestSurveyRepo_ListByTenant_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListByTenant(ctx, tenantID, ""); err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
}

func TestSurveyRepo_ListByTenant_UnmarshalError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte(`{not json`)
				return nil
			},
		}}, nil
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListByTenant(ctx, tenantID, ""); err == nil {
		t.Fatalf("expected the unmarshal error to propagate")
	}
}

// -----------------------------------------------------------------------------
// SaveResponse — nil guard / exec-error wrap
// -----------------------------------------------------------------------------

func TestSurveyRepo_SaveResponse_NilResponse_IsNoOp(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	if err := r.SaveResponse(tracing.WithTenantID(context.Background(), tenantID), nil); err != nil {
		t.Fatalf("SaveResponse nil: %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("nil response must not execute SQL; got %v", q.sqls)
	}
}

func TestSurveyRepo_SaveResponse_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO survey_responses") {
			return errors.New("write failed")
		}
		return nil
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.SaveResponse(ctx, &survey.SurveyResponse{
		ID: "01970000-0000-7000-9999-5000000eeee2", SurveyID: suxSurveyID,
		GCID: gcid, SubmittedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatalf("expected the Exec error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: insert survey response") {
		t.Fatalf("exec error must be wrapped with context; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// HasResponse — scan-error branch
// -----------------------------------------------------------------------------

func TestSurveyRepo_HasResponse_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowFn: func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return errors.New("conn closed") }}
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.HasResponse(ctx, suxSurveyID, gcid); err == nil {
		t.Fatalf("expected the scan error to propagate")
	}
}

// -----------------------------------------------------------------------------
// ListResponsesBySurvey — query / scan / unmarshal error branches
// -----------------------------------------------------------------------------

func TestSurveyRepo_ListResponsesBySurvey_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("connection reset by peer")
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return nil, wantErr
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListResponsesBySurvey(ctx, suxSurveyID); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v; got %v", wantErr, err)
	}
}

func TestSurveyRepo_ListResponsesBySurvey_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { return errors.New("bad row bytes") },
		}}, nil
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListResponsesBySurvey(ctx, suxSurveyID); err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
}

func TestSurveyRepo_ListResponsesBySurvey_UnmarshalError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte(`{not json`)
				return nil
			},
		}}, nil
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListResponsesBySurvey(ctx, suxSurveyID); err == nil {
		t.Fatalf("expected the unmarshal error to propagate")
	}
}

func TestSurveyRepo_SaveResponse_ApplySessionExecFails(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "SET LOCAL chora.tenant_id") {
			return errors.New("rlx blocked")
		}
		return nil
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	err := r.SaveResponse(ctx, &survey.SurveyResponse{
		ID: "01970000-0000-7000-9999-5000000eeee3", SurveyID: suxSurveyID,
		GCID: gcid, SubmittedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatalf("expected the SET LOCAL failure to propagate")
	}
	if !strings.Contains(err.Error(), "rlx blocked") {
		t.Fatalf("expected the underlying SET LOCAL error; got %v", err)
	}
}

func TestSurveyRepo_ListByTenant_RowsIteratorError_Propagates(t *testing.T) {
	t.Parallel()
	// A rows.Err() failure after successfully scanning a row must propagate
	// (the guard body of `return rows.Err()`).
	s := newSurvey(t, "01970000-0000-7000-9999-5000000eeee4", survey.SurveyStateDraft)
	q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
		return &suxErrRows{stubRows: &stubRows{rows: []func(dest ...any) error{
			func(dest ...any) error { *(dest[0].(*[]byte)) = surveyJSON(t, s); return nil },
		}}}, nil
	}}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, err := r.ListByTenant(ctx, tenantID, "")
	if err == nil {
		t.Fatalf("expected the rows.Err() failure to propagate")
	}
	if !strings.Contains(err.Error(), "iteration failed") {
		t.Fatalf("expected the underlying iter error; got %v", err)
	}
}

// suxErrRows is a stubRows whose Err() reports an iteration failure —
// exercises the `return rows.Err()` guard body without a live DB.
type suxErrRows struct {
	*stubRows
}

func (r *suxErrRows) Err() error { return errors.New("iteration failed") }

func TestSurveyRepo_Get_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	if _, _, err := r.Get(context.Background(), suxSurveyID); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestSurveyRepo_ListByTenant_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	if _, err := r.ListByTenant(context.Background(), tenantID, ""); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestSurveyRepo_HasResponse_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	if _, err := r.HasResponse(context.Background(), suxSurveyID, gcid); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}

func TestSurveyRepo_ListResponsesBySurvey_BareContext_FailsLoud(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	if _, err := r.ListResponsesBySurvey(context.Background(), suxSurveyID); !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
}
