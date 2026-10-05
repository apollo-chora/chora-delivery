// offering_certification_handler_test.go — handler-level verification of the
// R+ W2.D offering-nested Certification surface (read-only attached-course
// certification CONFIG): GET /api/v1/offerings/{id}/certification.
//
// Intra-chora_delivery (Offering + CJ#2 Course share the DB per
// ddd-enforcement #3) — NO cross-DB query, NO new aggregate, NO migration. The
// cert config is the CertDefinition VALUE OBJECT carried on the CJ#2 Course
// aggregate (course_cert.go) — config, NOT issuance (no Certification credential,
// no transcript, no consumption). Mirrors offering_curriculum_handler_test.go's
// in-mem harness + reqWithHeaders.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	certOfferingID = "01985e7f-3333-7abc-8def-0000000000d1"
	certCourseA    = "01985e7f-3333-7abc-8def-0000000000e1"
	certCourseB    = "01985e7f-3333-7abc-8def-0000000000e2"
)

// newOfferingCertTestServer wires Offerings + CourseCJ2.Courses with in-mem
// repos so the certification route resolves DB-free. NOTE: unlike curriculum,
// the cert-config read needs only the Course port (no CourseContent service).
func newOfferingCertTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCourseCJ2Store) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
	})
	return srv, oRepo, courseStore
}

// seedCertOffering stores a DRAFT offering over the given courses (order
// preserved) under the standard tenant.
func seedCertOffering(t *testing.T, oRepo *inmem.OfferingRepo, id string, courseIDs ...string) {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    courseIDs,
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "W2.D Certification Run",
		Capacity:     0,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
}

// seedCertCourse stores a CJ#2 Course shell carrying a title + optional cert
// definition (nil cert ⇒ the course awards no certificate).
func seedCertCourse(t *testing.T, store *delivery.InMemCourseCJ2Store, id, title string, cert *delivery.CertDefinition) {
	t.Helper()
	c, err := delivery.NewCJ2Course(delivery.NewCJ2CourseInput{
		TenantID:      tenantID,
		AuthorGCID:    instructor,
		Title:         title,
		TestSetIDs:    []string{testSetID},
		Certification: cert,
	})
	if err != nil {
		t.Fatalf("NewCJ2Course: %v", err)
	}
	c.ID = id
	if err := store.Save(context.Background(), c); err != nil {
		t.Fatalf("Save course: %v", err)
	}
}

// certConfig is the wire shape of one cert-config row.
type certConfig struct {
	Enabled           bool   `json:"enabled"`
	CertType          string `json:"cert_type"`
	PassingScorePct   int    `json:"passing_score_pct"`
	RequireAllContent bool   `json:"require_all_content"`
}

// certCourse is the wire shape of one attached course + its cert config list.
type certCourse struct {
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	Certifications []certConfig `json:"certifications"`
}

// certResp is the wire envelope returned by GET .../certification.
type certResp struct {
	Courses []certCourse `json:"courses"`
}

