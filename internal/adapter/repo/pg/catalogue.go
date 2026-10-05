// catalogue.go — Postgres adapter for the public course catalogue.
//
// SCHEMA:
//   - courses                  — migrations/0001_initial.sql
//   - public_courses_catalog   — migrations/0006_public_catalog_view.sql
//     (cross-tenant public-only VIEW; OWNER rule bypasses RLS, the
//     WHERE public=true AND deleted_at IS NULL clause is the gate)
//
// CatalogueRepo is the production adapter behind domain.CataloguePort. It
// replaces the in-memory domain.Catalogue in cmd/server wiring so the
// Phyllis demo's public course catalogue is backed by chora_delivery and
// SURVIVES POD RESTARTS — no stubs, no in-memory, no inline seed data.
//
// Two read paths:
//
//  1. PUBLIC discovery — q.Visibility == VisibilityFilterPublic, OR
//     q.TenantID == "" with no tenant-scoped filter. Reads the
//     public_courses_catalog VIEW. The view is cross-tenant by design;
//     its OWNER rule bypasses RLS, so NO `SET LOCAL chora.tenant_id` is
//     emitted (an empty tenant payload would error). Phyllis Step 5
//     PublicDiscovery — an anonymous learner browsing Mr. Chen's CSPO
//     course from any tenant context — flows here.
//
//  2. TENANT-SCOPED browse — q.TenantID set with a non-public visibility
//     filter. Reads the `courses` TABLE with rls.ApplySession applied
//     first; the RLS policy on `courses` filters to the caller's tenant.
//
// Save UPSERTs into the `courses` table (idempotent on course_id) with the
// tenant derived from pc.TenantID — the CataloguePort.Save signature carries
// no ctx-tenant (the HTTP layer passes X-Tenant-Id by header, not ctx), so
// the adapter sets the RLS session var itself from the aggregate.
//
// Resilience-priority directive (`feedback_resilience_priority`):
//   - Idempotency: UPSERT on conflict(course_id) DO UPDATE — Save is
//     retry-safe under multi-pod replays and concurrent writers.
//   - Soft-delete-aware: every read filters `deleted_at IS NULL` (the view
//     bakes it in; the table path adds it explicitly).
//   - Multi-tenant: tenant-scoped reads + every write run inside a
//     transaction with SET LOCAL applied first, so under PgBouncer
//     transaction-pooling the tenant context never leaks across siblings.
//   - Dead-pod resume: pure SQL; re-runs converge.
//
// Cross-DB queries forbidden — chora-delivery reads only chora_delivery.
package pg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// ErrInvalidPublicCourse is the sentinel for a nil-input write.
var ErrInvalidPublicCourse = errors.New("pg: public course is nil")

// ErrPublicCourseMissingTenant is returned when a PublicCourse has no
// TenantID — Save cannot RLS-scope the write without it (the `courses`
// table is RLS-protected and the SET LOCAL payload would be empty).
var ErrPublicCourseMissingTenant = errors.New("pg: public course TenantID required for persisted writes")

// ErrPublicCourseMissingInstructor is returned when InstructorGCID is empty
// (the `courses.instructor_gcid` column is NOT NULL).
var ErrPublicCourseMissingInstructor = errors.New("pg: public course InstructorGCID required for persisted writes")

// ErrInstructorListMissingTenant is returned when ListByInstructor is called
// with an empty tenant — the read is RLS-scoped and an empty SET LOCAL would
// either error or leak across tenants. Fail loud per [[no-stubs-real-wiring]].
var ErrInstructorListMissingTenant = errors.New("pg: ListByInstructor requires a non-empty tenant_id (RLS scope)")

// ErrInstructorListMissingInstructor is returned when ListByInstructor is
// called with an empty instructor — would conflate with "any instructor" and
// leak the tenant's full course list. Reject loud.
var ErrInstructorListMissingInstructor = errors.New("pg: ListByInstructor requires a non-empty instructor_gcid")

// -----------------------------------------------------------------------------
// SQL templates (exported so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

