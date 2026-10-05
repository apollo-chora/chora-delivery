// enrollment.go — Postgres adapter for chora_delivery.course_enrollments.
//
// SCHEMA: see migrations/0001_initial.sql (`course_enrollments` table).
//
// Replaces the in-memory domain.EnrollmentRegistry wiring for the CJ#2
// Stripe Checkout path. Before this adapter, PaymentsSubscriber wrote
// only to the in-memory registry — Enrolment rows never survived a pod
// restart and GET /api/v1/me/enrolments returned empty even after a
// successful payment. The course_enrollments table from
// migrations/0001_initial.sql was dead schema — no Go code touched it.
// This is the [[feedback-no-stubs-real-wiring]] fix for the Bug-2 path
// surfaced during CJ#2 last-gap diagnosis 2026-05-26.
//
// Resilience-priority directive (`feedback_resilience_priority`):
//
//   - Idempotency: Register uses INSERT ... ON CONFLICT (course_id, gcid)
//     DO NOTHING RETURNING + falls back to SELECT-by-natural-key when
//     RETURNING is empty (mirrors ApplicationRepo.SubmitOrGet).
//   - Soft-delete-aware: every read filters `WHERE deleted_at IS NULL`.
//   - RLS: every read/write applies rls.ApplySession before the user
//     query so the row-level policy on course_enrollments enforces
//     tenant isolation. Caller MUST set tenant via tracing.WithTenantID
//     before invoking — rls.ApplySession reads from
//     tracing.TenantIDFromContext.
//   - PgBouncer-safe: SET LOCAL chora.tenant_id only inside RunInTx.
//
// Cross-DB queries forbidden — chora-delivery reads only chora_delivery.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// ErrEnrollmentMissingTenant is returned when tenant_id is empty — Register
// cannot RLS-scope the write without it. Fail loud per [[no-stubs-real-wiring]].
var ErrEnrollmentMissingTenant = errors.New("pg: enrollment requires a non-empty tenant_id (RLS scope)")

// ErrEnrollmentMissingCourse is returned when course_id is empty.
var ErrEnrollmentMissingCourse = errors.New("pg: enrollment requires a non-empty course_id")

// ErrEnrollmentMissingGCID is returned when gcid is empty.
var ErrEnrollmentMissingGCID = errors.New("pg: enrollment requires a non-empty gcid")

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

// SQLInsertEnrollment idempotently inserts on the (course_id, gcid) UNIQUE
// constraint. The role column defaults to 'learner' per the table default;
// tenant_id is set explicitly (NOT NULL).
//
// One $5 param is reused across enrolled_at + created_at + updated_at so
// the candidate Enrollment.EnrolledAt timestamp is the single source of
// truth on a fresh insert. RETURNING column order matches scanEnrollment.
//
// CHO-2161 — the conflict target is a PLAIN unique index on (course_id, gcid);
// it does NOT exclude soft-deleted rows. Unenrol soft-deletes, so the tombstone
// still occupies the natural key. With the previous `DO NOTHING` the RETURNING
// clause yielded no row, and the caller's natural-key fallback SELECT filters
// `deleted_at IS NULL` — so it found nothing and Register failed with
// "conflict but natural-key lookup empty". An unenrolled learner could NEVER be
// re-enrolled.
//
// `DO UPDATE … WHERE course_enrollments.deleted_at IS NOT NULL` REVIVES the
// tombstone in place (keeping its original enrollment_id, and preserving
// completed_at/passed history — we never hard-delete and never widen the
// index). The three cases:
//
//	no row        → INSERT           → RETURNING the new row
//	tombstoned    → DO UPDATE        → RETURNING the revived row
//	already active→ WHERE is false   → no RETURNING row → caller's natural-key
//	                                   SELECT finds it → idempotent (unchanged)
const SQLInsertEnrollment = `
INSERT INTO course_enrollments (
    enrollment_id, tenant_id, course_id, gcid, role,
    enrolled_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, 'learner',
    $5, $5, $5
)
ON CONFLICT (course_id, gcid)
DO UPDATE SET
    deleted_at  = NULL,
    status      = 'active',
    enrolled_at = EXCLUDED.enrolled_at,
    updated_at  = EXCLUDED.updated_at
WHERE course_enrollments.deleted_at IS NOT NULL
RETURNING enrollment_id, tenant_id, course_id, gcid, enrolled_at, deleted_at, status, completed_at, passed
`

