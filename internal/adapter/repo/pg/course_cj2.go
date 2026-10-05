// course_cj2.go — CJ#2 pg adapter for the Course state-FSM authoring +
// release flow per migration 0014_cj2_course_state_fsm.up.sql.
//
// Extends CourseRepo (course.go) with CJ#2-specific CRUD: SaveCJ2 (UPSERT
// with the 9 new columns), GetCJ2, ListByState (cursor paginated for the
// R+ admin queue at /api/v1/courses?state=AWAITING_REVIEW).
//
// All reads/writes wrap in rls.ApplySession before queries so the
// tenant_isolation policy on `courses` filters by chora.tenant_id.
//
// Cross-DB queries FORBIDDEN — only reads chora_delivery.courses.
//
// Per CJ#2 directive row at `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// SQL templates (CJ#2 — exported so CI/lint can grep them)
// -----------------------------------------------------------------------------

// courseCJ2SelectCols is the canonical SELECT list — includes BOTH the
// legacy 0001 columns AND the 9 columns added by 0014.
const courseCJ2SelectCols = `
    course_id, tenant_id, instructor_gcid, title, description,
    atom_ids, public, price_sgd_cents, sf_eligible, max_capacity,
    created_at, updated_at, deleted_at,
    state, author_gcid, test_set_ids, instructor_gcids,
    scheduled_open_at, review_notes, learning_objectives,
    prerequisites, published_at
`

// SQLUpsertCourseCJ2 — UPSERT on course_id, idempotent.
//
// Mirrors application.go conventions: every mutable field is bound in one
// shot so Save is retry-safe under multi-pod replays and concurrent writers.
const SQLUpsertCourseCJ2 = `
INSERT INTO courses (
    course_id, tenant_id, instructor_gcid, title, description,
    atom_ids, public, price_sgd_cents, sf_eligible, max_capacity,
    created_at, updated_at, deleted_at,
    state, author_gcid, test_set_ids, instructor_gcids,
    scheduled_open_at, review_notes, learning_objectives,
    prerequisites, published_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10,
    $11, $12, $13,
    $14, $15, $16, $17,
    $18, $19, $20,
    $21, $22
)
ON CONFLICT (course_id) DO UPDATE SET
    title               = EXCLUDED.title,
    description         = EXCLUDED.description,
    atom_ids            = EXCLUDED.atom_ids,
    public              = EXCLUDED.public,
    price_sgd_cents     = EXCLUDED.price_sgd_cents,
    sf_eligible         = EXCLUDED.sf_eligible,
    max_capacity        = EXCLUDED.max_capacity,
    updated_at          = EXCLUDED.updated_at,
    deleted_at          = EXCLUDED.deleted_at,
    state               = EXCLUDED.state,
    author_gcid         = EXCLUDED.author_gcid,
    test_set_ids        = EXCLUDED.test_set_ids,
    instructor_gcids    = EXCLUDED.instructor_gcids,
    scheduled_open_at   = EXCLUDED.scheduled_open_at,
    review_notes        = EXCLUDED.review_notes,
    learning_objectives = EXCLUDED.learning_objectives,
    prerequisites       = EXCLUDED.prerequisites,
    published_at        = EXCLUDED.published_at
`

// SQLSelectCJ2CourseByID returns one CJ#2 course (with extended cols).
const SQLSelectCJ2CourseByID = `
SELECT ` + courseCJ2SelectCols + `
FROM courses
WHERE course_id = $1
  AND tenant_id = $2
  AND deleted_at IS NULL
`

// SQLListCJ2CoursesByState — admin queue base WHERE clause (tenant_id +
// state + soft-delete). ListByState appends an OPTIONAL `AND title ILIKE $N`
// (R+ course entity-picker search, ?q=) then `ORDER BY ... LIMIT $N OFFSET
// $N` with dynamically numbered args — mirrors chora-creation's atom ?q=
// pattern (commit 85b2bae14) so the arg count only grows when a query is
// actually supplied (backward compatible with the pre-search admin queue).
//
// For the R+ admin queue at GET /api/v1/courses?state=AWAITING_REVIEW the
// caller passes the cursor as an opaque base-64 of (created_at, course_id).
// Here we keep the query simple — offset+limit pagination — and let the
// handler decide cursor framing. Cursor v2 is M15 work.
const SQLListCJ2CoursesByState = `
SELECT ` + courseCJ2SelectCols + `
FROM courses
WHERE tenant_id = $1
  AND state = $2
  AND deleted_at IS NULL
`

