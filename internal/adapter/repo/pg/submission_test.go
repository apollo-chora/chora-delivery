// submission_test.go — unit tests for SubmissionRepo.ListReleasedByLearner
// (CHO-2040 ceremony learning-edges read).
//
// Stubs the Querier so the SQL surface + arg bindings are exercised without
// a live DB (mirrors course_cj2_test.go's pattern). The load-bearing
// assertions are the VISIBILITY GATE baked into the SQL:
//
//   - state = 'RELEASED'      — mirrors the learner-facing result endpoint
//     (internal/adapter/http/assessment_handler.go myResultHandler), which
//     only reveals grading artifacts incl. the ADR-172 overall_comment once
//     the submission is RELEASED. RELEASED is reachable only through the
//     ADR-172 §D6 HITL gate (MarkReleased refuses PENDING_REVIEW), so the
//     review_status check is subsumed by the state check.
//   - deleted_at IS NULL      — soft-delete respected (ddd-enforcement #4).
//   - tenant_id + learner_gcid binds — learner-scoped, RLS-backed.
//   - ORDER BY graded_at DESC — newest grading-completion first
//     (graded_at is stamped by Submission.MarkGradedPendingRelease, the
//     grading-completion instant every RELEASED row passed through).
package pg_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
)

const (
	subTenantID    = "11111111-1111-7111-8111-111111111111"
	subLearnerGCID = "00000000-0000-7000-8000-000000001999"
)

func TestSubmissionRepo_ListReleasedByLearner_NilTxRunner(t *testing.T) {
	t.Parallel()
	r := pg.NewSubmissionRepo(nil)
	_, err := r.ListReleasedByLearner(context.Background(), subTenantID, subLearnerGCID, 21, 0)
	if err == nil {
		t.Fatalf("expected ErrNotImplemented from nil TxRunner; got nil")
	}
}

func TestSubmissionRepo_ListReleasedByLearner_SQLShapeAndBinds(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewSubmissionRepo(tx)

	items, err := r.ListReleasedByLearner(context.Background(), subTenantID, subLearnerGCID, 21, 20)
	if err != nil {
		t.Fatalf("ListReleasedByLearner: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("stub returns no rows; got %d items", len(items))
	}
	// rls.ApplySession (SET LOCAL) + the SELECT.
	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (RLS session + SELECT); got %d", len(q.sqls))
	}
	sel := q.sqls[len(q.sqls)-1]
	for _, want := range []string{
		"FROM submissions",
		"state = 'RELEASED'",
		"deleted_at IS NULL",
		"ORDER BY graded_at DESC NULLS LAST, submission_id DESC",
		"LIMIT $3 OFFSET $4",
	} {
		if !strings.Contains(sel, want) {
			t.Fatalf("SELECT missing %q:\n%s", want, sel)
		}
	}
	args := q.args[len(q.args)-1]
	if len(args) != 4 {
		t.Fatalf("expected 4 bind args; got %d (%v)", len(args), args)
	}
	if args[0] != subTenantID || args[1] != subLearnerGCID {
		t.Fatalf("tenant/learner binds wrong: %v", args[:2])
	}
	if args[2] != 21 || args[3] != 20 {
		t.Fatalf("limit/offset binds wrong: %v", args[2:])
	}
}

func TestSubmissionRepo_ListReleasedByLearner_GuardsLimitAndOffset(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewSubmissionRepo(tx)

	// limit <= 0 → default 20; negative offset → 0.
	if _, err := r.ListReleasedByLearner(context.Background(), subTenantID, subLearnerGCID, 0, -5); err != nil {
		t.Fatalf("ListReleasedByLearner: %v", err)
	}
	args := q.args[len(q.args)-1]
	if args[2] != 20 || args[3] != 0 {
		t.Fatalf("expected guarded binds (20, 0); got (%v, %v)", args[2], args[3])
	}

	// limit > 200 → clamped to 200 (mirrors ListByAssessment's ceiling).
	if _, err := r.ListReleasedByLearner(context.Background(), subTenantID, subLearnerGCID, 999, 0); err != nil {
		t.Fatalf("ListReleasedByLearner: %v", err)
	}
	args = q.args[len(q.args)-1]
	if args[2] != 200 {
		t.Fatalf("expected limit clamped to 200; got %v", args[2])
	}
}

func TestSQLListReleasedSubmissionsByLearner_CarriesVisibilityGate(t *testing.T) {
	t.Parallel()
	// Belt-and-braces on the const itself: the release visibility gate and
	// the soft-delete filter must never be edited out of the SQL.
	for _, want := range []string{"state = 'RELEASED'", "deleted_at IS NULL"} {
		if !strings.Contains(pg.SQLListReleasedSubmissionsByLearner, want) {
			t.Fatalf("SQLListReleasedSubmissionsByLearner missing %q", want)
		}
	}
}
