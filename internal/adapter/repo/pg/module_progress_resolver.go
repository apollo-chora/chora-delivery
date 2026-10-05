// module_progress_resolver.go — pg adapter for moduleprogress.CompletionResolver.
//
// Maps a completed reference (kind+ref: an atom id or an assessment id) to the
// module targets it advances FOR AN ENROLLED learner, in ONE intra-chora_delivery
// query:
//
//	course_content_items (kind,ref → item_id, course)
//	  ⋈ course_module_items (item_id → module)
//	  ⋈ course_modules      (module active)
//	  ⋈ course_enrollments  (learner enrolled in the module's course)
//
// The enrolment join is the pollution guard: a learner who completes an atom
// OUTSIDE any course they take (e.g. self-paced discovery) produces zero targets,
// so the projection never writes a spurious row into a cohort roll-up. Cross-DB
// queries FORBIDDEN — all four tables are in chora_delivery; RLS scopes tenant.
package pg

import (
	"context"
	"strings"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	moduleprogress "github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// SQLResolveEnrolledModuleTargets is the enrolment-gated resolution query.
// $1 tenant_id, $2 gcid, $3 kind, $4 ref.
const SQLResolveEnrolledModuleTargets = `
SELECT DISTINCT cmi.content_item_id, cmi.module_id, cm.course_id
FROM course_content_items cci
JOIN course_module_items cmi
  ON cmi.tenant_id = cci.tenant_id
 AND cmi.content_item_id = cci.item_id
 AND cmi.deleted_at IS NULL
JOIN course_modules cm
  ON cm.tenant_id = cci.tenant_id
 AND cm.id = cmi.module_id
 AND cm.deleted_at IS NULL
JOIN course_enrollments ce
  ON ce.tenant_id = cci.tenant_id
 AND ce.course_id = cm.course_id
 AND ce.gcid = $2
 AND ce.deleted_at IS NULL
WHERE cci.tenant_id = $1
  AND cci.kind = $3
  AND cci.ref = $4
  AND cci.deleted_at IS NULL
`

// ProgressResolver is the Postgres-backed moduleprogress.CompletionResolver impl.
type ProgressResolver struct {
	tx TxRunner
}

// NewProgressResolver constructs a ProgressResolver around a TxRunner.
func NewProgressResolver(tx TxRunner) *ProgressResolver {
	return &ProgressResolver{tx: tx}
}

// Compile-time assertion.
var _ moduleprogress.CompletionResolver = (*ProgressResolver)(nil)

// ResolveEnrolledTargets returns the (content-item, module, course) targets a
// learner's completion of (kind, ref) advances. Empty (not an error) when the
// content is in no module of a course the learner takes.
func (r *ProgressResolver) ResolveEnrolledTargets(ctx context.Context, tenantID, gcid, kind, ref string) ([]moduleprogress.CompletionTarget, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || strings.TrimSpace(gcid) == "" || strings.TrimSpace(kind) == "" || strings.TrimSpace(ref) == "" {
		return nil, ErrProgressMissingScope
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	out := make([]moduleprogress.CompletionTarget, 0)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rs, err := q.Query(ctx, SQLResolveEnrolledModuleTargets, tenantID, gcid, kind, ref)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var t moduleprogress.CompletionTarget
			if err := rs.Scan(&t.ContentItemID, &t.ModuleID, &t.CourseID); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