// SQLListCJ2CoursesByStateAndAuthor — author-scoped variant of the admin
// queue base WHERE clause. Adds `AND author_gcid = $3` so an ADR-182
// `author` (A+ Creator), who holds no training-admin role, lists ONLY their
// OWN courses in the requested lifecycle state (ONBOARD-UI F1). RLS still
// scopes to the caller's tenant via rls.ApplySession — authorship is an
// extra filter, NOT a cross-tenant broadening. ListByStateAndAuthor appends
// the same optional ILIKE + ORDER BY/LIMIT/OFFSET tail as ListByState.
const SQLListCJ2CoursesByStateAndAuthor = `
SELECT ` + courseCJ2SelectCols + `
FROM courses
WHERE tenant_id = $1
  AND state = $2
  AND author_gcid = $3
  AND deleted_at IS NULL
`

// SQLUpdateCourseCert sets the cert-definition columns (migration 0030).
// Run after the main upsert in SaveCJ2 when cert columns are enabled.
const SQLUpdateCourseCert = `
UPDATE courses SET
    cert_enabled             = $1,
    cert_type                = $2,
    cert_passing_score_pct   = $3,
    cert_require_all_content = $4
WHERE course_id = $5 AND tenant_id = $6
`

// SQLSelectCourseCert reads the cert-definition columns for a course.
const SQLSelectCourseCert = `
SELECT cert_enabled, cert_type, cert_passing_score_pct, cert_require_all_content
FROM courses
WHERE course_id = $1 AND tenant_id = $2
`

// -----------------------------------------------------------------------------
// CourseRepo CJ#2 methods
// -----------------------------------------------------------------------------

// SaveCJ2 persists a CJ#2-shaped Course (UPSERT on course_id). Idempotent.
//
// The caller MUST have set tenant_id on ctx via tracing.WithTenantID;
// rls.ApplySession returns ErrNoTenantContext otherwise.
//
// Differs from CourseRepo.Save: binds the 9 CJ#2 columns + handles UUID[]
// for test_set_ids + instructor_gcids; binds TEXT[] for learning_objectives
// + prerequisites; binds the state CHECK-constrained TEXT column.
func (r *CourseRepo) SaveCJ2(ctx context.Context, c *domain.Course) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if c == nil {
		return ErrInvalidCourse
	}
	if c.AuthorGCID == "" {
		// CJ#2 always sets author_gcid via NewCJ2Course; defence-in-depth.
		return errors.New("pg: course.AuthorGCID required for CJ#2 persisted writes")
	}
	// Mirror author onto legacy instructor_gcid column for the existing
	// SaveCJ2 SQL — column is NOT NULL.
	if c.InstructorGCID == "" {
		c.InstructorGCID = c.AuthorGCID
	}
	state := c.State
	if state == "" {
		state = domain.CourseStateDraft
	}
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		// nil-safe array binds — pgx accepts []string for UUID[] + TEXT[]
		// when the underlying driver is configured for plain-text mode.
		atomIDs := c.AtomIDs
		if atomIDs == nil {
			atomIDs = []string{}
		}
		testSetIDs := c.TestSetIDs
		if testSetIDs == nil {
			testSetIDs = []string{}
		}
		objectives := c.LearningObjectives
		if objectives == nil {
			objectives = []string{}
		}
		prereqs := c.PrerequisiteNotes
		if prereqs == nil {
			prereqs = []string{}
		}
		// `public` mirrors state == PUBLISHED so the released CJ#2 course
		// surfaces in the `public_courses_catalog` view (which filters
		// `WHERE public = TRUE AND deleted_at IS NULL`). DRAFT,
		// AWAITING_REVIEW and ARCHIVED stay public=false — only the
		// PUBLISHED slice is publicly discoverable. Closes E2E-BE-CJ2-
		// CATALOG-PROJECTION (FE smoke 2026-05-24).
		publicFlag := state == domain.CourseStatePublished
		_, err := q.Exec(ctx, SQLUpsertCourseCJ2,
			c.ID, c.TenantID, c.InstructorGCID, c.Title, c.Description,
			atomIDs, publicFlag, c.PriceSGDCents, c.SFEligible, c.MaxCapacity,
			c.CreatedAt, c.UpdatedAt, nullTime(deref(c.DeletedAt)),
			string(state), c.AuthorGCID, testSetIDs, c.InstructorGCIDs,
			nullTime(deref(c.ScheduledOpenAt)), c.ReviewNotes, objectives,
			prereqs, nullTime(deref(c.PublishedAt)),
		)
		if err != nil {
			return fmt.Errorf("pg: upsert cj2 course: %w", err)
		}
		// CHO-1795 — persist the cert definition (migration 0030). Gated so the
		// code ships ahead of the migration; the extra UPDATE only fires once
		// EnableCertColumns has been set (post-migration activation).
		if r.certColumns {
			if _, err := q.Exec(ctx, SQLUpdateCourseCert,
				c.Certification.Enabled,
				nullString(string(c.Certification.CertType)),
				c.Certification.PassingScorePct,
				c.Certification.RequireAllContent,
				c.ID, c.TenantID,
			); err != nil {
				return fmt.Errorf("pg: update cj2 course cert: %w", err)
			}
		}
		return nil
	})
}