// SQLSelectEnrollmentByNaturalKey is the by-(tenant,course,gcid) SELECT used
// by both the post-conflict load AND GetByCourseAndGCID. The tenant_id +
// course_id pair narrows fast on the UNIQUE (course_id, gcid) index.
const SQLSelectEnrollmentByNaturalKey = `
SELECT enrollment_id, tenant_id, course_id, gcid, enrolled_at, deleted_at,
       status, completed_at, passed
FROM course_enrollments
WHERE tenant_id = $1
  AND course_id = $2
  AND gcid = $3
  AND deleted_at IS NULL
`

// SQLSelectEnrollmentByID is the by-id SELECT used by Get. RLS still
// applies (defence-in-depth) — the SET LOCAL chora.tenant_id wrapper
// ensures only the caller's tenant's rows are returned even though the
// PK is globally unique.
const SQLSelectEnrollmentByID = `
SELECT enrollment_id, tenant_id, course_id, gcid, enrolled_at, deleted_at,
       status, completed_at, passed
FROM course_enrollments
WHERE enrollment_id = $1
  AND deleted_at IS NULL
`

// SQLListEnrollmentsByGCID returns active enrolments for (tenant, gcid)
// ordered newest-first — the natural shape for the GET /me/enrolments
// dashboard panel (recently-enrolled courses surface at the top).
const SQLListEnrollmentsByGCID = `
SELECT enrollment_id, tenant_id, course_id, gcid, enrolled_at, deleted_at,
       status, completed_at, passed
FROM course_enrollments
WHERE tenant_id = $1
  AND gcid = $2
  AND deleted_at IS NULL
ORDER BY enrolled_at DESC
`

// SQLListEnrollmentsByCourse returns active enrolments for a (tenant, course)
// pair ordered oldest-first — the natural shape for the course Roster READ
// VIEW (R+ M4 GET /api/v1/rosters/{courseId}): learners surface in
// enrolment order (the inmem CourseRosterRepo re-sorts oldest-first +
// GCID-tiebreak, so this ORDER BY makes the source stream already match the
// presentation order). Mirror of SQLListEnrollmentsByGCID with the access
// key flipped from gcid to course_id. RETURNING column order matches
// scanEnrollment.
const SQLListEnrollmentsByCourse = `
SELECT enrollment_id, tenant_id, course_id, gcid, enrolled_at, deleted_at,
       status, completed_at, passed
FROM course_enrollments
WHERE tenant_id = $1
  AND course_id = $2
  AND deleted_at IS NULL
ORDER BY enrolled_at ASC
`

// SQLCountEnrollmentsByCourse returns the active enrolment count for a
// (tenant, course) pair — used by course-detail capacity displays.
const SQLCountEnrollmentsByCourse = `
SELECT COUNT(*)
FROM course_enrollments
WHERE tenant_id = $1
  AND course_id = $2
  AND deleted_at IS NULL
`

// SQLMarkEnrollmentCompleted persists the completion lifecycle fields onto an
// existing, non-cancelled row. RLS-scoped via the SET LOCAL wrapper; the
// `deleted_at IS NULL` guard is defence-in-depth so a cancelled enrolment can
// never be completed at the SQL layer (the domain already refuses it).
//
// Arg order matches the $N placeholders: enrollment_id, status, completed_at,
// passed.
const SQLMarkEnrollmentCompleted = `
UPDATE course_enrollments
SET status = $2,
    completed_at = $3,
    passed = $4,
    updated_at = now()
WHERE enrollment_id = $1
  AND deleted_at IS NULL
`

