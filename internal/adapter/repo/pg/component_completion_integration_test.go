//go:build integration

// component_completion_integration_test.go - CHO-2222 declared-component
// resolver, against a live chora_delivery.
//
// Two things only a live database can prove, and both have burned this project:
//
//  1. THE BARE CONTEXT. rls.ApplySession takes the tenant from the CONTEXT, not
//     from the SQL args, and a Pub/Sub push handler hands the repo a BARE
//     context.Background(). The unit tests cannot see this: they build a ctx.
//     ⚠ This test is deliberately driven with a bare context.Background() - an
//     integration test that BUILDS its own ctx supplies what production does
//     not, and passes over a repo that cannot make a single live call. This trap
//     has landed twice already (CHO-2153 cohort roster, CHO-2157 completion
//     engine), so it gets a live guard rather than a comment.
//
//  2. THE PREDICATES, against real SQL. The unit test asserts "passed IS TRUE"
//     is present in the SQL STRING, which proves the text and nothing about
//     Postgres. The NEGATIVE CONTROLS below seed a genuinely FAILED submission
//     and a genuinely FAILED exam result and require the resolver to leave them
//     out. Without them this suite would assert only additions, and a resolver
//     that returned every row it saw would sail through.
//
//     export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//     export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//     export CHORA_TEST_DB_PROJECT=chora-489812
//     go test -tags integration -run TestIntegration_ComponentCompletion ./services/chora-delivery/internal/adapter/repo/pg/...
package pg_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// seedExec runs one tenant-scoped statement outside the repo.
func seedExec(t *testing.T, pool *pgxpool.Pool, tenant, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("seed begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant)); err != nil {
		t.Fatalf("seed set tenant: %v", err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
}

func TestIntegration_ComponentCompletion_ResolvesOnlyReleasedAndPassing(t *testing.T) {
	pool := liveDB(t)
	repo := pg.NewComponentCompletionRepo(&liveTxRunner{pool: pool})

	tenant, _ := uuid.NewV7()
	learner, _ := uuid.NewV7()
	instructor, _ := uuid.NewV7()
	testSet, _ := uuid.NewV7()
	passedAssessment, _ := uuid.NewV7()
	failedAssessment, _ := uuid.NewV7()
	unreleasedAssessment, _ := uuid.NewV7()
	passedExam, _ := uuid.NewV7()
	failedExam, _ := uuid.NewV7()
	examForm, _ := uuid.NewV7()

	tenantCtx := tracing.WithTenantID(context.Background(), tenant.String())

	// ---- seed: three assessments, three submissions, two exam results ----
	for _, a := range []uuid.UUID{passedAssessment, failedAssessment, unreleasedAssessment} {
		seedExec(t, pool, tenant.String(),
			`INSERT INTO assessments (assessment_id, tenant_id, instructor_gcid, test_set_id, title, state, total_points)
			 VALUES ($1, $2, $3, $4, 'CHO-2222 resolver fixture', 'RELEASED', 100)`,
			a, tenant, instructor, testSet)
	}
	newSubmission := func(assessment uuid.UUID, state string, passed bool) {
		sub, _ := uuid.NewV7()
		seedExec(t, pool, tenant.String(),
			`INSERT INTO submissions (submission_id, assessment_id, tenant_id, learner_gcid, state,
			     total_score, max_score, passing_percent, passed)
			 VALUES ($1, $2, $3, $4, $5, $6, 100, 70, $7)`,
			sub, assessment, tenant, learner, state, map[bool]int{true: 95, false: 10}[passed], passed)
	}
	newSubmission(passedAssessment, "RELEASED", true)                   // ✓ complete
	newSubmission(failedAssessment, "RELEASED", false)                  // ✗ NEGATIVE CONTROL: failed
	newSubmission(unreleasedAssessment, "GRADED_PENDING_RELEASE", true) // ✗ NEGATIVE CONTROL: not released

	newExamResult := func(exam uuid.UUID, outcome string, raw int) {
		res, _ := uuid.NewV7()
		seedExec(t, pool, tenant.String(),
			`INSERT INTO exam_results (id, tenant_id, exam_id, exam_form_id, candidate_ref, outcome, raw_score, max_score, data)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 100, '{}'::jsonb)`,
			res, tenant, exam, examForm, learner, outcome, raw)
	}
	newExamResult(passedExam, "PASS", 90) // ✓ complete
	newExamResult(failedExam, "FAIL", 20) // ✗ NEGATIVE CONTROL: failed

	t.Cleanup(func() {
		ctx := context.Background()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return
		}
		_, _ = tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant.String()))
		_, _ = tx.Exec(ctx, `DELETE FROM exam_results WHERE tenant_id = $1`, tenant)
		_, _ = tx.Exec(ctx, `DELETE FROM submissions WHERE tenant_id = $1`, tenant)
		_, _ = tx.Exec(ctx, `DELETE FROM assessments WHERE tenant_id = $1`, tenant)
		_ = tx.Commit(ctx)
	})

	declared := []domain.CompletionComponent{
		{Kind: domain.ComponentKindAssessment, Ref: passedAssessment.String()},
		{Kind: domain.ComponentKindAssessment, Ref: failedAssessment.String()},
		{Kind: domain.ComponentKindAssessment, Ref: unreleasedAssessment.String()},
		{Kind: domain.ComponentKindExam, Ref: passedExam.String()},
		{Kind: domain.ComponentKindExam, Ref: failedExam.String()},
	}

	// ---- CONTROL + NEGATIVE CONTROLS: only the earned components resolve ----
	got, err := repo.CompletedComponents(tenantCtx, tenant.String(), learner.String(), declared)
	if err != nil {
		t.Fatalf("control CompletedComponents(tenantCtx): %v", err)
	}
	want := map[domain.CompletionComponent]bool{
		{Kind: domain.ComponentKindAssessment, Ref: passedAssessment.String()}: true,
		{Kind: domain.ComponentKindExam, Ref: passedExam.String()}:             true,
	}
	if len(got) != len(want) {
		t.Fatalf("want exactly %d completed components (the passed+released ones), got %d: %+v",
			len(want), len(got), got)
	}
	for _, c := range got {
		if !want[c] {
			switch c.Ref {
			case failedAssessment.String():
				t.Errorf("NEGATIVE CONTROL FAILED: a submission with passed=false was reported COMPLETE. " +
					"A learner who failed a declared component would be certified anyway.")
			case unreleasedAssessment.String():
				t.Errorf("NEGATIVE CONTROL FAILED: a GRADED_PENDING_RELEASE submission was reported " +
					"COMPLETE. The certificate would leak the outcome ahead of its release.")
			case failedExam.String():
				t.Errorf("NEGATIVE CONTROL FAILED: an exam result with outcome=FAIL was reported COMPLETE.")
			default:
				t.Errorf("unexpected component reported complete: %+v", c)
			}
		}
	}

	// ---- THE ASSERTION: a BARE ctx is an INFRA FAILURE, and must be LOUD. ----
	// This is the exact shape a Pub/Sub subscriber hands the repo.
	got, err = repo.CompletedComponents(context.Background(), tenant.String(), learner.String(), declared)
	if err == nil {
		t.Fatalf("SWALLOW: CompletedComponents(context.Background()) returned no error (got=%+v) for a "+
			"learner whose components demonstrably resolve under a tenant-bearing ctx. An RLS failure "+
			"is being reported as 'nothing completed' - which withholds an EARNED certificate and "+
			"reads as a policy decision.", got)
	}
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("CompletedComponents(bare ctx) must surface rls.ErrNoTenantContext, got: %v", err)
	}
	if got != nil {
		t.Fatalf("no components may be reported alongside an error, got %+v", got)
	}

	// ---- The other half: a learner with nothing completes nothing, quietly. ----
	stranger, _ := uuid.NewV7()
	got, err = repo.CompletedComponents(tenantCtx, tenant.String(), stranger.String(), declared)
	if err != nil {
		t.Fatalf("a learner with no submissions must NOT be an error, got: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a learner with no submissions completes nothing, got %+v", got)
	}
}