func getCert(t *testing.T, srv http.Handler, offeringID, role string) (*httptest.ResponseRecorder, certResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+offeringID+"/certification", nil, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp certResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/certification
// -----------------------------------------------------------------------------

func TestOfferingCertification_Get_ReturnsCoursesWithCertConfig(t *testing.T) {
	srv, oRepo, courseStore := newOfferingCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	seedCertCourse(t, courseStore, certCourseA, "Calculus I", &delivery.CertDefinition{
		Enabled:           true,
		CertType:          delivery.CertTypeCompetency,
		PassingScorePct:   70,
		RequireAllContent: true,
	})

	w, resp := getCert(t, srv, certOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 1 {
		t.Fatalf("courses: want 1, got %d body=%s", len(resp.Courses), w.Body.String())
	}
	got := resp.Courses[0]
	if got.ID != certCourseA {
		t.Fatalf("course id: want %s, got %s", certCourseA, got.ID)
	}
	if got.Title != "Calculus I" {
		t.Fatalf("course title: want Calculus I, got %q", got.Title)
	}
	if len(got.Certifications) != 1 {
		t.Fatalf("certifications: want 1, got %d body=%s", len(got.Certifications), w.Body.String())
	}
	cfg := got.Certifications[0]
	if !cfg.Enabled || cfg.CertType != "COMPETENCY" || cfg.PassingScorePct != 70 || !cfg.RequireAllContent {
		t.Fatalf("cert config: want enabled/COMPETENCY/70/true, got %+v", cfg)
	}
}

func TestOfferingCertification_Get_EmptyCertsWhenCourseHasNoCert(t *testing.T) {
	srv, oRepo, courseStore := newOfferingCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	// No cert definition (or a disabled one) ⇒ the course awards no certificate,
	// which is an EMPTY cert list (200, certifications: []), NOT an error.
	seedCertCourse(t, courseStore, certCourseA, "Uncertified Course", nil)

	w, resp := getCert(t, srv, certOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 1 {
		t.Fatalf("courses: want 1, got %d", len(resp.Courses))
	}
	if len(resp.Courses[0].Certifications) != 0 {
		t.Fatalf("certifications: want empty, got %#v", resp.Courses[0].Certifications)
	}
}

func TestOfferingCertification_Get_DisabledCertIsEmptyList(t *testing.T) {
	srv, oRepo, courseStore := newOfferingCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	// An explicitly DISABLED definition is "no cert config applies" → empty list.
	seedCertCourse(t, courseStore, certCourseA, "Disabled Cert", &delivery.CertDefinition{Enabled: false})

	w, resp := getCert(t, srv, certOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses[0].Certifications) != 0 {
		t.Fatalf("certifications: want empty for disabled cert, got %#v", resp.Courses[0].Certifications)
	}
}

func TestOfferingCertification_Get_PreservesCourseOrder(t *testing.T) {
	srv, oRepo, courseStore := newOfferingCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA, certCourseB)
	seedCertCourse(t, courseStore, certCourseA, "First", &delivery.CertDefinition{
		Enabled: true, CertType: delivery.CertTypeCompletion, PassingScorePct: 50,
	})
	seedCertCourse(t, courseStore, certCourseB, "Second", nil)

	w, resp := getCert(t, srv, certOfferingID, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 2 {
		t.Fatalf("courses: want 2, got %d", len(resp.Courses))
	}
	if resp.Courses[0].ID != certCourseA || resp.Courses[1].ID != certCourseB {
		t.Fatalf("order: want [%s,%s], got [%s,%s]", certCourseA, certCourseB, resp.Courses[0].ID, resp.Courses[1].ID)
	}
	if len(resp.Courses[0].Certifications) != 1 || resp.Courses[0].Certifications[0].CertType != "COMPLETION" {
		t.Fatalf("course A cert: want 1 COMPLETION, got %#v", resp.Courses[0].Certifications)
	}
	if len(resp.Courses[1].Certifications) != 0 {
		t.Fatalf("course B cert: want empty, got %#v", resp.Courses[1].Certifications)
	}
}

func TestOfferingCertification_Get_404WhenOfferingMissing(t *testing.T) {
	srv, _, courseStore := newOfferingCertTestServer(t)
	seedCertCourse(t, courseStore, certCourseA, "Orphan", nil) // offering NOT seeded

	w, _ := getCert(t, srv, certOfferingID, "instructor")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCertification_Get_403WithoutRole(t *testing.T) {
	srv, oRepo, _ := newOfferingCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)

	w, _ := getCert(t, srv, certOfferingID, "") // no instructor/admin role
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCertification_Post_405(t *testing.T) {
	srv, oRepo, _ := newOfferingCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)

	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+certOfferingID+"/certification", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/certification/issue — MANUAL per-offering issuance
//
// This is issuance (mints a real Certification credential + emits
// certification.issued.v1), NOT the read-only config above. It reuses the
// WIRED auto-issue engine's repo (Certifications.IssueCtx) + event shape, but
// gives an admin an explicit manual lane per offering. The cert is anchored on
// the offering's PRIMARY course by default, or a provided course_id that MUST
// be one of the offering's attached courses. Admin-gated (closes the gap that
// the legacy POST /api/certifications has NO role gate). Tenant-scoped.
// -----------------------------------------------------------------------------

const certRecipientGCID = "00000000-0000-7000-8000-0000000000c1"

// newOfferingIssueCertTestServer wires Offerings + a Certification registry +
// an in-mem publisher so the manual-issue route resolves DB-free. Unlike the
// read-only cert-config server, this one needs the issuance store + publisher
// (the config read needed the Course port instead).
func newOfferingIssueCertTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.CertificationRegistry, *events.InMemoryPublisher) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	certReg := delivery.NewCertificationRegistry()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:      oRepo,
		Certifications: certReg,
		Publisher:      pub,
	})
	return srv, oRepo, certReg, pub
}