// SQLCancelEnrollment soft-deletes an enrolment row (deleted_at=now, status=
// cancelled) RLS-scoped to the caller's tenant. This is the persist call the
// DELETE cancel path was missing — without it a loaded aggregate's SoftDelete()
// is a silent no-op on Postgres (Get materialises a fresh struct, never written
// back). The `deleted_at IS NULL` guard makes it idempotent: re-cancelling an
// already-cancelled row updates zero rows. Arg order: enrollment_id ($1),
// tenant_id ($2).
const SQLCancelEnrollment = `
UPDATE course_enrollments
SET deleted_at = now(),
    status = 'cancelled',
    updated_at = now()
WHERE enrollment_id = $1
  AND tenant_id = $2
  AND deleted_at IS NULL
`

// -----------------------------------------------------------------------------
// EnrollmentRepo
// -----------------------------------------------------------------------------

// EnrollmentRepo is the Postgres-backed EnrollmentPort impl.
type EnrollmentRepo struct {
	tx TxRunner
}

// NewEnrollmentRepo constructs an EnrollmentRepo around a TxRunner.
//
// Passing nil yields a Repo that returns ErrNotImplemented from every
// method — matches CourseRepo / ApplicationRepo fail-loud convention.
// Production wires this via cmd/server/main.go behind the same pool gate
// as catalogue + testSets.
func NewEnrollmentRepo(tx TxRunner) *EnrollmentRepo {
	return &EnrollmentRepo{tx: tx}
}

// Register either inserts a fresh enrolment row OR returns the existing
// row for (tenant, course, gcid). Idempotent on the UNIQUE (course_id,
// gcid) constraint — note that course_id alone is sufficient for
// uniqueness across tenants because course_id is FK to courses, which is
// per-tenant.
//
// On conflict (DO NOTHING returns 0 rows), falls back to a natural-key
// SELECT and returns the pre-existing row. Both paths return the same
// (enrollment, nil) shape — the EnrollmentTracker layer (the caller for
// the PaymentsSubscriber path) re-derives "was this a fresh insert?" by
// peeking via GetByCourseAndGCID first.
func (r *EnrollmentRepo) Register(ctx context.Context, tenantID, courseID, gcid string) (*domain.Enrollment, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrEnrollmentMissingTenant
	}
	if strings.TrimSpace(courseID) == "" {
		return nil, ErrEnrollmentMissingCourse
	}
	if strings.TrimSpace(gcid) == "" {
		return nil, ErrEnrollmentMissingGCID
	}

	// Mint the candidate enrolment first (UUIDv7 ID + EnrolledAt). On
	// conflict the candidate is discarded and the existing row loaded.
	candidate, err := domain.NewEnrollment(tenantID, courseID, gcid)
	if err != nil {
		return nil, err
	}

	var loaded *domain.Enrollment
	err = r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLInsertEnrollment,
			candidate.ID, candidate.TenantID, candidate.CourseID, candidate.GCID,
			candidate.EnrolledAt,
		)
		e, scanErr := scanEnrollment(row.Scan)
		if scanErr == nil {
			loaded = e
			return nil
		}
		// Conflict path — INSERT returned 0 rows. Load existing via
		// natural-key SELECT. The fall-through is the only branch that
		// hits when RETURNING is empty on DO NOTHING.
		existingRow := q.QueryRow(ctx, SQLSelectEnrollmentByNaturalKey,
			tenantID, courseID, gcid,
		)
		e2, lookupErr := scanEnrollment(existingRow.Scan)
		if lookupErr != nil {
			return fmt.Errorf("pg: enrollment register: conflict but natural-key lookup empty: %w", lookupErr)
		}
		loaded = e2
		return nil
	})
	if err != nil {
		return nil, err
	}
	return loaded, nil
}

