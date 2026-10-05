// testset.go — Postgres adapter for the TestSet aggregate.
//
// SCHEMA: see migrations/0009_test_sets.up.sql.
//
// TestSet is a Content Delivery aggregate root per
// chora-contracts/openapi/delivery-test-sets.yaml. The repo persists both
// the test_sets parent + its test_set_questions children inside a single
// transaction so the aggregate's invariants (question_count + total_points
// derive from child rows) never break under partial-write conditions.
//
// Resilience-priority directive (`feedback_resilience_priority`):
//
//   - Idempotency: UPSERT on conflict(test_set_id) DO UPDATE — Save is
//     retry-safe under multi-pod replays and concurrent writers.
//   - Multi-user concurrent: every write runs inside a transaction with
//     `SET LOCAL chora.tenant_id` applied first, so under PgBouncer
//     transaction-pooling the tenant context never leaks across sibling
//     requests.
//   - Soft-delete-aware: Get filters `WHERE deleted_at IS NULL` per
//     ddd-enforcement #6; child rows likewise.
//   - RLS: every read/write applies rls.ApplySession before the user query
//     so the row-level policy on `test_sets` (tenant_id match) enforces
//     tenant isolation. The child policy chains through the parent.
//
// Cross-DB queries forbidden — chora-delivery reads only chora_delivery.
// Inter-domain side effects flow through Pub/Sub via the outbox.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// ErrInvalidTestSet is the sentinel for a nil-input write.
var ErrInvalidTestSet = errors.New("pg: test_set is nil")

// -----------------------------------------------------------------------------
// SQL templates (exported for CI / Cloud Build lint to grep)
// -----------------------------------------------------------------------------

// SQLUpsertTestSet — INSERT … ON CONFLICT DO UPDATE on test_sets.
//
// Idempotent on test_set_id. Mirrors course.SQLUpsertCourse pattern so
// retries land cleanly without duplicate rows.
//
// Lane 1c (migration 0028): source_job_id is immutable provenance — the
// COALESCE keeps any stored non-null value so a later metadata Save (which
// re-upserts the parent row) can never un-set or re-point the batch-job
// linkage. The UNIQUE partial index idx_test_sets_source_job_id rejects a
// second row claiming the same job (subscriber idempotency backstop).
const SQLUpsertTestSet = `
INSERT INTO test_sets (
    test_set_id, tenant_id, author_gcid, title, description, state,
    created_at, updated_at, published_at, deleted_at, source_job_id
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11
)
ON CONFLICT (test_set_id) DO UPDATE SET
    title         = EXCLUDED.title,
    description   = EXCLUDED.description,
    state         = EXCLUDED.state,
    updated_at    = EXCLUDED.updated_at,
    published_at  = EXCLUDED.published_at,
    deleted_at    = EXCLUDED.deleted_at,
    source_job_id = COALESCE(test_sets.source_job_id, EXCLUDED.source_job_id)
`

// SQLUpsertTestSetQuestion — UPSERT on test_set_questions.
//
// Per-question idempotency keys land on test_set_question_id. RLS scope
// is enforced by the parent table's policy (the child row is only visible
// when the parent's tenant_id matches).
//
// Fix-F: payload_snapshot + snapshot_at land via migrations/0011_...up.sql.
// Captured at TestSet.PublishWithSnapshot() time from chora_creation via
// gRPC. Per ddd-enforcement #3 (cross-DB queries FORBIDDEN) the grading
// path NEVER touches chora_creation at runtime.
//
// LEG3-D R3 Option B (migrations/0013_test_set_questions_question_id.up.sql):
// the row carries a distinct `question_id` column (UUIDv7 of the embedded
// Question inside the atom payload) alongside `question_atom_id`. The
// snapshotter caller passes `question_id` to chora-creation.
//
// COALESCE preserves any pre-existing snapshot if the caller's aggregate
// state has an empty PayloadSnapshot (e.g., re-save of DRAFT mutations
// after publish). The Publish path always supplies a fresh snapshot.
const SQLUpsertTestSetQuestion = `
INSERT INTO test_set_questions (
    test_set_question_id, test_set_id, question_atom_id, question_id, question_type,
    display_order, points, created_at, updated_at, deleted_at,
    payload_snapshot, snapshot_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
    $11, $12
)
ON CONFLICT (test_set_question_id) DO UPDATE SET
    display_order    = EXCLUDED.display_order,
    points           = EXCLUDED.points,
    updated_at       = EXCLUDED.updated_at,
    deleted_at       = EXCLUDED.deleted_at,
    payload_snapshot = COALESCE(EXCLUDED.payload_snapshot, test_set_questions.payload_snapshot),
    snapshot_at      = COALESCE(EXCLUDED.snapshot_at, test_set_questions.snapshot_at)
`

