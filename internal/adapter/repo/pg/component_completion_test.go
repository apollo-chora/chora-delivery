// component_completion_test.go - CHO-2222 declared-component resolver.
//
// The resolver answers ONE question: of the components this offering DECLARED,
// which has this learner actually completed? Everything it reads lives in
// chora_delivery (submissions + exam_results), so no cross-DB query is involved
// (ddd-enforcement #1).
//
// What these tests defend, in order of how badly each would hurt:
//
//  1. The PREDICATES. Drop "passed IS TRUE" and a learner who FAILED a declared
//     assessment is reported as having completed it - a phantom credential, the
//     worst defect available here. Drop "state = 'RELEASED'" and a graded-but-
//     unreleased component certifies the learner ahead of their instructor,
//     which is the exact leak completion_inbox.go rides .released.v1 to avoid.
//  2. FAIL LOUD. An infra failure must never arrive as "not complete": that
//     reads as a policy decision and withholds a credential in silence, which is
//     the defect CHO-2157 existed to fix. Hence the port returns an error, and
//     an unresolvable kind is an error rather than a quiet absence.
//  3. RLS, and the ZERO-ROWS vs INCOMPLETE distinction it decides.
//     rls.ApplySession takes the tenant from the CONTEXT, and a Pub/Sub
//     subscriber hands the repo a BARE ctx. These tests pin that the session is
//     applied BEFORE any data query, that a bare ctx is LOUD and never reaches
//     the SQL, and that a genuine zero-row read stays QUIET. See the block above
//     TestComponentCompletionRepo_BareContext_IsLoudNotIncomplete for why the
//     state of migration 0055 cannot change any of those answers.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	ccAssessmentA = "01970000-0000-7000-a000-00000000aa01"
	ccAssessmentB = "01970000-0000-7000-a000-00000000aa02"
	ccExamE       = "01970000-0000-7000-b000-00000000bb01"
	ccLearner     = "01970000-0000-7000-9000-00000000cc01"
)

var (
	compAssessmentA = domain.CompletionComponent{Kind: domain.ComponentKindAssessment, Ref: ccAssessmentA}
	compAssessmentB = domain.CompletionComponent{Kind: domain.ComponentKindAssessment, Ref: ccAssessmentB}
	compExamE       = domain.CompletionComponent{Kind: domain.ComponentKindExam, Ref: ccExamE}
)

// refRows builds a stubRows yielding one text column per ref.
func refRows(refs ...string) *stubRows {
	rows := make([]func(dest ...any) error, 0, len(refs))
	for _, ref := range refs {
		r := ref
		rows = append(rows, func(dest ...any) error {
			*(dest[0].(*string)) = r
			return nil
		})
	}
	return &stubRows{rows: rows}
}

// componentQuerier routes each SQL to the right canned rows by table name.
func componentQuerier(submissionRefs, examRefs []string) *stubQuerier {
	return &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "FROM submissions") {
				return refRows(submissionRefs...), nil
			}
			if strings.Contains(sql, "FROM exam_results") {
				return refRows(examRefs...), nil
			}
			return nil, errors.New("unexpected SQL: " + sql)
		},
	}
}

func TestComponentCompletionRepo_ResolvesAssessmentAndExamComponents(t *testing.T) {
	t.Parallel()
	q := componentQuerier([]string{ccAssessmentA}, []string{ccExamE})
	r := pg.NewComponentCompletionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, err := r.CompletedComponents(ctx, tenantID, ccLearner,
		[]domain.CompletionComponent{compAssessmentA, compAssessmentB, compExamE})
	if err != nil {
		t.Fatalf("CompletedComponents: %v", err)
	}
	// A and E came back from their tables; B did not, so B is NOT complete.
	want := []domain.CompletionComponent{compAssessmentA, compExamE}
	if len(got) != len(want) {
		t.Fatalf("want %d completed components, got %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("completed[%d]: want %+v, got %+v", i, want[i], got[i])
		}
	}
}