// Get looks up an enrolment by enrollment_id, RLS-scoped to the caller's
// tenant. Returns (enrollment, true, nil) on hit, (nil, false, nil) on
// miss (including cross-tenant rows hidden by RLS), (nil, false, err) on
// infra error.
//
// Used by the V1 DELETE /v1/courses/{cid}/enrolments/{eid} cancel path —
// the caller submits the enrollment_id from a prior GET, the handler
// loads + soft-deletes + emits the cancellation event.
func (r *EnrollmentRepo) Get(ctx context.Context, enrollmentID string) (*domain.Enrollment, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var found *domain.Enrollment
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectEnrollmentByID, enrollmentID)
		e, scanErr := scanEnrollment(row.Scan)
		if scanErr != nil {
			return nil // miss
		}
		found = e
		return nil
	})
	if err != nil || found == nil {
		return nil, false, err
	}
	return found, true, nil
}

// GetByCourseAndGCID looks up the enrolment for (tenant, course, gcid).
//
// Returns (enrollment, true, nil) on hit, (nil, false, nil) on miss, (nil,
// false, err) on infra error. The "miss returns no error" shape mirrors
// the CourseRepo.Get convention so callers can short-circuit on !ok.
func (r *EnrollmentRepo) GetByCourseAndGCID(ctx context.Context, tenantID, courseID, gcid string) (*domain.Enrollment, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var found *domain.Enrollment
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectEnrollmentByNaturalKey, tenantID, courseID, gcid)
		e, scanErr := scanEnrollment(row.Scan)
		if scanErr != nil {
			return nil // not found — outer returns ok=false
		}
		found = e
		return nil
	})
	if err != nil || found == nil {
		return nil, false, err
	}
	return found, true, nil
}

// ListByGCID returns active enrolments for a learner inside a tenant,
// ordered newest-first.
//
// Used by GET /api/v1/me/enrolments — the dashboard panel that powers
// Comic Ch4 P3 dual-card render ("CSPO -> Instructor / CSM-Prep ->
// Learner") + the CJ#2 post-payment "Enrolled - view course" CTA.
func (r *EnrollmentRepo) ListByGCID(ctx context.Context, tenantID, gcid string) ([]*domain.Enrollment, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*domain.Enrollment
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rs, qErr := q.Query(ctx, SQLListEnrollmentsByGCID, tenantID, gcid)
		if qErr != nil {
			return qErr
		}
		defer rs.Close()
		for rs.Next() {
			e, scanErr := scanEnrollment(rs.Scan)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, e)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListByCourse returns active enrolments for a course inside a tenant,
// ordered oldest-first (enrolled_at ASC).
//
// This is the by-COURSE sibling of ListByGCID — same RLS-first +
// soft-delete-aware semantics, the access key is course_id instead of gcid.
// It is what makes *EnrollmentRepo satisfy
// domain.EnrollmentListByCoursePort, so cmd/server/main.go::wireRosters
// type-asserts the production pg adapter into that port and wires the
// course Roster READ VIEW (GET /api/v1/rosters/{courseId}). Without this
// method the assertion fails and the rosters route 404s (the gap this
// closes).
//
// RLS-scoped: SET LOCAL chora.tenant_id is applied before the SELECT, so
// the row-level policy on course_enrollments enforces tenant isolation
// even though the WHERE also pins tenant_id (defence in depth). Returns a
// nil slice (NOT an error) for a course with no enrolments — empty is a
// valid roster state, the handler renders an empty learners array.
func (r *EnrollmentRepo) ListByCourse(ctx context.Context, tenantID, courseID string) ([]*domain.Enrollment, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	var out []*domain.Enrollment
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rs, qErr := q.Query(ctx, SQLListEnrollmentsByCourse, tenantID, courseID)
		if qErr != nil {
			return qErr
		}
		defer rs.Close()
		for rs.Next() {
			e, scanErr := scanEnrollment(rs.Scan)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, e)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CountByCourse returns the active enrolment count for (tenant, course).
//
// Used by course-detail capacity displays + the public catalogue card's
// "X enrolled" badge. RLS filters to the caller's tenant on the read.
func (r *EnrollmentRepo) CountByCourse(ctx context.Context, tenantID, courseID string) (int, error) {
	if r == nil || r.tx == nil {
		return 0, ErrNotImplemented
	}
	var count int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLCountEnrollmentsByCourse, tenantID, courseID)
		return row.Scan(&count)
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// MarkCompleted persists the completion lifecycle fields (status /
// completed_at / passed) of an already-Completed Enrollment via an RLS-scoped
// UPDATE. The aggregate's domain transition (e.Complete) is done by the caller;
// this only persists the resulting state.
//
// Tenant context MUST be set (tracing.WithTenantID) so rls.ApplySession scopes
// the write; the WHERE deleted_at IS NULL clause additionally prevents
// resurrecting / completing a cancelled row. Satisfies
// domain.EnrollmentCompletionPort.
func (r *EnrollmentRepo) MarkCompleted(ctx context.Context, e *domain.Enrollment) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if e == nil {
		return fmt.Errorf("pg: MarkCompleted requires a non-nil enrollment")
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		_, err := q.Exec(ctx, SQLMarkEnrollmentCompleted,
			e.ID, string(e.Status), e.CompletedAt, e.Passed,
		)
		return err
	})
}

