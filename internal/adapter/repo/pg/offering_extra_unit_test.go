// offering_extra_unit_test.go — extra unit tests for pg.OfferingRepo (R+
// W1/W2), complementing offering_test.go + offering_search_test.go without
// duplicating them. Stubs the Querier (shared stubQuerier / stubTxRunner /
// stubRow / stubRows from application_test.go) so the remaining uncovered
// guarantees are exercised without a live DB.
//
// Because these tests live in the EXTERNAL package pg_test (like every other
// unit test here), the unexported finder helpers (offeringSearchWhere /
// offeringKeysetClause / offeringSortColumn / offeringCursorValue /
// offeringFacetQuery / scanOfferingData) are exercised INDIRECTLY through
// OfferingRepo.Search:
//
//  1. offeringSearchWhere — every filter branch present (Q ILIKE, CourseIDs
//     JSONB containment, delivery_type ANY, state ANY) and the query-minus-self
//     facet exclusions (each facet drops its own dimension's filter).
//  2. offeringKeysetClause — '<' desc / '>' asc, and label cast-less vs
//     ::timestamptz for timestamp columns.
//  3. offeringSortColumn — label / updated_at / created_at (default).
//  4. offeringCursorValue — next_cursor SortValue per sort field (label
//     verbatim, timestamps RFC3339Nano).
//  5. offeringFacetQuery + scanOfferingData — Query / scan / unmarshal error
//     propagation.
//  6. RLS-first: every method's first SQL is the SET LOCAL chora.tenant_id;
//     a ctx without a tenant aborts before ANY query (rls.ErrNoTenantContext).
//  7. Error wraps on Save/Get/ListByTenant preserve the cause via %w.
//
// Real keyset/facet/RLS behaviour against Cloud SQL is the integration test.
package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// ofx helpers — offering builders + Search stub
// -----------------------------------------------------------------------------

// ofxOffering builds an Offering with caller-chosen id / label / timestamps so
// cursor-value assertions stay exact.
func ofxOffering(id, label string, createdAt, updatedAt time.Time) *domain.Offering {
	return &domain.Offering{
		ID:           id,
		TenantID:     tenantID,
		CourseIDs:    []string{courseID},
		DeliveryType: domain.DeliveryTypeGraduate,
		Label:        label,
		Capacity:     30,
		State:        domain.OfferingStateDraft,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}
}

func ofxOfferingJSON(t *testing.T, o *domain.Offering) []byte {
	t.Helper()
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal offering: %v", err)
	}
	return b
}

// ofxItemRow fills the single `data []byte` dest of scanOfferingData with the
// marshalled offering.
func ofxItemRow(t *testing.T, o *domain.Offering) func(dest ...any) error {
	t.Helper()
	b := ofxOfferingJSON(t, o)
	return func(dest ...any) error {
		*(dest[0].(*[]byte)) = b
		return nil
	}
}

// ofxSearchStub stubs a Search run: items query yields the given offerings,
// the count QueryRow reports total, and facet GROUP BY queries return no
// buckets. Error injection is done per-test via rowsFn/rowFn overrides.
func ofxSearchStub(t *testing.T, items []*domain.Offering, total int) *stubQuerier {
	t.Helper()
	rowFns := make([]func(dest ...any) error, 0, len(items))
	for _, o := range items {
		rowFns = append(rowFns, ofxItemRow(t, o))
	}
	return &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "GROUP BY") {
				return &stubRows{}, nil // facets — no buckets needed here
			}
			return &stubRows{rows: rowFns}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				if ptr, ok := dest[0].(*int); ok {
					*ptr = total
				}
				return nil
			}}
		},
	}
}

// -----------------------------------------------------------------------------
// offeringSearchWhere — every filter branch + facet query-minus-self
// -----------------------------------------------------------------------------

