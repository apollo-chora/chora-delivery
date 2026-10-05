// catalogue_test.go — unit tests for the pgx-backed CatalogueRepo.
//
// Mirrors course_test.go + application_test.go: stubs the Querier so the
// SQL surface + RLS contract are exercised without a live DB. The live
// cross-tenant public-catalogue read is verified separately in
// catalogue_integration_test.go.
//
// CatalogueRepo is the production adapter behind domain.CataloguePort. It
// replaces the in-memory domain.Catalogue in cmd/server wiring so the
// public course catalogue is backed by chora_delivery and survives pod
// restarts (Phyllis Step 5 PublicDiscovery — no stubs, no in-memory).
//
// Two read paths, both covered here:
//
//   - PUBLIC browse (q.TenantID == "" or q.Visibility == VisibilityFilterPublic)
//     → reads the public_courses_catalog VIEW (migrations/0006). The view's
//     OWNER rule bypasses RLS, so it is cross-tenant by design and needs
//     NO SET LOCAL chora.tenant_id. The WHERE public=true clause is the
//     gate; non-public rows can never leak.
//   - TENANT-SCOPED browse (q.TenantID set, non-public visibility filter)
//     → reads the courses TABLE with rls.ApplySession applied first, so the
//     RLS policy on `courses` filters to the caller's tenant.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// catTenant is reused across CatalogueRepo unit tests. tenantID + courseID
// + gcid + instructorGCID constants are defined in application_test.go +
// course_test.go (same package).
const catTenant = "01970000-0000-7000-8000-0000000000ca"

// -----------------------------------------------------------------------------
// Port conformance — the in-memory domain.Catalogue (via the adapter wrapper)
// and the pg CatalogueRepo BOTH satisfy domain.CataloguePort.
// -----------------------------------------------------------------------------

func TestCatalogueRepo_SatisfiesCataloguePort(t *testing.T) {
	t.Parallel()
	var _ domain.CataloguePort = (*pg.CatalogueRepo)(nil)
	var _ domain.CataloguePort = domain.NewInMemCatalogue()
}

// -----------------------------------------------------------------------------
// Nil-guard — a nil TxRunner degrades to ErrNotImplemented (mirrors CourseRepo)
// -----------------------------------------------------------------------------

func TestCatalogueRepo_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCatalogueRepo(nil)
	ctx := context.Background()
	if err := r.Save(ctx, &domain.PublicCourse{}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Save: expected ErrNotImplemented; got %v", err)
	}
	if _, _, err := r.Get(ctx, courseID); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Get: expected ErrNotImplemented; got %v", err)
	}
	if _, _, err := r.Search(ctx, domain.CatalogueQuery{}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Search: expected ErrNotImplemented; got %v", err)
	}
	if _, err := r.SearchCursor(ctx, domain.CatalogueQuery{}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("SearchCursor: expected ErrNotImplemented; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Save — UPSERT on course_id, RLS applied first
// -----------------------------------------------------------------------------

func TestCatalogueRepo_Save_RejectsNil(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	if err := r.Save(context.Background(), nil); !errors.Is(err, pg.ErrInvalidPublicCourse) {
		t.Fatalf("expected ErrInvalidPublicCourse on nil; got %v", err)
	}
}

func TestCatalogueRepo_Save_AppliesRLSThenUpserts(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       catTenant,
		Title:          "CSPO Prep",
		InstructorGCID: instructorGCID,
		InstructorName: "Mr. Chen",
		Visibility:     domain.VisibilityPublic,
	})
	if err != nil {
		t.Fatalf("NewPublicCourse: %v", err)
	}
	// Save derives the RLS tenant from pc.TenantID (the HTTP port signature
	// carries no ctx-tenant for Save) — the adapter sets it on ctx itself.
	if err := r.Save(context.Background(), pc); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected at least 2 SQLs (SET LOCAL + UPSERT); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "INSERT INTO courses") {
		t.Fatalf("expected INSERT INTO courses; got %q", last)
	}
	if !strings.Contains(last, "ON CONFLICT (course_id) DO UPDATE") {
		t.Fatalf("expected UPSERT on conflict (course_id); got %q", last)
	}
}

