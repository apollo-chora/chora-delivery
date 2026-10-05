// course_cj2_test.go — unit tests for the CJ#2 SaveCJ2 / GetCJ2 / ListByState
// adapter methods.
//
// Focus: SaveCJ2 binds the `public` boolean column correctly to the course
// state — `true` iff state == PUBLISHED, `false` otherwise. The
// `public_courses_catalog` cross-tenant view filters `WHERE public = true`,
// so a released CJ#2 course MUST bind public=true to appear in the public
// catalog projection (E2E-BE-CJ2-CATALOG-PROJECTION, 2026-05-24).
//
// Stubs the Querier so the SQL surface + arg bindings are exercised without
// a live DB. Mirrors course_test.go's pattern.
package pg_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// publicArgIndex is the bind position of the `public` column in
// SQLUpsertCourseCJ2. Per course_cj2.go: column order is
//
//	1=course_id, 2=tenant_id, 3=instructor_gcid, 4=title, 5=description,
//	6=atom_ids, 7=public, ...
const publicArgIndex = 6 // 0-indexed: $7 → args[6]

func newCJ2DraftCourseForTest(t *testing.T) *domain.Course {
	t.Helper()
	c, err := domain.NewCJ2Course(domain.NewCJ2CourseInput{
		TenantID:           tenantID,
		AuthorGCID:         "00000000-0000-7000-8000-000000001999",
		Title:              "RED test — CJ#2 catalog projection",
		Description:        "test desc",
		LearningObjectives: []string{"learn one thing"},
		TestSetIDs:         []string{"01970000-0000-7000-8000-0000000000aa"},
	})
	if err != nil {
		t.Fatalf("NewCJ2Course: %v", err)
	}
	return c
}

func TestSaveCJ2_BindsPublicFalse_WhenStateDraft(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)

	c := newCJ2DraftCourseForTest(t)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveCJ2(ctx, c); err != nil {
		t.Fatalf("SaveCJ2: %v", err)
	}
	// Find the UPSERT (last Exec). args[publicArgIndex] = public boolean.
	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + UPSERT); got %d", len(q.sqls))
	}
	upsertIdx := len(q.sqls) - 1
	if !strings.Contains(q.sqls[upsertIdx], "INSERT INTO courses") {
		t.Fatalf("expected INSERT INTO courses; got %q", q.sqls[upsertIdx])
	}
	args := q.args[upsertIdx]
	if len(args) <= publicArgIndex {
		t.Fatalf("expected ≥%d args; got %d", publicArgIndex+1, len(args))
	}
	gotPublic, ok := args[publicArgIndex].(bool)
	if !ok {
		t.Fatalf("expected bool at public arg index; got %T", args[publicArgIndex])
	}
	if gotPublic {
		t.Fatalf("DRAFT course should bind public=false; got true")
	}
}

func TestSaveCJ2_BindsPublicFalse_WhenStateAwaitingReview(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)

	c := newCJ2DraftCourseForTest(t)
	if err := c.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if c.State != domain.CourseStateAwaitingReview {
		t.Fatalf("expected AWAITING_REVIEW; got %s", c.State)
	}
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveCJ2(ctx, c); err != nil {
		t.Fatalf("SaveCJ2: %v", err)
	}
	args := q.args[len(q.args)-1]
	if got, _ := args[publicArgIndex].(bool); got {
		t.Fatalf("AWAITING_REVIEW course should bind public=false; got true")
	}
}

func TestSaveCJ2_BindsPublicTrue_WhenStatePublished(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)

	c := newCJ2DraftCourseForTest(t)
	if err := c.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := c.Release(domain.ReleaseInput{
		PriceSGDCents:   99900,
		SFEligible:      true,
		InstructorGCIDs: []string{"00000000-0000-7000-8000-000000001999"},
	}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if c.State != domain.CourseStatePublished {
		t.Fatalf("expected PUBLISHED; got %s", c.State)
	}
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveCJ2(ctx, c); err != nil {
		t.Fatalf("SaveCJ2: %v", err)
	}
	args := q.args[len(q.args)-1]
	got, ok := args[publicArgIndex].(bool)
	if !ok {
		t.Fatalf("expected bool at public arg index; got %T", args[publicArgIndex])
	}
	if !got {
		t.Fatalf("PUBLISHED course MUST bind public=true to surface in /api/catalog; got false")
	}
}

func TestSaveCJ2_BindsPublicFalse_WhenStateArchived(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	tx := &stubTxRunner{q: q}
	r := pg.NewCourseRepo(tx)

	c := newCJ2DraftCourseForTest(t)
	c.State = domain.CourseStateArchived
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveCJ2(ctx, c); err != nil {
		t.Fatalf("SaveCJ2: %v", err)
	}
	args := q.args[len(q.args)-1]
	if got, _ := args[publicArgIndex].(bool); got {
		t.Fatalf("ARCHIVED course should bind public=false (drops from catalog); got true")
	}
}

