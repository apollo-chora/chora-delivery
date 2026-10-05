// exam_handler_test.go — TDD coverage for the R+ Exam HTTP surface.
//
// Endpoints under test:
//
//	GET  /api/v1/exams              → list (current tenant)
//	POST /api/v1/exams              → create DRAFT (training-admin/instructor)
//	GET  /api/v1/exams/{id}         → fetch one
//
// Coverage target: >=60% adapter (per .claude/rules/development-execution.md).
//
// TDD: written FIRST then drove exam_handler.go.
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

const (
	examTestTenantID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	examTestOtherTenant = "019e2f93-d586-71b5-8c3d-e2b0d0d50199"
	examTestAdminGCID   = "019e2f93-d586-71b5-8c3d-e2b0d0d50101"
	examTestLearnerGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d50102"
	examTestCourseID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50200"
)

// newExamServer wires the Exam routes against a fresh in-mem repo per test.
// The exam routes only need Deps.Exams + the standard repos NewServer expects;
// the rest stay as default in-mem so the rest of the mux still mounts.
func newExamServer() (http.Handler, *inmem.ExamRepo) {
	repo := inmem.NewExamRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:  inmem.NewCourseRepo(),
		Bookings: inmem.NewBookingRepo(),
		Exams:    repo,
	})
	return srv, repo
}

