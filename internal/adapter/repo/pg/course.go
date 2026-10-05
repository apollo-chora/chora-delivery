// course.go — Postgres adapter for the Course aggregate.
//
// SCHEMA: see migrations/0001_initial.sql (`courses` table).
//
// Per Phyllis MVP, Course is the primary aggregate of chora-delivery —
// instructors author Courses, learners discover them via the public
// catalogue (cross-tenant when public=true), and the Course is the
// natural anchor for Class scheduling, Booking, Application, and
// Certification.
//
// Resilience-priority directive (`feedback_resilience_priority`):
//
//   - Idempotency: UPSERT on conflict(course_id) DO UPDATE — Save is
//     retry-safe under multi-pod replays and concurrent writers
//   - Multi-user concurrent: every write runs inside a transaction
//     with `SET LOCAL chora.tenant_id` applied first, so under PgBouncer
//     transaction-pooling the tenant context never leaks across sibling
//     requests
//   - Soft-delete-aware: Get / List queries always filter
//     `WHERE deleted_at IS NULL` per ddd-enforcement #6
//   - RLS: every read/write applies rls.ApplySession before the user
//     query so the row-level policy on `courses` enforces tenant isolation
//
// Cross-DB queries forbidden — chora-delivery reads only chora_delivery.
// Inter-domain side effects flow through Pub/Sub (see ../events).
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// ErrInvalidCourse is the sentinel for a nil-input write.
var ErrInvalidCourse = errors.New("pg: course is nil")

// ErrCourseMissingInstructor is the sentinel when InstructorGCID is empty
// (the `courses.instructor_gcid` column is NOT NULL — Save fails fast
// instead of letting Postgres reject with a 22P02 cast error).
var ErrCourseMissingInstructor = errors.New("pg: course.InstructorGCID required for persisted writes")

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

// SQLUpsertCourse — INSERT … ON CONFLICT DO UPDATE.
//
// Idempotent on course_id. Mirrors chora-creation/atom_repository's UPSERT
// shape so retries land cleanly without duplicate rows. atom_ids is bound
// as UUID[] (the column type); the application layer marshals the
// []string slice straight through pgx's type mapping.
const SQLUpsertCourse = `
INSERT INTO courses (
    course_id, tenant_id, instructor_gcid, title, description,
    atom_ids, public, price_sgd_cents, sf_eligible, max_capacity,
    created_at, updated_at, deleted_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10,
    $11, $12, $13
)
ON CONFLICT (course_id) DO UPDATE SET
    title           = EXCLUDED.title,
    description     = EXCLUDED.description,
    atom_ids        = EXCLUDED.atom_ids,
    public          = EXCLUDED.public,
    price_sgd_cents = EXCLUDED.price_sgd_cents,
    sf_eligible     = EXCLUDED.sf_eligible,
    max_capacity    = EXCLUDED.max_capacity,
    updated_at      = EXCLUDED.updated_at,
    deleted_at      = EXCLUDED.deleted_at
`

// SQLSelectCourseByID returns one course by id, RLS-scoped.
const SQLSelectCourseByID = `
SELECT course_id, tenant_id, instructor_gcid, title, description,
       atom_ids, public, price_sgd_cents, sf_eligible, max_capacity,
       created_at, updated_at, deleted_at
FROM courses
WHERE course_id = $1
  AND tenant_id = $2
  AND deleted_at IS NULL
`

// SQLListCoursesByTenant returns active courses for a tenant.
const SQLListCoursesByTenant = `
SELECT course_id, tenant_id, instructor_gcid, title, description,
       atom_ids, public, price_sgd_cents, sf_eligible, max_capacity,
       created_at, updated_at, deleted_at
FROM courses
WHERE tenant_id = $1
  AND deleted_at IS NULL
ORDER BY created_at ASC
LIMIT $2 OFFSET $3
`

// SQLListPublicCourses returns active public courses across tenants.
//
// Cross-tenant read is allowed because RLS policy on `courses` admits the
// row when tenant_id = current_setting('chora.tenant_id'), AND we
// explicitly query the additional public flag here. The migrate role
// bypasses RLS (used in seed scripts), but production app_rw still needs
// the tenant-scoped filter; for the public catalogue path the caller
// MUST pass the user's own tenant_id and accept that only their tenant +
// platform-public rows surface. A separate platform-wide catalogue read
// path (cross-tenant) is reserved for the marketplace service which uses
// a dedicated read-only role with its own RLS policy override (M14).
const SQLListPublicCourses = `
SELECT course_id, tenant_id, instructor_gcid, title, description,
       atom_ids, public, price_sgd_cents, sf_eligible, max_capacity,
       created_at, updated_at, deleted_at
FROM courses
WHERE public = TRUE
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $1 OFFSET $2
`

// -----------------------------------------------------------------------------
// CourseRepo
// -----------------------------------------------------------------------------

// CourseRepo is the Postgres-backed Course repo.
type CourseRepo struct {
	tx TxRunner
	// certColumns gates the cert-definition columns added by migration 0030
	// (CHO-1795). Off until the migration is applied (set via EnableCertColumns
	// from COURSE_CERT_PG_ENABLED); when off SaveCJ2/GetCJ2 skip the cert
	// UPDATE/SELECT so the code can ship/auto-deploy ahead of the migration.
	certColumns bool
}