// RLS must be applied before any data query - the session GUC is what scopes
// every row the resolver is about to read.
func TestComponentCompletionRepo_AppliesRLSBeforeQuerying(t *testing.T) {
	t.Parallel()
	q := componentQuerier(nil, nil)
	r := pg.NewComponentCompletionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.CompletedComponents(ctx, tenantID, ccLearner,
		[]domain.CompletionComponent{compAssessmentA, compExamE}); err != nil {
		t.Fatalf("CompletedComponents: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
}

// THE predicate guard. Each clause below, if dropped, turns the gate from a
// requirement into a rubber stamp or a leak.
func TestComponentCompletionRepo_SQLCarriesTheLoadBearingPredicates(t *testing.T) {
	t.Parallel()
	q := componentQuerier(nil, nil)
	r := pg.NewComponentCompletionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.CompletedComponents(ctx, tenantID, ccLearner,
		[]domain.CompletionComponent{compAssessmentA, compExamE}); err != nil {
		t.Fatalf("CompletedComponents: %v", err)
	}

	var submissionSQL, examSQL string
	for _, sql := range q.sqls {
		switch {
		case strings.Contains(sql, "FROM submissions"):
			submissionSQL = sql
		case strings.Contains(sql, "FROM exam_results"):
			examSQL = sql
		}
	}
	if submissionSQL == "" || examSQL == "" {
		t.Fatalf("both component tables must be queried; got %#v", q.sqls)
	}

	for _, want := range []struct{ clause, why string }{
		{"state = 'RELEASED'", "an unreleased grade must NOT count as complete, or the certificate " +
			"leaks the outcome to the learner ahead of their instructor"},
		{"passed IS TRUE", "a FAILED assessment must NOT count as complete, or a learner who failed " +
			"a declared component is certified anyway - a phantom credential"},
		{"deleted_at IS NULL", "a soft-deleted submission must not satisfy a component"},
		{"learner_gcid = $2", "completion is per-learner"},
	} {
		if !strings.Contains(submissionSQL, want.clause) {
			t.Errorf("submissions SQL is missing %q: %s\nSQL: %s", want.clause, want.why, submissionSQL)
		}
	}
	for _, want := range []struct{ clause, why string }{
		{"outcome = 'PASS'", "only PASS certifies (exam_result_inbox.go); a FAIL must not satisfy " +
			"a declared exam component"},
		{"deleted_at IS NULL", "a soft-deleted result must not satisfy a component"},
		{"candidate_ref = $2", "candidate_ref IS the learner GCID (exam_result_inbox.go:34-35)"},
	} {
		if !strings.Contains(examSQL, want.clause) {
			t.Errorf("exam_results SQL is missing %q: %s\nSQL: %s", want.clause, want.why, examSQL)
		}
	}
}

// Only the declared kinds are queried: an exam-only offering must not pay for a
// submissions scan, and vice versa.
func TestComponentCompletionRepo_QueriesOnlyTheDeclaredKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		declared      []domain.CompletionComponent
		wantTable     string
		unwantedTable string
	}{
		{"assessment only", []domain.CompletionComponent{compAssessmentA}, "FROM submissions", "FROM exam_results"},
		{"exam only", []domain.CompletionComponent{compExamE}, "FROM exam_results", "FROM submissions"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := componentQuerier(nil, nil)
			r := pg.NewComponentCompletionRepo(&stubTxRunner{q: q})
			ctx := tracing.WithTenantID(context.Background(), tenantID)
			if _, err := r.CompletedComponents(ctx, tenantID, ccLearner, c.declared); err != nil {
				t.Fatalf("CompletedComponents: %v", err)
			}
			all := strings.Join(q.sqls, "\n")
			if !strings.Contains(all, c.wantTable) {
				t.Errorf("expected a query against %s; got %#v", c.wantTable, q.sqls)
			}
			if strings.Contains(all, c.unwantedTable) {
				t.Errorf("must NOT query %s when none is declared; got %#v", c.unwantedTable, q.sqls)
			}
		})
	}
}

// Declaring nothing is the live shape of every offering today. It must not cost
// a database round-trip - and it must not error.
func TestComponentCompletionRepo_NoDeclaredComponents_TouchesNoDatabase(t *testing.T) {
	t.Parallel()
	q := componentQuerier(nil, nil)
	r := pg.NewComponentCompletionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, err := r.CompletedComponents(ctx, tenantID, ccLearner, nil)
	if err != nil {
		t.Fatalf("resolving an empty declaration must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want no completed components, got %+v", got)
	}
	if len(q.sqls) != 0 {
		t.Errorf("an empty declaration must not touch the DB; ran %#v", q.sqls)
	}
}

// An unwired repo is a WIRING BUG, not an empty result. Returning "nothing is
// complete" here would withhold every certificate on every offering that
// declares a component, and would look like a policy decision.
func TestComponentCompletionRepo_NilTx_IsLoudNotEmpty(t *testing.T) {
	t.Parallel()
	r := pg.NewComponentCompletionRepo(nil)
	got, err := r.CompletedComponents(context.Background(), tenantID, ccLearner,
		[]domain.CompletionComponent{compAssessmentA})
	if !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("an unwired repo must be loud; want ErrNotImplemented, got err=%v got=%+v", err, got)
	}
	if got != nil {
		t.Errorf("no components may be reported alongside an error, got %+v", got)
	}
}

// A kind nothing can resolve must be an ERROR, never a quiet "not complete".
// Reported as incomplete, it would be a gate that can never open, wearing the
// costume of an unmet requirement.
func TestComponentCompletionRepo_UnresolvableKind_IsLoudNotIncomplete(t *testing.T) {
	t.Parallel()
	q := componentQuerier(nil, nil)
	r := pg.NewComponentCompletionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, err := r.CompletedComponents(ctx, tenantID, ccLearner, []domain.CompletionComponent{
		compAssessmentA,
		{Kind: domain.ComponentKindProject, Ref: "01970000-0000-7000-c000-00000000dd01"},
	})
	if !errors.Is(err, domain.ErrCompletionComponentUnresolvable) {
		t.Fatalf("a project component must fail LOUD; want ErrCompletionComponentUnresolvable, got err=%v got=%+v", err, got)
	}
	if got != nil {
		t.Errorf("no components may be reported alongside an error, got %+v", got)
	}
}

