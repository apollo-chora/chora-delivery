// course_prerequisite.go — pg adapter for the Course Prerequisite DAG edges
// (ADR-226), persisting chora_delivery.course_prerequisites per migration
// 0041_course_prerequisites.up.sql.
//
// Every read/write wraps rls.ApplySession first so the tenant_isolation policy
// filters by chora.tenant_id (defence-in-depth over the explicit tenant_id in
// each WHERE). Cross-DB queries FORBIDDEN — this only touches chora_delivery;
// prerequisite_course_id is a bare intra-DB course reference (no cross-DB link).
//
// Soft-delete + revive: the unique index on (tenant, course, prereq) is
// NON-partial, so there is at most one physical row per edge across its whole
// history. Upsert INSERTs, or on conflict UPDATEs the kind AND clears deleted_at
// (revive) — so re-adding a removed edge reuses the same row. List queries
// filter deleted_at IS NULL. Never hard-delete.
package pg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// ErrPrereqMissingTenant is returned when tenant_id is empty — the write cannot
// be RLS-scoped without it. Fail loud.
var ErrPrereqMissingTenant = errors.New("pg: course prerequisite requires a non-empty tenant_id (RLS scope)")

// ErrPrereqMissingCourse is returned when a course id on the edge is empty.
var ErrPrereqMissingCourse = errors.New("pg: course prerequisite requires non-empty course ids")

// -----------------------------------------------------------------------------
// SQL templates (exported so CI/lint can grep them)
// -----------------------------------------------------------------------------

// SQLUpsertCoursePrerequisite inserts an edge or, on the unique (tenant, course,
// prerequisite) conflict, updates its kind and REVIVES a previously soft-deleted
// row (deleted_at = NULL). Idempotent. $6 is reused for created_at + updated_at.
const SQLUpsertCoursePrerequisite = `
INSERT INTO course_prerequisites (
    id, tenant_id, course_id, prerequisite_course_id, kind, created_at, updated_at, deleted_at
) VALUES ($1, $2, $3, $4, $5, $6, $6, NULL)
ON CONFLICT (tenant_id, course_id, prerequisite_course_id) DO UPDATE SET
    kind       = EXCLUDED.kind,
    updated_at = EXCLUDED.updated_at,
    deleted_at = NULL
`

// SQLSoftDeleteCoursePrerequisite soft-deletes one active edge (idempotent — a
// missing / already-deleted edge affects 0 rows).
const SQLSoftDeleteCoursePrerequisite = `
UPDATE course_prerequisites
SET deleted_at = $4, updated_at = $4
WHERE tenant_id = $1 AND course_id = $2 AND prerequisite_course_id = $3 AND deleted_at IS NULL
`

// SQLSelectCoursePrerequisitesByCourse lists the active prerequisites of one
// course, ordered by prerequisite course id for determinism.
const SQLSelectCoursePrerequisitesByCourse = `
SELECT prerequisite_course_id, kind
FROM course_prerequisites
WHERE tenant_id = $1 AND course_id = $2 AND deleted_at IS NULL
ORDER BY prerequisite_course_id ASC
`

// SQLSelectCoursePrerequisitesByTenant returns every active edge in the tenant —
// the graph the service reads for cycle detection + reverse lookups.
const SQLSelectCoursePrerequisitesByTenant = `
SELECT course_id, prerequisite_course_id, kind
FROM course_prerequisites
WHERE tenant_id = $1 AND deleted_at IS NULL
ORDER BY course_id ASC, prerequisite_course_id ASC
`

// -----------------------------------------------------------------------------
// CoursePrerequisiteRepo
// -----------------------------------------------------------------------------

// CoursePrerequisiteRepo is the Postgres-backed delivery.CoursePrerequisitePort
// impl. A nil TxRunner degrades every method to ErrNotImplemented (fail-loud,
// matches the other chora-delivery repos).
type CoursePrerequisiteRepo struct {
	tx TxRunner
}

// NewCoursePrerequisiteRepo constructs a CoursePrerequisiteRepo around a TxRunner.
func NewCoursePrerequisiteRepo(tx TxRunner) *CoursePrerequisiteRepo {
	return &CoursePrerequisiteRepo{tx: tx}
}

// Compile-time assertion: the repo satisfies the domain port.
var _ domain.CoursePrerequisitePort = (*CoursePrerequisiteRepo)(nil)

// Upsert adds or revives-and-updates an edge under the tenant scope.
func (r *CoursePrerequisiteRepo) Upsert(ctx context.Context, tenantID string, e domain.PrerequisiteEdge) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return ErrPrereqMissingTenant
	}
	if strings.TrimSpace(e.CourseID) == "" || strings.TrimSpace(e.PrerequisiteCourseID) == "" {
		return ErrPrereqMissingCourse
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	now := time.Now().UTC()
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLUpsertCoursePrerequisite,
			domain.NewUUIDv7(), tenantID, e.CourseID, e.PrerequisiteCourseID, string(e.Kind), now,
		); err != nil {
			return err
		}
		return nil
	})
}

// Remove soft-deletes the (tenant, course, prerequisite) edge (idempotent).
func (r *CoursePrerequisiteRepo) Remove(ctx context.Context, tenantID, courseID, prerequisiteCourseID string) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return ErrPrereqMissingTenant
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	now := time.Now().UTC()
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLSoftDeleteCoursePrerequisite, tenantID, courseID, prerequisiteCourseID, now); err != nil {
			return err
		}
		return nil
	})
}

// ListForCourse returns the active prerequisites of courseID in the tenant.
func (r *CoursePrerequisiteRepo) ListForCourse(ctx context.Context, tenantID, courseID string) ([]domain.CoursePrerequisite, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrPrereqMissingTenant
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var out []domain.CoursePrerequisite
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLSelectCoursePrerequisitesByCourse, tenantID, courseID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var prereqID, kind string
			if scanErr := rows.Scan(&prereqID, &kind); scanErr != nil {
				return scanErr
			}
			out = append(out, domain.CoursePrerequisite{
				PrerequisiteCourseID: prereqID,
				Kind:                 domain.PrereqKind(kind),
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListForTenant returns every active edge in the tenant.
func (r *CoursePrerequisiteRepo) ListForTenant(ctx context.Context, tenantID string) ([]domain.PrerequisiteEdge, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrPrereqMissingTenant
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var out []domain.PrerequisiteEdge
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, qErr := q.Query(ctx, SQLSelectCoursePrerequisitesByTenant, tenantID)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		for rows.Next() {
			var courseID, prereqID, kind string
			if scanErr := rows.Scan(&courseID, &prereqID, &kind); scanErr != nil {
				return scanErr
			}
			out = append(out, domain.PrerequisiteEdge{
				CourseID:             courseID,
				PrerequisiteCourseID: prereqID,
				Kind:                 domain.PrereqKind(kind),
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