// SQLUpsertPublicCourse — INSERT … ON CONFLICT DO UPDATE on the `courses`
// table. Idempotent on course_id. The `public` boolean column is the
// denorm of Visibility == VisibilityPublic — the public_courses_catalog
// view filters on it.
const SQLUpsertPublicCourse = `
INSERT INTO courses (
    course_id, tenant_id, instructor_gcid, title, description,
    public, price_sgd_cents, sf_eligible, max_capacity,
    created_at, updated_at, deleted_at, visibility
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9,
    $10, $11, $12, $13
)
ON CONFLICT (course_id) DO UPDATE SET
    title           = EXCLUDED.title,
    description     = EXCLUDED.description,
    public          = EXCLUDED.public,
    price_sgd_cents = EXCLUDED.price_sgd_cents,
    sf_eligible     = EXCLUDED.sf_eligible,
    max_capacity    = EXCLUDED.max_capacity,
    updated_at      = EXCLUDED.updated_at,
    deleted_at      = EXCLUDED.deleted_at,
    visibility      = EXCLUDED.visibility
`

// publicCatalogCols is the shared SELECT column list for the
// public_courses_catalog view — kept in one place so the scan helper and
// every SQL template stay column-aligned.
//
// View columns (migrations/0006_public_catalog_view.sql):
//
//	course_id, tenant_id, instructor_gcid, title, description,
//	price_sgd_cents, sf_eligible, max_capacity, created_at, updated_at
const publicCatalogCols = `course_id, tenant_id, instructor_gcid, title, description,
       price_sgd_cents, sf_eligible, max_capacity, created_at, updated_at`

// SQLSelectPublicCourseByID returns one public course by id from the view.
//
// The view is cross-tenant + public-only; a non-public or soft-deleted
// course is unreachable through it regardless of caller context. This is
// the catalogue's by-id discovery handle.
const SQLSelectPublicCourseByID = `
SELECT ` + publicCatalogCols + `
FROM public_courses_catalog
WHERE course_id = $1
`

// SQLSelectPublicCatalogByCursor — Relay-style cursor page over the public
// catalogue view.
//
// `course_id > $1` is the cursor predicate (” empty cursor → first page,
// because ” sorts before every UUID). ORDER BY course_id ASC gives a
// stable UUIDv7 = creation-order sort. LIMIT $2 binds First+1 — the
// sentinel +1 row drives HasNextPage; the adapter trims it.
const SQLSelectPublicCatalogByCursor = `
SELECT ` + publicCatalogCols + `
FROM public_courses_catalog
WHERE course_id > $1
ORDER BY course_id ASC
LIMIT $2
`

// SQLSelectPublicCatalogPage — offset/limit page over the public catalogue
// view (1-indexed page semantics computed in the adapter).
const SQLSelectPublicCatalogPage = `
SELECT ` + publicCatalogCols + `
FROM public_courses_catalog
ORDER BY course_id ASC
LIMIT $1 OFFSET $2
`

// SQLCountPublicCatalog — total public-course count (pre-pagination) for
// Search()'s total field.
const SQLCountPublicCatalog = `SELECT count(*) FROM public_courses_catalog`

// tenantCourseCols is the SELECT column list for the `courses` TABLE path
// (tenant-scoped browse). Wider than the view: includes `public` so the
// scan can derive Visibility, plus deleted_at for the soft-delete filter.
const tenantCourseCols = `course_id, tenant_id, instructor_gcid, title, description,
       public, price_sgd_cents, sf_eligible, max_capacity, created_at, updated_at, visibility`

// SQLSelectTenantCatalogByCursor — Relay-style cursor page over the
// `courses` TABLE, RLS-scoped. Used for tenant-scoped browses (the caller
// passes their own X-Tenant-Id and sees tenant_only + public rows in their
// tenant). RLS on `courses` does the tenant isolation.
//
// state = 'PUBLISHED' is the app-layer enrolment gate: the catalogue browse is
// a learner surface (CourseState PUBLISHED = "released to learners" per
// course_cj2.go), and the B2.4 `courses` RLS (mig 0043) FAIL-OPENS on state
// when chora.user_gcid is unset — which this path leaves unset. Without this
// predicate DRAFT/ARCHIVED authoring work leaks to any tenant member. Authors
// preview drafts via the management surfaces (CJ2 / by-instructor), not here.
const SQLSelectTenantCatalogByCursor = `
SELECT ` + tenantCourseCols + `
FROM courses
WHERE deleted_at IS NULL
  AND state = 'PUBLISHED'
  AND course_id > $1
ORDER BY course_id ASC
LIMIT $2
`