func TestOfferingRepo_Search_AllFilters_WhereShapeAndFacetExclusions(t *testing.T) {
	t.Parallel()
	q := ofxSearchStub(t, nil, 0)
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.Search(ctx, domain.OfferingQuery{
		TenantID:      tenantID,
		Q:             "spring",
		CourseID:      courseID,
		DeliveryTypes: []string{"graduate"},
		States:        []string{"DRAFT"},
		Limit:         10,
	}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	// SQL order inside the tx: [SET LOCAL, items, count, facet-delivery_type,
	// facet-state].
	if len(q.sqls) < 5 {
		t.Fatalf("expected >=5 SQLs; got %d: %#v", len(q.sqls), q.sqls)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}

	items := q.sqls[1]
	for _, want := range []string{
		"tenant_id = $1",
		"deleted_at IS NULL",
		"label ILIKE $2",
		"data->'CourseIDs' @> to_jsonb($3::text)",
		"delivery_type = ANY($4)",
		"state = ANY($5)",
	} {
		if !strings.Contains(items, want) {
			t.Fatalf("items WHERE missing %q; got %q", want, items)
		}
	}

	// Facet queries apply query-minus-self: each drops its own dimension.
	dtFacet, stFacet := q.sqls[3], q.sqls[4]
	if !strings.Contains(dtFacet, "state = ANY") || strings.Contains(dtFacet, "delivery_type = ANY") {
		t.Fatalf("delivery_type facet must keep the state filter but drop delivery_type; got %q", dtFacet)
	}
	if !strings.Contains(stFacet, "delivery_type = ANY") || strings.Contains(stFacet, "state = ANY") {
		t.Fatalf("state facet must keep the delivery_type filter but drop state; got %q", stFacet)
	}

	// Bind args on the items query: tenant, %q%, course_id, [delivery_types],
	// [states], limit+1 (the one-past lookahead).
	itemsArgs := q.args[1]
	if len(itemsArgs) != 6 {
		t.Fatalf("expected 6 item args; got %d: %#v", len(itemsArgs), itemsArgs)
	}
	if itemsArgs[0] != tenantID || itemsArgs[1] != "%spring%" || itemsArgs[2] != courseID {
		t.Fatalf("bound args[0:3] mismatch; got %#v", itemsArgs[:3])
	}
	if itemsArgs[3] == nil || itemsArgs[4] == nil {
		t.Fatalf("delivery_type/state filters must be bound; got %#v", itemsArgs)
	}
	if itemsArgs[5] != 11 {
		t.Fatalf("expected limit+1 lookahead bound last; got %v", itemsArgs[5])
	}
}

func TestOfferingRepo_Search_NoFilters_PlainWhere(t *testing.T) {
	t.Parallel()
	q := ofxSearchStub(t, nil, 0)
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if _, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 5}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	items := q.sqls[1]
	if !strings.Contains(items, "tenant_id = $1") || !strings.Contains(items, "deleted_at IS NULL") {
		t.Fatalf("base WHERE missing; got %q", items)
	}
	for _, absent := range []string{"ILIKE", "ANY(", "data->'CourseIDs'"} {
		if strings.Contains(items, absent) {
			t.Fatalf("no-filter search must not bind %q; got %q", absent, items)
		}
	}
}

// -----------------------------------------------------------------------------
// offeringSortColumn + offeringKeysetClause — ORDER BY / keyset SQL variants
// -----------------------------------------------------------------------------