// doExam issues a request with the standard headers for exam routes.
func doExam(t *testing.T, h http.Handler, method, path, body, tenantID, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if tenantID != "" {
		req.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		req.Header.Set("gcid", gcid)
	}
	if roles != "" {
		req.Header.Set("x-mesh-user-roles", roles)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// createDefaultExamBody returns a minimal valid create-exam JSON body.
func createDefaultExamBody() string {
	return `{
		"course_id": "` + examTestCourseID + `",
		"title": "Certified Scrum Product Owner",
		"scheduled_at": "2026-06-12T09:00:00Z",
		"duration_minutes": 120,
		"capacity": 30,
		"proctor_method": "PROCTOR_METHOD_AUTO_AI"
	}`
}

// -----------------------------------------------------------------------------
// POST /api/v1/exams
// -----------------------------------------------------------------------------

func TestExams_PostExam_AsAdmin_201(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "DRAFT" {
		t.Errorf("state=%v want DRAFT", got["state"])
	}
	if got["tenant_id"] != examTestTenantID {
		t.Errorf("tenant_id=%v want %s", got["tenant_id"], examTestTenantID)
	}
	if got["course_id"] != examTestCourseID {
		t.Errorf("course_id=%v", got["course_id"])
	}
	if got["title"] != "Certified Scrum Product Owner" {
		t.Errorf("title=%v", got["title"])
	}
	if got["proctor_method"] != "PROCTOR_METHOD_AUTO_AI" {
		t.Errorf("proctor_method=%v", got["proctor_method"])
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Errorf("id missing")
	}
}

func TestExams_PostExam_AsInstructor_201(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
}

func TestExams_PostExam_NoTenantHeader_400(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), "", examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestExams_PostExam_NoGCID_401(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, "", "training-admin")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestExams_PostExam_AsLearner_403(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestExams_PostExam_MissingTitle_400(t *testing.T) {
	srv, _ := newExamServer()
	body := `{
		"course_id": "` + examTestCourseID + `",
		"title": "",
		"scheduled_at": "2026-06-12T09:00:00Z",
		"duration_minutes": 120,
		"capacity": 30,
		"proctor_method": "PROCTOR_METHOD_AUTO_AI"
	}`
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestExams_PostExam_InvalidProctorMethod_400(t *testing.T) {
	srv, _ := newExamServer()
	body := `{
		"course_id": "` + examTestCourseID + `",
		"title": "T",
		"scheduled_at": "2026-06-12T09:00:00Z",
		"duration_minutes": 60,
		"capacity": 10,
		"proctor_method": "PROCTOR_METHOD_TELEPATHY"
	}`
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestExams_PostExam_BadJSON_400(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		`{not-valid`, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// TestExams_PostExam_NonUUIDCourseID_400 - the live R+ EXAM cert-rollup finding,
// pinned at the surface it entered through. "course-cspo" was accepted here as a
// 201, sat inertly in the row until the candidate PASSED, and only THEN became a
// 22P02 that killed cert issuance inside a Pub/Sub subscriber - about as far from
// the operator who typed it as a failure can travel. It is now a 400 at the point
// of entry, naming the field so the admin can fix it while they are still looking
// at the form.
func TestExams_PostExam_NonUUIDCourseID_400(t *testing.T) {
	srv, repo := newExamServer()
	body := `{
		"course_id": "course-cspo",
		"title": "Certified Scrum Product Owner",
		"scheduled_at": "2026-06-12T09:00:00Z",
		"duration_minutes": 120,
		"capacity": 30,
		"proctor_method": "PROCTOR_METHOD_AUTO_AI"
	}`
	rec := doExam(t, srv, "POST", "/api/v1/exams",
		body, examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	msg, _ := got["message"].(string)
	if !strings.Contains(msg, "course_id") {
		t.Errorf("message=%q must name the offending field (course_id)", msg)
	}
	// A rejected exam must not reach the store: the whole point is that the bad
	// value never becomes a row that detonates a fortnight later.
	items, err := repo.ListByTenant(tracing.WithTenantID(context.Background(), examTestTenantID), examTestTenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("a 400 must persist nothing; found %d exam(s)", len(items))
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/exams (list)
// -----------------------------------------------------------------------------

func TestExams_ListExams_EmptyTenant_OK(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "GET", "/api/v1/exams",
		"", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, ok := got["items"].([]interface{})
	if !ok {
		t.Fatalf("items missing or wrong type: %v", got)
	}
	if len(items) != 0 {
		t.Errorf("len(items)=%d want 0", len(items))
	}
}

func TestExams_ListExams_ReturnsCreatedRows(t *testing.T) {
	srv, _ := newExamServer()
	// Seed two exams via POST so the test exercises end-to-end.
	_ = doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	body2 := `{
		"course_id": "` + examTestCourseID + `",
		"title": "DSA-101 Cert",
		"scheduled_at": "2026-06-18T09:00:00Z",
		"duration_minutes": 90,
		"capacity": 25,
		"proctor_method": "PROCTOR_METHOD_HUMAN_LIVE"
	}`
	_ = doExam(t, srv, "POST", "/api/v1/exams",
		body2, examTestTenantID, examTestAdminGCID, "training-admin")

	rec := doExam(t, srv, "GET", "/api/v1/exams",
		"", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("len(items)=%d want 2 body=%s", len(items), rec.Body.String())
	}
}

func TestExams_ListExams_ScopesByTenant(t *testing.T) {
	srv, _ := newExamServer()
	// One exam under tenant A.
	_ = doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	// List from tenant B should see zero rows.
	rec := doExam(t, srv, "GET", "/api/v1/exams",
		"", examTestOtherTenant, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("cross-tenant leak: len(items)=%d want 0", len(items))
	}
}

func TestExams_ListExams_NoTenantHeader_400(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "GET", "/api/v1/exams",
		"", "", examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/exams/{id}
// -----------------------------------------------------------------------------

func TestExams_GetByID_OK(t *testing.T) {
	srv, _ := newExamServer()
	createRec := doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	if createRec.Code != http.StatusCreated {
		t.Fatalf("setup POST: %d body=%s", createRec.Code, createRec.Body.String())
	}
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	rec := doExam(t, srv, "GET", "/api/v1/exams/"+id,
		"", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["id"] != id {
		t.Errorf("id=%v want %s", got["id"], id)
	}
	if got["state"] != "DRAFT" {
		t.Errorf("state=%v want DRAFT", got["state"])
	}
}

func TestExams_GetByID_NotFound_404(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "GET", "/api/v1/exams/does-not-exist",
		"", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestExams_GetByID_CrossTenant_404(t *testing.T) {
	srv, _ := newExamServer()
	createRec := doExam(t, srv, "POST", "/api/v1/exams",
		createDefaultExamBody(), examTestTenantID, examTestAdminGCID, "training-admin")
	var created map[string]interface{}
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	id := created["id"].(string)

	// Caller from tenant B must NOT see the exam.
	rec := doExam(t, srv, "GET", "/api/v1/exams/"+id,
		"", examTestOtherTenant, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (cross-tenant isolation)", rec.Code)
	}
}

func TestExams_GetByID_NoTenantHeader_400(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "GET", "/api/v1/exams/anything",
		"", "", examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Method dispatch
// -----------------------------------------------------------------------------

func TestExams_Root_DeleteNotAllowed(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "DELETE", "/api/v1/exams",
		"", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}

func TestExams_Sub_DeleteNotAllowed(t *testing.T) {
	srv, _ := newExamServer()
	rec := doExam(t, srv, "DELETE", "/api/v1/exams/anything",
		"", examTestTenantID, examTestAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}