// GetCJ2 returns a CJ#2 Course (with state-FSM fields) by (tenantID, id).
//
// Returns (nil, false, nil) when no row matches the RLS-scoped query.
func (r *CourseRepo) GetCJ2(ctx context.Context, tenantID, courseID string) (*domain.Course, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	var found *domain.Course
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectCJ2CourseByID, courseID, tenantID)
		c, scanErr := scanCJ2Course(row.Scan)
		if scanErr != nil {
			// Treat "no rows" as not-found rather than error-propagate.
			return nil
		}
		// CHO-1795 — load the cert definition (migration 0030) when enabled.
		if r.certColumns {
			cd, cErr := scanCourseCert(q.QueryRow(ctx, SQLSelectCourseCert, courseID, tenantID).Scan)
			if cErr != nil {
				return fmt.Errorf("pg: select cj2 course cert: %w", cErr)
			}
			c.Certification = cd
		}
		found = c
		return nil
	})
	if err != nil || found == nil {
		return nil, false, err
	}
	return found, true, nil
}

// SaveCJ2Adapter wraps SaveCJ2 to satisfy domain.CourseCJ2Port.Save.
//
// The pg adapter splits SaveCJ2 (CJ#2-aware UPSERT) from Save (legacy
// upsert) so existing Course callers keep working unchanged. The port
// is exposed via CourseRepoCJ2Port wrapper below.
type CourseRepoCJ2Port struct{ Repo *CourseRepo }

// Save persists via SaveCJ2.
func (p *CourseRepoCJ2Port) Save(ctx context.Context, c *domain.Course) error {
	return p.Repo.SaveCJ2(ctx, c)
}

// Get persists via GetCJ2.
func (p *CourseRepoCJ2Port) Get(ctx context.Context, tenantID, courseID string) (*domain.Course, bool, error) {
	return p.Repo.GetCJ2(ctx, tenantID, courseID)
}

// ListByState forwards to CourseRepo.ListByState.
func (p *CourseRepoCJ2Port) ListByState(ctx context.Context, tenantID string, state domain.CourseState, query string, offset, limit int) ([]*domain.Course, error) {
	return p.Repo.ListByState(ctx, tenantID, state, query, offset, limit)
}

// ListByStateAndAuthor forwards to CourseRepo.ListByStateAndAuthor.
func (p *CourseRepoCJ2Port) ListByStateAndAuthor(ctx context.Context, tenantID string, state domain.CourseState, authorGCID string, query string, offset, limit int) ([]*domain.Course, error) {
	return p.Repo.ListByStateAndAuthor(ctx, tenantID, state, authorGCID, query, offset, limit)
}

// Compile-time guard.
var _ domain.CourseCJ2Port = (*CourseRepoCJ2Port)(nil)

