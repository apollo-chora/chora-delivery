// course_cert_consume_test.go — cert-definition authoring DTO + the issuance
// chain consuming the course's cert_type (CHO-1795).
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestCJ2_PostCourse_WithCertification_EmitsCertDTO(t *testing.T) {
	srv, _, _ := newCJ2Server()
	body := `{
		"title": "Certified Scrum 101",
		"learning_objectives": ["LO1"],
		"test_set_ids": ["` + cj2TestTestSetID + `"],
		"certification": {"enabled": true, "cert_type": "competency", "passing_score_pct": 80}
	}`
	rec := doCJ2(t, srv, "POST", "/api/v1/courses", body, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	cert, ok := got["certification"].(map[string]any)
	if !ok {
		t.Fatalf("certification block missing: %v", got["certification"])
	}
	if cert["enabled"] != true {
		t.Fatalf("cert.enabled want true, got %v", cert["enabled"])
	}
	if cert["cert_type"] != "COMPETENCY" { // normalised upper-case
		t.Fatalf("cert.cert_type want COMPETENCY, got %v", cert["cert_type"])
	}
	if cert["passing_score_pct"].(float64) != 80 {
		t.Fatalf("cert.passing_score_pct want 80, got %v", cert["passing_score_pct"])
	}
}

func TestCJ2_PostCourse_RejectsInvalidCert_400(t *testing.T) {
	srv, _, _ := newCJ2Server()
	body := `{
		"title": "Bad cert",
		"learning_objectives": ["LO1"],
		"test_set_ids": ["` + cj2TestTestSetID + `"],
		"certification": {"enabled": true, "cert_type": "DIPLOMA"}
	}`
	rec := doCJ2(t, srv, "POST", "/api/v1/courses", body, cj2TestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cert_type: want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// fakeCertLookup returns a fixed course (with its cert def) for the consume test.
type fakeCertLookup struct {
	course *domain.Course
}

func (f fakeCertLookup) Get(_ context.Context, _, _ string) (*domain.Course, bool, error) {
	if f.course == nil {
		return nil, false, nil
	}
	return f.course, true, nil
}

func issueCertServer(lookup httpapi.CourseCertLookupPort) http.Handler {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	return httpapi.NewServer(httpapi.Deps{
		Courses:          inmem.NewCourseRepo(),
		Bookings:         inmem.NewBookingRepo(),
		Certifications:   domain.NewCertificationRegistry(),
		CourseCertLookup: lookup,
		Catalogue:        domain.NewInMemCatalogue(),
		Enrollments:      domain.NewInMemEnrollmentStore(),
		Publisher:        pub,
	})
}

func issueCert(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/certifications", bytes.NewReader([]byte(body)))
	req.Header.Set("X-Tenant-Id", cj2TestTenantID)
	req.Header.Set("gcid", cj2TestAuthorGCID)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCertIssuance_ConsumesCourseCertType(t *testing.T) {
	course := &domain.Course{
		ID: "019e2f93-d586-71b5-8c3d-e2b0d0d50208", TenantID: cj2TestTenantID,
		Certification: domain.CertDefinition{Enabled: true, CertType: domain.CertTypeCompetency, PassingScorePct: 70},
	}
	srv := issueCertServer(fakeCertLookup{course: course})
	rec := issueCert(t, srv, `{"learner_gcid":"`+cj2TestAuthorGCID+`","course_id":"019e2f93-d586-71b5-8c3d-e2b0d0d50208","accomplishments":["Completed all modules"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("issue: want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	accs, _ := got["accomplishments"].([]any)
	found := false
	for _, a := range accs {
		if a == "cert_type:COMPETENCY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issued cert should consume the course cert_type; accomplishments=%v", accs)
	}
}

func TestCertIssuance_NoLookup_AccomplishmentsUnchanged(t *testing.T) {
	srv := issueCertServer(nil) // no lookup wired
	rec := issueCert(t, srv, `{"learner_gcid":"`+cj2TestAuthorGCID+`","course_id":"019e2f93-d586-71b5-8c3d-e2b0d0d50208","accomplishments":["Only this"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("issue: want 201, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	accs, _ := got["accomplishments"].([]any)
	if len(accs) != 1 || accs[0] != "Only this" {
		t.Fatalf("no lookup ⇒ accomplishments unchanged, got %v", accs)
	}
}
