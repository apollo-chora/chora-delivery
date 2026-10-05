// catalogue_extra_test.go — extra pg.CatalogueRepo unit tests: finish partial
// statement coverage of catalogue.go.
//
// Extends catalogue_test.go (same pg_test package) — reuses the shared
// stubQuerier / stubTxRunner / stubRow / stubRows fixtures from
// application_test.go + the catalogue helpers (scanViewRowInto,
// scanTenantRowInto, stubCountRow, catTenant).
//
// New helpers/fillers/consts in this file are prefixed `cx` to keep the
// package-level pg_test namespace unique across parallel agents.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// cxPublicCourse builds a PublicCourse with zero timestamps — the zero-time
// Save branch needs a bare struct (a domain constructor pre-fills the clock).
func cxPublicCourse(visibility domain.Visibility) *domain.PublicCourse {
	return &domain.PublicCourse{
		ID:             courseID,
		TenantID:       catTenant,
		Title:          "CSPO Prep",
		InstructorGCID: instructorGCID,
		Public:         visibility == domain.VisibilityPublic,
		Visibility:     visibility,
	}
}

// cxTenantRow fills a Scan dest shaped like tenantCourseCols (11 cols) with a
// caller-chosen public flag + persisted visibility string. created_at /
// updated_at are left zero (tolerated by the scan).
func cxTenantRow(id string, public bool, visibility string) func(dest ...any) error {
	return func(dest ...any) error {
		setStr := func(i int, v string) {
			if d, ok := dest[i].(*string); ok {
				*d = v
			}
		}
		setStr(0, id)
		setStr(1, catTenant)
		setStr(2, "00000000-0000-7000-8000-000000001999")
		setStr(3, "CSPO Course")
		setStr(4, "desc")
		if d, ok := dest[5].(*bool); ok {
			*d = public
		}
		setStr(11, visibility)
		return nil
	}
}

// -----------------------------------------------------------------------------
// Save — remaining branches (missing instructor, zero timestamps, exec error)
// -----------------------------------------------------------------------------

func TestCatalogueRepo_Save_RejectsMissingInstructor(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	err := r.Save(context.Background(), &domain.PublicCourse{
		ID:         courseID,
		TenantID:   catTenant,
		Title:      "x",
		Visibility: domain.VisibilityPublic,
	})
	if !errors.Is(err, pg.ErrPublicCourseMissingInstructor) {
		t.Fatalf("expected ErrPublicCourseMissingInstructor; got %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("no SQL must execute on missing instructor; got %v", q.sqls)
	}
}

func TestCatalogueRepo_Save_ZeroTimestamps_DefaultToUpsertTime(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	before := time.Now().UTC().Add(-time.Second)
	if err := r.Save(context.Background(), cxPublicCourse(domain.VisibilityPublic)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) < 13 {
		t.Fatalf("UPSERT must bind 13 args; got %d (%v)", len(lastArgs), lastArgs)
	}
	createdAt, ok := lastArgs[9].(time.Time)
	if !ok {
		t.Fatalf("created_at arg must be a time.Time; got %T", lastArgs[9])
	}
	if createdAt.Before(before) {
		t.Fatalf("zero CreatedAt must default to now(); got %v", createdAt)
	}
	updatedAt, ok := lastArgs[10].(time.Time)
	if !ok {
		t.Fatalf("updated_at arg must be a time.Time; got %T", lastArgs[10])
	}
	if !updatedAt.Equal(createdAt) {
		t.Fatalf("zero UpdatedAt must mirror CreatedAt; got %v vs %v", updatedAt, createdAt)
	}
}

func TestCatalogueRepo_Save_ExecError_WrapsUpsertContext(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO courses") {
			return errors.New("connection lost")
		}
		return nil
	}}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	err := r.Save(context.Background(), cxPublicCourse(domain.VisibilityPublic))
	if err == nil {
		t.Fatalf("expected the underlying Exec error to propagate")
	}
	if !strings.Contains(err.Error(), "pg: upsert public course") {
		t.Fatalf("exec error must be wrapped with context; got %v", err)
	}
	if !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("wrapped error must preserve the underlying cause; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Search — pagination defaults + count/query/scan error branches
// -----------------------------------------------------------------------------

func TestCatalogueRepo_Search_PublicDefaultsPagination(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn:  func(sql string, args ...any) pg.Row { return stubCountRow(0) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	// page=0, per=0 → defaults page=1, per=20.
	if _, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
	}); err != nil {
		t.Fatalf("Search zero pagination: %v", err)
	}
	lastArgs := q.args[len(q.args)-1]
	if len(lastArgs) != 2 {
		t.Fatalf("page query must bind (limit, offset); got %d (%v)", len(lastArgs), lastArgs)
	}
	if n, _ := lastArgs[0].(int); n != 20 {
		t.Fatalf("default per: limit arg = %v, want 20", lastArgs[0])
	}
	if n, _ := lastArgs[1].(int); n != 0 {
		t.Fatalf("default page: offset arg = %v, want 0", lastArgs[1])
	}
}

