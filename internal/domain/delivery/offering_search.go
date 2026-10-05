// offering_search.go — the pure Offering search engine for the R+ universal
// finder (W2.A, ADR-190 + docs/design/ux_universal_search_collection.md).
//
// SearchOfferings is the single executable reference for the finder's query
// model: free-text label + delivery_type/state filters, server multi-sort
// (created_at|updated_at|label with an id tiebreak), keyset (cursor) pagination
// that is stable under inserts, query-minus-self facet counts, and a total
// estimate. The in-memory adapter delegates here directly; the pg adapter
// encodes the SAME ordering + facet semantics as SQL. Keeping the logic pure
// (no I/O) means the intricate part is unit-tested once, under the 85% domain
// gate, and both adapters stay thin.
package delivery

import (
	"sort"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// Query vocabulary
// -----------------------------------------------------------------------------

// Sort fields (the offering columns the finder may order by).
const (
	OfferingSortCreatedAt = "created_at"
	OfferingSortUpdatedAt = "updated_at"
	OfferingSortLabel     = "label"
)

// Sort directions.
const (
	OfferingSortDirAsc  = "asc"
	OfferingSortDirDesc = "desc"
)

// Page-size policy: default when unset, hard cap to keep payloads lean.
const (
	OfferingSearchDefaultLimit = 20
	OfferingSearchMaxLimit     = 100
)

// Facet field names (the faceted dimensions the finder badges + filters by).
const (
	OfferingFacetDeliveryType = "delivery_type"
	OfferingFacetState        = "state"
)

// OfferingCursor is the keyset position carried between pages: the sort-field
// value + id of the last row of the previous page. Opaque on the wire
// (base64-encoded by the handler); typed comparison happens here.
type OfferingCursor struct {
	SortValue string
	ID        string
}

// OfferingQuery is a finder search request over one tenant's offerings.
// Construct freely then call Normalize before use.
type OfferingQuery struct {
	TenantID      string
	Q             string   // free-text, matched case-insensitively against label
	CourseID      string   // blast-radius filter — offerings BUNDLING this course (matches the CourseIDs array, not just primary); empty = all
	DeliveryTypes []string // OR within the facet; empty = all
	States        []string // OR within the facet; empty = all
	SortField     string
	SortDir       string
	Cursor        *OfferingCursor
	Limit         int
}

// OfferingFacetValue is one bucket of a facet with its match count.
type OfferingFacetValue struct {
	Value string
	Count int
}

// OfferingFacet is a faceted dimension and its value buckets (value-sorted).
type OfferingFacet struct {
	Field  string
	Values []OfferingFacetValue
}

// OfferingSearchPage is one page of finder results: the items, the facet
// counts, the next keyset cursor (nil = last page), and a total estimate of
// the full filtered set (independent of the cursor/page).
type OfferingSearchPage struct {
	Items         []*Offering
	Facets        []OfferingFacet
	NextCursor    *OfferingCursor
	TotalEstimate int
}

// -----------------------------------------------------------------------------
// Normalize
// -----------------------------------------------------------------------------

// Normalize applies defaults, clamps, and validation so adapters can trust the
// query: unknown sort field → created_at; unknown dir → desc; limit clamped to
// [1, max] (0/absent → default); unknown delivery_type/state tokens dropped;
// q + tenant trimmed. Pure — no I/O. Idempotent.
func (q OfferingQuery) Normalize() OfferingQuery {
	out := q
	out.TenantID = strings.TrimSpace(q.TenantID)
	out.Q = strings.TrimSpace(q.Q)

	switch q.SortField {
	case OfferingSortCreatedAt, OfferingSortUpdatedAt, OfferingSortLabel:
		out.SortField = q.SortField
	default:
		out.SortField = OfferingSortCreatedAt
	}

	switch strings.ToLower(strings.TrimSpace(q.SortDir)) {
	case OfferingSortDirAsc:
		out.SortDir = OfferingSortDirAsc
	default:
		out.SortDir = OfferingSortDirDesc
	}

	switch {
	case q.Limit <= 0:
		out.Limit = OfferingSearchDefaultLimit
	case q.Limit > OfferingSearchMaxLimit:
		out.Limit = OfferingSearchMaxLimit
	default:
		out.Limit = q.Limit
	}

	out.DeliveryTypes = filterValid(q.DeliveryTypes, func(s string) bool { return DeliveryType(s).IsValid() })
	out.States = filterValid(q.States, func(s string) bool { return OfferingState(s).IsValid() })

	return out
}

func filterValid(in []string, ok func(string) bool) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		t := strings.TrimSpace(v)
		if t != "" && ok(t) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// -----------------------------------------------------------------------------
// Search
// -----------------------------------------------------------------------------

// SearchOfferings runs a finder query over a slice of offerings (typically one
// tenant's active rows). Pure: it normalizes the query, filters, sorts, applies
// the keyset cursor + limit, and computes query-minus-self facets. The input
// slice is not mutated.
func SearchOfferings(all []*Offering, q OfferingQuery) *OfferingSearchPage {
	q = q.Normalize()

	// Full filter across every dimension → the result + total set.
	matched := make([]*Offering, 0, len(all))
	for _, o := range all {
		if offeringPassesBase(o, q) && offeringInSet(string(o.DeliveryType), q.DeliveryTypes) && offeringInSet(string(o.State), q.States) {
			matched = append(matched, o)
		}
	}

	sortOfferings(matched, q.SortField, q.SortDir)
	total := len(matched)

	// Keyset: keep only rows strictly after the cursor under (sort,dir).
	if q.Cursor != nil {
		after := matched[:0:0]
		for _, o := range matched {
			if offeringAfterCursor(o, q.SortField, q.SortDir, q.Cursor) {
				after = append(after, o)
			}
		}
		matched = after
	}

	// One-past lookahead → next_cursor without a second query.
	var next *OfferingCursor
	items := matched
	if len(items) > q.Limit {
		items = items[:q.Limit]
		last := items[len(items)-1]
		next = &OfferingCursor{SortValue: offeringSortValue(last, q.SortField), ID: last.ID}
	}

	facets := []OfferingFacet{
		buildOfferingFacet(all, q, OfferingFacetDeliveryType),
		buildOfferingFacet(all, q, OfferingFacetState),
	}

	return &OfferingSearchPage{
		Items:         items,
		Facets:        facets,
		NextCursor:    next,
		TotalEstimate: total,
	}
}

// offeringPassesBase applies the tenant + soft-delete + free-text predicates
// shared by the result set and every facet computation.
func offeringPassesBase(o *Offering, q OfferingQuery) bool {
	if o == nil || o.DeletedAt != nil {
		return false
	}
	if q.TenantID != "" && o.TenantID != q.TenantID {
		return false
	}
	if q.Q != "" && !strings.Contains(strings.ToLower(o.Label), strings.ToLower(q.Q)) {
		return false
	}
	return true
}

func offeringInSet(v string, set []string) bool {
	if len(set) == 0 {
		return true
	}
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// buildOfferingFacet computes one facet's value counts applying every filter
// EXCEPT the facet's own dimension (so a selected value still shows the other
// reachable values — the spec's "current query minus the facet being shown").
func buildOfferingFacet(all []*Offering, q OfferingQuery, field string) OfferingFacet {
	counts := map[string]int{}
	for _, o := range all {
		if !offeringPassesBase(o, q) {
			continue
		}
		if field != OfferingFacetDeliveryType && !offeringInSet(string(o.DeliveryType), q.DeliveryTypes) {
			continue
		}
		if field != OfferingFacetState && !offeringInSet(string(o.State), q.States) {
			continue
		}
		key := string(o.State)
		if field == OfferingFacetDeliveryType {
			key = string(o.DeliveryType)
		}
		counts[key]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vals := make([]OfferingFacetValue, 0, len(keys))
	for _, k := range keys {
		vals = append(vals, OfferingFacetValue{Value: k, Count: counts[k]})
	}
	return OfferingFacet{Field: field, Values: vals}
}

// -----------------------------------------------------------------------------
// Ordering (sort + keyset) — typed comparison; timestamps never string-compared
// (RFC3339Nano fractional precision is not lexicographically safe).
// -----------------------------------------------------------------------------

func sortOfferings(items []*Offering, field, dir string) {
	sort.SliceStable(items, func(i, j int) bool {
		c := compareOfferings(items[i], items[j], field)
		if dir == OfferingSortDirDesc {
			return c > 0
		}
		return c < 0
	})
}

// compareOfferings returns -1/0/1 by (sort value, then id) ascending.
func compareOfferings(a, b *Offering, field string) int {
	var vc int
	switch field {
	case OfferingSortLabel:
		vc = strings.Compare(a.Label, b.Label)
	case OfferingSortUpdatedAt:
		vc = compareTimes(a.UpdatedAt, b.UpdatedAt)
	default:
		vc = compareTimes(a.CreatedAt, b.CreatedAt)
	}
	if vc != 0 {
		return vc
	}
	return strings.Compare(a.ID, b.ID)
}

// offeringAfterCursor reports whether o sorts strictly after the cursor under
// (field, dir) — the keyset predicate for "the next page".
func offeringAfterCursor(o *Offering, field, dir string, c *OfferingCursor) bool {
	cmp := compareOfferingToCursor(o, field, c)
	if dir == OfferingSortDirAsc {
		return cmp > 0
	}
	return cmp < 0
}

func compareOfferingToCursor(o *Offering, field string, c *OfferingCursor) int {
	var vc int
	switch field {
	case OfferingSortLabel:
		vc = strings.Compare(o.Label, c.SortValue)
	case OfferingSortUpdatedAt:
		vc = compareTimeToRaw(o.UpdatedAt, c.SortValue)
	default:
		vc = compareTimeToRaw(o.CreatedAt, c.SortValue)
	}
	if vc != 0 {
		return vc
	}
	return strings.Compare(o.ID, c.ID)
}

// offeringSortValue is the wire form of the sort field for a cursor: timestamps
// as RFC3339Nano (round-trips through pg timestamptz + Go time.Parse), label
// verbatim.
func offeringSortValue(o *Offering, field string) string {
	switch field {
	case OfferingSortLabel:
		return o.Label
	case OfferingSortUpdatedAt:
		return o.UpdatedAt.UTC().Format(time.RFC3339Nano)
	default:
		return o.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
}

func compareTimes(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	default:
		return 0
	}
}

func compareTimeToRaw(t time.Time, raw string) int {
	c, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		// Unparseable cursor value: fall back to string compare so paging still
		// makes monotone progress rather than wedging.
		return strings.Compare(t.UTC().Format(time.RFC3339Nano), raw)
	}
	return compareTimes(t.UTC(), c.UTC())
}