func TestCatalogueRepo_Save_RejectsMissingTenant(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	// A PublicCourse with no TenantID can't be RLS-scoped — fail fast rather
	// than emit a SET LOCAL with an empty payload.
	err := r.Save(context.Background(), &domain.PublicCourse{
		ID:             courseID,
		Title:          "x",
		InstructorGCID: instructorGCID,
		Visibility:     domain.VisibilityPublic,
	})
	if !errors.Is(err, pg.ErrPublicCourseMissingTenant) {
		t.Fatalf("expected ErrPublicCourseMissingTenant; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// SearchCursor — PUBLIC path reads the view, NO SET LOCAL
// -----------------------------------------------------------------------------

// stubCountRow returns a stubRow that scans a single int count value.
func stubCountRow(n int) pg.Row {
	return stubRow{scanFn: func(dest ...any) error {
		if v, ok := dest[0].(*int); ok {
			*v = n
		}
		return nil
	}}
}

func TestCatalogueRepo_SearchCursor_PublicFilter_ReadsViewWithoutRLS(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(0) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	// VisibilityFilterPublic with no TenantID — the cross-tenant public
	// discovery path. MUST hit the view and MUST NOT emit SET LOCAL (the
	// view's OWNER rule bypasses RLS; setting an empty tenant would error).
	_, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      20,
	})
	if err != nil {
		t.Fatalf("SearchCursor public: %v", err)
	}
	for _, s := range q.sqls {
		if strings.Contains(s, "SET LOCAL") {
			t.Fatalf("public view path must NOT emit SET LOCAL; got %q", s)
		}
	}
	if len(q.sqls) == 0 || !strings.Contains(q.sqls[len(q.sqls)-1], "public_courses_catalog") {
		t.Fatalf("expected SELECT FROM public_courses_catalog; got %v", q.sqls)
	}
}

func TestCatalogueRepo_SearchCursor_PublicFilter_AppliesCursorAndLimit(t *testing.T) {
	t.Parallel()
	var capturedSQL string
	var capturedArgs []any
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			capturedSQL = sql
			capturedArgs = args
			return &stubRows{}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(0) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	_, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility:  domain.VisibilityFilterPublic,
		AfterCursor: "01970000-0000-7000-8000-000000000001",
		First:       5,
	})
	if err != nil {
		t.Fatalf("SearchCursor: %v", err)
	}
	// Relay cursor pagination: WHERE course_id > $cursor ORDER BY course_id
	// LIMIT $first+1 (the +1 row drives HasNextPage).
	if !strings.Contains(capturedSQL, "course_id >") {
		t.Fatalf("expected cursor predicate course_id > $; got %q", capturedSQL)
	}
	if !strings.Contains(capturedSQL, "ORDER BY course_id") {
		t.Fatalf("expected ORDER BY course_id; got %q", capturedSQL)
	}
	if !strings.Contains(capturedSQL, "LIMIT") {
		t.Fatalf("expected LIMIT; got %q", capturedSQL)
	}
	if len(capturedArgs) < 2 {
		t.Fatalf("expected cursor + limit args; got %v", capturedArgs)
	}
}

// TestCatalogueRepo_SearchCursor_EmptyCursor_BindsNilUUID is the regression
// guard for the first-page cursor bug: `course_id > ”` makes Postgres throw
// `invalid input syntax for type uuid`. The empty AfterCursor MUST bind the
// all-zero nil UUID instead (every real course_id sorts after it).
func TestCatalogueRepo_SearchCursor_EmptyCursor_BindsNilUUID(t *testing.T) {
	t.Parallel()
	var cursorArg any
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if len(args) > 0 {
				cursorArg = args[0]
			}
			return &stubRows{}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(0) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	// First-page request — AfterCursor empty.
	_, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      20,
	})
	if err != nil {
		t.Fatalf("SearchCursor: %v", err)
	}
	got, _ := cursorArg.(string)
	if got != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("empty cursor must bind the nil UUID (Postgres rejects uuid > ''); got %q", got)
	}
}

