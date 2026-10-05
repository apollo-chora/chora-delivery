// offering_search_handler_test.go — TDD coverage for the R+ universal-finder
// offering search HTTP surface (four-mode refactor W2.A, CHO-1850).
//
//	GET /api/v1/search/offerings?q=&filter[delivery_type]=&filter[state]=
//	    &sort=field:dir&cursor=&limit=
//	→ 200 { items, facets, next_cursor, total_estimate }
//
// Exercises the handler + param parsing + opaque cursor round-trip + DTO over
// the in-memory adapter (which delegates to the pure domain.SearchOfferings),
// so the wire contract is verified DB-free. RLS/keyset against live pg is the
// integration test.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func createOfferingBodyLabel(deliveryType, label string) string {
	return `{
		"course_id": "` + offTestCourseID + `",
		"delivery_type": "` + deliveryType + `",
		"label": "` + label + `",
		"capacity": 0
	}`
}

func seedOfferings(t *testing.T, srv http.Handler, specs [][2]string) {
	t.Helper()
	for _, s := range specs {
		rec := doOffering(t, srv, "POST", "/api/v1/offerings",
			createOfferingBodyLabel(s[0], s[1]), offTestTenantID, offTestAdminGCID, "training-admin")
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed offering %v: status=%d body=%s", s, rec.Code, rec.Body.String())
		}
	}
}

func searchOfferings(t *testing.T, srv http.Handler, query, tenant string) map[string]interface{} {
	t.Helper()
	rec := doOffering(t, srv, "GET", "/api/v1/search/offerings"+query, "", tenant, offTestAdminGCID, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("search %q: status=%d body=%s", query, rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("search %q: bad json: %v", query, err)
	}
	return got
}

func searchItems(m map[string]interface{}) []interface{} {
	items, _ := m["items"].([]interface{})
	return items
}

// -----------------------------------------------------------------------------

func TestOfferingSearch_Empty_200(t *testing.T) {
	srv, _ := newOfferingServer()
	got := searchOfferings(t, srv, "", offTestTenantID)
	if items := searchItems(got); len(items) != 0 {
		t.Fatalf("want empty items; got %d", len(items))
	}
	if got["total_estimate"].(float64) != 0 {
		t.Fatalf("want total_estimate 0; got %v", got["total_estimate"])
	}
	if got["next_cursor"] != nil {
		t.Fatalf("want next_cursor null; got %v", got["next_cursor"])
	}
	// facets present (delivery_type + state) even when empty
	facets, _ := got["facets"].([]interface{})
	if len(facets) != 2 {
		t.Fatalf("want 2 facet dimensions; got %d", len(facets))
	}
}

func TestOfferingSearch_ListsCreated_Badged(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{
		{"graduate", "Alpha Cohort"},
		{"short", "Bravo Bootcamp"},
		{"async", "Charlie Self-Paced"},
	})
	got := searchOfferings(t, srv, "", offTestTenantID)
	if items := searchItems(got); len(items) != 3 {
		t.Fatalf("want 3 items; got %d", len(items))
	}
	if got["total_estimate"].(float64) != 3 {
		t.Fatalf("want total_estimate 3; got %v", got["total_estimate"])
	}
	// every item carries its delivery_type badge
	for _, it := range searchItems(got) {
		o := it.(map[string]interface{})
		if o["delivery_type"] == nil || o["delivery_type"] == "" {
			t.Fatalf("item missing delivery_type: %v", o)
		}
	}
	// delivery_type facet shows all three reachable values
	dt := facetValues(t, got, "delivery_type")
	if dt["graduate"] != 1 || dt["short"] != 1 || dt["async"] != 1 {
		t.Fatalf("delivery_type facet counts wrong: %v", dt)
	}
}

func TestOfferingSearch_FilterByDeliveryType(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{
		{"graduate", "Alpha"}, {"short", "Bravo"}, {"graduate", "Delta"},
	})
	got := searchOfferings(t, srv, "?filter[delivery_type]=graduate", offTestTenantID)
	if items := searchItems(got); len(items) != 2 {
		t.Fatalf("graduate filter want 2; got %d", len(items))
	}
	// query-minus-self: the delivery_type facet still shows short as reachable
	dt := facetValues(t, got, "delivery_type")
	if dt["short"] != 1 {
		t.Fatalf("delivery_type facet should still show short (query-minus-self); got %v", dt)
	}
}

func TestOfferingSearch_FreeTextLabel(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{
		{"graduate", "Alpha Cohort"}, {"short", "Bravo Bootcamp"}, {"graduate", "Delta Cohort"},
	})
	got := searchOfferings(t, srv, "?q=cohort", offTestTenantID)
	if items := searchItems(got); len(items) != 2 {
		t.Fatalf("q=cohort want 2 (case-insensitive); got %d", len(items))
	}
}

func TestOfferingSearch_SortLabelAsc(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{
		{"graduate", "Charlie"}, {"short", "Alpha"}, {"async", "Bravo"},
	})
	got := searchOfferings(t, srv, "?sort=label:asc", offTestTenantID)
	items := searchItems(got)
	want := []string{"Alpha", "Bravo", "Charlie"}
	for i, w := range want {
		if lbl := items[i].(map[string]interface{})["label"]; lbl != w {
			t.Fatalf("label asc pos %d: want %q got %v", i, w, lbl)
		}
	}
}

func TestOfferingSearch_Keyset_Walk_NoOverlap(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{
		{"graduate", "A"}, {"short", "B"}, {"async", "C"}, {"graduate", "D"}, {"short", "E"},
	})

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		q := "?limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		got := searchOfferings(t, srv, q, offTestTenantID)
		if got["total_estimate"].(float64) != 5 {
			t.Fatalf("total_estimate should stay 5; got %v", got["total_estimate"])
		}
		for _, it := range searchItems(got) {
			id := it.(map[string]interface{})["id"].(string)
			if seen[id] {
				t.Fatalf("keyset overlap: id %s seen twice", id)
			}
			seen[id] = true
		}
		nc, ok := got["next_cursor"].(string)
		if !ok || nc == "" {
			break
		}
		cursor = nc
		pages++
		if pages > 10 {
			t.Fatalf("keyset walk did not terminate")
		}
	}
	if len(seen) != 5 {
		t.Fatalf("keyset walk should cover all 5 offerings; got %d", len(seen))
	}
}

func TestOfferingSearch_CrossTenantIsolated(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{{"graduate", "Alpha"}})
	got := searchOfferings(t, srv, "", offTestOtherTenant)
	if items := searchItems(got); len(items) != 0 {
		t.Fatalf("cross-tenant search must see 0; got %d", len(items))
	}
}

func TestOfferingSearch_NoTenant_400(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "GET", "/api/v1/search/offerings", "", "", offTestAdminGCID, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestOfferingSearch_MethodNotAllowed_405(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/search/offerings", "{}", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}

// facetValues extracts a {value:count} map for the named facet dimension.
func facetValues(t *testing.T, got map[string]interface{}, field string) map[string]float64 {
	t.Helper()
	facets, _ := got["facets"].([]interface{})
	for _, f := range facets {
		fm := f.(map[string]interface{})
		if fm["field"] != field {
			continue
		}
		out := map[string]float64{}
		vals, _ := fm["values"].([]interface{})
		for _, v := range vals {
			vm := v.(map[string]interface{})
			out[vm["value"].(string)] = vm["count"].(float64)
		}
		return out
	}
	t.Fatalf("facet %q not found in %v", field, got["facets"])
	return nil
}