// ListByState returns CJ#2 courses in the given state for the tenant,
// paginated by offset/limit. query, when non-empty, appends `AND title
// ILIKE $N` (case-insensitive substring, wrapped in `%`) with dynamic arg
// numbering — backs the R+ course entity-picker search
// (GET /api/v1/courses?q=); empty query leaves the bind order untouched.
//
// Primary use-case: R+ admin queue at GET /api/v1/courses?state=AWAITING_REVIEW.
func (r *CourseRepo) ListByState(ctx context.Context, tenantID string, state domain.CourseState, query string, offset, limit int) ([]*domain.Course, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var out []*domain.Course
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		sql := SQLListCJ2CoursesByState
		args := []any{tenantID, string(state)}
		if query != "" {
			args = append(args, "%"+query+"%")
			sql += fmt.Sprintf(" AND title ILIKE $%d", len(args))
		}
		args = append(args, limit, offset)
		sql += fmt.Sprintf(" ORDER BY created_at DESC, course_id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCJ2Course(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// ListByStateAndAuthor returns CJ#2 courses in the given state authored by
// authorGCID for the tenant, paginated by offset/limit. query has the same
// optional `title ILIKE $N` dynamic-numbering semantics as ListByState.
//
// Backs the ONBOARD-UI F1 author-scoped training-admin list: an ADR-182
// `author` lists ONLY their own non-PUBLISHED courses. RLS (rls.ApplySession)
// keeps the read tenant-scoped; the `author_gcid = $3` filter is additive.
func (r *CourseRepo) ListByStateAndAuthor(ctx context.Context, tenantID string, state domain.CourseState, authorGCID string, query string, offset, limit int) ([]*domain.Course, error) {
	if r == nil || r.tx == nil {
		return nil, ErrNotImplemented
	}
	if authorGCID == "" {
		// Empty author would conflate with "any" — refuse rather than leak.
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var out []*domain.Course
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		sql := SQLListCJ2CoursesByStateAndAuthor
		args := []any{tenantID, string(state), authorGCID}
		if query != "" {
			args = append(args, "%"+query+"%")
			sql += fmt.Sprintf(" AND title ILIKE $%d", len(args))
		}
		args = append(args, limit, offset)
		sql += fmt.Sprintf(" ORDER BY created_at DESC, course_id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCJ2Course(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// -----------------------------------------------------------------------------
// Scanner — 22 cols (13 legacy + 9 CJ#2)
// -----------------------------------------------------------------------------

func scanCJ2Course(scan func(...any) error) (*domain.Course, error) {
	var (
		// Legacy 13 cols (mirror scanCourse).
		id, tenantID, instructorGCID, title, description string
		atomIDs                                          []string
		public, sfEligible                               bool
		priceCents                                       int64
		maxCapacity                                      int
		createdAt, updatedAt                             time.Time
		deletedAt                                        *time.Time

		// CJ#2 9 cols.
		state              string
		authorGCID         *string
		testSetIDs         []string
		instructorGCIDs    []string
		scheduledOpenAt    *time.Time
		reviewNotes        *string
		learningObjectives []string
		prerequisites      []string
		publishedAt        *time.Time
	)
	if err := scan(
		&id, &tenantID, &instructorGCID, &title, &description,
		&atomIDs, &public, &priceCents, &sfEligible, &maxCapacity,
		&createdAt, &updatedAt, &deletedAt,
		&state, &authorGCID, &testSetIDs, &instructorGCIDs,
		&scheduledOpenAt, &reviewNotes, &learningObjectives,
		&prerequisites, &publishedAt,
	); err != nil {
		return nil, err
	}
	c := &domain.Course{
		ID:                 id,
		TenantID:           tenantID,
		InstructorGCID:     instructorGCID,
		Title:              title,
		Description:        description,
		AtomIDs:            atomIDs,
		PriceSGDCents:      priceCents,
		SFEligible:         sfEligible,
		MaxCapacity:        maxCapacity,
		CreatedAt:          createdAt.UTC(),
		UpdatedAt:          updatedAt.UTC(),
		State:              domain.CourseState(state),
		TestSetIDs:         testSetIDs,
		InstructorGCIDs:    instructorGCIDs,
		LearningObjectives: learningObjectives,
		PrerequisiteNotes:  prerequisites,
	}
	if deletedAt != nil {
		t := deletedAt.UTC()
		c.DeletedAt = &t
	}
	if authorGCID != nil {
		c.AuthorGCID = *authorGCID
	}
	if reviewNotes != nil {
		c.ReviewNotes = *reviewNotes
	}
	if scheduledOpenAt != nil {
		t := scheduledOpenAt.UTC()
		c.ScheduledOpenAt = &t
	}
	if publishedAt != nil {
		t := publishedAt.UTC()
		c.PublishedAt = &t
	}
	_ = public // legacy column kept queryable but not stored on aggregate.
	return c, nil
}

// scanCourseCert scans the cert-definition columns (migration 0030) into a
// CertDefinition. cert_type + cert_passing_score_pct are nullable.
func scanCourseCert(scan func(...any) error) (domain.CertDefinition, error) {
	var (
		enabled    bool
		certType   *string
		passing    *int
		requireAll bool
	)
	if err := scan(&enabled, &certType, &passing, &requireAll); err != nil {
		return domain.CertDefinition{}, err
	}
	cd := domain.CertDefinition{Enabled: enabled, RequireAllContent: requireAll}
	if certType != nil {
		cd.CertType = domain.CertType(*certType)
	}
	if passing != nil {
		cd.PassingScorePct = *passing
	}
	return cd, nil
}