func TestCatalogueRepo_SearchCursor_PublicFilter_HasNextPage(t *testing.T) {
	t.Parallel()
	// Return First+1 rows so the adapter trims the sentinel and sets
	// HasNextPage=true.
	mkRow := func(id string) func(dest ...any) error {
		return func(dest ...any) error { return scanViewRowInto(dest, id, catTenant) }
	}
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				mkRow("01970000-0000-7000-8000-00000000a001"),
				mkRow("01970000-0000-7000-8000-00000000a002"),
				mkRow("01970000-0000-7000-8000-00000000a003"), // sentinel +1
			}}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(3) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	page, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      2,
	})
	if err != nil {
		t.Fatalf("SearchCursor: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected exactly First=2 items after sentinel trim; got %d", len(page.Items))
	}
	if !page.HasNextPage {
		t.Fatalf("expected HasNextPage=true when First+1 rows returned")
	}
	if page.EndCursor != "01970000-0000-7000-8000-00000000a002" {
		t.Fatalf("EndCursor must be last returned item; got %q", page.EndCursor)
	}
}

// -----------------------------------------------------------------------------
// SearchCursor — TENANT-SCOPED path reads the courses table, RLS applied
// -----------------------------------------------------------------------------

func TestCatalogueRepo_SearchCursor_TenantScoped_AppliesRLS(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(0) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	// TenantID set + VisibilityFilterTenantOrPublic — must SET LOCAL the
	// tenant (RLS on `courses` does the isolation) then read the table.
	_, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
		First:      20,
	})
	if err != nil {
		t.Fatalf("SearchCursor tenant-scoped: %v", err)
	}
	if len(q.sqls) < 2 {
		t.Fatalf("expected SET LOCAL + SELECT; got %d sqls", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("tenant-scoped path must emit SET LOCAL first; got %q", q.sqls[0])
	}
	if !strings.Contains(q.sqls[len(q.sqls)-1], "FROM courses") {
		t.Fatalf("expected SELECT FROM courses; got %q", q.sqls[len(q.sqls)-1])
	}
	if strings.Contains(q.sqls[len(q.sqls)-1], "public_courses_catalog") {
		t.Fatalf("tenant-scoped path must NOT use the public view; got %q", q.sqls[len(q.sqls)-1])
	}
}

// -----------------------------------------------------------------------------
// SECFIX — TENANT-SCOPED browse excludes non-PUBLISHED (open-loop #3).
//
// The tenant catalogue browse is an ENROLMENT surface: CourseState PUBLISHED is
// "released to learners (/a/catalog)" (domain/delivery/course_cj2.go). The B2.4
// `courses` RLS (mig 0043) intentionally FAIL-OPENS on state when
// chora.user_gcid is unset — and the browse path (tenantCtx) sets only the
// tenant, never the gcid. The 0043 header itself calls this out: "the
// tenant-scoped catalogue browse has no app state filter today". Without an
// app-layer predicate the browse leaks DRAFT/ARCHIVED authoring work to any
// tenant member. These lock state = 'PUBLISHED' onto the page SELECT AND its
// COUNT (pagination-consistent) for both the cursor and offset paths.
// -----------------------------------------------------------------------------

// assertTenantBrowseExcludesUnpublished fails unless every `FROM courses`
// statement in the captured SQL constrains state to PUBLISHED. SET LOCAL
// statements (RLS session setup) never touch `courses`, so they are exempt.
func assertTenantBrowseExcludesUnpublished(t *testing.T, sqls []string) {
	t.Helper()
	sawCoursesQuery := false
	for _, s := range sqls {
		if !strings.Contains(s, "FROM courses") {
			continue
		}
		sawCoursesQuery = true
		if !strings.Contains(s, "state = 'PUBLISHED'") {
			t.Fatalf("tenant catalogue browse must exclude non-PUBLISHED via "+
				"state = 'PUBLISHED' (leaks DRAFT/ARCHIVED otherwise); got %q", s)
		}
	}
	if !sawCoursesQuery {
		t.Fatalf("expected at least one `FROM courses` query on the tenant path; got %v", sqls)
	}
}

func TestCatalogueRepo_SearchCursor_TenantScoped_ExcludesUnpublished(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
		rowFn:  func(sql string, args ...any) pg.Row { return stubCountRow(0) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	if _, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
		First:      20,
	}); err != nil {
		t.Fatalf("SearchCursor tenant-scoped: %v", err)
	}
	assertTenantBrowseExcludesUnpublished(t, q.sqls)
}

func TestCatalogueRepo_Search_TenantScoped_ExcludesUnpublished(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
		rowFn:  func(sql string, args ...any) pg.Row { return stubCountRow(0) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	if _, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
		Page:       1,
		Per:        20,
	}); err != nil {
		t.Fatalf("Search tenant-scoped: %v", err)
	}
	assertTenantBrowseExcludesUnpublished(t, q.sqls)
}

