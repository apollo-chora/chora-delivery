// course_learner_progress.go - pg adapter for the CourseLearnerProgress
// projection (R+ ASYNC analytics, epic CHO-1827) per migration
// 0054_course_learner_progress.
//
// Advance/Complete are the load-mutate-persist write paths the push-inbox
// subscriber calls. Each runs the whole unit under ONE tx: an ENSURE insert
// (ON CONFLICT DO NOTHING) guarantees the (tenant,gcid,course) row exists, then
// SELECT ... FOR UPDATE locks it so a concurrent advance and completion for the
// same learner+course serialize (no lost update - the aggregate mutation runs on
// the locked row). The domain aggregate's RecordAdvance/RecordCompletion run
// INSIDE the tx, so every invariant executes on the production path.
//
// Every read/write wraps rls.ApplySession FIRST, so the tenant_isolation policy
// on course_learner_progress filters by chora.tenant_id. Skipping it against a
// FORCE-RLS table is not an error, it is a SILENT NO-OP: 0 rows, no complaint,
// because a pooled connection leaves the GUC at ” and the policy matches
// nothing. tracing.WithTenantID puts the tenant on ctx because ApplySession
// reads it from THERE, not from these arguments.
//
// Cross-DB queries FORBIDDEN - this touches only chora_delivery.
// course_learner_progress. course_id is a bare cross-aggregate UUID and path_id
// refers to a row in ANOTHER database (chora_consumption), so neither is an FK;
// path_id is correlation only and is never joined.
//
// No outbox tee here (deliberate, unlike student_module_progress): this
// projection is a READ MODEL for the Analytics tab and announces nothing. The
// completion event it consumes is already the announcement; re-emitting a
// delivery-side twin would give the platform two events for one fact and invite
// exactly the double-award class of bug the graded.v1 lane already learned about.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	courseprogress "github.com/apollo-chora/chora-delivery/internal/domain/courseprogress"
)

// ErrCourseProgressMissingScope is returned when a required RLS-scope field is
// empty. Fail-loud: without a tenant the write would silently affect 0 rows.
var ErrCourseProgressMissingScope = errors.New("pg: course_learner_progress requires non-empty tenant_id/gcid/course_id (RLS scope)")

const courseProgressSelectCols = `id, tenant_id, gcid, course_id, path_id, ` +
	`completed_atoms, total_atoms, is_complete, completed_at, last_advance_at, ` +
	`created_at, updated_at, deleted_at`

// SQLEnsureCourseProgressRow inserts a fresh zero projection for
// (tenant,gcid,course) or does nothing if one already exists (the partial UNIQUE
// index). $6 seeds created_at + updated_at. This makes the subsequent
// SELECT ... FOR UPDATE always find a row to lock - closing the first-event
// insert race between two concurrent deliveries.
const SQLEnsureCourseProgressRow = `
INSERT INTO course_learner_progress (
    id, tenant_id, gcid, course_id, path_id,
    completed_atoms, total_atoms, is_complete, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, 0, 0, false, $6, $6)
ON CONFLICT (tenant_id, gcid, course_id) WHERE deleted_at IS NULL DO NOTHING
`

// SQLSelectCourseProgressForUpdate row-locks the active projection.
const SQLSelectCourseProgressForUpdate = `
SELECT ` + courseProgressSelectCols + `
FROM course_learner_progress
WHERE tenant_id = $1 AND gcid = $2 AND course_id = $3 AND deleted_at IS NULL
FOR UPDATE
`

// SQLSelectCourseProgressByLearnerCourse loads one active projection (no lock).
const SQLSelectCourseProgressByLearnerCourse = `
SELECT ` + courseProgressSelectCols + `
FROM course_learner_progress
WHERE tenant_id = $1 AND gcid = $2 AND course_id = $3 AND deleted_at IS NULL
`

// SQLUpdateCourseProgress writes the mutated aggregate onto the locked row.
const SQLUpdateCourseProgress = `
UPDATE course_learner_progress
SET completed_atoms = $1, total_atoms = $2, is_complete = $3,
    completed_at = $4, last_advance_at = $5, updated_at = $6, path_id = $7
WHERE id = $8 AND deleted_at IS NULL
`

// SQLSelectCourseProgressByCourseIDs returns all active projections for the
// given courses (tenant-scoped) - the analytics roll-up's source.
const SQLSelectCourseProgressByCourseIDs = `
SELECT ` + courseProgressSelectCols + `
FROM course_learner_progress
WHERE tenant_id = $1 AND course_id = ANY($2::uuid[]) AND deleted_at IS NULL
ORDER BY course_id ASC, gcid ASC
`

// CourseProgressRepo is the Postgres-backed courseprogress.ProgressPort impl.
type CourseProgressRepo struct {
	tx TxRunner
}

// NewCourseProgressRepo constructs a CourseProgressRepo around a TxRunner. A nil
// TxRunner degrades every method to ErrNotImplemented (fail-loud, matches
// sibling repos) rather than pretending to persist.
func NewCourseProgressRepo(tx TxRunner) *CourseProgressRepo {
	return &CourseProgressRepo{tx: tx}
}

// Compile-time assertion.
var _ courseprogress.ProgressPort = (*CourseProgressRepo)(nil)