// SQLSelectTenantCatalogPage — offset/limit page over the `courses` TABLE,
// RLS-scoped. state = 'PUBLISHED' — enrolment gate; see
// SQLSelectTenantCatalogByCursor for the fail-open-backstop rationale.
const SQLSelectTenantCatalogPage = `
SELECT ` + tenantCourseCols + `
FROM courses
WHERE deleted_at IS NULL
  AND state = 'PUBLISHED'
ORDER BY course_id ASC
LIMIT $1 OFFSET $2
`

// SQLCountTenantCatalog — total count over the `courses` TABLE, RLS-scoped.
// state = 'PUBLISHED' keeps the count consistent with the paged browse
// (see SQLSelectTenantCatalogByCursor).
const SQLCountTenantCatalog = `SELECT count(*) FROM courses WHERE deleted_at IS NULL AND state = 'PUBLISHED'`

// SQLListCoursesByInstructor — offset/limit page over the `courses` TABLE
// filtered by instructor_gcid. RLS-scoped — the caller must apply
// rls.ApplySession with the tenant context BEFORE running this query so the
// `courses` policy filters to the caller's tenant (defence in depth on top
// of the instructor filter).
//
// 1-indexed (page, per) translates to limit=$2, offset=$3 in the adapter.
// Stable sort by course_id (UUIDv7 ⇒ creation order ascending, mirrors
// SQLSelectTenantCatalogPage so the catalogue's by-instructor projection
// has the same ordering as the tenant browse).
const SQLListCoursesByInstructor = `
SELECT ` + tenantCourseCols + `
FROM courses
WHERE deleted_at IS NULL
  AND instructor_gcid = $1
ORDER BY course_id ASC
LIMIT $2 OFFSET $3
`

// SQLCountCoursesByInstructor — total count of non-deleted courses for an
// instructor inside the RLS-scoped tenant. Binds $1 = instructor_gcid.
const SQLCountCoursesByInstructor = `SELECT count(*) FROM courses WHERE deleted_at IS NULL AND instructor_gcid = $1`

// -----------------------------------------------------------------------------
// CatalogueRepo
// -----------------------------------------------------------------------------

// CatalogueRepo is the Postgres-backed adapter behind domain.CataloguePort.
type CatalogueRepo struct {
	tx TxRunner
}

// NewCatalogueRepo constructs a pg CatalogueRepo around a TxRunner.
//
// A nil TxRunner degrades every method to ErrNotImplemented — mirrors
// CourseRepo / ApplicationRepo so cmd/server can fall back to the
// in-memory adapter in local dev.
func NewCatalogueRepo(tx TxRunner) *CatalogueRepo {
	return &CatalogueRepo{tx: tx}
}

// compile-time port conformance.
var _ domain.CataloguePort = (*CatalogueRepo)(nil)

// Save UPSERTs a PublicCourse into the `courses` table (idempotent on
// course_id). Tenant context is derived from pc.TenantID and applied via
// rls.ApplySession before the write.
func (r *CatalogueRepo) Save(ctx context.Context, pc *domain.PublicCourse) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if pc == nil {
		return ErrInvalidPublicCourse
	}
	if strings.TrimSpace(pc.TenantID) == "" {
		return ErrPublicCourseMissingTenant
	}
	if strings.TrimSpace(pc.InstructorGCID) == "" {
		return ErrPublicCourseMissingInstructor
	}
	// The CataloguePort.Save signature carries no ctx-tenant (the HTTP layer
	// passes X-Tenant-Id by header) — set it from the aggregate so RLS on
	// `courses` admits the write.
	ctx = tracing.WithTenantID(ctx, pc.TenantID)

	createdAt := pc.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := pc.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}

	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		// `courses` columns (migrations/0001_initial.sql):
		//   course_id, tenant_id, instructor_gcid, title, description,
		//   public, price_sgd_cents, sf_eligible, max_capacity,
		//   created_at, updated_at, deleted_at
		// PublicCourse is a marketplace projection — it carries no
		// max_capacity / description, so those bind to 0 / '' (the
		// catalogue read paths never surface them).
		_, err := q.Exec(ctx, SQLUpsertPublicCourse,
			pc.ID, pc.TenantID, pc.InstructorGCID, pc.Title, "",
			pc.Public, int64(pc.PriceSGDCents), pc.SFEligible, 0,
			createdAt, updatedAt, nullTime(deref(pc.DeletedAt)), safeVisibility(pc),
		)
		if err != nil {
			return fmt.Errorf("pg: upsert public course: %w", err)
		}
		return nil
	})
}