// testSetCols is the SELECT column list for the test_sets header row.
//
// Lane 1c: source_job_id trails at the end so the existing position-based
// scans extend without reordering (mirrors the 0011 payload_snapshot
// precedent on the child table).
const testSetCols = `test_set_id, tenant_id, author_gcid, title, description, state,
       created_at, updated_at, published_at, source_job_id`

// SQLSelectTestSetByID returns one test_set by id, RLS-scoped.
const SQLSelectTestSetByID = `
SELECT ` + testSetCols + `
FROM test_sets
WHERE test_set_id = $1
  AND tenant_id = $2
  AND deleted_at IS NULL
`

// SQLSelectTestSetBySourceJobID returns the (at most one — UNIQUE partial
// index) test_set assembled from the given chora-creation batch job,
// RLS-scoped + tenant-bound. Header only: the subscriber's idempotency
// check needs existence, not children.
const SQLSelectTestSetBySourceJobID = `
SELECT ` + testSetCols + `
FROM test_sets
WHERE source_job_id = $1
  AND tenant_id = $2
  AND deleted_at IS NULL
`

// SQLSelectTestSetsList — tenant-scoped list with OR-within-family
// AND-across-family filters per delivery-test-sets.yaml#listTestSets.
//
// Bind order:
//
//	$1 tenant_id (UUID)
//	$2 states ARRAY (text[]); empty array ⇒ no state filter on this clause
//	$3 author_gcids ARRAY (text[]); empty array ⇒ no author filter
//	$4 title needle (text); empty string ⇒ no needle
//	$5 page_size (int) — caller-capped before bind
//
// ORDER BY pins on (created_at DESC, test_set_id DESC) to give the cursor a
// stable tie-break. Pagination cursor work lands in a follow-up (M14.x);
// for v1 the demo persona has <20 test-sets so a single page is enough.
//
// LEG3-E / FE-BUG-6 fix — left-joins an aggregate subquery on the children
// table so each row carries the live question_count + total_points. Without
// this the list view rendered "0 questions" for every test-set, even ones
// the single-GET endpoint confirmed have N MCQs. Subquery filters
// `deleted_at IS NULL` to match the live count on /test-sets/{id} (soft-
// deleted question inclusions don't count toward totals).
// Lane 1c: $6 is the source_job_id exact-match needle. It binds SQL NULL
// (NOT the empty string — ”::uuid is a 22P02) when the filter is absent so
// the `$6::uuid IS NULL` term disables the clause without evaluating the
// cast against a non-uuid value.
const SQLSelectTestSetsList = `
SELECT ts.test_set_id, ts.tenant_id, ts.author_gcid, ts.title, ts.description, ts.state,
       ts.created_at, ts.updated_at, ts.published_at, ts.source_job_id,
       COALESCE(child.question_count, 0) AS question_count,
       COALESCE(child.total_points, 0)   AS total_points
FROM test_sets ts
LEFT JOIN (
    SELECT test_set_id,
           COUNT(*)::int         AS question_count,
           COALESCE(SUM(points), 0)::float8 AS total_points
    FROM test_set_questions
    WHERE deleted_at IS NULL
    GROUP BY test_set_id
) child ON child.test_set_id = ts.test_set_id
WHERE ts.tenant_id = $1::uuid
  AND ts.deleted_at IS NULL
  AND (cardinality($2::text[]) = 0 OR ts.state = ANY($2::text[]))
  AND (cardinality($3::text[]) = 0 OR ts.author_gcid = ANY($3::uuid[]))
  AND ($4::text = '' OR lower(ts.title) LIKE '%' || lower($4) || '%')
  AND ($6::uuid IS NULL OR ts.source_job_id = $6::uuid)
ORDER BY ts.created_at DESC, ts.test_set_id DESC
LIMIT $5
`