// -----------------------------------------------------------------------------
// Zero rows vs genuinely incomplete: the distinction the gate depends on.
//
// exam_results' RLS policy (mig 0046) casts the GUC UNGUARDED:
//
//	USING (tenant_id = current_setting('chora.tenant_id', true)::uuid)
//
// A pooled connection does not leave that GUC unset, it reverts it to the EMPTY
// STRING, and ''::uuid raises 22P02. Migration 0055 replaces the cast with a
// NULLIF-guarded one, which converts that loud crash into a DETERMINISTIC
// ZERO-ROW denial. 0055 is written but NOT YET APPLIED, so both states are live
// possibilities.
//
// Either state is fatal to a resolver that lets the DATABASE decide, because
// zero rows would read as "component not complete" ⇒ a certificate withheld in
// SILENCE, for a reason that looks like policy. That is the CHO-2157 defect
// exactly, and it is what this whole story exists to prevent.
//
// This resolver never asks the database that question. rls.ApplySession is the
// FIRST statement inside RunInTx (one pgx transaction, one connection), so:
//
//   - no tenant on the ctx ⇒ ApplySession returns ErrNoTenantContext BEFORE any
//     SQL runs. No query is issued, so there are no rows to misread, under
//     EITHER state of 0055.
//   - tenant on the ctx ⇒ SET LOCAL re-establishes the GUC inside THIS
//     transaction, so a pooled connection's reverted GUC is irrelevant: the
//     empty-GUC state is unreachable here by construction.
//
// The two tests below pin both halves: a failed read is LOUD, and a genuine
// zero-row read under a healthy session is QUIET. They must stay distinguishable.
// -----------------------------------------------------------------------------

// A missing tenant context is an INFRA failure, and must never be mistaken for
// "this learner completed nothing". ⚠ Driven with a BARE context.Background():
// that is exactly what a Pub/Sub push handler hands the engine.
func TestComponentCompletionRepo_BareContext_IsLoudNotIncomplete(t *testing.T) {
	t.Parallel()
	q := componentQuerier([]string{ccAssessmentA}, []string{ccExamE}) // rows EXIST
	r := pg.NewComponentCompletionRepo(&stubTxRunner{q: q})

	got, err := r.CompletedComponents(context.Background(), tenantID, ccLearner,
		[]domain.CompletionComponent{compAssessmentA, compExamE})
	if err == nil {
		t.Fatalf("SWALLOW: a bare ctx returned no error (got=%+v). An RLS failure reported as "+
			"'nothing completed' withholds an EARNED certificate and reads as a policy decision.", got)
	}
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Fatalf("want rls.ErrNoTenantContext, got: %v", err)
	}
	if got != nil {
		t.Errorf("no components may be reported alongside an error, got %+v", got)
	}
	// The load-bearing part: we never reached the SQL at all. The 22P02-vs-
	// zero-rows divergence that migration 0055 flips lives BEYOND this point, so
	// this resolver's behaviour is identical whether 0055 is applied or not.
	for _, sql := range q.sqls {
		if strings.Contains(sql, "FROM submissions") || strings.Contains(sql, "FROM exam_results") {
			t.Errorf("a data query ran without a tenant session (%q) - the DB would then decide "+
				"between a 22P02 crash and a silent zero-row denial. Neither may reach the gate.", sql)
		}
	}
}

// The other half of the contract: under a HEALTHY tenant session, zero rows is a
// genuine, quiet "not completed" - NOT an error. If this ever started erroring,
// every learner who simply has not finished a component yet would NACK the
// event and fill the DLQ.
func TestComponentCompletionRepo_ZeroRowsUnderAValidTenant_IsQuietlyIncomplete(t *testing.T) {
	t.Parallel()
	q := componentQuerier(nil, nil) // the learner has completed nothing
	r := pg.NewComponentCompletionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, err := r.CompletedComponents(ctx, tenantID, ccLearner,
		[]domain.CompletionComponent{compAssessmentA, compExamE})
	if err != nil {
		t.Fatalf("a learner who has genuinely completed nothing is NOT an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want nothing completed, got %+v", got)
	}
	// And it DID ask - this is a real answer from the DB, not a skipped read.
	if len(q.sqls) < 2 {
		t.Errorf("expected RLS + at least one data query; got %#v", q.sqls)
	}
}

// An infra failure must surface, not degrade into "not complete".
func TestComponentCompletionRepo_QueryError_IsReturned(t *testing.T) {
	t.Parallel()
	boom := errors.New("connection reset by peer")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return nil, boom },
	}
	r := pg.NewComponentCompletionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	got, err := r.CompletedComponents(ctx, tenantID, ccLearner, []domain.CompletionComponent{compAssessmentA})
	if !errors.Is(err, boom) {
		t.Fatalf("a query failure must surface; got err=%v got=%+v", err, got)
	}
	if got != nil {
		t.Errorf("no components may be reported alongside an error, got %+v", got)
	}
}