// -----------------------------------------------------------------------------
// Search (offset/page) — public path
// -----------------------------------------------------------------------------

func TestCatalogueRepo_Search_PublicFilter_ReadsView(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if v, ok := dest[0].(*int); ok {
					*v = 0
				}
				return nil
			}}
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	_, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		Public: domain.PublicFilterTrue,
		Page:   1,
		Per:    20,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	sawView := false
	for _, s := range q.sqls {
		if strings.Contains(s, "SET LOCAL") {
			t.Fatalf("public Search must NOT emit SET LOCAL; got %q", s)
		}
		if strings.Contains(s, "public_courses_catalog") {
			sawView = true
		}
	}
	if !sawView {
		t.Fatalf("expected a query against public_courses_catalog; got %v", q.sqls)
	}
}

// -----------------------------------------------------------------------------
// Get — by id, reads the public view (cross-tenant discovery handle)
// -----------------------------------------------------------------------------

func TestCatalogueRepo_Get_NoRow_ReturnsOkFalse(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return errors.New("no rows in result set")
			}}
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	pc, ok, err := r.Get(context.Background(), "nope")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok || pc != nil {
		t.Fatalf("expected ok=false / nil for unknown id")
	}
	if len(q.sqls) == 0 || !strings.Contains(q.sqls[0], "public_courses_catalog") {
		t.Fatalf("expected SELECT FROM public_courses_catalog; got %v", q.sqls)
	}
}

func TestCatalogueRepo_Get_HappyPath_ScansViewRow(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				return scanViewRowInto(dest, courseID, catTenant)
			}}
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	pc, ok, err := r.Get(context.Background(), courseID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || pc == nil {
		t.Fatalf("expected ok=true / non-nil")
	}
	if pc.ID != courseID || pc.TenantID != catTenant {
		t.Fatalf("scanned course mismatch: %+v", pc)
	}
	// The public view only ever exposes public rows — the scanned course
	// must carry Visibility=public + Public=true.
	if pc.Visibility != domain.VisibilityPublic || !pc.Public {
		t.Fatalf("public view rows must scan as VisibilityPublic; got %q", pc.Visibility)
	}
}

// -----------------------------------------------------------------------------
// TENANT-SCOPED path — scans the wider `courses` TABLE row shape, derives
// Visibility from the `public` boolean, and applies the in-adapter
// visibility + free-text filter.
// -----------------------------------------------------------------------------

// scanTenantRowInto fills a Scan dest slice shaped like the `courses` TABLE
// SELECT list (tenantCourseCols — 11 cols). The `public` boolean drives the
// derived Visibility.
func scanTenantRowInto(dest []any, id, tenant, title string, public bool) error {
	set := func(i int, val any) {
		if i >= len(dest) {
			return
		}
		switch d := dest[i].(type) {
		case *string:
			if s, ok := val.(string); ok {
				*d = s
			}
		case *int64:
			if n, ok := val.(int64); ok {
				*d = n
			}
		case *bool:
			if b, ok := val.(bool); ok {
				*d = b
			}
		case *int:
			if n, ok := val.(int); ok {
				*d = n
			}
		}
	}
	set(0, id)
	set(1, tenant)
	set(2, "00000000-0000-7000-8000-000000001999") // instructor_gcid
	set(3, title)                                  // title
	set(4, "desc")                                 // description
	set(5, public)                                 // public boolean
	set(6, int64(15000))                           // price_sgd_cents
	set(7, false)                                  // sf_eligible
	set(8, 50)                                     // max_capacity
	// created_at / updated_at left zero by the stub.
	return nil
}