// testSetQuestionCols is the SELECT column list for the child row.
//
// Fix-F: payload_snapshot + snapshot_at trail at the end so existing
// position-based scans don't reorder. Both are nullable for DRAFT rows.
//
// LEG3-D R3 Option B — `question_id` is the embedded Question UUID
// (distinct from `question_atom_id`). Added between question_atom_id and
// question_type so the scan order stays predictable. NOT NULL post-
// 0013 backfill.
const testSetQuestionCols = `test_set_question_id, test_set_id, question_atom_id, question_id,
       question_type, display_order, points, created_at,
       payload_snapshot, snapshot_at`

// SQLSelectTestSetQuestionsByTestSet returns the non-deleted question
// inclusions for a test_set, ordered by display_order ASC. The RLS policy
// on test_set_questions only admits rows whose parent's tenant_id matches
// the SET LOCAL value, so the explicit tenant filter on test_sets is the
// only place we bind it.
const SQLSelectTestSetQuestionsByTestSet = `
SELECT ` + testSetQuestionCols + `
FROM test_set_questions
WHERE test_set_id = $1
  AND deleted_at IS NULL
ORDER BY display_order ASC, created_at ASC
`

// -----------------------------------------------------------------------------
// TestSetRepo
// -----------------------------------------------------------------------------

// TestSetRepo is the Postgres-backed TestSet repo.
type TestSetRepo struct {
	tx TxRunner
}

// NewTestSetRepo constructs a pg TestSetRepo around a TxRunner.
//
// A nil TxRunner degrades every method to ErrNotImplemented — mirrors
// CourseRepo / ApplicationRepo so cmd/server can fall back to the
// in-memory adapter in local dev.
func NewTestSetRepo(tx TxRunner) *TestSetRepo {
	return &TestSetRepo{tx: tx}
}

// Save persists a TestSet aggregate. Idempotent on test_set_id +
// test_set_question_id.
//
// Single transaction over (parent UPSERT, child UPSERTs) so the aggregate's
// question_count + total_points invariant never sees a partial write.
//
// The caller MUST have set tenant_id on ctx via tracing.WithTenantID;
// rls.ApplySession returns ErrNoTenantContext otherwise.
func (r *TestSetRepo) Save(ctx context.Context, ts *domain.TestSet) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if ts == nil {
		return ErrInvalidTestSet
	}
	// The TestSetPort.Save signature carries no ctx-tenant (the HTTP layer
	// passes X-Tenant-Id by header, not ctx), so the adapter derives tenant
	// from the aggregate before rls.ApplySession.
	ctx = tracing.WithTenantID(ctx, ts.TenantID)
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		// Parent UPSERT. $11 source_job_id binds NULL for hand-authored
		// sets (nil pointer) — never the empty string.
		if _, err := q.Exec(ctx, SQLUpsertTestSet,
			ts.ID, ts.TenantID, ts.AuthorGCID, ts.Title, nullableString(ts.Description),
			string(ts.State),
			ts.CreatedAt, ts.UpdatedAt, nullTime(deref(ts.PublishedAt)),
			nullTime(deref(ts.DeletedAt)), nullableStringPtr(ts.SourceJobID),
		); err != nil {
			return fmt.Errorf("pg: upsert test_set: %w", err)
		}
		// Child UPSERTs — only persist non-deleted rows. RemoveQuestion soft-
		// deletes the in-memory child; we don't surface those to the DB on
		// Save (the row already exists with deleted_at NULL elsewhere if it
		// pre-existed; for a fresh aggregate, removed rows simply never
		// reach Save).
		for _, qrow := range ts.Questions() {
			// LEG3-D R3 Option B — bind question_id ($4) explicitly,
			// distinct from question_atom_id ($3). 12 args after the
			// column addition.
			if _, err := q.Exec(ctx, SQLUpsertTestSetQuestion,
				qrow.ID, qrow.TestSetID, qrow.QuestionAtomID, qrow.QuestionID, qrow.QuestionType,
				qrow.DisplayOrder, qrow.Points,
				qrow.CreatedAt, qrow.UpdatedAt, nullTime(deref(qrow.DeletedAt)),
				nullJSONString(qrow.PayloadSnapshot), nullTime(deref(qrow.SnapshotAt)),
			); err != nil {
				return fmt.Errorf("pg: upsert test_set_question %s: %w", qrow.ID, err)
			}
		}
		return nil
	})
}