func TestOfferingRepo_Search_SortAndKeyset_SQLVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		sortField  string
		sortDir    string
		wantOrder  string
		wantKeyset string
	}{
		{"label-desc", domain.OfferingSortLabel, "", "ORDER BY label DESC, id DESC", "AND (label, id) < ($2, $3::uuid)"},
		{"label-asc", domain.OfferingSortLabel, domain.OfferingSortDirAsc, "ORDER BY label ASC, id ASC", "AND (label, id) > ($2, $3::uuid)"},
		{"updated-desc", domain.OfferingSortUpdatedAt, "", "ORDER BY updated_at DESC, id DESC", "AND (updated_at, id) < ($2::timestamptz, $3::uuid)"},
		{"created-desc-default", "", "", "ORDER BY created_at DESC, id DESC", "AND (created_at, id) < ($2::timestamptz, $3::uuid)"},
		{"created-asc", domain.OfferingSortCreatedAt, domain.OfferingSortDirAsc, "ORDER BY created_at ASC, id ASC", "AND (created_at, id) > ($2::timestamptz, $3::uuid)"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := ofxSearchStub(t, nil, 0)
			r := pg.NewOfferingRepo(&stubTxRunner{q: q})
			ctx := tracing.WithTenantID(context.Background(), tenantID)

			cursor := &domain.OfferingCursor{SortValue: "cursor-value", ID: "o-last"}
			if _, err := r.Search(ctx, domain.OfferingQuery{
				TenantID:  tenantID,
				SortField: tc.sortField,
				SortDir:   tc.sortDir,
				Cursor:    cursor,
				Limit:     10,
			}); err != nil {
				t.Fatalf("Search: %v", err)
			}
			items := q.sqls[1]
			if !strings.Contains(items, tc.wantOrder) {
				t.Fatalf("expected %q; got %q", tc.wantOrder, items)
			}
			if !strings.Contains(items, tc.wantKeyset) {
				t.Fatalf("expected keyset %q; got %q", tc.wantKeyset, items)
			}
			// keyset binds: tenant, sort_value, cursor id, limit+1.
			args := q.args[1]
			if len(args) != 4 {
				t.Fatalf("expected 4 item args with a cursor; got %d: %#v", len(args), args)
			}
			if args[1] != "cursor-value" || args[2] != "o-last" || args[3] != 11 {
				t.Fatalf("keyset args mismatch; got %#v", args)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// offeringCursorValue — next_cursor SortValue per sort field
// -----------------------------------------------------------------------------

func TestOfferingRepo_Search_NextCursor_SortValuePerField(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)
	t2 := t0.Add(2 * time.Hour)
	a := ofxOffering("0197bb00-0000-7000-8000-0000000000a1", "Alpha", t0, t2)
	b := ofxOffering("0197bb00-0000-7000-8000-0000000000b2", "Beta", t0.Add(time.Hour), t0.Add(3*time.Hour))

	cases := []struct {
		name  string
		field string
		want  string
	}{
		{"created_at", domain.OfferingSortCreatedAt, t0.Format(time.RFC3339Nano)},
		{"updated_at", domain.OfferingSortUpdatedAt, t2.Format(time.RFC3339Nano)},
		{"label", domain.OfferingSortLabel, "Alpha"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := ofxSearchStub(t, []*domain.Offering{a, b}, 2)
			r := pg.NewOfferingRepo(&stubTxRunner{q: q})
			ctx := tracing.WithTenantID(context.Background(), tenantID)

			page, err := r.Search(ctx, domain.OfferingQuery{
				TenantID:  tenantID,
				SortField: tc.field,
				Limit:     1, // 2 rows → lookahead fires
			})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if page.NextCursor == nil {
				t.Fatalf("a limit=1 page over 2 rows must produce NextCursor")
			}
			if page.NextCursor.SortValue != tc.want {
				t.Fatalf("NextCursor.SortValue = %q, want %q (field %s)", page.NextCursor.SortValue, tc.want, tc.field)
			}
			if page.NextCursor.ID != a.ID {
				t.Fatalf("NextCursor.ID = %q, want last page row %q", page.NextCursor.ID, a.ID)
			}
			if len(page.Items) != 1 {
				t.Fatalf("page must hold exactly limit items; got %d", len(page.Items))
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Error propagation — scanOfferingData / offeringFacetQuery / count
// -----------------------------------------------------------------------------

func TestOfferingRepo_Search_ItemsQueryError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("items query failed")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) { return nil, sentinel },
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return nil }}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 10})
	if !errors.Is(err, sentinel) {
		t.Fatalf("items query error must propagate verbatim; got %v", err)
	}
}

func TestOfferingRepo_Search_ItemsScanError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("bad row bytes")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "GROUP BY") {
				return &stubRows{}, nil
			}
			return &stubRows{rows: []func(dest ...any) error{func(dest ...any) error {
				return sentinel
			}}}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return nil }}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 10})
	if !errors.Is(err, sentinel) {
		t.Fatalf("item scan error must propagate verbatim; got %v", err)
	}
}

func TestOfferingRepo_Search_ItemsUnmarshalError_Propagates(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "GROUP BY") {
				return &stubRows{}, nil
			}
			return &stubRows{rows: []func(dest ...any) error{func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte("{bad")
				return nil
			}}}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return nil }}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 10}); err == nil {
		t.Fatalf("corrupt snapshot must fail Search")
	}
}

