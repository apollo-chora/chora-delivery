// component_completion.go - Postgres adapter resolving which DECLARED
// components a learner has actually completed (CHO-2222, ADR-190 D1).
//
// The offering DECLARES components (Offering.CompletionRequirement); this
// resolves the learner-side half of the gate. EvaluateCertificate then asks the
// only question that matters: is every declared component in the completed set?
//
// # Intra-domain by construction
//
// Both sources - submissions and exam_results - live in chora_delivery, the same
// database this repo already reads offerings and certifications from. No
// cross-DB query, no dblink, no cross-domain event (ddd-enforcement #1).
//
// # What counts as "complete"
//
//	assessment ⇒ submissions   WHERE state = 'RELEASED' AND passed IS TRUE
//	exam       ⇒ exam_results  WHERE outcome = 'PASS'
//
// PASSED, not merely finished. A learner who FAILED a declared assessment has
// not completed it for the purpose of a credential, and treating "sat it" as
// "completed it" would certify them anyway: the offering's CompletionPolicy
// cut-score only ever sees the ONE submission whose release triggered the
// engine, so a failed sibling component would go unexamined by every other gate.
// This also matches the exam lane, where only PASS certifies and that is
// owner-ratified (exam_result_inbox.go).
//
// RELEASED, not merely graded. completion_inbox.go rides .released.v1 precisely
// so a certificate cannot leak an outcome to the learner ahead of their
// instructor; counting a graded-but-unreleased sibling would reopen that leak
// through the side door.
//
// Multiple attempts (max_attempts up to 3) mean a learner can hold several
// submissions per assessment. EXISTS-style semantics are correct: any released,
// passing attempt completes the component.
//
// # candidate_ref IS the learner GCID
//
// Not a candidate-row id. This is the established, owner-ratified reading of the
// column (exam_result_inbox.go:34-35 "candidate_ref IS the learner GCID"), and
// the exam-result push handler treats it the same way
// (exam_result_pubsub_handler.go:56 reads it straight into a gcid). The exam
// lane has certified live traffic on that basis.
//
// # Fail loud
//
// CompletedComponents returns an ERROR, never a (T, bool): an infra/RLS failure
// reported as "not complete" is indistinguishable from a policy decision, and
// would withhold an EARNED credential in silence. That is the exact defect
// CHO-2184 fixed one layer up, and CHO-2157 existed because a silently withheld
// certificate looked like success.
package pg

