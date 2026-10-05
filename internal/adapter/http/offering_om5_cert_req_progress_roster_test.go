// offering_om5_cert_req_progress_roster_test.go — statement-coverage battery
// (TestOm5*) for the remaining offering-nested surfaces:
//
//	offering_certification_handler.go        (GET /certification, PATCH
//	                                         /certification, POST
//	                                         /certification/issue)
//	offering_completion_requirement_handler.go (PATCH /completion-requirement)
//	offering_module_progress_handler.go      (GET /modules/progress)
//	offering_roster_handler.go               (GET/POST /roster, POST /roster/remove)
//
// Drives the wiring 503s + repo-error 500s the sibling happy-path suites leave
// out, using the om5 failing doubles from offering_om5_fakes_test.go.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/module"
	"github.com/apollo-chora/chora-delivery/internal/domain/moduleprogress"
)

// om5PatchRequirement PATCHes /completion-requirement on the given offering
// (unlike the fixed-id patchRequirement helper in the CHO-2222 suite).
func om5PatchRequirement(t *testing.T, srv http.Handler, offeringID, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+offeringID+"/completion-requirement", b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// om5SeedModule persists a one-item course module under the standard tenant.
func om5SeedModule(t *testing.T, modStore module.ModulePort) {
	t.Helper()
	m, err := module.New(module.NewParams{TenantID: tenantID, CourseID: curCourseA, Title: "om5 M"})
	if err != nil {
		t.Fatalf("module.New: %v", err)
	}
	if _, err := m.AddItem(mpCID); err != nil {
		t.Fatalf("module.AddItem: %v", err)
	}
	if _, err := modStore.Create(context.Background(), m); err != nil {
		t.Fatalf("modStore.Create: %v", err)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/certification — wiring + repo-error branches
// -----------------------------------------------------------------------------

func TestOm5Cert_Get_503WhenOfferingsUnwired(t *testing.T) {
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore}})
	w, _ := getCert(t, srv, certOfferingID, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Cert_Get_503WhenCourseCJ2Unwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // CourseCJ2 nil
	w, _ := getCert(t, srv, certOfferingID, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (course port unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Cert_Get_500OnCourseLookup(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: &om5ErrCourseStore{failGet: true}},
	})
	w, _ := getCert(t, srv, certOfferingID, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (course lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// The GET bundles the completed::policy + the DECLARED requirement (CHO-2222)
// in one round-trip — both must render after their PATCHes.
func TestOm5Cert_Get_IncludesPolicyAndRequirement(t *testing.T) {
	srv, oRepo, courseStore := newOfferingCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	seedCertCourse(t, courseStore, certCourseA, "Course", &delivery.CertDefinition{
		Enabled: true, CertType: delivery.CertTypeCompletion, PassingScorePct: 70,
	})
	if w := patchCertification(t, srv, certOfferingID, "instructor", map[string]any{
		"awards_certificate": true, "passing_score_pct": 70,
	}); w.Code != http.StatusOK {
		t.Fatalf("policy PATCH: %d %s", w.Code, w.Body.String())
	}
	if w := om5PatchRequirement(t, srv, certOfferingID, "instructor", map[string]any{
		"components": []map[string]any{{"kind": "assessment", "ref": reqRefAssessment}},
	}); w.Code != http.StatusOK {
		t.Fatalf("requirement PATCH: %d %s", w.Code, w.Body.String())
	}

	w, resp := getCert(t, srv, certOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 1 || len(resp.Courses[0].Certifications) != 1 {
		t.Fatalf("courses/certs: want 1 course + 1 enabled cert, got %+v", resp.Courses)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"completion_policy"`) {
		t.Fatalf("GET must render completion_policy after PATCH: %s", body)
	}
	if !strings.Contains(body, `"completion_requirement"`) {
		t.Fatalf("GET must render completion_requirement after PATCH (CHO-2222): %s", body)
	}
}

// -----------------------------------------------------------------------------
// PATCH /api/v1/offerings/{id}/certification — wiring + guard branches
// -----------------------------------------------------------------------------

func TestOm5Cert_SetPolicy_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{}) // Offerings nil
	w := patchCertification(t, srv, certOfferingID, "instructor", map[string]any{"awards_certificate": true})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Cert_SetPolicy_400MalformedBody(t *testing.T) {
	srv, oRepo, _ := newOfferingCertTestServer(t)
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	r := reqWithHeaders(http.MethodPatch, "/api/v1/offerings/"+certOfferingID+"/certification", []byte("{nope"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 (malformed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Cert_SetPolicy_500OnSaveError(t *testing.T) {
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{certCourseA},
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "Save-boom run",
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = certOfferingID
	srv := httpapi.NewServer(httpapi.Deps{Offerings: &om5ErrOfferingRepo{stored: o, failSave: true}})
	w := patchCertification(t, srv, certOfferingID, "instructor", map[string]any{
		"awards_certificate": true, "passing_score_pct": 70,
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (policy save failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/certification/issue — wiring + error branches
// -----------------------------------------------------------------------------

func TestOm5Issue_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{Certifications: delivery.NewCertificationRegistry()})
	w, _ := postIssueCert(t, srv, certOfferingID, `{"learner_gcid":"`+certRecipientGCID+`"}`, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Issue_503WhenCertificationsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // Certifications nil
	w, _ := postIssueCert(t, srv, certOfferingID, `{"learner_gcid":"`+certRecipientGCID+`"}`, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (certifications unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Issue_500OnIssueError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Certifications: &om5ErrCertStore{}})
	w, _ := postIssueCert(t, srv, certOfferingID, `{"learner_gcid":"`+certRecipientGCID+`"}`, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (issue failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Issue_400MalformedBody(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Certifications: delivery.NewCertificationRegistry()})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+certOfferingID+"/certification/issue", []byte("{nope"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 (malformed), got %d body=%s", w.Code, w.Body.String())
	}
}

// Publisher is nil-tolerant: a 201 must still be written when the emission
// lane is unwired (the event is a projection concern, not the write's result).
func TestOm5Issue_201WhenPublisherUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCertOffering(t, oRepo, certOfferingID, certCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Certifications: delivery.NewCertificationRegistry()})
	w, resp := postIssueCert(t, srv, certOfferingID, `{"learner_gcid":"`+certRecipientGCID+`"}`, "instructor")
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201 (publisher unwired), got %d body=%s", w.Code, w.Body.String())
	}
	if resp.CertificationID == "" {
		t.Fatalf("certification_id missing: %s", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// PATCH /api/v1/offerings/{id}/completion-requirement — save-error branch
// -----------------------------------------------------------------------------

func TestOm5Req_Set_500OnSaveError(t *testing.T) {
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{curCourseA},
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "Req save-boom run",
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = curOfferingID
	srv := httpapi.NewServer(httpapi.Deps{Offerings: &om5ErrOfferingRepo{stored: o, failSave: true}})
	w := om5PatchRequirement(t, srv, curOfferingID, "instructor", map[string]any{
		"components": []map[string]any{{"kind": "assessment", "ref": reqRefAssessment}},
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (requirement save failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/modules/progress — wiring + repo-error branches
// -----------------------------------------------------------------------------

func TestOm5ModProgress_503WhenOfferingsUnwired(t *testing.T) {
	srv := om5ProgressServer(nil, module.NewInMemModuleStore(), moduleprogress.NewInMemProgressStore(), delivery.NewInMemEnrollmentStore())
	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5ModProgress_503WhenModulesUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	srv := om5ProgressServer(oRepo, nil, moduleprogress.NewInMemProgressStore(), delivery.NewInMemEnrollmentStore())
	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (modules unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5ModProgress_503WhenModuleProgressUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	srv := om5ProgressServer(oRepo, module.NewInMemModuleStore(), nil, delivery.NewInMemEnrollmentStore())
	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (module progress unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5ModProgress_404WhenOfferingMissing(t *testing.T) {
	srv := om5ProgressServer(inmem.NewOfferingRepo(), module.NewInMemModuleStore(), moduleprogress.NewInMemProgressStore(), delivery.NewInMemEnrollmentStore())
	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 (offering missing), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5ModProgress_400CourseNotAttached(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	srv := om5ProgressServer(oRepo, module.NewInMemModuleStore(), moduleprogress.NewInMemProgressStore(), delivery.NewInMemEnrollmentStore())
	w := getProgress(t, srv, instructor, "instructor", "?course_id=not-attached")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 (course not attached), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5ModProgress_503WhenEnrollmentsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	srv := om5ProgressServer(oRepo, module.NewInMemModuleStore(), moduleprogress.NewInMemProgressStore(), nil) // Enrollments nil
	w := getProgress(t, srv, learner, "learner", "?course_id="+curCourseA)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (enrollments unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5ModProgress_500OnEnrollmentError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	srv := om5ProgressServer(oRepo, module.NewInMemModuleStore(), moduleprogress.NewInMemProgressStore(), &om5ErrEnrollStore{InMemEnrollmentStore: delivery.NewInMemEnrollmentStore(), failGet: true})
	w := getProgress(t, srv, learner, "learner", "?course_id="+curCourseA)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (enrollment lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5ModProgress_500OnModuleError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	mods := &om5ErrModuleStore{InMemModuleStore: module.NewInMemModuleStore(), failList: true}
	srv := om5ProgressServer(oRepo, mods, moduleprogress.NewInMemProgressStore(), delivery.NewInMemEnrollmentStore())
	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (module list failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5ModProgress_500OnProgressError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	mods := module.NewInMemModuleStore()
	om5SeedModule(t, mods)
	srv := om5ProgressServer(oRepo, mods, &om5ErrModuleProgressPort{}, delivery.NewInMemEnrollmentStore())
	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (progress read failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// An instructor may narrow the cohort view to ONE learner via ?gcid= — the
// single-learner branch then carries completed_count / completed_at.
func TestOm5ModProgress_Instructor_NarrowToGCID(t *testing.T) {
	srv, _ := seedProgressReadServer(t) // learner advanced 1/1 ⇒ CompletedAt set
	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA+"&gcid="+learner)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ViewerRole string `json:"viewer_role"`
		Modules    []struct {
			GCID       string `json:"gcid"`
			Completed  int    `json:"completed_count"`
			IsComplete bool   `json:"is_complete"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if resp.ViewerRole != "instructor" || len(resp.Modules) != 1 {
		t.Fatalf("want instructor single-module view, got %s / %d", resp.ViewerRole, len(resp.Modules))
	}
	m := resp.Modules[0]
	if m.GCID != learner || m.Completed != 1 || !m.IsComplete {
		t.Fatalf("narrowed row mismatch: %+v", m)
	}
	if !strings.Contains(w.Body.String(), `"completed_at"`) {
		t.Fatalf("instructor-narrowed view must render completed_at for a completed module: %s", w.Body.String())
	}
}

// A half-done module (CompletedAt nil) is honest: 1/2, not complete, NO
// completed_at on the wire.
func TestOm5ModProgress_Learner_PartialInflightOmitsCompletedAt(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	modStore := module.NewInMemModuleStore()
	progStore := moduleprogress.NewInMemProgressStore()
	enroll := delivery.NewInMemEnrollmentStore()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	ctx := context.Background()
	m, err := module.New(module.NewParams{TenantID: tenantID, CourseID: curCourseA, Title: "Partial"})
	if err != nil {
		t.Fatalf("module.New: %v", err)
	}
	i1, err := m.AddItem(mpCID)
	if err != nil {
		t.Fatalf("AddItem 1: %v", err)
	}
	if _, err := m.AddItem("01970000-0000-7000-9000-0000000000a2"); err != nil {
		t.Fatalf("AddItem 2: %v", err)
	}
	if _, err := modStore.Create(ctx, m); err != nil {
		t.Fatalf("modStore.Create: %v", err)
	}
	if _, err := enroll.Register(ctx, tenantID, curCourseA, learner); err != nil {
		t.Fatalf("enroll.Register: %v", err)
	}
	if _, err := progStore.Advance(ctx, tenantID, learner, m.ID, curCourseA, i1.ContentItemID, m); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Modules: modStore, ModuleProgress: progStore, Enrollments: enroll})

	w := getProgress(t, srv, learner, "learner", "?course_id="+curCourseA)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"completed_count":1`) || !strings.Contains(body, `"is_complete":false`) {
		t.Fatalf("want 1/2 in-flight learner row, got %s", body)
	}
	if strings.Contains(body, `"completed_at"`) {
		t.Fatalf("an in-flight module must NOT render completed_at: %s", body)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/roster — wiring + repo-error branches
// -----------------------------------------------------------------------------

func TestOm5Enroll_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{Enrollments: delivery.NewInMemEnrollmentStore()})
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Enroll_503WhenEnrollmentsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // Enrollments nil
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (enrollments unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Enroll_400MalformedBody(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: delivery.NewInMemEnrollmentStore()})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+enrollOfferingID+"/roster", []byte("{nope"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 (malformed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Enroll_500OnLookupError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: &om5ErrEnrollStore{InMemEnrollmentStore: delivery.NewInMemEnrollmentStore(), failGet: true}})
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (enrolment lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Enroll_500OnCountError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 5) // bounded capacity ⇒ count gate runs
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: &om5ErrEnrollStore{InMemEnrollmentStore: delivery.NewInMemEnrollmentStore(), failCount: true}})
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (enrolment count failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Enroll_400OnRegisterError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: &om5ErrEnrollStore{InMemEnrollmentStore: delivery.NewInMemEnrollmentStore(), failRegister: true}})
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 (register refused), got %d body=%s", w.Code, w.Body.String())
	}
}

// CHO-2152 — a publish failure must NOT fail the request (the row is already
// committed); the handler loud-logs and still writes 201.
func TestOm5Enroll_201WhenPublishFails(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	pub := &om5FailPublisher{InMemoryPublisher: events.NewInMemoryPublisher("chora-489812", "chora-delivery")}
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: delivery.NewInMemEnrollmentStore(), Publisher: pub})
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201 despite publish error, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/roster/remove — wiring + repo-error branches
// -----------------------------------------------------------------------------

func TestOm5Remove_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{Enrollments: delivery.NewInMemEnrollmentStore()})
	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Remove_503WhenEnrollmentsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // Enrollments nil
	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (enrollments unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Remove_500OnLookupError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: &om5ErrEnrollStore{InMemEnrollmentStore: delivery.NewInMemEnrollmentStore(), failGet: true}})
	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (enrolment lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Remove_500OnCancelError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	enroll := &om5ErrEnrollStore{InMemEnrollmentStore: delivery.NewInMemEnrollmentStore(), failCancel: true}
	if _, err := enroll.Register(context.Background(), tenantID, enrollCourseA, enrollLearner1); err != nil {
		t.Fatalf("seed enrol: %v", err)
	}
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: enroll})
	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (enrolment cancel failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Remove_204WhenPublisherUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	enroll := delivery.NewInMemEnrollmentStore()
	if _, err := enroll.Register(context.Background(), tenantID, enrollCourseA, enrollLearner1); err != nil {
		t.Fatalf("seed enrol: %v", err)
	}
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: enroll}) // Publisher nil
	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204 (publisher unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/roster — wiring + repo-error branches
// -----------------------------------------------------------------------------

func TestOm5Roster_Get_503WhenOfferingsUnwired(t *testing.T) {
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
		Rosters:   inmem.NewCourseRosterRepo(delivery.NewInMemEnrollmentStore()),
	})
	w, _ := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Roster_Get_503WhenCourseCJ2Unwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		Rosters:   inmem.NewCourseRosterRepo(delivery.NewInMemEnrollmentStore()),
	}) // CourseCJ2 nil
	w, _ := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (course port unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Roster_Get_500OnCourseLookup(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: &om5ErrCourseStore{failGet: true}},
		Rosters:   inmem.NewCourseRosterRepo(delivery.NewInMemEnrollmentStore()),
	})
	w, _ := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (course lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Roster_Get_500OnRosterError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedOfferingRoster(t, oRepo, orostOfferingID, tenantID, orostCourseA)
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
		Rosters:   &om5ErrRosterRepo{},
	})
	w, _ := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (roster lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Offering-lookup 500s on the WRITE paths (the read-side Get-error branches
// are covered above; each WRITE handler has its own Get call to trip).
// -----------------------------------------------------------------------------

func TestOm5Cert_Get_500OnOfferingLookup(t *testing.T) {
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: &om5ErrOfferingRepo{failGet: true},
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
	})
	w, _ := getCert(t, srv, certOfferingID, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (GET offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Cert_SetPolicy_500OnOfferingLookup(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{Offerings: &om5ErrOfferingRepo{failGet: true}})
	w := patchCertification(t, srv, certOfferingID, "instructor", map[string]any{"awards_certificate": true})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (PATCH offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Issue_500OnOfferingLookup(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:      &om5ErrOfferingRepo{failGet: true},
		Certifications: delivery.NewCertificationRegistry(),
	})
	w, _ := postIssueCert(t, srv, certOfferingID, `{"learner_gcid":"`+certRecipientGCID+`"}`, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (issue offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Req_Set_500OnOfferingLookup(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{Offerings: &om5ErrOfferingRepo{failGet: true}})
	w := om5PatchRequirement(t, srv, curOfferingID, "instructor", map[string]any{
		"components": []map[string]any{{"kind": "assessment", "ref": reqRefAssessment}},
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (requirement offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5ModProgress_500OnOfferingLookup(t *testing.T) {
	srv := om5ProgressServer(&om5ErrOfferingRepo{failGet: true}, module.NewInMemModuleStore(), moduleprogress.NewInMemProgressStore(), delivery.NewInMemEnrollmentStore())
	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// The cohort viewer sorts learners by GCID — a single-learner cohort never
// forces a comparator comparison, so two learners exercise the sort body.
func TestOm5ModProgress_Instructor_CohortSortsLearners(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	modStore := module.NewInMemModuleStore()
	progStore := moduleprogress.NewInMemProgressStore()
	enroll := delivery.NewInMemEnrollmentStore()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	ctx := context.Background()
	m, err := module.New(module.NewParams{TenantID: tenantID, CourseID: curCourseA, Title: "Cohort"})
	if err != nil {
		t.Fatalf("module.New: %v", err)
	}
	item, err := m.AddItem(mpCID)
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if _, err := modStore.Create(ctx, m); err != nil {
		t.Fatalf("modStore.Create: %v", err)
	}
	// Two learners, both advanced on the same module; IDs chosen so the sort
	// comparator has to reorder (learner > other lexicographically? no — the
	// sort is ascending by GCID, so feed them out of order is irrelevant; the
	// point is TWO rows so Slice actually compares).
	for _, g := range []string{learner, gcidB} {
		if _, err := enroll.Register(ctx, tenantID, curCourseA, g); err != nil {
			t.Fatalf("enroll.Register(%s): %v", g, err)
		}
		if _, err := progStore.Advance(ctx, tenantID, g, m.ID, curCourseA, item.ContentItemID, m); err != nil {
			t.Fatalf("Advance(%s): %v", g, err)
		}
	}
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Modules: modStore, ModuleProgress: progStore, Enrollments: enroll})

	w := getProgress(t, srv, instructor, "instructor", "?course_id="+curCourseA)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"learners"`) || !strings.Contains(body, `"`+gcidB+`"`) || !strings.Contains(body, `"`+learner+`"`) {
		t.Fatalf("cohort must render both learners, got %s", body)
	}
}

func TestOm5Roster_Get_500OnOfferingLookup(t *testing.T) {
	courseStore := delivery.NewInMemCourseCJ2Store()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: &om5ErrOfferingRepo{failGet: true},
		CourseCJ2: &httpapi.CourseCJ2Deps{Courses: courseStore},
		Rosters:   inmem.NewCourseRosterRepo(delivery.NewInMemEnrollmentStore()),
	})
	w, _ := getOfferingRoster(t, srv, orostOfferingID, instructor, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Enroll_500OnOfferingLookup(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:   &om5ErrOfferingRepo{failGet: true},
		Enrollments: delivery.NewInMemEnrollmentStore(),
	})
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (enrol offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Remove_500OnOfferingLookup(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:   &om5ErrOfferingRepo{failGet: true},
		Enrollments: delivery.NewInMemEnrollmentStore(),
	})

	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (remove offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Remove_400MalformedBody(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: delivery.NewInMemEnrollmentStore()})
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+enrollOfferingID+"/roster/remove", []byte("{nope"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 (malformed), got %d body=%s", w.Code, w.Body.String())
	}
}