// issueCertResp is the small 201 DTO returned by the manual-issue route.
type issueCertResp struct {
	CertificationID string `json:"certification_id"`
	CourseID        string `json:"course_id"`
	GCID            string `json:"gcid"`
}

// postIssueCert POSTs a manual-issue request (caller = instructor identity) and
// returns the recorder + decoded DTO.
func postIssueCert(t *testing.T, srv http.Handler, offeringID, body, role string) (*httptest.ResponseRecorder, issueCertResp) {
	t.Helper()
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringID+"/certification/issue", []byte(body), instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp issueCertResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

func TestOfferingIssueCertification_201_HappyPath(t *testing.T) {
	srv, oRepo, _, pub := newOfferingIssueCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)

	w, resp := postIssueCert(t, srv, certOfferingID, `{"learner_gcid":"`+certRecipientGCID+`"}`, "instructor")
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.CertificationID == "" {
		t.Fatalf("certification_id: want non-empty, got empty (body=%s)", w.Body.String())
	}
	// Anchored on the offering's PRIMARY course when no course_id is given.
	if resp.CourseID != certCourseA {
		t.Fatalf("course_id: want %s (primary), got %s", certCourseA, resp.CourseID)
	}
	if resp.GCID != certRecipientGCID {
		t.Fatalf("gcid: want %s, got %s", certRecipientGCID, resp.GCID)
	}
	// certification.issued.v1 emitted (same publisher/shape as the manual handler).
	found := false
	for _, ev := range pub.History() {
		if ev.Topic == events.TopicCertificationIssued {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected certification.issued.v1 in publisher history, got %d events", len(pub.History()))
	}
}

func TestOfferingIssueCertification_201_ProvidedAttachedCourse(t *testing.T) {
	srv, oRepo, _, _ := newOfferingIssueCertTestServer(t)
	// Offering bundles two courses; a provided course_id ∈ CourseIDs anchors there.
	seedCertOffering(t, oRepo, certOfferingID, certCourseA, certCourseB)

	w, resp := postIssueCert(t, srv, certOfferingID,
		`{"learner_gcid":"`+certRecipientGCID+`","course_id":"`+certCourseB+`"}`, "instructor")
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	if resp.CourseID != certCourseB {
		t.Fatalf("course_id: want %s (provided), got %s", certCourseB, resp.CourseID)
	}
}

func TestOfferingIssueCertification_403_WithoutRole(t *testing.T) {
	srv, oRepo, _, _ := newOfferingIssueCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)

	// A learner-role caller has identity but NOT an offering-admin role.
	w, _ := postIssueCert(t, srv, certOfferingID, `{"learner_gcid":"`+certRecipientGCID+`"}`, "learner")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingIssueCertification_409_Duplicate(t *testing.T) {
	srv, oRepo, certReg, _ := newOfferingIssueCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	// Pre-issue (course, learner) so the manual issue is a duplicate.
	if _, err := certReg.IssueCtx(context.Background(), tenantID, certRecipientGCID, certCourseA, nil, nil); err != nil {
		t.Fatalf("pre-issue: %v", err)
	}

	w, _ := postIssueCert(t, srv, certOfferingID, `{"learner_gcid":"`+certRecipientGCID+`"}`, "instructor")
	if w.Code != http.StatusConflict {
		t.Fatalf("status: want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingIssueCertification_400_CourseNotInOffering(t *testing.T) {
	srv, oRepo, _, _ := newOfferingIssueCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA) // only certCourseA attached

	// certCourseB is a valid UUID but NOT one of the offering's attached courses.
	w, _ := postIssueCert(t, srv, certOfferingID,
		`{"learner_gcid":"`+certRecipientGCID+`","course_id":"`+certCourseB+`"}`, "instructor")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingIssueCertification_400_MissingLearnerGCID(t *testing.T) {
	srv, oRepo, _, _ := newOfferingIssueCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)

	w, _ := postIssueCert(t, srv, certOfferingID, `{}`, "instructor")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingIssueCertification_404_MissingOffering(t *testing.T) {
	srv, _, _, _ := newOfferingIssueCertTestServer(t) // offering NOT seeded

	w, _ := postIssueCert(t, srv, certOfferingID, `{"learner_gcid":"`+certRecipientGCID+`"}`, "instructor")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingIssueCertification_405_OnGet(t *testing.T) {
	srv, oRepo, _, _ := newOfferingIssueCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)

	// The sub-path is POST-only; a GET must 405 (not fall through to 404).
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+certOfferingID+"/certification/issue", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d body=%s", w.Code, w.Body.String())
	}
}