func TestCatalogueRepo_SearchCursor_TenantScoped_ScansTableRowsAndDerivesVisibility(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				// A public row + a tenant_only row, both in the caller's
				// tenant — VisibilityFilterTenantOrPublic keeps both.
				func(dest ...any) error {
					return scanTenantRowInto(dest, "01970000-0000-7000-8000-00000000b001", catTenant, "Public Course", true)
				},
				func(dest ...any) error {
					return scanTenantRowInto(dest, "01970000-0000-7000-8000-00000000b002", catTenant, "Tenant-Only Course", false)
				},
			}}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(2) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	page, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
		First:      20,
	})
	if err != nil {
		t.Fatalf("SearchCursor tenant-scoped: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected 2 items (public + tenant_only in caller tenant); got %d", len(page.Items))
	}
	var sawPublic, sawTenantOnly bool
	for _, pc := range page.Items {
		switch pc.Visibility {
		case domain.VisibilityPublic:
			sawPublic = true
			if !pc.Public {
				t.Errorf("public=true row must scan Public=true")
			}
		case domain.VisibilityTenantOnly:
			sawTenantOnly = true
			if pc.Public {
				t.Errorf("public=false row must scan Public=false")
			}
		}
	}
	if !sawPublic || !sawTenantOnly {
		t.Fatalf("expected both a public + a tenant_only row; public=%v tenant_only=%v", sawPublic, sawTenantOnly)
	}
}

func TestCatalogueRepo_SearchCursor_TenantScoped_FiltersForeignTenantOnlyRow(t *testing.T) {
	t.Parallel()
	// A tenant_only row belonging to a DIFFERENT tenant must be dropped by
	// the in-adapter visibility filter even if the SQL layer surfaced it
	// (defence-in-depth on top of RLS).
	const foreignTenant = "01970000-0000-7000-8000-0000000000fe"
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					return scanTenantRowInto(dest, "01970000-0000-7000-8000-00000000c001", foreignTenant, "Foreign Tenant-Only", false)
				},
				func(dest ...any) error {
					return scanTenantRowInto(dest, "01970000-0000-7000-8000-00000000c002", foreignTenant, "Foreign Public", true)
				},
			}}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(2) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	page, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
		First:      20,
	})
	if err != nil {
		t.Fatalf("SearchCursor: %v", err)
	}
	// Foreign tenant_only filtered out; foreign public kept (public is
	// always cross-tenant visible).
	if len(page.Items) != 1 {
		t.Fatalf("expected only the foreign PUBLIC row to survive; got %d", len(page.Items))
	}
	if page.Items[0].Visibility != domain.VisibilityPublic {
		t.Fatalf("surviving row must be the public one; got %q", page.Items[0].Visibility)
	}
}

func TestCatalogueRepo_Search_TenantScoped_AppliesRLSAndFreeText(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					return scanTenantRowInto(dest, "01970000-0000-7000-8000-00000000d001", catTenant, "Scrum Mastery", true)
				},
				func(dest ...any) error {
					return scanTenantRowInto(dest, "01970000-0000-7000-8000-00000000d002", catTenant, "Project Management", true)
				},
			}}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(2) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	// Free-text "scrum" must narrow to the one matching title.
	items, total, err := r.Search(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
		Q:          "scrum",
		Page:       1,
		Per:        20,
	})
	if err != nil {
		t.Fatalf("Search tenant-scoped: %v", err)
	}
	if total != 2 {
		t.Fatalf("total is the pre-filter DB count; expected 2, got %d", total)
	}
	if len(items) != 1 {
		t.Fatalf("free-text 'scrum' should narrow to 1 item; got %d", len(items))
	}
	if items[0].Title != "Scrum Mastery" {
		t.Fatalf("wrong item survived free-text filter: %q", items[0].Title)
	}
	// First SQL of the tenant path must be SET LOCAL.
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("tenant Search must SET LOCAL first; got %q", q.sqls[0])
	}
}

// -----------------------------------------------------------------------------
// SQL templates are exported (so CI / Cloud Build lint can grep them)
// -----------------------------------------------------------------------------