// ONBOARD-UI F1 — ListByStateAndAuthor must apply RLS first, filter the
// SELECT on author_gcid + state + soft-delete, and bind args in order
// (tenant, state, author, limit, offset).
func TestListByStateAndAuthor_AppliesRLSThenAuthorScopedSelect(t *testing.T) {
	t.Parallel()
	const author = "00000000-0000-7000-8000-000000001999"
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByStateAndAuthor(ctx, tenantID, domain.CourseStateDraft, author, "", 40, 20); err != nil {
		t.Fatalf("ListByStateAndAuthor: %v", err)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + select); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id (RLS); got %q", q.sqls[0])
	}
	sel := q.sqls[len(q.sqls)-1]
	if !strings.Contains(sel, "FROM courses") {
		t.Fatalf("select must read FROM courses; got %q", sel)
	}
	if !strings.Contains(sel, "author_gcid = $3") {
		t.Fatalf("select must scope on author_gcid; got %q", sel)
	}
	if !strings.Contains(sel, "deleted_at IS NULL") {
		t.Fatalf("select must exclude soft-deletes; got %q", sel)
	}
	// Bind order: $1 tenant, $2 state, $3 author, $4 limit, $5 offset.
	args := q.args[len(q.args)-1]
	if len(args) != 5 {
		t.Fatalf("expected 5 bound args; got %d (%v)", len(args), args)
	}
	if args[0] != tenantID || args[1] != string(domain.CourseStateDraft) || args[2] != author {
		t.Fatalf("arg bind mismatch: tenant=%v state=%v author=%v", args[0], args[1], args[2])
	}
	if args[3] != 20 || args[4] != 40 {
		t.Fatalf("expected limit=20 offset=40; got limit=%v offset=%v", args[3], args[4])
	}
}

// Empty author must refuse loud (no SQL) — conflating with "any" would leak.
func TestListByStateAndAuthor_EmptyAuthor_NoSQL(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	out, err := r.ListByStateAndAuthor(ctx, tenantID, domain.CourseStateDraft, "", "", 0, 20)
	if err != nil {
		t.Fatalf("ListByStateAndAuthor empty author: %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil result on empty author; got %v", out)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("no SQL must execute on empty author; got %d", len(q.sqls))
	}
}

// The CourseRepoCJ2Port forwarder delegates to CourseRepo.ListByStateAndAuthor.
func TestCourseRepoCJ2Port_ListByStateAndAuthor_Forwards(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	port := &pg.CourseRepoCJ2Port{Repo: pg.NewCourseRepo(&stubTxRunner{q: q})}
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := port.ListByStateAndAuthor(ctx, tenantID, domain.CourseStateAwaitingReview, "00000000-0000-7000-8000-000000001999", "", 0, 20); err != nil {
		t.Fatalf("port.ListByStateAndAuthor: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[len(q.sqls)-1], "author_gcid = $3") {
		t.Fatalf("forwarder did not reach the author-scoped SELECT; sqls=%v", q.sqls)
	}
}

// -----------------------------------------------------------------------------
// R+ course picker (?q= entity-picker search) — dynamic ILIKE arg numbering.
// Mirrors chora-creation's atom ?q= pattern (commit 85b2bae14): a non-empty
// query appends `AND title ILIKE $N`, shifting limit/offset to the next
// dynamic positions; an empty query leaves the bind order untouched
// (backward compatible with the pre-search admin queue).
// -----------------------------------------------------------------------------

func TestListByState_AppliesTitleILIKE_WhenQueryNonEmpty(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByState(ctx, tenantID, domain.CourseStatePublished, "road", 40, 20); err != nil {
		t.Fatalf("ListByState: %v", err)
	}
	sel := q.sqls[len(q.sqls)-1]
	if !strings.Contains(sel, "AND title ILIKE $3") {
		t.Fatalf("select must apply the ILIKE needle at $3; got %q", sel)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 5 {
		t.Fatalf("expected 5 bound args; got %d (%v)", len(args), args)
	}
	if args[0] != tenantID || args[1] != string(domain.CourseStatePublished) {
		t.Fatalf("arg bind mismatch: tenant=%v state=%v", args[0], args[1])
	}
	if args[2] != "%road%" {
		t.Fatalf("expected the needle wrapped in percent signs; got %v", args[2])
	}
	if args[3] != 20 || args[4] != 40 {
		t.Fatalf("expected limit=20 offset=40 after the needle; got limit=%v offset=%v", args[3], args[4])
	}
}

func TestListByState_NoILIKE_WhenQueryEmpty(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.ListByState(ctx, tenantID, domain.CourseStatePublished, "", 0, 20); err != nil {
		t.Fatalf("ListByState: %v", err)
	}
	sel := q.sqls[len(q.sqls)-1]
	if strings.Contains(sel, "ILIKE") {
		t.Fatalf("empty query must not add an ILIKE clause; got %q", sel)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 4 {
		t.Fatalf("expected 4 bound args (tenant,state,limit,offset); got %d (%v)", len(args), args)
	}
}

func TestListByStateAndAuthor_AppliesTitleILIKE_WhenQueryNonEmpty(t *testing.T) {
	t.Parallel()
	const author = "00000000-0000-7000-8000-000000001999"
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.ListByStateAndAuthor(ctx, tenantID, domain.CourseStateDraft, author, "road", 40, 20); err != nil {
		t.Fatalf("ListByStateAndAuthor: %v", err)
	}
	sel := q.sqls[len(q.sqls)-1]
	if !strings.Contains(sel, "AND title ILIKE $4") {
		t.Fatalf("select must apply the ILIKE needle at $4; got %q", sel)
	}
	args := q.args[len(q.args)-1]
	if len(args) != 6 {
		t.Fatalf("expected 6 bound args; got %d (%v)", len(args), args)
	}
	if args[3] != "%road%" {
		t.Fatalf("expected the needle wrapped in percent signs; got %v", args[3])
	}
	if args[4] != 20 || args[5] != 40 {
		t.Fatalf("expected limit=20 offset=40 after the needle; got limit=%v offset=%v", args[4], args[5])
	}
}
