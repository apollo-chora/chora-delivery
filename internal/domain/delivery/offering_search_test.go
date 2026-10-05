// offering_search_test.go — unit tests for the pure Offering search engine
// (R+ four-mode W2.A, ADR-190 + the universal-finder spec
// docs/design/ux_universal_search_collection.md).
//
// SearchOfferings is the executable reference for both the in-memory adapter
// and the pg adapter's SQL: filters (q / delivery_type / state), server
// multi-sort (created_at|updated_at|label with id tiebreak), keyset cursor
// pagination (stable, no overlap/gap), query-minus-self facet counts, and a
// total estimate. Pure — no I/O — so the intricate logic is tested here once
// and the adapters stay thin.
package delivery_test

import (
	"testing"
	"time"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const searchTenantB = "01970000-0000-7000-8000-0000000000B2"

func mkOffering(id string, dt domain.DeliveryType, state domain.OfferingState, label string, created time.Time) *domain.Offering {
	return &domain.Offering{
		ID:           id,
		TenantID:     tenantA,
		CourseIDs:    []string{offeringCourseA},
		DeliveryType: dt,
		Label:        label,
		Capacity:     0,
		State:        state,
		CreatedAt:    created,
		UpdatedAt:    created,
	}
}

func baseTime() time.Time { return time.Date(2026, 6, 25, 9, 0, 0, 0, time.UTC) }

// ----------------------------------------------------------------------------
// Normalize
// ----------------------------------------------------------------------------

func TestOfferingQuery_Normalize_Defaults(t *testing.T) {
	t.Parallel()
	got := domain.OfferingQuery{TenantID: tenantA}.Normalize()
	if got.SortField != domain.OfferingSortCreatedAt {
		t.Fatalf("default sort field: want created_at, got %q", got.SortField)
	}
	if got.SortDir != domain.OfferingSortDirDesc {
		t.Fatalf("default sort dir: want desc, got %q", got.SortDir)
	}
	if got.Limit != domain.OfferingSearchDefaultLimit {
		t.Fatalf("default limit: want %d, got %d", domain.OfferingSearchDefaultLimit, got.Limit)
	}
}

func TestOfferingQuery_Normalize_ClampsAndValidates(t *testing.T) {
	t.Parallel()
	q := domain.OfferingQuery{
		TenantID:      tenantA,
		Q:             "  spring  ",
		DeliveryTypes: []string{"graduate", "weekend", "async"}, // weekend invalid
		States:        []string{"RUNNING", "BOGUS"},             // BOGUS invalid
		SortField:     "nope",
		SortDir:       "sideways",
		Limit:         9999,
	}.Normalize()

	if q.SortField != domain.OfferingSortCreatedAt {
		t.Fatalf("invalid sort field must fall back to created_at, got %q", q.SortField)
	}
	if q.SortDir != domain.OfferingSortDirDesc {
		t.Fatalf("invalid sort dir must fall back to desc, got %q", q.SortDir)
	}
	if q.Limit != domain.OfferingSearchMaxLimit {
		t.Fatalf("limit must clamp to max %d, got %d", domain.OfferingSearchMaxLimit, q.Limit)
	}
	if q.Q != "spring" {
		t.Fatalf("q must be trimmed, got %q", q.Q)
	}
	if len(q.DeliveryTypes) != 2 || q.DeliveryTypes[0] != "graduate" || q.DeliveryTypes[1] != "async" {
		t.Fatalf("invalid delivery_type must be dropped, got %v", q.DeliveryTypes)
	}
	if len(q.States) != 1 || q.States[0] != "RUNNING" {
		t.Fatalf("invalid state must be dropped, got %v", q.States)
	}

	if z := (domain.OfferingQuery{TenantID: tenantA, Limit: -3}).Normalize(); z.Limit != domain.OfferingSearchDefaultLimit {
		t.Fatalf("negative limit must reset to default, got %d", z.Limit)
	}
}

// ----------------------------------------------------------------------------
// Filtering
// ----------------------------------------------------------------------------

func sampleOfferings() []*domain.Offering {
	b := baseTime()
	return []*domain.Offering{
		mkOffering("0197aa00-0000-7000-8000-000000000001", domain.DeliveryTypeGraduate, domain.OfferingStateDraft, "Alpha Cohort", b.Add(1*time.Minute)),
		mkOffering("0197aa00-0000-7000-8000-000000000002", domain.DeliveryTypeShort, domain.OfferingStateRunning, "Bravo Bootcamp", b.Add(2*time.Minute)),
		mkOffering("0197aa00-0000-7000-8000-000000000003", domain.DeliveryTypeAsync, domain.OfferingStateLaunched, "Charlie Self-Paced", b.Add(3*time.Minute)),
		mkOffering("0197aa00-0000-7000-8000-000000000004", domain.DeliveryTypeGraduate, domain.OfferingStateRunning, "Delta Cohort", b.Add(4*time.Minute)),
		mkOffering("0197aa00-0000-7000-8000-000000000005", domain.DeliveryTypeShort, domain.OfferingStateDraft, "Echo Workshop", b.Add(5*time.Minute)),
	}
}

func TestSearchOfferings_FilterByDeliveryTypeAndState(t *testing.T) {
	t.Parallel()
	all := sampleOfferings()

	page := domain.SearchOfferings(all, domain.OfferingQuery{
		TenantID:      tenantA,
		DeliveryTypes: []string{"graduate"},
		States:        []string{"RUNNING"},
	})
	if len(page.Items) != 1 || page.Items[0].ID != "0197aa00-0000-7000-8000-000000000004" {
		t.Fatalf("graduate+RUNNING should match exactly Delta; got %d items", len(page.Items))
	}
	if page.TotalEstimate != 1 {
		t.Fatalf("total estimate want 1, got %d", page.TotalEstimate)
	}
}

func TestSearchOfferings_FreeTextLabel_CaseInsensitive(t *testing.T) {
	t.Parallel()
	all := sampleOfferings()
	page := domain.SearchOfferings(all, domain.OfferingQuery{TenantID: tenantA, Q: "cohort"})
	if len(page.Items) != 2 {
		t.Fatalf("q=cohort should match Alpha + Delta; got %d", len(page.Items))
	}
}

func TestSearchOfferings_TenantScopedAndSoftDeleteExcluded(t *testing.T) {
	t.Parallel()
	all := sampleOfferings()
	// foreign tenant row + soft-deleted row must never surface
	foreign := mkOffering("0197aa00-0000-7000-8000-0000000000ff", domain.DeliveryTypeGraduate, domain.OfferingStateRunning, "Foreign", baseTime())
	foreign.TenantID = searchTenantB
	del := mkOffering("0197aa00-0000-7000-8000-0000000000fe", domain.DeliveryTypeGraduate, domain.OfferingStateArchived, "Deleted", baseTime())
	dt := baseTime()
	del.DeletedAt = &dt
	all = append(all, foreign, del)

	page := domain.SearchOfferings(all, domain.OfferingQuery{TenantID: tenantA})
	if page.TotalEstimate != 5 {
		t.Fatalf("tenant A active set is 5 (foreign + soft-deleted excluded); got %d", page.TotalEstimate)
	}
	for _, o := range page.Items {
		if o.TenantID != tenantA || o.DeletedAt != nil {
			t.Fatalf("leak: %s tenant=%s deleted=%v", o.ID, o.TenantID, o.DeletedAt)
		}
	}
}

// ----------------------------------------------------------------------------
// Sort + keyset pagination
// ----------------------------------------------------------------------------

func TestSearchOfferings_DefaultSortCreatedDesc(t *testing.T) {
	t.Parallel()
	page := domain.SearchOfferings(sampleOfferings(), domain.OfferingQuery{TenantID: tenantA})
	// newest (Echo, +5m) first
	if page.Items[0].ID != "0197aa00-0000-7000-8000-000000000005" {
		t.Fatalf("default created_at desc should put Echo first; got %s", page.Items[0].ID)
	}
	if page.Items[len(page.Items)-1].ID != "0197aa00-0000-7000-8000-000000000001" {
		t.Fatalf("default created_at desc should put Alpha last; got %s", page.Items[len(page.Items)-1].ID)
	}
}

func TestSearchOfferings_SortLabelAsc(t *testing.T) {
	t.Parallel()
	page := domain.SearchOfferings(sampleOfferings(), domain.OfferingQuery{
		TenantID: tenantA, SortField: domain.OfferingSortLabel, SortDir: domain.OfferingSortDirAsc,
	})
	want := []string{"Alpha Cohort", "Bravo Bootcamp", "Charlie Self-Paced", "Delta Cohort", "Echo Workshop"}
	for i, w := range want {
		if page.Items[i].Label != w {
			t.Fatalf("label asc position %d: want %q got %q", i, w, page.Items[i].Label)
		}
	}
}

func TestSearchOfferings_KeysetPagination_NoOverlapNoGap(t *testing.T) {
	t.Parallel()
	all := sampleOfferings()

	var collected []string
	var cursor *domain.OfferingCursor
	for page := 0; page < 10; page++ {
		res := domain.SearchOfferings(all, domain.OfferingQuery{
			TenantID: tenantA, Limit: 2, Cursor: cursor,
		})
		if res.TotalEstimate != 5 {
			t.Fatalf("page %d: total estimate should stay 5, got %d", page, res.TotalEstimate)
		}
		for _, o := range res.Items {
			collected = append(collected, o.ID)
		}
		if res.NextCursor == nil {
			break
		}
		if len(res.Items) != 2 {
			t.Fatalf("non-final page must be full (2), got %d", len(res.Items))
		}
		cursor = res.NextCursor
	}

	// Exactly the 5 ids, in created_at-desc order, no dupes.
	want := []string{
		"0197aa00-0000-7000-8000-000000000005",
		"0197aa00-0000-7000-8000-000000000004",
		"0197aa00-0000-7000-8000-000000000003",
		"0197aa00-0000-7000-8000-000000000002",
		"0197aa00-0000-7000-8000-000000000001",
	}
	if len(collected) != len(want) {
		t.Fatalf("keyset walk collected %d ids, want %d (%v)", len(collected), len(want), collected)
	}
	for i := range want {
		if collected[i] != want[i] {
			t.Fatalf("keyset order pos %d: want %s got %s", i, want[i], collected[i])
		}
	}
}

// ----------------------------------------------------------------------------
// Facets (query-minus-self)
// ----------------------------------------------------------------------------

func facetByField(facets []domain.OfferingFacet, field string) *domain.OfferingFacet {
	for i := range facets {
		if facets[i].Field == field {
			return &facets[i]
		}
	}
	return nil
}

func facetCount(f *domain.OfferingFacet, value string) int {
	if f == nil {
		return -1
	}
	for _, v := range f.Values {
		if v.Value == value {
			return v.Count
		}
	}
	return 0
}

func TestSearchOfferings_KeysetPagination_LabelAsc(t *testing.T) {
	t.Parallel()
	all := sampleOfferings()
	var collected []string
	var cursor *domain.OfferingCursor
	for i := 0; i < 10; i++ {
		res := domain.SearchOfferings(all, domain.OfferingQuery{
			TenantID:  tenantA,
			Limit:     2,
			SortField: domain.OfferingSortLabel,
			SortDir:   domain.OfferingSortDirAsc,
			Cursor:    cursor,
		})
		for _, o := range res.Items {
			collected = append(collected, o.Label)
		}
		if res.NextCursor == nil {
			break
		}
		cursor = res.NextCursor
	}
	want := []string{"Alpha Cohort", "Bravo Bootcamp", "Charlie Self-Paced", "Delta Cohort", "Echo Workshop"}
	if len(collected) != len(want) {
		t.Fatalf("label-asc keyset walk collected %d (%v)", len(collected), collected)
	}
	for i := range want {
		if collected[i] != want[i] {
			t.Fatalf("label-asc keyset pos %d: want %q got %q", i, want[i], collected[i])
		}
	}
}

func TestSearchOfferings_SortUpdatedAtDescWithCursor(t *testing.T) {
	t.Parallel()
	all := sampleOfferings()
	// Make UpdatedAt the discriminator (inverse of CreatedAt order) so the
	// updated_at sort + keyset path is exercised distinctly from created_at.
	for i, o := range all {
		o.UpdatedAt = baseTime().Add(time.Duration(100-i) * time.Minute)
	}
	p1 := domain.SearchOfferings(all, domain.OfferingQuery{
		TenantID: tenantA, Limit: 2, SortField: domain.OfferingSortUpdatedAt,
	})
	if p1.NextCursor == nil {
		t.Fatalf("expected a next cursor on a 5-row, limit-2 updated_at sort")
	}
	// Index 0 has the largest UpdatedAt (100m) → first under desc.
	if p1.Items[0].ID != "0197aa00-0000-7000-8000-000000000001" {
		t.Fatalf("updated_at desc should put the highest-UpdatedAt row first; got %s", p1.Items[0].ID)
	}
	p2 := domain.SearchOfferings(all, domain.OfferingQuery{
		TenantID: tenantA, Limit: 2, SortField: domain.OfferingSortUpdatedAt, Cursor: p1.NextCursor,
	})
	seen := map[string]bool{}
	for _, o := range append(p1.Items, p2.Items...) {
		if seen[o.ID] {
			t.Fatalf("updated_at keyset overlap on %s", o.ID)
		}
		seen[o.ID] = true
	}
}

func TestSearchOfferings_UnparseableTimeCursor_FallsBack(t *testing.T) {
	t.Parallel()
	// A garbage cursor sort value on a timestamp sort must not panic or wedge —
	// it falls back to a string compare so paging still makes progress.
	res := domain.SearchOfferings(sampleOfferings(), domain.OfferingQuery{
		TenantID: tenantA, Limit: 2,
		Cursor: &domain.OfferingCursor{SortValue: "not-a-timestamp", ID: "zzz"},
	})
	if res == nil {
		t.Fatalf("expected a (possibly empty) page, not nil")
	}
}

func TestOfferingQuery_Normalize_AllInvalidFiltersDropToNil(t *testing.T) {
	t.Parallel()
	q := domain.OfferingQuery{
		TenantID:      tenantA,
		DeliveryTypes: []string{"weekend", "  "},
		States:        []string{"BOGUS"},
	}.Normalize()
	if q.DeliveryTypes != nil {
		t.Fatalf("all-invalid delivery types should drop to nil; got %v", q.DeliveryTypes)
	}
	if q.States != nil {
		t.Fatalf("all-invalid states should drop to nil; got %v", q.States)
	}
}

func TestSearchOfferings_Facets_QueryMinusSelf(t *testing.T) {
	t.Parallel()
	all := sampleOfferings()
	// Filter to delivery_type=graduate. The delivery_type facet must STILL show
	// all reachable types (computed without its own filter), while the state
	// facet must reflect the graduate filter.
	page := domain.SearchOfferings(all, domain.OfferingQuery{
		TenantID:      tenantA,
		DeliveryTypes: []string{"graduate"},
	})

	dtFacet := facetByField(page.Facets, domain.OfferingFacetDeliveryType)
	if dtFacet == nil {
		t.Fatalf("missing delivery_type facet")
	}
	// query-minus-self: graduate(2), short(2), async(1) all visible
	if facetCount(dtFacet, "graduate") != 2 || facetCount(dtFacet, "short") != 2 || facetCount(dtFacet, "async") != 1 {
		t.Fatalf("delivery_type facet should ignore its own filter; got %+v", dtFacet.Values)
	}

	stateFacet := facetByField(page.Facets, domain.OfferingFacetState)
	if stateFacet == nil {
		t.Fatalf("missing state facet")
	}
	// among graduates only: Alpha(DRAFT) + Delta(RUNNING)
	if facetCount(stateFacet, "DRAFT") != 1 || facetCount(stateFacet, "RUNNING") != 1 {
		t.Fatalf("state facet should reflect the graduate filter; got %+v", stateFacet.Values)
	}
	if facetCount(stateFacet, "LAUNCHED") != 0 {
		t.Fatalf("no graduate is LAUNCHED; got %+v", stateFacet.Values)
	}
}