// SaveQuestionRemoval persists a soft-delete on a single question inclusion.
// Used by the HTTP layer's DELETE endpoint without re-saving the full
// aggregate (which would re-upsert sibling rows unnecessarily).
//
// The parent test_set is also touched (updated_at bump) so cache-busting
// downstream stays correct.
func (r *TestSetRepo) SaveQuestionRemoval(ctx context.Context, ts *domain.TestSet, testSetQuestionID string) error {
	if r == nil || r.tx == nil {
		return ErrNotImplemented
	}
	if ts == nil {
		return ErrInvalidTestSet
	}
	ctx = tracing.WithTenantID(ctx, ts.TenantID)
	return r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		now := time.Now().UTC()
		const sql = `UPDATE test_set_questions
                     SET deleted_at = $2, updated_at = $2
                     WHERE test_set_question_id = $1
                       AND deleted_at IS NULL`
		if _, err := q.Exec(ctx, sql, testSetQuestionID, now); err != nil {
			return fmt.Errorf("pg: soft-delete test_set_question: %w", err)
		}
		// Bump parent updated_at.
		const bump = `UPDATE test_sets SET updated_at = $2 WHERE test_set_id = $1`
		if _, err := q.Exec(ctx, bump, ts.ID, now); err != nil {
			return fmt.Errorf("pg: bump test_set updated_at: %w", err)
		}
		return nil
	})
}

// Get returns a TestSet by (tenantID, id) with its question list populated.
// Returns (nil, false, nil) when no row matches under the RLS-scoped query.
func (r *TestSetRepo) Get(ctx context.Context, tenantID, id string) (*domain.TestSet, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var found *domain.TestSet
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectTestSetByID, id, tenantID)
		ts, err := scanTestSetHeader(row.Scan)
		if err != nil {
			// Not-found path — surface as ok=false.
			return nil
		}
		// Load child question rows.
		rows, err := q.Query(ctx, SQLSelectTestSetQuestionsByTestSet, ts.ID)
		if err != nil {
			return fmt.Errorf("pg: list test_set_questions: %w", err)
		}
		defer rows.Close()
		var children []*childRow
		for rows.Next() {
			child, scanErr := scanTestSetQuestionRow(rows.Scan)
			if scanErr != nil {
				return fmt.Errorf("pg: scan test_set_question: %w", scanErr)
			}
			children = append(children, child)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pg: iter test_set_questions: %w", err)
		}
		// Rehydrate questions via the load-time HydrateQuestion API which
		// bypasses the append-only-on-PUBLISHED state guard. This is
		// load-time only — mutation paths still go through AddQuestion.
		//
		// Fix-F: carry through PayloadSnapshot + SnapshotAt so the runtime
		// grading path can consult the canonical answer key (test-set's
		// snapshot column populated at PublishWithSnapshot time).
		for _, ch := range children {
			ts.HydrateQuestion(domain.TestSetQuestion{
				ID:              ch.ID,
				TestSetID:       ch.TestSetID,
				QuestionAtomID:  ch.QuestionAtomID,
				QuestionID:      ch.QuestionID,
				QuestionType:    ch.QuestionType,
				DisplayOrder:    ch.DisplayOrder,
				Points:          ch.Points,
				CreatedAt:       ch.CreatedAt,
				UpdatedAt:       ch.CreatedAt,
				PayloadSnapshot: ch.PayloadSnapshot,
				SnapshotAt:      ch.SnapshotAt,
			})
		}
		found = ts
		return nil
	})
	if err != nil || found == nil {
		return nil, false, err
	}
	return found, true, nil
}

// GetBySourceJobID returns the test_set assembled from the supplied
// chora-creation batch job (tenant-scoped header, no question children).
// Returns (nil, false, nil) when no row matches under the RLS-scoped query.
//
// Lane 1c (CHO-1703 / ADR-180 D10): this is the subscriber's read-side
// idempotency check — at-least-once event redelivery finds the existing row
// + ACK-no-ops; the UNIQUE partial index (migration 0028) backstops the
// concurrent-redelivery race two pods could otherwise hit between this read
// and Save.
func (r *TestSetRepo) GetBySourceJobID(ctx context.Context, tenantID, sourceJobID string) (*domain.TestSet, bool, error) {
	if r == nil || r.tx == nil {
		return nil, false, ErrNotImplemented
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var found *domain.TestSet
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		row := q.QueryRow(ctx, SQLSelectTestSetBySourceJobID, sourceJobID, tenantID)
		ts, err := scanTestSetHeader(row.Scan)
		if err != nil {
			// Not-found path — surface as ok=false (mirrors Get).
			return nil
		}
		found = ts
		return nil
	})
	if err != nil || found == nil {
		return nil, false, err
	}
	return found, true, nil
}