// mutate is the shared load-lock-apply-persist spine for Advance and Complete.
// apply runs on the LOCKED aggregate and reports whether state moved.
func (r *CourseProgressRepo) mutate(
	ctx context.Context,
	tenantID, gcid, courseID, pathID string,
	apply func(*courseprogress.CourseLearnerProgress) (bool, error),
) (bool, error) {
	if r == nil || r.tx == nil {
		return false, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(gcid) == "" || strings.TrimSpace(courseID) == "" {
		return false, ErrCourseProgressMissingScope
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	changed := false
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		// 1. Ensure the row exists so the lock has something to hold.
		seed, err := courseprogress.New(courseprogress.NewParams{
			TenantID: tenantID, GCID: gcid, CourseID: courseID, PathID: pathID,
		})
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, SQLEnsureCourseProgressRow,
			seed.ID, tenantID, gcid, courseID, nullIfEmpty(pathID), seed.CreatedAt.UTC()); err != nil {
			return fmt.Errorf("pg: ensure course progress row: %w", err)
		}
		// 2. Lock + load the active row (always present after the ensure).
		prog, err := scanCourseProgress(q.QueryRow(ctx, SQLSelectCourseProgressForUpdate, tenantID, gcid, courseID).Scan)
		if err != nil {
			return fmt.Errorf("pg: lock course progress row: %w", err)
		}
		// 3. Apply the domain mutation on the locked aggregate.
		didChange, err := apply(prog)
		if err != nil {
			return err
		}
		if !didChange {
			return nil
		}
		// 4. Persist. path_id is backfilled opportunistically: the ensure may have
		// created the row from an event that carried no path, and a later event
		// that does carry one should fill it in rather than leave it null forever.
		if strings.TrimSpace(prog.PathID) == "" {
			prog.PathID = strings.TrimSpace(pathID)
		}
		if _, err := q.Exec(ctx, SQLUpdateCourseProgress,
			prog.CompletedAtoms, prog.TotalAtoms, prog.IsComplete,
			prog.CompletedAt, prog.LastAdvanceAt, prog.UpdatedAt.UTC(),
			nullIfEmpty(prog.PathID), prog.ID); err != nil {
			return fmt.Errorf("pg: update course progress: %w", err)
		}
		changed = true
		return nil
	})
	return changed, err
}

// Advance folds one learning_path.advanced.v1 into the learner's projection.
func (r *CourseProgressRepo) Advance(ctx context.Context, tenantID, gcid, courseID, pathID string, completedAtoms, totalAtoms int, occurredAt time.Time) (bool, error) {
	return r.mutate(ctx, tenantID, gcid, courseID, pathID, func(p *courseprogress.CourseLearnerProgress) (bool, error) {
		return p.RecordAdvance(completedAtoms, totalAtoms, occurredAt)
	})
}

// Complete folds one learning_path.completed.v1 into the learner's projection.
func (r *CourseProgressRepo) Complete(ctx context.Context, tenantID, gcid, courseID, pathID string, occurredAt time.Time) (bool, error) {
	return r.mutate(ctx, tenantID, gcid, courseID, pathID, func(p *courseprogress.CourseLearnerProgress) (bool, error) {
		return p.RecordCompletion(occurredAt)
	})
}

// GetByLearnerCourse loads a learner's active projection for one course. A
// genuine miss is (nil, false, nil); an infra/RLS failure is a non-nil error, so
// a dead read can never masquerade as "this learner has no progress".
func (r *CourseProgressRepo) GetByLearnerCourse(ctx context.Context, tenantID, gcid, courseID string) (*courseprogress.CourseLearnerProgress, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(gcid) == "" || strings.TrimSpace(courseID) == "" {
		return nil, false, ErrCourseProgressMissingScope
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	var (
		out   *courseprogress.CourseLearnerProgress
		found bool
	)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		p, err := scanCourseProgress(q.QueryRow(ctx, SQLSelectCourseProgressByLearnerCourse, tenantID, gcid, courseID).Scan)
		if err != nil {
			if isNoRows(err) {
				return nil // genuine miss
			}
			return fmt.Errorf("pg: get course progress: %w", err)
		}
		out, found = p, true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, found, nil
}

// ListByCourseIDs returns active projections for the given courses, tenant-scoped.
func (r *CourseProgressRepo) ListByCourseIDs(ctx context.Context, tenantID string, courseIDs []string) ([]*courseprogress.CourseLearnerProgress, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrCourseProgressMissingScope
	}
	if len(courseIDs) == 0 {
		return []*courseprogress.CourseLearnerProgress{}, nil
	}
	ctx = tracing.WithTenantID(ctx, tenantID)

	out := make([]*courseprogress.CourseLearnerProgress, 0, len(courseIDs))
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rs, err := q.Query(ctx, SQLSelectCourseProgressByCourseIDs, tenantID, courseIDs)
		if err != nil {
			return fmt.Errorf("pg: list course progress: %w", err)
		}
		defer rs.Close()
		for rs.Next() {
			p, err := scanCourseProgress(rs.Scan)
			if err != nil {
				return fmt.Errorf("pg: scan course progress: %w", err)
			}
			out = append(out, p)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanCourseProgress materialises one row into the aggregate.
func scanCourseProgress(scan func(dest ...any) error) (*courseprogress.CourseLearnerProgress, error) {
	var (
		p             courseprogress.CourseLearnerProgress
		pathID        *string
		completedAt   *time.Time
		lastAdvanceAt *time.Time
		deletedAt     *time.Time
	)
	if err := scan(
		&p.ID, &p.TenantID, &p.GCID, &p.CourseID, &pathID,
		&p.CompletedAtoms, &p.TotalAtoms, &p.IsComplete, &completedAt, &lastAdvanceAt,
		&p.CreatedAt, &p.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if pathID != nil {
		p.PathID = *pathID
	}
	p.CompletedAt = completedAt
	p.LastAdvanceAt = lastAdvanceAt
	p.DeletedAt = deletedAt
	return &p, nil
}

// nullIfEmpty maps "" to a NULL text value so an absent path_id is stored as
// NULL rather than an empty string (they are different facts).
func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}
