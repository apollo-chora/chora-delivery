// offering_search_test.go — unit tests for pg.OfferingRepo.Search (R+ W2.A).
//
// Stubs the Querier so the finder SQL surface + RLS contract are exercised
// without a live DB:
//  1. rls.ApplySession runs BEFORE any finder query.
//  2. items query is tenant + soft-delete filtered, ORDER BY <sort>+id, LIMIT.
//  3. a total count(*) + one GROUP BY per facet dimension fire.
//  4. a cursor adds the keyset row-value predicate.
//  5. nil-tx is fail-loud (ErrNotImplemented).
//
// Real keyset/facet/RLS behaviour against Cloud SQL is the integration test.
package pg_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func offeringSearchStub(t *testing.T) *stubQuerier {
	a := newOffering("0197bb00-0000-7000-8000-0000000000a1", domain.OfferingStateDraft)
	b := newOffering("0197bb00-0000-7000-8000-0000000000b2", domain.OfferingStateRunning)
	return &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			switch {
			case strings.Contains(sql, "GROUP BY delivery_type"):
				return &stubRows{rows: []func(dest ...any) error{
					func(dest ...any) error { *(dest[0].(*string)) = "graduate"; *(dest[1].(*int)) = 2; return nil },
				}}, nil
			case strings.Contains(sql, "GROUP BY state"):
				return &stubRows{rows: []func(dest ...any) error{
					func(dest ...any) error { *(dest[0].(*string)) = "DRAFT"; *(dest[1].(*int)) = 1; return nil },
				}}, nil
			default: // items
				return &stubRows{rows: []func(dest ...any) error{
					func(dest ...any) error { *(dest[0].(*[]byte)) = offeringJSON(t, a); return nil },
					func(dest ...any) error { *(dest[0].(*[]byte)) = offeringJSON(t, b); return nil },
				}}, nil
			}
		},
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { *(dest[0].(*int)) = 2; return nil }}
		},
	}
}

func TestOfferingRepo_Search_NilTx(t *testing.T) {
	t.Parallel()
	r := pg.NewOfferingRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID}); !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Search nil-tx: expected ErrNotImplemented; got %v", err)
	}
}

func TestOfferingRepo_Search_SQLShapeAndRLS(t *testing.T) {
	t.Parallel()
	q := offeringSearchStub(t)
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	page, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(q.sqls) == 0 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	joined := strings.Join(q.sqls, "\n")
	for _, want := range []string{
		"FROM offerings", "deleted_at IS NULL", "ORDER BY", "LIMIT",
		"GROUP BY delivery_type", "GROUP BY state", "count(*)",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("finder SQL missing %q; got:\n%s", want, joined)
		}
	}

	if len(page.Items) != 2 {
		t.Fatalf("want 2 items; got %d", len(page.Items))
	}
	if page.TotalEstimate != 2 {
		t.Fatalf("want total_estimate 2; got %d", page.TotalEstimate)
	}
	if len(page.Facets) != 2 {
		t.Fatalf("want 2 facets; got %d", len(page.Facets))
	}
	var dtCount int
	for _, f := range page.Facets {
		if f.Field == domain.OfferingFacetDeliveryType {
			for _, v := range f.Values {
				if v.Value == "graduate" {
					dtCount = v.Count
				}
			}
		}
	}
	if dtCount != 2 {
		t.Fatalf("delivery_type facet graduate count want 2; got %d", dtCount)
	}
}

// TestOfferingRepo_Search_CourseIDFilter — B1.1 blast-radius. Filtering by
// CourseID must match offerings that BUNDLE the course anywhere in their
// CourseIDs list (JSONB array containment), not just where it is the primary
// (denormalised course_id column) — else a course that is a secondary member
// of a multi-course offering is silently missed from the "used by N offerings"
// count.
func TestOfferingRepo_Search_CourseIDFilter(t *testing.T) {
	t.Parallel()
	q := offeringSearchStub(t)
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	courseID := "0197bb00-0000-7000-8000-0000000000c3"
	if _, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, CourseID: courseID, Limit: 20}); err != nil {
		t.Fatalf("Search course filter: %v", err)
	}
	joined := strings.Join(q.sqls, "\n")
	if !strings.Contains(joined, "data->'CourseIDs' @> to_jsonb(") {
		t.Fatalf("course filter must use CourseIDs JSONB-array containment (not the primary course_id column); got:\n%s", joined)
	}
	found := false
	for _, a := range q.args {
		for _, v := range a {
			if s, ok := v.(string); ok && s == courseID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("course UUID %q must be bound as a query arg; got %#v", courseID, q.args)
	}
}

func TestOfferingRepo_Search_KeysetSQL(t *testing.T) {
	t.Parallel()
	q := offeringSearchStub(t)
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	cursor := &domain.OfferingCursor{
		SortValue: time.Now().UTC().Format(time.RFC3339Nano),
		ID:        "0197bb00-0000-7000-8000-0000000000a1",
	}
	if _, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 2, Cursor: cursor}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	var itemsSQL string
	for _, s := range q.sqls {
		if strings.Contains(s, "LIMIT") { // items query (facets have no LIMIT)
			itemsSQL = s
		}
	}
	if itemsSQL == "" {
		t.Fatalf("no items query captured; got %#v", q.sqls)
	}
	if !strings.Contains(itemsSQL, "(created_at, id) <") {
		t.Fatalf("default-desc keyset predicate missing; got %q", itemsSQL)
	}
}