func TestOfferingRepo_Search_CountError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("count failed")
	q := ofxSearchStub(t, nil, 0)
	q.rowFn = func(sql string, args ...any) pg.Row {
		return stubRow{scanFn: func(dest ...any) error { return sentinel }}
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 10})
	if !errors.Is(err, sentinel) {
		t.Fatalf("count error must propagate verbatim; got %v", err)
	}
}

func TestOfferingRepo_Search_FacetQueryError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("facet query failed")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "GROUP BY") {
				return nil, sentinel
			}
			return &stubRows{}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return nil }}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 10})
	if !errors.Is(err, sentinel) {
		t.Fatalf("facet query error must propagate verbatim; got %v", err)
	}
}

func TestOfferingRepo_Search_FacetScanError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("facet row scan failed")
	q := &stubQuerier{
		rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			if strings.Contains(sql, "GROUP BY") {
				return &stubRows{rows: []func(dest ...any) error{func(dest ...any) error {
					return sentinel
				}}}, nil
			}
			return &stubRows{}, nil
		},
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return nil }}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	_, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 10})
	if !errors.Is(err, sentinel) {
		t.Fatalf("facet scan error must propagate verbatim; got %v", err)
	}
}

// -----------------------------------------------------------------------------
// RLS application failures — a ctx without a tenant aborts every method
// before any data query (the `return err` right after rls.ApplySession).
// -----------------------------------------------------------------------------

func TestOfferingRepo_RLSFailure_AbortsBeforeAnyQuery(t *testing.T) {
	t.Parallel()
	// No tracing.WithTenantID → ApplySession returns rls.ErrNoTenantContext.
	ctx := context.Background()
	o := ofxOffering("o-1", "L", time.Now(), time.Now())

	for _, tc := range []struct {
		name string
		run  func(t *testing.T, q *stubQuerier, r *pg.OfferingRepo) error
	}{
		{"Save", func(t *testing.T, q *stubQuerier, r *pg.OfferingRepo) error {
			return r.Save(ctx, o)
		}},
		{"Get", func(t *testing.T, q *stubQuerier, r *pg.OfferingRepo) error {
			_, _, err := r.Get(ctx, o.ID)
			return err
		}},
		{"ListByTenant", func(t *testing.T, q *stubQuerier, r *pg.OfferingRepo) error {
			_, err := r.ListByTenant(ctx, tenantID)
			return err
		}},
		{"Search", func(t *testing.T, q *stubQuerier, r *pg.OfferingRepo) error {
			_, err := r.Search(ctx, domain.OfferingQuery{TenantID: tenantID, Limit: 10})
			return err
		}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := &stubQuerier{}
			r := pg.NewOfferingRepo(&stubTxRunner{q: q})
			err := tc.run(t, q, r)
			if !errors.Is(err, rls.ErrNoTenantContext) {
				t.Fatalf("expected rls.ErrNoTenantContext; got %v", err)
			}
			if len(q.sqls) != 0 {
				t.Fatalf("RLS failure must abort before ANY query; got %#v", q.sqls)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Save / Get / ListByTenant — error wraps, bind args, nil guards
// -----------------------------------------------------------------------------

func TestOfferingRepo_Save_ExecError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{execErrFor: func(sql string) error {
		if strings.Contains(sql, "INSERT INTO offerings") {
			return errors.New("disk full")
		}
		return nil
	}}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	err := r.Save(ctx, &domain.Offering{
		ID: "o-1", TenantID: tenantID, CourseIDs: []string{courseID},
		DeliveryType: domain.DeliveryTypeGraduate, Label: "L", State: domain.OfferingStateDraft,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err == nil {
		t.Fatalf("expected an error on failed upsert")
	}
	if !strings.Contains(err.Error(), "pg: upsert offering") || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("upsert error must wrap the cause with context; got %v", err)
	}
}

func TestOfferingRepo_Save_Args_BoundIncludingPrimaryCourseID(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	o := &domain.Offering{
		ID: "o-1", TenantID: tenantID, CourseIDs: []string{courseID, "01970000-0000-7000-8000-000000000011"},
		DeliveryType: domain.DeliveryTypeGraduate, Label: "2026 Spring Cohort", State: domain.OfferingStateDraft,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := r.Save(ctx, o); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(q.sqls) < 2 || !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %#v", q.sqls)
	}
	insertIdx := -1
	for i, s := range q.sqls {
		if strings.Contains(s, "INSERT INTO offerings") {
			insertIdx = i
		}
	}
	if insertIdx < 0 {
		t.Fatalf("no upsert captured; got %#v", q.sqls)
	}
	args := q.args[insertIdx]
	if len(args) != 9 {
		t.Fatalf("expected 9 bind args (incl. label extract); got %d: %#v", len(args), args)
	}
	// course_id is the write-only PRIMARY extract (CourseIDs[0]); secondary
	// members live only in the JSONB blob.
	if args[0] != o.ID || args[1] != o.TenantID || args[2] != courseID {
		t.Fatalf("bind args[0:3] mismatch; got %#v", args[:3])
	}
	if args[3] != string(domain.DeliveryTypeGraduate) || args[4] != string(domain.OfferingStateDraft) || args[5] != "2026 Spring Cohort" {
		t.Fatalf("bind args[3:6] (delivery_type/state/label) mismatch; got %#v", args[3:6])
	}
	var back domain.Offering
	if err := json.Unmarshal(args[6].([]byte), &back); err != nil {
		t.Fatalf("data arg must be the marshalled snapshot: %v", err)
	}
	if len(back.CourseIDs) != 2 {
		t.Fatalf("snapshot must keep ALL CourseIDs (secondary members ride the blob); got %+v", back)
	}
}

func TestOfferingRepo_Save_NilOffering_NoOp(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	if len(q.sqls) != 0 {
		t.Fatalf("Save(nil) must not execute any SQL; got %#v", q.sqls)
	}
}

func TestOfferingRepo_Get_ScanError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error { return errors.New("conn closed") }}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	// A hard read error is LOUD — NOT a miss (CHO-2184).
	if _, ok, err := r.Get(ctx, "o-1"); ok || err == nil {
		t.Fatalf("Get hard scan error must be loud; got (ok=%v, err=%v)", ok, err)
	} else if !strings.Contains(err.Error(), "pg: get offering") || !strings.Contains(err.Error(), "conn closed") {
		t.Fatalf("scan error must wrap the cause with context; got %v", err)
	}
}

func TestOfferingRepo_Get_UnmarshalError_Wrapped(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte("{bad")
				return nil
			}}
		},
	}
	r := pg.NewOfferingRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if _, _, err := r.Get(ctx, "o-1"); err == nil || !strings.Contains(err.Error(), "pg: unmarshal offering") {
		t.Fatalf("unmarshal error must carry context; got %v", err)
	}
}