import (
	"context"
	"fmt"
	"strings"

	"github.com/apollo-chora/chora-common/rls"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

const (
	// SQLCompletedAssessmentComponents returns the declared assessment refs this
	// learner has a RELEASED, PASSING submission for. The refs are cast to
	// uuid[]: assessment_id is a UUID column, and the declaration guard
	// (canonicalRef) has already refused any ref that is not a UUID, so the cast
	// cannot 22P02 on admin input.
	SQLCompletedAssessmentComponents = `
SELECT DISTINCT assessment_id::text
FROM submissions
WHERE tenant_id = $1::uuid
  AND learner_gcid = $2::uuid
  AND assessment_id = ANY($3::uuid[])
  AND state = 'RELEASED'
  AND passed IS TRUE
  AND deleted_at IS NULL`

	// SQLCompletedExamComponents returns the declared exam refs this learner
	// PASSED. candidate_ref IS the learner GCID (see the file header).
	SQLCompletedExamComponents = `
SELECT DISTINCT exam_id::text
FROM exam_results
WHERE tenant_id = $1::uuid
  AND candidate_ref = $2::uuid
  AND exam_id = ANY($3::uuid[])
  AND outcome = 'PASS'
  AND deleted_at IS NULL`
)

// -----------------------------------------------------------------------------
// ComponentCompletionRepo
// -----------------------------------------------------------------------------

// ComponentCompletionRepo resolves declared-component completion from
// chora_delivery's own tables.
type ComponentCompletionRepo struct {
	tx TxRunner
}

// NewComponentCompletionRepo constructs the repo around a TxRunner. A nil
// TxRunner makes CompletedComponents return ErrNotImplemented: an unwired repo
// is a WIRING BUG, not a learner who has completed nothing (CHO-2184).
func NewComponentCompletionRepo(tx TxRunner) *ComponentCompletionRepo {
	return &ComponentCompletionRepo{tx: tx}
}

// componentKey is the comparison form: identity is the (kind, ref) PAIR, since
// an assessment and an exam may share an id space.
type componentKey struct {
	kind domain.CompletionComponentKind
	ref  string
}

// CompletedComponents returns the subset of `declared` this learner has
// completed, in declaration order.
//
// It takes the declared set rather than resolving everything a learner has ever
// done: the gate only ever asks about declared components, and passing the set
// in bounds the query, keeps it index-friendly, and lets an unresolvable kind be
// refused LOUDLY instead of silently contributing nothing.
//
// tenantID is passed explicitly AND carried on the ctx: the ctx is what
// rls.ApplySession reads (the tenant is NOT taken from these args), while the
// explicit predicate is defence in depth, mirroring SQLListOfferingsByTenant.
func (r *ComponentCompletionRepo) CompletedComponents(
	ctx context.Context,
	tenantID, learnerGCID string,
	declared []domain.CompletionComponent,
) ([]domain.CompletionComponent, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if len(declared) == 0 {
		// Vacuous: nothing declared, nothing to resolve. Not a DB round-trip.
		return nil, nil
	}

	// Partition by kind. An unresolvable kind is refused here rather than
	// skipped: a skipped component would be reported as incomplete forever, and
	// the withheld certificate would read as policy rather than as the missing
	// feature it is. See CompletionComponentKind.Resolvable.
	var assessmentRefs, examRefs []string
	for i, c := range declared {
		ref := strings.TrimSpace(c.Ref)
		switch c.Kind {
		case domain.ComponentKindAssessment:
			assessmentRefs = append(assessmentRefs, ref)
		case domain.ComponentKindExam:
			examRefs = append(examRefs, ref)
		default:
			return nil, fmt.Errorf("pg: resolve completed components: %w (declared[%d].kind=%q)",
				domain.ErrCompletionComponentUnresolvable, i, c.Kind)
		}
	}

	done := make(map[componentKey]struct{}, len(declared))
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if len(assessmentRefs) > 0 {
			refs, err := scanComponentRefs(ctx, q, SQLCompletedAssessmentComponents, tenantID, learnerGCID, assessmentRefs)
			if err != nil {
				return fmt.Errorf("pg: completed assessment components: %w", err)
			}
			for _, ref := range refs {
				done[componentKey{kind: domain.ComponentKindAssessment, ref: ref}] = struct{}{}
			}
		}
		if len(examRefs) > 0 {
			refs, err := scanComponentRefs(ctx, q, SQLCompletedExamComponents, tenantID, learnerGCID, examRefs)
			if err != nil {
				return fmt.Errorf("pg: completed exam components: %w", err)
			}
			for _, ref := range refs {
				done[componentKey{kind: domain.ComponentKindExam, ref: ref}] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err // LOUD: an infra/RLS failure is NOT "nothing completed"
	}

	// Rebuild in declaration order, returning the DECLARED form of each
	// component (not the DB's echo) so the caller's set comparison matches.
	out := make([]domain.CompletionComponent, 0, len(done))
	for _, c := range declared {
		if _, ok := done[componentKey{kind: c.Kind, ref: strings.TrimSpace(c.Ref)}]; ok {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// scanComponentRefs runs one ref-returning query and collects the single text
// column.
func scanComponentRefs(ctx context.Context, q Querier, sql, tenantID, learnerGCID string, refs []string) ([]string, error) {
	rows, err := q.Query(ctx, sql, tenantID, learnerGCID, refs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
