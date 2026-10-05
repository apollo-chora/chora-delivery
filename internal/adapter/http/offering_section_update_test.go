// offering_section_update_test.go — R+ Phase-2 S4: section detail edit.
// PATCH /api/v1/offerings/{id}/sections/{sectionId} — edit an intra-cohort
// section's delivery logistics (name / lead / room / dates). Only provided
// (non-nil) fields change. Reuses the sections in-mem harness.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func patchSection(t *testing.T, srv http.Handler, offeringID, sectionID, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+offeringID+"/sections/"+sectionID, b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestOfferingSections_Patch_UpdatesProvidedFieldsOnly(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	o := seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	sec, err := o.AddSection(delivery.AddSectionInput{Name: "Morning", Room: "Old Room"})
	if err != nil {
		t.Fatalf("AddSection: %v", err)
	}
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save: %v", err)
	}

	w := patchSection(t, srv, sectionOfferingID, sec.SectionID, "instructor", map[string]any{
		"room":                 "Lab 5",
		"lead_instructor_gcid": "00000000-0000-7000-8000-0000000000aa",
		"start_date":           "2026-10-01",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got sectionRow
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if got.Room != "Lab 5" || got.StartDate != "2026-10-01" || got.LeadInstructorGCID != "00000000-0000-7000-8000-0000000000aa" {
		t.Fatalf("provided fields not applied: %#v", got)
	}
	if got.Name != "Morning" { // name NOT in the body → unchanged
		t.Fatalf("name should be unchanged, got %q", got.Name)
	}

	// Persisted → GET lists the edit.
	_, resp := getSections(t, srv, sectionOfferingID, "instructor")
	if len(resp.Sections) != 1 || resp.Sections[0].Room != "Lab 5" {
		t.Fatalf("edit not persisted, got %#v", resp.Sections)
	}
}

func TestOfferingSections_Patch_404WhenSectionMissing(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	w := patchSection(t, srv, sectionOfferingID, "01985e7f-4444-7abc-8def-000000000fff", "instructor", map[string]any{"room": "X"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404 (section missing), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSections_Patch_404WhenOfferingMissing(t *testing.T) {
	srv, _ := newOfferingSectionTestServer(t)
	w := patchSection(t, srv, sectionOfferingID, "01985e7f-4444-7abc-8def-000000000fff", "instructor", map[string]any{"room": "X"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404 (offering missing), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSections_Patch_400WhenBlankName(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	o := seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	sec, _ := o.AddSection(delivery.AddSectionInput{Name: "M"})
	_ = oRepo.Save(context.Background(), o)
	w := patchSection(t, srv, sectionOfferingID, sec.SectionID, "instructor", map[string]any{"name": "   "})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (blank name), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSections_Patch_403WithoutRole(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	o := seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	sec, _ := o.AddSection(delivery.AddSectionInput{Name: "M"})
	_ = oRepo.Save(context.Background(), o)
	w := patchSection(t, srv, sectionOfferingID, sec.SectionID, "", map[string]any{"room": "X"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingSections_Patch_405OnGet(t *testing.T) {
	srv, oRepo := newOfferingSectionTestServer(t)
	o := seedSectionOffering(t, oRepo, sectionOfferingID, delivery.DeliveryTypeGraduate)
	sec, _ := o.AddSection(delivery.AddSectionInput{Name: "M"})
	_ = oRepo.Save(context.Background(), o)
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+sectionOfferingID+"/sections/"+sec.SectionID, nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d body=%s", w.Code, w.Body.String())
	}
}