// Get returns the public course with the given ID from the
// public_courses_catalog VIEW. ok=false when no row matches.
//
// The view is cross-tenant + public-only, so this is the catalogue's by-id
// discovery handle: an anonymous caller can resolve any public course by
// id regardless of tenant context. Non-public / soft-deleted courses are
// unreachable through the view — Get returns ok=false for them.
func (r *CatalogueRepo) Get(ctx context.Context, id string) (*domain.PublicCourse, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var found *domain.PublicCourse
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		// NO rls.ApplySession — the public view bypasses RLS via its OWNER
		// rule; emitting SET LOCAL with the (often empty) caller tenant would
		// be wrong here.
		row := q.QueryRow(ctx, SQLSelectPublicCourseByID, id)
		pc, err := scanPublicCatalogRow(row.Scan)
		if err != nil {
			return nil // not-found → ok=false at the outer return
		}
		found = pc
		return nil
	})
	if err != nil || found == nil {
		return nil, false, err
	}
	return found, true, nil
}

// Search returns the courses matching the query plus the total count
// (pre-pagination), with 1-indexed page/per pagination.
//
// Public path (q.Visibility == VisibilityFilterPublic, or no tenant scope)
// reads the public_courses_catalog view; tenant-scoped path reads the
// `courses` table RLS-scoped.
func (r *CatalogueRepo) Search(ctx context.Context, q domain.CatalogueQuery) ([]*domain.PublicCourse, int, error) {
	if r == nil || r.tx == nil {
		return nil, 0, ErrNotImplemented
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	per := q.Per
	if per < 1 {
		per = 20
	}
	offset := (page - 1) * per

	var items []*domain.PublicCourse
	var total int

	if r.isPublicPath(q) {
		err := r.tx.RunInTx(ctx, func(ctx context.Context, qr Querier) error {
			cnt, err := scanCount(qr.QueryRow(ctx, SQLCountPublicCatalog))
			if err != nil {
				return err
			}
			total = cnt
			rows, err := qr.Query(ctx, SQLSelectPublicCatalogPage, per, offset)
			if err != nil {
				return err
			}
			defer rows.Close()
			items, err = collectPublicCatalogRows(rows)
			return err
		})
		if err != nil {
			return nil, 0, err
		}
		return filterAndSort(items, q), total, nil
	}

	// Tenant-scoped path — RLS-scoped read of the `courses` table.
	ctx = r.tenantCtx(ctx, q)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, qr Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(qr)); err != nil {
			return err
		}
		cnt, err := scanCount(qr.QueryRow(ctx, SQLCountTenantCatalog))
		if err != nil {
			return err
		}
		total = cnt
		rows, err := qr.Query(ctx, SQLSelectTenantCatalogPage, per, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		items, err = collectTenantCourseRows(rows)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return filterAndSort(items, q), total, nil
}

// SearchCursor returns a Relay-style cursor-paginated page.
//
// Public path reads the public_courses_catalog view (cross-tenant, no RLS);
// tenant-scoped path reads the `courses` table RLS-scoped. The DB query
// fetches First+1 rows — the sentinel +1 drives HasNextPage; the adapter
// trims it.
func (r *CatalogueRepo) SearchCursor(ctx context.Context, q domain.CatalogueQuery) (domain.CursorPage, error) {
	if r == nil || r.tx == nil {
		return domain.CursorPage{}, ErrNotImplemented
	}
	first := q.First
	if first < 1 {
		first = 20
	}
	if first > 200 {
		first = 200
	}
	limit := first + 1 // sentinel row for HasNextPage

	var raw []*domain.PublicCourse
	var total int

	cursor := cursorBound(q.AfterCursor)

	if r.isPublicPath(q) {
		err := r.tx.RunInTx(ctx, func(ctx context.Context, qr Querier) error {
			cnt, err := scanCount(qr.QueryRow(ctx, SQLCountPublicCatalog))
			if err != nil {
				return err
			}
			total = cnt
			rows, err := qr.Query(ctx, SQLSelectPublicCatalogByCursor, cursor, limit)
			if err != nil {
				return err
			}
			defer rows.Close()
			raw, err = collectPublicCatalogRows(rows)
			return err
		})
		if err != nil {
			return domain.CursorPage{}, err
		}
		return assembleCursorPage(filterAndSort(raw, q), first, total), nil
	}

	// Tenant-scoped path.
	ctx = r.tenantCtx(ctx, q)
	err := r.tx.RunInTx(ctx, func(ctx context.Context, qr Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(qr)); err != nil {
			return err
		}
		cnt, err := scanCount(qr.QueryRow(ctx, SQLCountTenantCatalog))
		if err != nil {
			return err
		}
		total = cnt
		rows, err := qr.Query(ctx, SQLSelectTenantCatalogByCursor, cursor, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		raw, err = collectTenantCourseRows(rows)
		return err
	})
	if err != nil {
		return domain.CursorPage{}, err
	}
	return assembleCursorPage(filterAndSort(raw, q), first, total), nil
}

// ListByInstructor returns the courses where instructor_gcid matches the
// argument, RLS-scoped to the caller's tenant context.
//
// Reads the `courses` TABLE (NOT the public_courses_catalog view) with
// rls.ApplySession applied first; the RLS policy on `courses` filters to
// the caller's tenant. The instructor filter is the secondary WHERE
// clause (defence in depth on top of RLS).
//
// Empty tenant or instructor: rejected loud (would silently leak the tenant's
// full course list otherwise). Soft-deleted rows are excluded.
//
// Pagination is 1-indexed (page, per) and mirrors Search; default page=1,
// per=20 when 0/negative.
func (r *CatalogueRepo) ListByInstructor(ctx context.Context, tenantID, instructorGCID string, page, per int) ([]*domain.PublicCourse, int, error) {
	if r == nil || r.tx == nil {
		return nil, 0, ErrNotImplemented
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, 0, ErrInstructorListMissingTenant
	}
	if strings.TrimSpace(instructorGCID) == "" {
		return nil, 0, ErrInstructorListMissingInstructor
	}
	if page < 1 {
		page = 1
	}
	if per < 1 {
		per = 20
	}
	offset := (page - 1) * per

	ctx = tracing.WithTenantID(ctx, tenantID)

	var items []*domain.PublicCourse
	var total int
	err := r.tx.RunInTx(ctx, func(ctx context.Context, qr Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(qr)); err != nil {
			return err
		}
		cnt, err := scanCount(qr.QueryRow(ctx, SQLCountCoursesByInstructor, instructorGCID))
		if err != nil {
			return err
		}
		total = cnt
		rows, err := qr.Query(ctx, SQLListCoursesByInstructor, instructorGCID, per, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		items, err = collectTenantCourseRows(rows)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	if items == nil {
		items = []*domain.PublicCourse{}
	}
	return items, total, nil
}

// -----------------------------------------------------------------------------
// Path selection + ctx helpers
// -----------------------------------------------------------------------------

// isPublicPath reports whether the query is a cross-tenant public-discovery
// read (→ public_courses_catalog view) vs a tenant-scoped browse (→ courses
// table RLS-scoped).
//
//   - VisibilityFilterPublic → ALWAYS the public view (cross-tenant).
//   - Empty TenantID → the public view (no tenant context to RLS-scope; the
//     anonymous /api/catalog?public=true browse lands here).
//   - Otherwise (TenantID set, tenant-or-public / any filter) → the
//     `courses` table, RLS-scoped to that tenant.
func (r *CatalogueRepo) isPublicPath(q domain.CatalogueQuery) bool {
	if q.Visibility == domain.VisibilityFilterPublic {
		return true
	}
	return strings.TrimSpace(q.TenantID) == ""
}

// tenantCtx returns ctx with the query's TenantID set for rls.ApplySession.
func (r *CatalogueRepo) tenantCtx(ctx context.Context, q domain.CatalogueQuery) context.Context {
	if t := strings.TrimSpace(q.TenantID); t != "" {
		return tracing.WithTenantID(ctx, t)
	}
	return ctx
}

// -----------------------------------------------------------------------------
// Row scanning + collection
// -----------------------------------------------------------------------------

// scanPublicCatalogRow consumes a public_courses_catalog view row into a
// PublicCourse. The view exposes only public rows, so Visibility is always
// VisibilityPublic + Public=true.
//
// Column order matches publicCatalogCols (10 cols).
func scanPublicCatalogRow(scan func(...any) error) (*domain.PublicCourse, error) {
	var (
		id, tenantID, instructorGCID, title, description string
		priceCents                                       int64
		sfEligible                                       bool
		maxCapacity                                      int
		createdAt, updatedAt                             time.Time
	)
	if err := scan(
		&id, &tenantID, &instructorGCID, &title, &description,
		&priceCents, &sfEligible, &maxCapacity, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	_ = description
	_ = maxCapacity
	return &domain.PublicCourse{
		ID:             id,
		TenantID:       tenantID,
		Title:          title,
		InstructorGCID: instructorGCID,
		PriceSGDCents:  int32(priceCents),
		Public:         true,
		Visibility:     domain.VisibilityPublic,
		SFEligible:     sfEligible,
		Tags:           []string{},
		CreatedAt:      createdAt.UTC(),
		UpdatedAt:      updatedAt.UTC(),
	}, nil
}

// scanTenantCourseRow consumes a `courses` TABLE row into a PublicCourse.
// Wider than the view scan: the table carries the `public` boolean, so
// Visibility is derived from it (public → VisibilityPublic, else
// VisibilityTenantOnly — the table has no finer-grained scope column;
// VisibilityPrivate is a domain-only nuance not persisted in 0001).
//
// Column order matches tenantCourseCols (11 cols).
// safeVisibility returns a valid, non-empty visibility string for the courses.
// visibility CHECK constraint (private|tenant_only|public). The domain
// constructor always sets a valid scope; this coerces any zero/invalid value
// (defence-in-depth) from the public denorm so a write never violates the CHECK.
func safeVisibility(pc *domain.PublicCourse) string {
	if pc.Visibility.IsValid() {
		return string(pc.Visibility)
	}
	if pc.Public {
		return string(domain.VisibilityPublic)
	}
	return string(domain.VisibilityTenantOnly)
}

func scanTenantCourseRow(scan func(...any) error) (*domain.PublicCourse, error) {
	var (
		id, tenantID, instructorGCID, title, description string
		public, sfEligible                               bool
		priceCents                                       int64
		maxCapacity                                      int
		createdAt, updatedAt                             time.Time
		visibilityStr                                    string
	)
	if err := scan(
		&id, &tenantID, &instructorGCID, &title, &description,
		&public, &priceCents, &sfEligible, &maxCapacity, &createdAt, &updatedAt, &visibilityStr,
	); err != nil {
		return nil, err
	}
	_ = description
	_ = maxCapacity
	// B2.3 — visibility reconciliation. Two writers touch `courses`: SaveCJ2 sets
	// `public` from the CJ#2 state (but never `visibility`), while the catalogue
	// Save sets the real `visibility` scope. So `public` stays AUTHORITATIVE for
	// cross-tenant public-ness (it drives the public_courses_catalog view), and the
	// persisted `visibility` column only distinguishes private vs tenant_only for a
	// NON-public row (the B2.3 fix: private no longer collapses to tenant_only).
	var visibility domain.Visibility
	switch {
	case public:
		visibility = domain.VisibilityPublic
	case domain.Visibility(visibilityStr) == domain.VisibilityPrivate:
		visibility = domain.VisibilityPrivate
	default:
		visibility = domain.VisibilityTenantOnly
	}
	return &domain.PublicCourse{
		ID:             id,
		TenantID:       tenantID,
		Title:          title,
		InstructorGCID: instructorGCID,
		PriceSGDCents:  int32(priceCents),
		Public:         public,
		Visibility:     visibility,
		SFEligible:     sfEligible,
		Tags:           []string{},
		CreatedAt:      createdAt.UTC(),
		UpdatedAt:      updatedAt.UTC(),
	}, nil
}

// collectPublicCatalogRows drains a Rows into a []*PublicCourse via the
// view scan.
func collectPublicCatalogRows(rows Rows) ([]*domain.PublicCourse, error) {
	var out []*domain.PublicCourse
	for rows.Next() {
		pc, err := scanPublicCatalogRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	return out, rows.Err()
}

// collectTenantCourseRows drains a Rows into a []*PublicCourse via the
// `courses` table scan.
func collectTenantCourseRows(rows Rows) ([]*domain.PublicCourse, error) {
	var out []*domain.PublicCourse
	for rows.Next() {
		pc, err := scanTenantCourseRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	return out, rows.Err()
}

// scanCount scans a single-int count row.
func scanCount(row Row) (int, error) {
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// -----------------------------------------------------------------------------
// Free-text + visibility filtering (in-adapter)
// -----------------------------------------------------------------------------

// filterAndSort applies the free-text query + the in-tenant visibility
// projection that the SQL layer does not natively express, then sorts by
// course_id (UUIDv7 = creation order) for a stable page.
//
// The SQL already scopes the row SET (public view = public-only;
// `courses` table = RLS tenant slice). filterAndSort narrows further:
//
//   - q.Q free-text — case-insensitive substring on title (+ tags, though
//     the catalogue view does not materialise tags; tenant-table rows also
//     carry no tags column in 0001 — title match is the effective filter).
//   - VisibilityFilterTenantOrPublic — keep tenant_only rows only when
//     they match q.TenantID; public rows always. (For the public-view
//     path every row is already public, so this is a no-op there.)
func filterAndSort(in []*domain.PublicCourse, q domain.CatalogueQuery) []*domain.PublicCourse {
	needle := strings.ToLower(strings.TrimSpace(q.Q))
	out := make([]*domain.PublicCourse, 0, len(in))
	for _, pc := range in {
		if pc == nil {
			continue
		}
		if !visibilityKeep(pc, q) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(pc.Title), needle) {
			continue
		}
		out = append(out, pc)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Compare(out[i].ID, out[j].ID) < 0
	})
	return out
}

// visibilityKeep mirrors domain.visibilityMatch for the rows the adapter
// loaded. The SQL paths already pre-scope, so this only has bite on the
// tenant-table path with VisibilityFilterTenantOrPublic.
func visibilityKeep(pc *domain.PublicCourse, q domain.CatalogueQuery) bool {
	switch q.Visibility {
	case domain.VisibilityFilterPublic:
		return pc.Visibility == domain.VisibilityPublic
	case domain.VisibilityFilterTenantOrPublic:
		if pc.Visibility == domain.VisibilityPublic {
			return true
		}
		if pc.Visibility == domain.VisibilityTenantOnly {
			return q.TenantID == "" || pc.TenantID == q.TenantID
		}
		return false
	default: // VisibilityFilterAny
		if q.TenantID != "" && pc.TenantID != q.TenantID {
			// On the public-view path TenantID is empty so this never trips;
			// on the tenant-table path RLS already guarantees the match, but
			// keep the guard for defence-in-depth.
			return false
		}
		return true
	}
}

// nilUUID is the all-zero UUID — every real UUIDv7 / UUIDv4 sorts strictly
// after it, so it is the correct "before the first row" cursor sentinel.
const nilUUID = "00000000-0000-0000-0000-000000000000"

// cursorBound maps a Relay AfterCursor onto a value safe to bind into a
// `course_id > $1` predicate where course_id is a Postgres `uuid` column.
//
// The empty cursor ("" — the first-page request) CANNOT be bound directly:
// Postgres rejects `uuid > ”` with `invalid input syntax for type uuid`.
// We substitute the nil UUID, which sorts before every real course_id, so
// `course_id > nilUUID` is the whole first page. A non-empty cursor is
// passed through untouched (a malformed cursor will still surface a uuid
// cast error from Postgres — that is a client bug, not a first-page case).
func cursorBound(afterCursor string) string {
	if strings.TrimSpace(afterCursor) == "" {
		return nilUUID
	}
	return afterCursor
}

// assembleCursorPage trims the sentinel +1 row, computes HasNextPage +
// EndCursor, and packages the Relay-style page.
//
// `rows` is the post-filter, post-sort slice (which may itself be shorter
// than first+1 once free-text filtering drops rows). HasNextPage is true
// when MORE rows than `first` survived — the last surviving sentinel.
func assembleCursorPage(rows []*domain.PublicCourse, first, total int) domain.CursorPage {
	hasNext := len(rows) > first
	if hasNext {
		rows = rows[:first]
	}
	endCursor := ""
	if len(rows) > 0 {
		endCursor = rows[len(rows)-1].ID
	}
	if rows == nil {
		rows = []*domain.PublicCourse{}
	}
	return domain.CursorPage{
		Items:       rows,
		HasNextPage: hasNext,
		EndCursor:   endCursor,
		Total:       total,
	}
}