func TestSQLCatalogueTemplates_AreExported(t *testing.T) {
	t.Parallel()
	if !strings.Contains(pg.SQLUpsertPublicCourse, "INSERT INTO courses") {
		t.Fatalf("SQLUpsertPublicCourse malformed")
	}
	if !strings.Contains(pg.SQLUpsertPublicCourse, "ON CONFLICT (course_id) DO UPDATE") {
		t.Fatalf("SQLUpsertPublicCourse missing UPSERT clause")
	}
	if !strings.Contains(pg.SQLSelectPublicCatalogByCursor, "public_courses_catalog") {
		t.Fatalf("SQLSelectPublicCatalogByCursor must read the view")
	}
	if !strings.Contains(pg.SQLSelectPublicCatalogByCursor, "course_id >") {
		t.Fatalf("SQLSelectPublicCatalogByCursor missing cursor predicate")
	}
	if !strings.Contains(pg.SQLSelectPublicCourseByID, "public_courses_catalog") {
		t.Fatalf("SQLSelectPublicCourseByID must read the view")
	}
	if !strings.Contains(pg.SQLListCoursesByInstructor, "FROM courses") {
		t.Fatalf("SQLListCoursesByInstructor must read the courses table")
	}
	if !strings.Contains(pg.SQLListCoursesByInstructor, "instructor_gcid =") {
		t.Fatalf("SQLListCoursesByInstructor must filter on instructor_gcid")
	}
	if !strings.Contains(pg.SQLListCoursesByInstructor, "deleted_at IS NULL") {
		t.Fatalf("SQLListCoursesByInstructor must filter soft-deletes")
	}
	if !strings.Contains(pg.SQLCountCoursesByInstructor, "count(*)") {
		t.Fatalf("SQLCountCoursesByInstructor must be a count query")
	}
}

// -----------------------------------------------------------------------------
// ListByInstructor — RLS-scoped read of the `courses` table by instructor.
// Closes debt #4 / A6 — FE instructor-roster surface.
// -----------------------------------------------------------------------------

func TestCatalogueRepo_ListByInstructor_NilTxRunner_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewCatalogueRepo(nil)
	if _, _, err := r.ListByInstructor(context.Background(), tenantID, instructorGCID, 1, 20); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("ListByInstructor: expected ErrNotImplemented; got %v", err)
	}
}

func TestCatalogueRepo_ListByInstructor_RejectsMissingTenant(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	// Empty tenant cannot be RLS-scoped — the adapter must fail fast rather
	// than emit a SET LOCAL with an empty payload.
	_, _, err := r.ListByInstructor(context.Background(), "", instructorGCID, 1, 20)
	if !errors.Is(err, pg.ErrInstructorListMissingTenant) {
		t.Fatalf("expected ErrInstructorListMissingTenant; got %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("no SQL must execute on missing tenant; got %d", len(q.sqls))
	}
}

func TestCatalogueRepo_ListByInstructor_RejectsMissingInstructor(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	// Empty instructor would conflate with "any" — reject loud.
	_, _, err := r.ListByInstructor(context.Background(), tenantID, "", 1, 20)
	if !errors.Is(err, pg.ErrInstructorListMissingInstructor) {
		t.Fatalf("expected ErrInstructorListMissingInstructor; got %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("no SQL must execute on missing instructor; got %d", len(q.sqls))
	}
}

func TestCatalogueRepo_ListByInstructor_AppliesRLSThenCountThenSelect(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			// count(*) row.
			return stubCountRow(0)
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	if _, _, err := r.ListByInstructor(context.Background(), tenantID, instructorGCID, 1, 20); err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	if len(q.sqls) < 3 {
		t.Fatalf("expected at least 3 SQLs (SET LOCAL + count + select); got %d", len(q.sqls))
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must be SET LOCAL chora.tenant_id; got %q", q.sqls[0])
	}
	if !strings.Contains(q.sqls[1], "count(*)") {
		t.Fatalf("second SQL must be count(*); got %q", q.sqls[1])
	}
	if !strings.Contains(q.sqls[2], "FROM courses") {
		t.Fatalf("third SQL must read FROM courses; got %q", q.sqls[2])
	}
	if !strings.Contains(q.sqls[2], "instructor_gcid =") {
		t.Fatalf("third SQL must filter on instructor_gcid; got %q", q.sqls[2])
	}
	if !strings.Contains(q.sqls[2], "deleted_at IS NULL") {
		t.Fatalf("third SQL must filter soft-deletes; got %q", q.sqls[2])
	}
}

func TestCatalogueRepo_ListByInstructor_BindsInstructorAndPagination(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubCountRow(42)
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	_, total, err := r.ListByInstructor(context.Background(), tenantID, instructorGCID, 2 /*page*/, 10 /*per*/)
	if err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	if total != 42 {
		t.Fatalf("total = %d, want 42", total)
	}
	// q.sqls: [0]=SET LOCAL chora.tenant_id ..., [1]=count, [2]=select.
	// SET LOCAL is parameter-free (the tenant is inlined into the SQL by
	// rls.ApplySession); count + select carry the bound args.
	if len(q.args) < 3 {
		t.Fatalf("expected SET LOCAL + count + select arg captures; got %d", len(q.args))
	}
	countArgs := q.args[1]
	if len(countArgs) != 1 {
		t.Fatalf("count must bind 1 arg (instructor); got %d", len(countArgs))
	}
	if s, _ := countArgs[0].(string); s != instructorGCID {
		t.Fatalf("count arg[0] = %v, want %s", countArgs[0], instructorGCID)
	}
	selectArgs := q.args[2]
	if len(selectArgs) != 3 {
		t.Fatalf("select must bind 3 args (instructor, limit, offset); got %d", len(selectArgs))
	}
	if s, _ := selectArgs[0].(string); s != instructorGCID {
		t.Fatalf("select arg[0] = %v, want %s", selectArgs[0], instructorGCID)
	}
	// page=2, per=10 → limit=10, offset=10.
	if n, _ := selectArgs[1].(int); n != 10 {
		t.Fatalf("select arg[1] (limit) = %v, want 10", selectArgs[1])
	}
	if n, _ := selectArgs[2].(int); n != 10 {
		t.Fatalf("select arg[2] (offset) = %v, want 10", selectArgs[2])
	}
}

func TestCatalogueRepo_ListByInstructor_DefaultsPagination(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubCountRow(0)
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	// page=0, per=0 → page=1, per=20 (mirrors Search).
	if _, _, err := r.ListByInstructor(context.Background(), tenantID, instructorGCID, 0, 0); err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	selectArgs := q.args[2]
	if n, _ := selectArgs[1].(int); n != 20 {
		t.Fatalf("default per: select arg[1] (limit) = %v, want 20", selectArgs[1])
	}
	if n, _ := selectArgs[2].(int); n != 0 {
		t.Fatalf("default page: select arg[2] (offset) = %v, want 0", selectArgs[2])
	}
}

func TestCatalogueRepo_ListByInstructor_ReturnsRows(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubCountRow(1)
		},
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{
				rows: []func(dest ...any) error{
					func(dest ...any) error {
						// ListByInstructor reads the `courses` TABLE (RLS-scoped),
						// NOT the public_courses_catalog view — scan with the
						// tenant-table column layout.
						return scanTenantRowInto(dest, courseID, tenantID, "Phyllis's MTM Math Bootcamp", false)
					},
				},
			}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	items, total, err := r.ListByInstructor(context.Background(), tenantID, instructorGCID, 1, 20)
	if err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].ID != courseID {
		t.Fatalf("item id = %s, want %s", items[0].ID, courseID)
	}
	if items[0].TenantID != tenantID {
		t.Fatalf("item tenant_id = %s, want %s", items[0].TenantID, tenantID)
	}
	if items[0].Title != "Phyllis's MTM Math Bootcamp" {
		t.Fatalf("item title = %q, want %q", items[0].Title, "Phyllis's MTM Math Bootcamp")
	}
}

