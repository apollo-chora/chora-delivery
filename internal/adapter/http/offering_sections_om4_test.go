// offering_sections_om4_test.go — om4 coverage top-ups for
// offering_sections_handler.go. The pre-existing sections + section-update
// suites cover the happy list/create/patch flows, the 404/400/403/405 matrix
// and the graduate-only 409; these tests drive the reachable leftovers: the
// offerings-unwired 503 on all three endpoints, the callerTenantGCID 401s, and
// the decodeBody 400s (malformed JSON) on create + update.
package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// om4RawSectionPost POSTs a raw byte body (unmarshalable on purpose) to a
// sections path so decodeBody's 400 guard fires.
func om4RawSectionPost(t *testing.T, srv http.Handler, path, raw string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodPost, path, []byte(raw), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// -----------------------------------------------------------------------------
// Wiring + identity gates across GET/POST/PATCH
// -----------------------------------------------------------------------------

// TestOm4Sections_GET_503WhenOfferingsUnwired drives handleOfferingListSections'
// unwired guard.
func TestOm4Sections_GET_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+sectionOfferingID+"/sections", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Sections_GET_401WithoutGCID drives the list's callerTenantGCID 401.
func TestOm4Sections_GET_401WithoutGCID(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	r := om4NoGCID(http.MethodGet, "/api/v1/offerings/"+sectionOfferingID+"/sections", nil, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Sections_POST_503WhenOfferingsUnwired drives handleOfferingCreateSection's
// unwired guard.
func TestOm4Sections_POST_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	w := om4RawSectionPost(t, srv, "/api/v1/offerings/"+sectionOfferingID+"/sections", `{"name": "Group 1"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Sections_POST_401WithoutGCID drives the create's callerTenantGCID 401.
func TestOm4Sections_POST_401WithoutGCID(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	r := om4NoGCID(http.MethodPost, "/api/v1/offerings/"+sectionOfferingID+"/sections", []byte(`{"name": "Group 1"}`), "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Sections_POST_400MalformedJSON drives the create's decodeBody guard
// (the suite's 400 covered only the blank-name domain refusal).
func TestOm4Sections_POST_400MalformedJSON(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	w := om4RawSectionPost(t, srv, "/api/v1/offerings/"+sectionOfferingID+"/sections", `{"name":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Sections_PATCH_503WhenOfferingsUnwired drives
// handleOfferingUpdateSection's unwired guard.
func TestOm4Sections_PATCH_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+sectionOfferingID+"/sections/x", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Sections_PATCH_401WithoutGCID drives the update's callerTenantGCID 401.
func TestOm4Sections_PATCH_401WithoutGCID(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	r := om4NoGCID(http.MethodPatch, "/api/v1/offerings/"+sectionOfferingID+"/sections/x", []byte("{}"), "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Sections_PATCH_400MalformedJSON drives the update's decodeBody guard.
func TestOm4Sections_PATCH_400MalformedJSON(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	o := seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	sec, err := o.AddSection(delivery.AddSectionInput{Name: "M"})
	if err != nil {
		t.Fatalf("AddSection: %v", err)
	}
	if err := oRepo.Save(t.Context(), o); err != nil {
		t.Fatalf("Save: %v", err)
	}
	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+sectionOfferingID+"/sections/"+sec.SectionID, []byte(`{"room":`), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}