// NewCourseRepo constructs a pg CourseRepo around a TxRunner.
func NewCourseRepo(tx TxRunner) *CourseRepo {
	return &CourseRepo{tx: tx}
}

// Compile-time assertion: pg.CourseRepo satisfies the domain-owned
// delivery.CourseRepo port (ADR-236 D1) — the canonical durable store for
// the legacy (pre-CJ#2) Course shape, verbatim (no adapter shim needed).
var _ domain.CourseRepo = (*CourseRepo)(nil)

// EnableCertColumns turns on cert-definition persistence (migration 0030 must
// be applied first). Chainable. Mirrors the COURSE_CONTENT_PG_ENABLED gate.
func (r *CourseRepo) EnableCertColumns() *CourseRepo {
	if r != nil {
		r.certColumns = true
	}
	return r
}

// Save persists a Course aggregate (UPSERT on course_id). Idempotent.
//
// The caller MUST have set tenant_id on ctx via tracing.WithTenantID;
// rls.ApplySession panics-loudly (returns ErrNoTenantContext) otherwise.
func (r *CourseRepo) Save(ctx context.Context, c *domain.Course) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if c == nil {
		return ErrInvalidCourse
	}
	if c.InstructorGCID == "" {
		return ErrCourseMissingInstructor
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		_, err := q.Exec(ctx, SQLUpsertCourse,
			c.ID, c.TenantID, c.InstructorGCID, c.Title, "",
			c.AtomIDs, false, int64(0), false, c.MaxCapacity,
			c.CreatedAt, c.UpdatedAt, nullTime(deref(c.DeletedAt)),
		)
		if err != nil {
			return fmt.Errorf("pg: upsert course: %w", err)
		}
		return nil
	})
}

// Get returns a Course by (tenantID, courseID). Returns (nil, false, nil)
// when no row matches under the RLS-scoped query.
func (r *CourseRepo) Get(ctx context.Context, tenantID, courseID string) (*domain.Course, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var found *domain.Course
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectCourseByID, courseID, tenantID)
		c, err := scanCourse(row.Scan)
		if err != nil {
			// Not-found path — the underlying scan returns a "no rows"-shaped
			// error; we surface that as ok=false rather than propagating.
			return nil
		}
		found = c
		return nil
	})
	if err != nil || found == nil {
		return nil, false, err
	}
	return found, true, nil
}

// ListByTenant returns active courses for a tenant, paged.
func (r *CourseRepo) ListByTenant(ctx context.Context, tenantID string, offset, limit int) ([]*domain.Course, int, error) {
	if r == nil || r.tx == nil {
		return nil, 0, ErrNotImplemented
	}
	if limit <= 0 {
		limit = 50
	}
	var out []*domain.Course
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, err := q.Query(ctx, SQLListCoursesByTenant, tenantID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCourse(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, len(out), err
}

// ListPublic returns active public courses across tenants the caller can see.
//
// NOTE: RLS policy on `courses` filters by tenant_id, so under app_rw this
// query returns only the caller's own tenant's public courses. A
// future cross-tenant catalogue read (M14 marketplace) requires a
// dedicated `marketplace_ro` role with a relaxed policy.
func (r *CourseRepo) ListPublic(ctx context.Context, offset, limit int) ([]*domain.Course, int, error) {
	if r == nil || r.tx == nil {
		return nil, 0, ErrNotImplemented
	}
	if limit <= 0 {
		limit = 50
	}
	var out []*domain.Course
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, err := q.Query(ctx, SQLListPublicCourses, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCourse(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, len(out), err
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// scanCourse consumes a row scanner into a fresh Course aggregate.
//
// Column order matches SELECT lists in SQLSelectCourseByID + SQLListCoursesByTenant
// + SQLListPublicCourses (13 cols).
func scanCourse(scan func(...any) error) (*domain.Course, error) {
	var (
		id, tenantID, instructorGCID, title, description string
		atomIDs                                          []string
		public, sfEligible                               bool
		priceCents                                       int64
		maxCapacity                                      int
		createdAt, updatedAt                             time.Time
		deletedAt                                        *time.Time
	)
	if err := scan(
		&id, &tenantID, &instructorGCID, &title, &description,
		&atomIDs, &public, &priceCents, &sfEligible, &maxCapacity,
		&createdAt, &updatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	c := &domain.Course{
		ID:             id,
		TenantID:       tenantID,
		InstructorGCID: instructorGCID,
		Title:          title,
		AtomIDs:        atomIDs,
		MaxCapacity:    maxCapacity,
		CreatedAt:      createdAt.UTC(),
		UpdatedAt:      updatedAt.UTC(),
	}
	if deletedAt != nil {
		t := deletedAt.UTC()
		c.DeletedAt = &t
	}
	// description + public + priceCents + sfEligible are captured by the
	// SELECT but not currently stored on the domain aggregate — they are
	// part of the planned M14 expansion; the values remain queryable via
	// raw SQL until the domain model catches up.
	_ = description
	_ = public
	_ = priceCents
	_ = sfEligible
	return c, nil
}

// deref returns the time pointed to by t, or zero time when t is nil.
func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