// Cancel soft-deletes the enrolment row (deleted_at=now, status=cancelled) via
// an RLS-scoped UPDATE — the persist call the V1 DELETE cancel path (and the
// offering roster-remove path) needs. Mirrors MarkCompleted: RunInTx +
// rls.ApplySession first, then a single Exec. Tenant is pinned in the WHERE
// (defence-in-depth alongside RLS) and the `deleted_at IS NULL` guard makes it
// idempotent — cancelling an already-cancelled or absent row updates zero rows
// and returns nil. Tenant context MUST be set (tracing.WithTenantID) so
// rls.ApplySession scopes the write. Satisfies EnrollmentPort.
func (r *EnrollmentRepo) Cancel(ctx context.Context, tenantID, enrollmentID string) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		_, err := q.Exec(ctx, SQLCancelEnrollment, enrollmentID, tenantID)
		return err
	})
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// scanEnrollment maps one row from a SELECT/RETURNING into the domain
// entity. Matches the column order shared across SQLInsertEnrollment +
// SQLSelectEnrollmentByNaturalKey + SQLListEnrollmentsByGCID:
//
//	enrollment_id, tenant_id, course_id, gcid, enrolled_at, deleted_at,
//	status, completed_at, passed
func scanEnrollment(scan func(dest ...any) error) (*domain.Enrollment, error) {
	var (
		id, tenantID, courseID, gcid string
		enrolledAt                   time.Time
		deletedAt                    *time.Time
		status                       string
		completedAt                  *time.Time
		passed                       *bool
	)
	if err := scan(&id, &tenantID, &courseID, &gcid, &enrolledAt, &deletedAt, &status, &completedAt, &passed); err != nil {
		return nil, err
	}
	return &domain.Enrollment{
		ID:          id,
		TenantID:    tenantID,
		CourseID:    courseID,
		GCID:        gcid,
		EnrolledAt:  enrolledAt,
		DeletedAt:   deletedAt,
		Status:      domain.EnrollmentStatus(status),
		CompletedAt: completedAt,
		Passed:      passed,
	}, nil
}

// Compile-time assertion: EnrollmentRepo must satisfy EnrollmentPort.
var _ domain.EnrollmentPort = (*EnrollmentRepo)(nil)

// Compile-time assertion: EnrollmentRepo must satisfy EnrollmentCompletionPort
// so the enrollment-completion driver can type-assert the production pg adapter
// into it and persist the chora.delivery.enrollment.completed.v1 fact.
var _ domain.EnrollmentCompletionPort = (*EnrollmentRepo)(nil)

// Compile-time assertion: EnrollmentRepo must ALSO satisfy
// EnrollmentListByCoursePort so cmd/server/main.go::wireRosters can
// type-assert the production pg adapter into it and wire the course Roster
// READ VIEW (GET /api/v1/rosters/{courseId}). A signature drift on either
// side surfaces at build time instead of as a silent 404 at runtime.
var _ domain.EnrollmentListByCoursePort = (*EnrollmentRepo)(nil)
