// offering_om4_base_test.go — om4 coverage top-ups for offering_handler.go
// (the R+ Offering root surface: list/get/transition/search/cursor + the
// offeringsSubHandler dispatch edges). Every test here drives a branch that the
// pre-existing offering_*_test.go suites did not reach: the fail-loud 503s for
// an unwired offerings repo, the opaque-cursor decode guards (invalid base64 /
// missing id tiebreak), the colon-less sort form, the invalid limit param, the
// dispatch 405 on an unknown leaf, the empty-segment 404, and the unknown
// modules sub-path 404. Tenant header still present on every request (the mux
// mounts tenantRequired), so the handlers' own tenant-missing branches remain
// middleware-gated and are deliberately not asserted here.
package httpapi_test

import (
	"encoding/base64"
	"net/http"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
)

// TestOm4OfferingCreate_503Unwired drives handleOfferingCreate's unwired guard
// (the last reachable statement class in that handler — everything else is
// covered by the W1 create suite).
func TestOm4OfferingCreate_503Unwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	w := reqJSON(t, srv, http.MethodPost, "/api/v1/offerings", map[string]any{
		"course_id": "x", "delivery_type": "graduate",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Unwired-repo fail-loud (503)
// -----------------------------------------------------------------------------

// TestOm4Offerings_List_503Unwired drives handleOfferingList's
// `deps.Offerings == nil` guard.
func TestOm4Offerings_List_503Unwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	w := reqGET(t, srv, "/api/v1/offerings")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_Get_503Unwired drives handleOfferingGet's unwired guard.
func TestOm4Offerings_Get_503Unwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	w := reqGET(t, srv, "/api/v1/offerings/01970000-0000-7000-9999-fffffffffff0")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_Transition_503Unwired drives handleOfferingTransition's
// unwired guard (the only statement class left uncovered there — every other
// branch is exercised by offering_transition_handler_test.go + the launch
// readiness suite).
func TestOm4Offerings_Transition_503Unwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	w := reqJSON(t, srv, http.MethodPatch, "/api/v1/offerings/abc/launch", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4OfferingSearch_503Unwired drives handleOfferingSearch's unwired guard.
func TestOm4OfferingSearch_503Unwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	w := reqGET(t, srv, "/api/v1/search/offerings")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleOfferingSearch — param parses the finder suite did not cover
// -----------------------------------------------------------------------------

// TestOm4OfferingSearch_InvalidLimitFallsBackToDefault drives the
// `strconv.Atoi` error path: a non-numeric limit is ignored (default 20) and
// the full filtered set still returns.
func TestOm4OfferingSearch_InvalidLimitFallsBackToDefault(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{
		{"graduate", "Alpha"}, {"short", "Bravo"}, {"async", "Charlie"},
	})
	got := searchOfferings(t, srv, "?limit=abc", offTestTenantID)
	if items := searchItems(got); len(items) != 3 {
		t.Fatalf("limit=abc must be ignored: want 3 items, got %d", len(items))
	}
}

// TestOm4OfferingSearch_InvalidCursorIgnored drives decodeOfferingCursor's
// base64 decode failure (nil cursor → full page).
func TestOm4OfferingSearch_InvalidCursorIgnored(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{{"graduate", "Alpha"}, {"short", "Bravo"}})
	got := searchOfferings(t, srv, "?cursor=!!!not-base64!!!", offTestTenantID)
	if items := searchItems(got); len(items) != 2 {
		t.Fatalf("invalid cursor must be ignored: want 2 items, got %d", len(items))
	}
}

// TestOm4OfferingSearch_CursorMissingTiebreakIgnored drives decodeOfferingCursor's
// `wire.I == ""` guard — valid base64 JSON that simply lacks the id tiebreak
// must not resurrect a page position.
func TestOm4OfferingSearch_CursorMissingTiebreakIgnored(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{{"graduate", "Alpha"}, {"short", "Bravo"}})
	opaque := base64.RawURLEncoding.EncodeToString([]byte(`{"s":"2026-01-01T00:00:00Z"}`))
	got := searchOfferings(t, srv, "?cursor="+opaque, offTestTenantID)
	if items := searchItems(got); len(items) != 2 {
		t.Fatalf("cursor without id tiebreak must be ignored: want 2 items, got %d", len(items))
	}
}

// TestOm4OfferingSearch_SortLabelWithoutColon drives parseOfferingSort's
// no-colon fallback (`return raw, ""`) → Normalize applies desc. The finder
// suite only ever sent "field:dir" forms.
func TestOm4OfferingSearch_SortLabelWithoutColon(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{
		{"graduate", "Alpha"}, {"short", "Bravo"}, {"async", "Charlie"},
	})
	got := searchOfferings(t, srv, "?sort=label", offTestTenantID)
	items := searchItems(got)
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d", len(items))
	}
	if lbl := items[0].(map[string]interface{})["label"]; lbl != "Charlie" {
		t.Fatalf("sort=label (no dir) must default to desc: want Charlie first, got %v", lbl)
	}
}

