// survey_test.go — unit tests for pg.SurveyRepo (R+ durability sweep Wave 2).
//
// Mirrors exam_test.go: stubs the Querier (shared stubQuerier / stubTxRunner
// / stubRow / stubRows + tenantID / courseID / gcid consts from
// application_test.go) so the SQL surface + RLS contract are exercised
// without a live DB. Both aggregates persist as JSONB snapshots, so reads
// stub a single `data []byte` column carrying the marshalled aggregate.
//
// Guarantees:
//  1. rls.ApplySession runs BEFORE the data query (every method).
//  2. SQL matches shape (UPSERT ON CONFLICT (id), SELECT data … deleted_at
//     IS NULL, state filter, EXISTS for HasResponse).
//  3. Nil-tx is fail-loud on writes (Save / SaveResponse / lists →
//     ErrNotImplemented) and degrades on point reads (Get → ok=false).
//  4. JSONB round-trip rehydrates the aggregate.
//  5. SaveResponse demands a tenant on ctx (the RLS WITH CHECK invariant).
package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/survey"
)

func newSurvey(t *testing.T, id string, state survey.SurveyState) *survey.Survey {
	t.Helper()
	s, err := survey.NewSurvey(survey.NewSurveyInput{
		TenantID: tenantID,
		CourseID: courseID,
		Title:    "Post-course feedback",
		Questions: []survey.Question{
			{Prompt: "Rate the course", Type: survey.QuestionTypeLikert},
		},
	})
	if err != nil {
		t.Fatalf("NewSurvey: %v", err)
	}
	s.ID = id
	s.State = state
	return s
}

func surveyJSON(t *testing.T, s *survey.Survey) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal survey: %v", err)
	}
	return b
}

func TestSurveyRepo_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewSurveyRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newSurvey(t, "01970000-0000-7000-9999-50000000001", survey.SurveyStateDraft)); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListByTenant(ctx, tenantID, ""); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByTenant: expected ErrNotImplemented; got %v", err)
	}
	if err := r.SaveResponse(ctx, &survey.SurveyResponse{ID: "r1", SurveyID: "s1", GCID: gcid}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("SaveResponse: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.HasResponse(ctx, "s1", gcid); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("HasResponse: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.ListResponsesBySurvey(ctx, "s1"); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListResponsesBySurvey: expected ErrNotImplemented; got %v", err)
	}
	if _, ok, _ := r.Get(ctx, "s1"); ok {
		t.Fatalf("Get: expected ok=false on nil-tx")
	}
}

func TestSurveyRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, newSurvey(t, "01970000-0000-7000-9999-50000000002", survey.SurveyStateDistributed)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO surveys") || !strings.Contains(last, "ON CONFLICT (id)") || !strings.Contains(last, "DO UPDATE") {
		t.Fatalf("expected idempotent upsert into surveys; got %q", last)
	}
}

func TestSurveyRepo_Get_Hit_RoundTrips(t *testing.T) {
	t.Parallel()
	want := newSurvey(t, "01970000-0000-7000-9999-50000000003", survey.SurveyStateClosed)
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = surveyJSON(t, want)
				return nil
			}}
		},
	}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	s, ok, _ := r.Get(ctx, want.ID)
	if !ok || s == nil {
		t.Fatalf("Get: expected hit")
	}
	if s.ID != want.ID || s.State != survey.SurveyStateClosed || s.Title != want.Title {
		t.Fatalf("Get: rehydration mismatch; got %+v", s)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM surveys") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("expected soft-delete-filtered SELECT FROM surveys; got %q", last)
	}
}

func TestSurveyRepo_ListByTenant_StateFilterSelectsParametrisedSQL(t *testing.T) {
	t.Parallel()
	a := newSurvey(t, "01970000-0000-7000-9999-5000000000a", survey.SurveyStateDistributed)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = surveyJSON(t, a); return nil },
			}}, nil
		},
	}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListByTenant(ctx, tenantID, survey.SurveyStateDistributed)
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 row; got %d", len(out))
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "state = $2") || !strings.Contains(last, "deleted_at IS NULL") {
		t.Fatalf("state-filtered list must bind state = $2; got %q", last)
	}
	// Confirm the state arg was bound.
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 2 || lastArgs[1] != string(survey.SurveyStateDistributed) {
		t.Fatalf("expected (tenant, state) args; got %#v", lastArgs)
	}
}

func TestSurveyRepo_ListByTenant_NoFilterOmitsState(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByTenant(ctx, tenantID, ""); err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	last := q.sqls[len(q.sqls)-1]
	if strings.Contains(last, "state = $2") {
		t.Fatalf("empty filter must NOT bind state; got %q", last)
	}
}

func TestSurveyRepo_SaveResponse_RequiresTenantOnCtx(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	// No tenant on ctx → RLS WITH CHECK would reject; fail loud BEFORE SQL.
	err := r.SaveResponse(context.Background(), &survey.SurveyResponse{
		ID: "01970000-0000-7000-9999-5000000000f", SurveyID: "s1", GCID: gcid,
		SubmittedAt: time.Now().UTC(),
	})
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("expected ErrNoTenantContext; got %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("expected no SQL when tenant missing; got %#v", q.sqls)
	}
}

func TestSurveyRepo_SaveResponse_AppliesRLSThenInsertsWithCtxTenant(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	err := r.SaveResponse(ctx, &survey.SurveyResponse{
		ID: "01970000-0000-7000-9999-50000000010", SurveyID: "01970000-0000-7000-9999-50000000003",
		GCID: gcid, SubmittedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("SaveResponse: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO survey_responses") || !strings.Contains(last, "ON CONFLICT (id) DO NOTHING") {
		t.Fatalf("expected append-only insert; got %q", last)
	}
	// tenant_id arg ($3) must be the ctx tenant.
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 6 || lastArgs[2] != tenantID {
		t.Fatalf("expected ctx tenant bound as $3; got %#v", lastArgs)
	}
}

func TestSurveyRepo_HasResponse_ScansExists(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*bool)) = true
				return nil
			}}
		},
	}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, err := r.HasResponse(ctx, "01970000-0000-7000-9999-50000000003", gcid)
	if err != nil {
		t.Fatalf("HasResponse: %v", err)
	}
	if !got {
		t.Fatalf("expected exists=true")
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "EXISTS") || !strings.Contains(last, "survey_responses") {
		t.Fatalf("expected EXISTS query; got %q", last)
	}
}

func TestSurveyRepo_ListResponsesBySurvey_RoundTrips(t *testing.T) {
	t.Parallel()
	resp := &survey.SurveyResponse{
		ID: "01970000-0000-7000-9999-50000000020", SurveyID: "01970000-0000-7000-9999-50000000003",
		GCID: gcid, Answers: []survey.Answer{{QuestionID: "q1", Value: "5"}},
		SubmittedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	rj, _ := json.Marshal(resp)
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { *(dest[0].(*[]byte)) = rj; return nil },
			}}, nil
		},
	}
	r := pg.NewSurveyRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	out, err := r.ListResponsesBySurvey(ctx, resp.SurveyID)
	if err != nil {
		t.Fatalf("ListResponsesBySurvey: %v", err)
	}
	if len(out) != 1 || out[0].ID != resp.ID || len(out[0].Answers) != 1 {
		t.Fatalf("response round-trip mismatch; got %+v", out)
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "FROM survey_responses") || !strings.Contains(last, "ORDER BY submitted_at") {
		t.Fatalf("expected ordered per-survey response list; got %q", last)
	}
}