// List returns tenant-scoped test-set headers (no question children).
//
// Filter semantics per the domain port:
//
//   - filter.States: nil/empty ⇒ default {DRAFT, PUBLISHED} (ARCHIVED opt-in).
//   - filter.AuthorGCIDs: nil/empty ⇒ no author filter.
//   - filter.TitleQuery: "" ⇒ no needle; non-empty ⇒ case-insensitive substring.
//   - filter.SourceJobID: "" ⇒ no filter; non-empty ⇒ exact match (Lane 1c).
//   - filter.PageSize ≤ 0 coerces to 20; cap at 100.
//   - filter.PageToken: accepted for forward-compat; ignored in v1 (cursor
//     pagination is M14.x scope).
//
// Returns (items, nextPageToken, error). nextPageToken == "" in v1. Per
// ddd-enforcement #3 the query never crosses to chora_creation; the
// snapshot column is intentionally NOT joined here — the list view shows
// headers only.
func (r *TestSetRepo) List(ctx context.Context, tenantID string, filter domain.TestSetListFilter) ([]*domain.TestSet, string, error) {
	if r == nil || r.tx == nil {
		return nil, "", ErrNotImplemented
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	// Apply the default state-set when caller passes none.
	effectiveStates := make([]string, 0, max(len(filter.States), 2))
	if len(filter.States) == 0 {
		effectiveStates = append(effectiveStates,
			string(domain.TestSetStateDraft),
			string(domain.TestSetStatePublished),
		)
	} else {
		for _, st := range filter.States {
			effectiveStates = append(effectiveStates, string(st))
		}
	}
	authors := filter.AuthorGCIDs
	if authors == nil {
		authors = []string{}
	}
	ctx = tracing.WithTenantID(ctx, tenantID)
	var out []*domain.TestSet
	err := r.tx.RunInTx(ctx, func(ctx context.Context, q Querier) error {
		if err := rls.ApplySession(ctx, qToExecer(q)); err != nil {
			return err
		}
		rows, err := q.Query(ctx, SQLSelectTestSetsList,
			tenantID, effectiveStates, authors, filter.TitleQuery, pageSize,
			nullableString(strings.TrimSpace(filter.SourceJobID)))
		if err != nil {
			return fmt.Errorf("pg: select test_sets list: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			ts, qCount, tPoints, scanErr := scanTestSetHeaderWithCounters(rows.Scan)
			if scanErr != nil {
				return fmt.Errorf("pg: scan test_set row: %w", scanErr)
			}
			// LEG3-E — inject the aggregate subquery counts so list views
			// render accurate question_count + total_points without forcing
			// an N+1 child load per row.
			ts.HydrateSummaryCounters(qCount, tPoints)
			out = append(out, ts)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("pg: iter test_sets list: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return out, "", nil
}

// max returns the larger of two ints. Go 1.21+ has builtin max() but our
// minimum is 1.21 so this stays for older toolchains.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// -----------------------------------------------------------------------------
// Row scanning
// -----------------------------------------------------------------------------

// scanTestSetHeaderWithCounters consumes one test_sets list row scanned
// against SQLSelectTestSetsList: the 9 parent header columns plus the 2
// aggregated child counters (question_count + total_points). Wraps the
// shared scanTestSetHeader logic so the counter projection stays optional
// to the broader scan path used by Get + the legacy header-only call sites.
func scanTestSetHeaderWithCounters(scan func(...any) error) (*domain.TestSet, int, float64, error) {
	var (
		id, tenantID, authorGCID, title string
		description                     *string
		state                           string
		createdAt, updatedAt            time.Time
		publishedAt                     *time.Time
		sourceJobID                     *string
		qCount                          int
		tPoints                         float64
	)
	if err := scan(
		&id, &tenantID, &authorGCID, &title, &description, &state,
		&createdAt, &updatedAt, &publishedAt, &sourceJobID,
		&qCount, &tPoints,
	); err != nil {
		return nil, 0, 0, err
	}
	desc := ""
	if description != nil {
		desc = *description
	}
	ts := &domain.TestSet{
		ID:          id,
		TenantID:    tenantID,
		AuthorGCID:  authorGCID,
		Title:       title,
		Description: desc,
		State:       domain.TestSetState(state),
		CreatedAt:   createdAt.UTC(),
		UpdatedAt:   updatedAt.UTC(),
		SourceJobID: sourceJobID,
	}
	if publishedAt != nil {
		t := publishedAt.UTC()
		ts.PublishedAt = &t
	}
	return ts, qCount, tPoints, nil
}

// scanTestSetHeader consumes one test_sets row into a domain.TestSet (without
// questions; the caller fans out to load the children).
//
// Column order matches testSetCols (10 cols — source_job_id trails per the
// Lane 1c column add).
func scanTestSetHeader(scan func(...any) error) (*domain.TestSet, error) {
	var (
		id, tenantID, authorGCID, title string
		description                     *string
		state                           string
		createdAt, updatedAt            time.Time
		publishedAt                     *time.Time
		sourceJobID                     *string
	)
	if err := scan(
		&id, &tenantID, &authorGCID, &title, &description, &state,
		&createdAt, &updatedAt, &publishedAt, &sourceJobID,
	); err != nil {
		return nil, err
	}
	desc := ""
	if description != nil {
		desc = *description
	}
	ts := &domain.TestSet{
		ID:          id,
		TenantID:    tenantID,
		AuthorGCID:  authorGCID,
		Title:       title,
		Description: desc,
		State:       domain.TestSetState(state),
		CreatedAt:   createdAt.UTC(),
		UpdatedAt:   updatedAt.UTC(),
		SourceJobID: sourceJobID,
	}
	if publishedAt != nil {
		t := publishedAt.UTC()
		ts.PublishedAt = &t
	}
	return ts, nil
}

// childRow is a temporary holder for a scanned test_set_questions row. The
// domain.TestSet has no setter for the question slice (it owns its children
// strictly), so we replay AddQuestion to rehydrate.
//
// LEG3-D R3 Option B — `QuestionID` is the embedded Question UUID
// (distinct from `QuestionAtomID`). Both round-trip through the scan
// path so the snapshot at publish time uses the correct identifier.
type childRow struct {
	ID              string
	TestSetID       string
	QuestionAtomID  string
	QuestionID      string
	QuestionType    string
	DisplayOrder    int
	Points          float64
	CreatedAt       time.Time
	PayloadSnapshot string
	SnapshotAt      *time.Time
}

func scanTestSetQuestionRow(scan func(...any) error) (*childRow, error) {
	var (
		id, testSetID, questionAtomID, questionID, questionType string
		displayOrder                                            int
		points                                                  float64
		createdAt                                               time.Time
		payloadSnapshot                                         *string
		snapshotAt                                              *time.Time
	)
	if err := scan(
		&id, &testSetID, &questionAtomID, &questionID, &questionType,
		&displayOrder, &points, &createdAt,
		&payloadSnapshot, &snapshotAt,
	); err != nil {
		return nil, err
	}
	out := &childRow{
		ID:             id,
		TestSetID:      testSetID,
		QuestionAtomID: questionAtomID,
		QuestionID:     questionID,
		QuestionType:   questionType,
		DisplayOrder:   displayOrder,
		Points:         points,
		CreatedAt:      createdAt.UTC(),
	}
	if payloadSnapshot != nil {
		out.PayloadSnapshot = *payloadSnapshot
	}
	if snapshotAt != nil {
		t := snapshotAt.UTC()
		out.SnapshotAt = &t
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// nullableString returns nil when s is empty so we bind SQL NULL on the
// description column rather than ” (the column is nullable per migration).
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullJSONString returns nil when s is empty so we bind SQL NULL on a JSONB
// column. Non-empty strings are passed through as text — pgx will quote them
// + cast as JSONB. Per migrations/0011_test_set_questions_snapshot.up.sql
// payload_snapshot is nullable; this preserves NULL semantics for DRAFT rows
// that haven't been published yet.
func nullJSONString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableStringPtr binds SQL NULL for a nil-or-empty *string (the
// source_job_id column is nullable; ”::uuid would be a 22P02).
func nullableStringPtr(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}