func TestOfferingRepo_Get_NilTx_ReturnsErrNotImplemented(t *testing.T) {
	t.Parallel()
	r := pg.NewOfferingRepo(nil)
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	// An unwired repo is a WIRING BUG, not an empty database (CHO-2184).
	if _, ok, err := r.Get(ctx, "o-1"); ok || !errors.Is(err, pg.ErrNotImplemented) {
		t.Fatalf("Get nil-tx = (ok=%v, err=%v), want (false, ErrNotImplemented)", ok, err)
	}
}

func TestOfferingRepo_ListByTenant_ErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	{
		sentinel := errors.New("list query failed")
		q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return nil, sentinel
		}}
		r := pg.NewOfferingRepo(&stubTxRunner{q: q})
		if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, sentinel) {
			t.Fatalf("query error must propagate verbatim; got %v", err)
		}
	}
	{
		sentinel := errors.New("bad row bytes")
		q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{func(dest ...any) error {
				return sentinel
			}}}, nil
		}}
		r := pg.NewOfferingRepo(&stubTxRunner{q: q})
		if _, err := r.ListByTenant(ctx, tenantID); !errors.Is(err, sentinel) {
			t.Fatalf("scan error must propagate verbatim; got %v", err)
		}
	}
	{
		q := &stubQuerier{rowsFn: func(sql string, args ...any) (pg.Rows, error) {
			return &stubRows{rows: []func(dest ...any) error{func(dest ...any) error {
				*(dest[0].(*[]byte)) = []byte("{bad")
				return nil
			}}}, nil
		}}
		r := pg.NewOfferingRepo(&stubTxRunner{q: q})
		if _, err := r.ListByTenant(ctx, tenantID); err == nil {
			t.Fatalf("unmarshal error must propagate; got nil")
		}
	}
}
