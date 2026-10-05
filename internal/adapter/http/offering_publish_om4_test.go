// offering_publish_om4_test.go — om4 coverage top-ups for
// offering_publish_handler.go. The pre-existing suite covers the per-course GET
// matrix, the publish/idempotency happy paths, the B2.1/B2.2 gates, and the
// catalogue-unwired 503; these tests drive the leftovers reachable through the
// in-memory stores: the offerings-unwired 503s on GET+PATCH, the 401 no-gcid
// gate, the 404s, the PATCH with a nil Publisher (publish still succeeds, just
// no fan-out), the B2.1 fail-closed branch when the CJ#2 course is MISSING from
// a wired store (offeringPublishStateAllows → false), and the unlisted-row
// title enrichment from a wired CJ#2 course store (publishRowFromTitle).
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// -----------------------------------------------------------------------------
// GET /publish — wiring + identity + miss
// -----------------------------------------------------------------------------

// TestOm4Publish_Get_503WhenOfferingsUnwired drives the GET's first unwired
// guard (the suite's 503 only dropped the catalogue).
func TestOm4Publish_Get_503WhenOfferingsUnwired(t *testing.T) {
	cat := delivery.NewInMemCatalogue()
	srv := httpapi.NewServer(httpapi.Deps{Catalogue: cat})
	w, _ := getPublish(t, srv, opubOfferingID, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Publish_Get_401WithoutGCID drives the GET's callerTenantGCID 401.
func TestOm4Publish_Get_401WithoutGCID(t *testing.T) {
	srv, oRepo, _, _ := newOfferingPublishTestServer(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	r := om4NoGCID(http.MethodGet, "/api/v1/offerings/"+opubOfferingID+"/publish", nil, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Publish_Get_404WhenOfferingMissing drives the GET's tenant-scoped 404
// (the suite only 404'd the write path).
func TestOm4Publish_Get_404WhenOfferingMissing(t *testing.T) {
	srv, _, _, _ := newOfferingPublishTestServer(t)
	w, _ := getPublish(t, srv, opubOfferingID, "instructor")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Publish_Get_ResolvesUnlistedTitleFromCJ2 drives publishRowFromTitle's
// wired-course-store enrichment: a course absent from the catalogue but present
// in the CJ#2 store renders its title with listed:false.
func TestOm4Publish_Get_ResolvesUnlistedTitleFromCJ2(t *testing.T) {
	srv, oRepo, _, _, courseStore := newOfferingPublishTestServerCJ2(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseC) // C never in catalogue
	seedCJ2Course(t, courseStore, opubCourseC, "Not Listed Course")

	w, resp := getPublish(t, srv, opubOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 1 {
		t.Fatalf("courses: want 1, got %d", len(resp.Courses))
	}
	c := resp.Courses[0]
	if c.Listed || c.Published {
		t.Fatalf("unlisted course must NOT be listed/published: %+v", c)
	}
	if c.Title != "Not Listed Course" {
		t.Fatalf("title: want 'Not Listed Course' (from CJ2 store), got %q", c.Title)
	}
}

// -----------------------------------------------------------------------------
// PATCH /publish — wiring + miss + nil-publisher + fail-closed B2.1
// -----------------------------------------------------------------------------

// TestOm4Publish_Patch_503WhenOfferingsUnwired drives the PATCH's first
// unwired guard.
func TestOm4Publish_Patch_503WhenOfferingsUnwired(t *testing.T) {
	cat := delivery.NewInMemCatalogue()
	srv := httpapi.NewServer(httpapi.Deps{Catalogue: cat})
	w, _ := patchPublish(t, srv, opubOfferingID, "training-admin")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Publish_Patch_503WhenCatalogueUnwired drives the PATCH's second
// unwired guard (the suite's 503 covered the GET only).
func TestOm4Publish_Patch_503WhenCatalogueUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo})
	w, _ := patchPublish(t, srv, opubOfferingID, "training-admin")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Publish_Patch_404WhenOfferingMissing drives the PATCH's tenant-scoped
// 404.
func TestOm4Publish_Patch_404WhenOfferingMissing(t *testing.T) {
	srv, _, _, _ := newOfferingPublishTestServer(t)
	w, _ := patchPublish(t, srv, opubOfferingID, "training-admin")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Publish_Patch_401WithoutGCID drives the PATCH's callerTenantGCID 401.
func TestOm4Publish_Patch_401WithoutGCID(t *testing.T) {
	srv, oRepo, _, _ := newOfferingPublishTestServer(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	r := om4NoGCID(http.MethodPatch, "/api/v1/offerings/"+opubOfferingID+"/publish", []byte("{}"), "training-admin")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Publish_Patch_WorksWithoutPublisher drives the `deps.Publisher != nil`
// skip: the publish itself must succeed (catalogue write + count) even when no
// event publisher is wired — the fan-out is best-effort.
func TestOm4Publish_Patch_WorksWithoutPublisher(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	cat := delivery.NewInMemCatalogue()
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	seedPublicCourse(t, cat, opubCourseA, "Course A", delivery.VisibilityPrivate)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Catalogue: cat}) // Publisher nil

	w, resp := patchPublish(t, srv, opubOfferingID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.PublishedCount != 1 {
		t.Fatalf("published_count: want 1, got %d", resp.PublishedCount)
	}
}

// TestOm4Publish_Patch_FailClosedWhenCourseMissingFromWiredCJ2 drives
// offeringPublishStateAllows' `err != nil || !ok || c == nil` fail-closed
// branch: the course EXISTS in the catalogue as private but is MISSING from the
// wired CJ#2 course store → the flip to public is refused (blocked_reason), not
// silently authorised.
func TestOm4Publish_Patch_FailClosedWhenCourseMissingFromWiredCJ2(t *testing.T) {
	srv, oRepo, cat, _, _ := newOfferingPublishTestServerCJ2(t)
	seedAnalyticsOffering(t, oRepo, opubOfferingID, 0, opubCourseA)
	seedPublicCourse(t, cat, opubCourseA, "Course A", delivery.VisibilityPrivate)
	// courseA deliberately NOT seeded into the CJ2 store.

	w, resp := patchPublish(t, srv, opubOfferingID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.PublishedCount != 0 {
		t.Fatalf("missing CJ2 course must refuse the flip, published_count=%d", resp.PublishedCount)
	}
	if len(resp.Courses) != 1 {
		t.Fatalf("courses: want 1, got %d body=%s", len(resp.Courses), w.Body.String())
	}
	body := w.Body.Bytes()
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	row := raw["courses"].([]interface{})[0].(map[string]interface{})
	if row["blocked_reason"] == nil || row["blocked_reason"] == "" {
		t.Fatalf("missing-CJ2 course must carry a blocked_reason; got %v", row)
	}
}
