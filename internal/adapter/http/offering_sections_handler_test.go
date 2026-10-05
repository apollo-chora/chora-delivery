// offering_sections_handler_test.go — handler-level verification of the
// offering-nested Sections surface (R+ four-mode W7 / D2): intra-cohort
// sub-groups of a GRADUATE offering. GET + POST /api/v1/offerings/{id}/sections.
//
// A Section is a CHILD of the Offering aggregate, persisted inside the
// Offering's JSONB snapshot — NO own table, NO migration, NO new event, NO
// cross-DB query (ddd-enforcement #3). Mirrors offering_certification_handler_test.go's
// in-mem harness + reqWithHeaders. Sections exist only on a graduate offering
// (create 409s otherwise — the domain ErrSectionNotGraduate).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	sectionOfferingID = "01985e7f-4444-7abc-8def-0000000000f1"
	sectionCourseID   = "01985e7f-4444-7abc-8def-0000000000f2"
)

// newOfferingSectionTestServer wires just the Offerings port with an in-mem repo
// so the sections route resolves DB-free (sections ride in the Offering JSONB
// aggregate — no other dependency).
func newOfferingSectionTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo})
	return srv, oRepo
}

// seedSectionOffering stores a DRAFT offering of the given delivery_type under
// the standard tenant and returns it (the in-mem store holds this pointer, so a
// test may AddSection on it directly to pre-seed sections).
func seedSectionOffering(t *testing.T, oRepo *inmem.OfferingRepo, id string, dt delivery.DeliveryType) *delivery.Offering {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{sectionCourseID},
		DeliveryType: dt,
		Label:        "W7 Sections Run",
		Capacity:     0,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
	return o
}

// sectionRow is the wire shape of one section row.
type sectionRow struct {
	SectionID          string `json:"section_id"`
	Name               string `json:"name"`
	LeadInstructorGCID string `json:"lead_instructor_gcid"`
	Room               string `json:"room"`
	StartDate          string `json:"start_date"`
	EndDate            string `json:"end_date"`
}

// sectionsResp is the wire envelope returned by GET .../sections.
type sectionsResp struct {
	Sections []sectionRow `json:"sections"`
}

func getSections(t *testing.T, srv http.Handler, offeringID, role string) (*httptest.ResponseRecorder, sectionsResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringID+"/sections", nil, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp sectionsResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/sections
// -----------------------------------------------------------------------------

func TestOfferingSections_Post_CreatesAndPersistsSection(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)

	body, _ := json.Marshal(map[string]any{
		"name":                 "  Morning Cohort  ",
		"lead_instructor_gcid": "00000000-0000-7000-8000-0000000000f1",
		"room":                 "Room 204",
		"start_date":           "2026-09-01",
		"end_date":             "2026-12-15",
	})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+sectionOfferingID+"/sections", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var created sectionRow
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if created.SectionID == "" {
		t.Fatalf("created section must carry a section_id, body=%s", w.Body.String())
	}
	if created.Name != "Morning Cohort" { // trimmed
		t.Fatalf("name: want trimmed 'Morning Cohort', got %q", created.Name)
	}
	if created.Room != "Room 204" || created.StartDate != "2026-09-01" || created.EndDate != "2026-12-15" {
		t.Fatalf("optional fields not echoed: %#v", created)
	}

	// Persisted into the JSONB aggregate → a subsequent GET lists it.
	w2, resp := getSections(t, srv, sectionOfferingID, "instructor")
	if w2.Code != http.StatusOK {
		t.Fatalf("GET after create: want 200, got %d", w2.Code)
	}
	if len(resp.Sections) != 1 || resp.Sections[0].SectionID != created.SectionID {
		t.Fatalf("created section not persisted, got %#v", resp.Sections)
	}
}

func TestOfferingSections_Post_404WhenOfferingMissing(t *testing.T) {
	srv, _ := newOfferingSectionTestServer(t)
	// Offering NOT seeded.
	body, _ := json.Marshal(map[string]any{"name": "Group 1"})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+sectionOfferingID+"/sections", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSections_Post_400WhenNameMissing(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)

	body, _ := json.Marshal(map[string]any{"name": "   "})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+sectionOfferingID+"/sections", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (blank name), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSections_Post_409WhenNotGraduate(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	// Sections require a graduate cohort; a short offering has none to subdivide.
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeShort)

	body, _ := json.Marshal(map[string]any{"name": "Group 1"})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+sectionOfferingID+"/sections", body, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: want 409 (not graduate), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSections_Post_403WithoutRole(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)

	body, _ := json.Marshal(map[string]any{"name": "Group 1"})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+sectionOfferingID+"/sections", body, instructor, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/sections
// -----------------------------------------------------------------------------

func TestOfferingSections_Get_ReturnsSectionsInOrder(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	o := seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	a, err := o.AddSection(delivery.AddSectionInput{Name: "Morning"})
	if err != nil {
		t.Fatalf("AddSection A: %v", err)
	}
	b, err := o.AddSection(delivery.AddSectionInput{Name: "Evening"})
	if err != nil {
		t.Fatalf("AddSection B: %v", err)
	}
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save: %v", err)
	}

	w, resp := getSections(t, srv, sectionOfferingID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Sections) != 2 {
		t.Fatalf("sections: want 2, got %d body=%s", len(resp.Sections), w.Body.String())
	}
	if resp.Sections[0].SectionID != a.SectionID || resp.Sections[1].SectionID != b.SectionID {
		t.Fatalf("order: want [%s,%s], got [%s,%s]", a.SectionID, b.SectionID, resp.Sections[0].SectionID, resp.Sections[1].SectionID)
	}
	if resp.Sections[0].Name != "Morning" || resp.Sections[1].Name != "Evening" {
		t.Fatalf("names: got %q,%q", resp.Sections[0].Name, resp.Sections[1].Name)
	}
}

func TestOfferingSections_Get_EmptyWhenNoSections(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)

	w, resp := getSections(t, srv, sectionOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Sections) != 0 {
		t.Fatalf("sections: want empty, got %#v", resp.Sections)
	}
}

func TestOfferingSections_Get_404WhenOfferingMissing(t *testing.T) {
	srv, _ := newOfferingSectionTestServer(t)
	w, _ := getSections(t, srv, sectionOfferingID, "instructor") // offering NOT seeded
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSections_Get_403WithoutRole(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)

	w, _ := getSections(t, srv, sectionOfferingID, "") // no instructor/admin role
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSections_MethodNotAllowed(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)

	r := reqWithHeaders(http.MethodPut, "/api/v1/offerings/"+sectionOfferingID+"/sections", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d body=%s", w.Code, w.Body.String())
	}
}