func TestCatalogueRepo_Search_PublicCountError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("conn closed") }}
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	items, total, err := r.Search(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
	})
	if err == nil {
		t.Fatalf("expected the count scan error to propagate")
	}
	if !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("count error must preserve the cause; got %v", err)
	}
	if items != nil || total != 0 {
		t.Fatalf("expected zero-value result on count error; got %d items / %d total", len(items), total)
	}
}

func TestCatalogueRepo_Search_PublicQueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn:  func(sql string, args ...any) pg.Row { return stubCountRow(0) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return nil, errors.New("query timeout") },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	if _, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
	}); err == nil {
		t.Fatalf("expected the page query error to propagate")
	}
}

func TestCatalogueRepo_Search_PublicScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(1) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	_, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
	})
	if err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
	if !strings.Contains(err.Error(), "bad row bytes") {
		t.Fatalf("scan error must preserve the cause; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// SearchCursor — First clamp branches + count error
// -----------------------------------------------------------------------------

func TestCatalogueRepo_SearchCursor_FirstDefaultsAndClamps(t *testing.T) {
	t.Parallel()
	var pageArgs []any
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(0) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			pageArgs = args
			return &stubRows{}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})

	// First=0 → default 20 → LIMIT 21 (sentinel +1).
	if _, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
	}); err != nil {
		t.Fatalf("SearchCursor First=0: %v", err)
	}
	if n, _ := pageArgs[1].(int); n != 21 {
		t.Fatalf("First=0 must default to 20 (limit 21); got %v", pageArgs[1])
	}

	// First=500 → capped at 200 → LIMIT 201.
	pageArgs = nil
	if _, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      500,
	}); err != nil {
		t.Fatalf("SearchCursor First=500: %v", err)
	}
	if n, _ := pageArgs[1].(int); n != 201 {
		t.Fatalf("First=500 must cap at 200 (limit 201); got %v", pageArgs[1])
	}
}

func TestCatalogueRepo_SearchCursor_PublicCountError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("conn closed") }}
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	if _, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
	}); err == nil {
		t.Fatalf("expected the count scan error to propagate")
	}
}

func TestCatalogueRepo_SearchCursor_TenantScoped_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(1) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	if _, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
		First:      20,
	}); err == nil {
		t.Fatalf("expected the tenant row-scan error to propagate")
	}
}

// -----------------------------------------------------------------------------
// ListByInstructor — count error / scan error / empty-result branches
// -----------------------------------------------------------------------------

func TestCatalogueRepo_ListByInstructor_CountError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("count failed") }}
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	_, _, err := r.ListByInstructor(context.Background(), tenantID, instructorGCID, 1, 20)
	if err == nil {
		t.Fatalf("expected the count error to propagate")
	}
}

func TestCatalogueRepo_ListByInstructor_ScanError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(1) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error { return errors.New("bad row bytes") },
			}}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	if _, _, err := r.ListByInstructor(context.Background(), tenantID, instructorGCID, 1, 20); err == nil {
		t.Fatalf("expected the row-scan error to propagate")
	}
}

func TestCatalogueRepo_ListByInstructor_EmptyResult_NonNilSlice(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn:  func(sql string, args ...any) pg.Row { return stubCountRow(0) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	items, total, err := r.ListByInstructor(context.Background(), tenantID, instructorGCID, 1, 20)
	if err != nil {
		t.Fatalf("ListByInstructor: %v", err)
	}
	if items == nil {
		t.Fatalf("empty result must be an empty (non-nil) slice")
	}
	if total != 0 {
		t.Fatalf("expected total 0; got %d", total)
	}
}

// -----------------------------------------------------------------------------
// scanTenantCourseRow — the private-visibility reconciliation branch (B2.3)
// -----------------------------------------------------------------------------

func TestCatalogueRepo_ScanTenantCourseRow_PrivateVisibility(t *testing.T) {
	t.Parallel()
	// public=false + persisted visibility 'private' → VisibilityPrivate stays
	// distinct from tenant_only (the B2.3 fix).
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				cxTenantRow(courseID, false, string(domain.VisibilityPrivate)),
			}}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(1) },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	// VisibilityFilterAny keeps every row.
	items, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterAny,
		Page:       1,
		Per:        20,
	})
	if err != nil {
		t.Fatalf("Search tenant-scoped: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 row under VisibilityFilterAny; got %d", len(items))
	}
	if items[0].Visibility != domain.VisibilityPrivate {
		t.Fatalf("private row must scan VisibilityPrivate; got %q", items[0].Visibility)
	}
	if items[0].Public {
		t.Fatalf("public=false row must scan Public=false")
	}
}

// -----------------------------------------------------------------------------
// visibilityKeep — the private-drop + any-scope guard branches
// -----------------------------------------------------------------------------