// TestOm4OfferingSearch_FilterByState drives the repeatable filter[state]
// facet param against a mid-lifecycle offering.
func TestOm4OfferingSearch_FilterByState(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{{"graduate", "Alpha"}})
	items, _ := searchOfferings(t, srv, "", offTestTenantID)["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("seed failed: want 1 item, got %d", len(items))
	}
	id := items[0].(map[string]interface{})["id"].(string)
	if rec := doOffering(t, srv, "PATCH", "/api/v1/offerings/"+id+"/launch", "", offTestTenantID, offTestAdminGCID, "admin"); rec.Code != http.StatusOK {
		t.Fatalf("launch: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if items := searchItems(searchOfferings(t, srv, "?filter[state]=LAUNCHED", offTestTenantID)); len(items) != 1 {
		t.Fatalf("filter[state]=LAUNCHED: want 1 item, got %d", len(items))
	}
	if items := searchItems(searchOfferings(t, srv, "?filter[state]=DRAFT", offTestTenantID)); len(items) != 0 {
		t.Fatalf("filter[state]=DRAFT: want 0 items, got %d", len(items))
	}
}

// -----------------------------------------------------------------------------
// offeringsSubHandler dispatch edges
// -----------------------------------------------------------------------------

// TestOm4Offerings_UnknownLeafNonPatch_405 drives the case-2 method guard for a
// leaf that is neither a named sub-route nor a PATCH action.
func TestOm4Offerings_UnknownLeafNonPatch_405(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "GET", "/api/v1/offerings/abc/frobnicate", "", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_ModulesUnknownSubPath_404 drives the case-3 modules
// switch default (an unknown POST sub-path under /modules is 404).
func TestOm4Offerings_ModulesUnknownSubPath_404(t *testing.T) {
	srv, _ := newOfferingServer()
	w := reqJSON(t, srv, http.MethodPost, "/api/v1/offerings/abc/modules/whatever", map[string]any{"course_id": "x"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_LeafNonGet_405 drives the case-1 method guard: a
// non-GET on the /{id} leaf is 405 (the suites only 405'd the root and the
// action sub-routes).
func TestOm4Offerings_LeafNonGet_405(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "DELETE", "/api/v1/offerings/abc", "", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_ModulesNonPost_405 drives the case-2 modules dispatch
// default (GET+POST only ⇒ PUT is 405).
func TestOm4Offerings_ModulesNonPost_405(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "PUT", "/api/v1/offerings/abc/modules", "{}", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_PrerequisitesNonPost_405 drives the case-2 prerequisites
// dispatch default.
func TestOm4Offerings_PrerequisitesNonPost_405(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "PUT", "/api/v1/offerings/abc/prerequisites", "{}", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_ModulesSubPathNonPost_405 drives the case-3 modules method
// guard: the item/requirement sub-paths are POST-only.
func TestOm4Offerings_ModulesSubPathNonPost_405(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "GET", "/api/v1/offerings/abc/modules/add-item", "", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_PrereqRemoveNonPost_405 drives the case-3 prereq-remove
// method guard (POST-only).
func TestOm4Offerings_PrereqRemoveNonPost_405(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "GET", "/api/v1/offerings/abc/prerequisites/remove", "", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_ModuleProgressNonGet_405 drives the case-3 progress
// dispatch guard: a non-GET on /modules/progress is 405 before any offering
// load.
func TestOm4Offerings_ModuleProgressNonGet_405(t *testing.T) {
	srv, _ := newOfferingServer()
	w := reqJSON(t, srv, http.MethodPost, "/api/v1/offerings/abc/modules/progress?course_id=x", map[string]any{})
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_AssessmentsOtherMethod_405 drives the case-2 assessments
// dispatch default (GET+POST only ⇒ DELETE is 405).
func TestOm4Offerings_AssessmentsOtherMethod_405(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "DELETE", "/api/v1/offerings/abc/assessments", "", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_RosterBulkNonPost_405 drives the case-3 roster/bulk method
// guard (POST-only).
func TestOm4Offerings_RosterBulkNonPost_405(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "GET", "/api/v1/offerings/abc/roster/bulk", "", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_UnknownThreeSegment_404 drives the case-3 fall-through 404
// (a 3-segment path matching no named sub-route family).
func TestOm4Offerings_UnknownThreeSegment_404(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "GET", "/api/v1/offerings/abc/foo/bar", "", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Offerings_TooDeep_404 drives the case default (4+ segments never
// match any registered surface).
func TestOm4Offerings_TooDeep_404(t *testing.T) {
	srv, _ := newOfferingServer()
	w := doOffering(t, srv, "GET", "/api/v1/offerings/abc/foo/bar/baz", "", offTestTenantID, offTestAdminGCID, "admin")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4OfferingSearch_RoundTripCursorCoversEncode drives the non-nil cursor
// encode path again with a fresh page walk (defensive: the finder suite already
// walks it, so this only guards against future renames breaking the opaque
// contract).
func TestOm4OfferingSearch_RoundTripCursorCoversEncode(t *testing.T) {
	srv, _ := newOfferingServer()
	seedOfferings(t, srv, [][2]string{{"graduate", "A"}, {"short", "B"}, {"async", "C"}})
	got := searchOfferings(t, srv, "?limit=2", offTestTenantID)
	nc, ok := got["next_cursor"].(string)
	if !ok || nc == "" {
		t.Fatalf("limit=2 over 3 rows must yield a next_cursor; got %v", got["next_cursor"])
	}
	page2 := searchOfferings(t, srv, "?limit=2&cursor="+nc, offTestTenantID)
	if items := searchItems(page2); len(items) != 1 {
		t.Fatalf("page 2: want 1 item, got %d", len(items))
	}
	if page2["next_cursor"] != nil {
		t.Fatalf("last page must carry next_cursor null, got %v", page2["next_cursor"])
	}
}