// scanViewRowInto fills a Scan dest slice shaped like the public_courses_catalog
// SELECT list used by the CatalogueRepo. Column order MUST match
// pg.SQLSelectPublicCatalogByCursor / pg.SQLSelectPublicCourseByID.
//
// View columns (migrations/0006_public_catalog_view.sql):
//
//	course_id, tenant_id, instructor_gcid, title, description,
//	price_sgd_cents, sf_eligible, max_capacity, created_at, updated_at
func scanViewRowInto(dest []any, id, tenant string) error {
	set := func(i int, val any) {
		if i >= len(dest) {
			return
		}
		switch d := dest[i].(type) {
		case *string:
			if s, ok := val.(string); ok {
				*d = s
			}
		case *int64:
			if n, ok := val.(int64); ok {
				*d = n
			}
		case *bool:
			if b, ok := val.(bool); ok {
				*d = b
			}
		case *int:
			if n, ok := val.(int); ok {
				*d = n
			}
		}
	}
	set(0, id)
	set(1, tenant)
	set(2, "00000000-0000-7000-8000-000000001002") // instructor_gcid
	set(3, "Certified Scrum Product Owner Prep")   // title
	set(4, "desc")                                 // description
	set(5, int64(0))                               // price_sgd_cents
	set(6, false)                                  // sf_eligible
	set(7, 300)                                    // max_capacity
	// created_at / updated_at scanned as *time.Time — left zero by the stub;
	// the adapter tolerates zero timestamps.
	return nil
}