func TestCatalogueRepo_Search_PrivateRow_DroppedUnderTenantOrPublic(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(2) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				cxTenantRow("01970000-0000-7000-8000-00000000e001", false, string(domain.VisibilityPrivate)),
				cxTenantRow("01970000-0000-7000-8000-00000000e002", false, string(domain.VisibilityTenantOnly)),
			}}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	items, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("private row must be dropped under TenantOrPublic; got %d", len(items))
	}
	if items[0].Visibility != domain.VisibilityTenantOnly {
		t.Fatalf("expected the tenant_only row to survive; got %q", items[0].Visibility)
	}
}

func TestCatalogueRepo_Search_TenantScoped_AnyFilter_KeepsOnlyOwnTenantRows(t *testing.T) {
	t.Parallel()
	// VisibilityFilterAny with a tenant scope: a row from ANOTHER tenant must
	// be dropped by the defence-in-depth guard; same-tenant rows stay.
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row { return stubCountRow(2) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{
				func(dest ...any) error {
					// Row from a DIFFERENT tenant.
					setStr := func(i int, v string) {
						if d, ok := dest[i].(*string); ok {
							*d = v
						}
					}
					setStr(0, "01970000-0000-7000-8000-00000000f001")
					setStr(1, "01970000-0000-7000-8000-00000000ff00")
					setStr(3, "Foreign")
					if d, ok := dest[5].(*bool); ok {
						*d = true
					}
					setStr(11, string(domain.VisibilityPublic))
					return nil
				},
				cxTenantRow("01970000-0000-7000-8000-00000000f002", true, string(domain.VisibilityPublic)),
			}}, nil
		},
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	items, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterAny,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("foreign-tenant row must be dropped under Any; got %d", len(items))
	}
	if items[0].ID != "01970000-0000-7000-8000-00000000f002" {
		t.Fatalf("surviving row must be the same-tenant one; got %q", items[0].ID)
	}
}

// -----------------------------------------------------------------------------
// assembleCursorPage — empty-page contract (no sentinel, non-nil Items)
// -----------------------------------------------------------------------------

func TestCatalogueRepo_SearchCursor_EmptyPage_Contract(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn:  func(sql string, args ...any) pg.Row { return stubCountRow(0) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	page, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
		First:      20,
	})
	if err != nil {
		t.Fatalf("SearchCursor: %v", err)
	}
	if page.HasNextPage {
		t.Fatalf("no sentinel row ⇒ HasNextPage=false")
	}
	if page.EndCursor != "" {
		t.Fatalf("empty page must have empty EndCursor; got %q", page.EndCursor)
	}
	if page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("empty page items must be empty non-nil; got %#v", page.Items)
	}
}

// -----------------------------------------------------------------------------
// tenantCtx — the query-tenant derivation (no ctx tenant on input)
// -----------------------------------------------------------------------------

func TestCatalogueRepo_Search_TenantScoped_DerivesTenantFromQuery(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn:  func(sql string, args ...any) pg.Row { return stubCountRow(0) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return &stubRows{}, nil },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	// No tenant on ctx — the tenant path derives it from q.TenantID via
	// tenantCtx, so ApplySession succeeds and the RLS SET LOCAL carries the
	// query tenant.
	_, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
	})
	if err != nil {
		t.Fatalf("Search with explicit query tenant: %v", err)
	}
	if len(q.sqls) == 0 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id = '"+catTenant+"'") {
		t.Fatalf("SET LOCAL must carry the query tenant; got %v", q.sqls)
	}
}

func TestCatalogueRepo_Search_TenantScoped_ApplySessionExecFails(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "SET LOCAL chora.tenant_id") {
			return errors.New("rlx blocked")
		}
		return nil
	}}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	_, _, err := r.Search(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
	})
	if err == nil {
		t.Fatalf("expected the SET LOCAL failure to propagate (wrapped by rls)")
	}
	if !strings.Contains(err.Error(), "rlx blocked") {
		t.Fatalf("expected the underlying SET LOCAL error; got %v", err)
	}
}

func TestCatalogueRepo_SearchCursor_PublicQueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn:  func(sql string, args ...any) pg.Row { return stubCountRow(0) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return nil, errors.New("query timeout") },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	if _, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		Visibility: domain.VisibilityFilterPublic,
	}); err == nil {
		t.Fatalf("expected the page query error to propagate")
	}
}

func TestCatalogueRepo_SearchCursor_TenantScoped_ApplySessionExecFails(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "SET LOCAL chora.tenant_id") {
			return errors.New("rlx blocked")
		}
		return nil
	}}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	if _, err := r.SearchCursor(context.Background(), domain.CatalogueQuery{
		TenantID:   catTenant,
		Visibility: domain.VisibilityFilterTenantOrPublic,
	}); err == nil {
		t.Fatalf("expected the SET LOCAL failure to propagate")
	}
}

func TestCatalogueRepo_ListByInstructor_QueryError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn:  func(sql string, args ...any) pg.Row { return stubCountRow(0) },
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return nil, errors.New("query timeout") },
	}
	r := pg.NewCatalogueRepo(&stubTxRunner{q: q})
	if _, _, err := r.ListByInstructor(context.Background(), tenantID, instructorGCID, 1, 20); err == nil {
		t.Fatalf("expected the page query error to propagate")
	}
}
