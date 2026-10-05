// offering_handler_test.go — TDD coverage for the R+ Offering HTTP surface
// (four-mode refactor W1).
//
// Endpoints under test:
//
//	GET  /api/v1/offerings           → list (current tenant)
//	POST /api/v1/offerings           → create DRAFT (training-admin/instructor)
//	GET  /api/v1/offerings/{id}      → fetch one (tenant-scoped 404)
//
// Coverage target: >=60% adapter (per .claude/rules/development-execution.md).
// TDD: written FIRST then drove offering_handler.go.
package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

const (
	offTestTenantID    = "019e2f93-d586-71b5-8c3d-e2b0d0d60100"
	offTestOtherTenant = "019e2f93-d586-71b5-8c3d-e2b0d0d60199"
	offTestAdminGCID   = "019e2f93-d586-71b5-8c3d-e2b0d0d60101"
	offTestLearnerGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d60102"
	offTestCourseID    = "019e2f93-d586-71b5-8c3d-e2b0d0d60200"
)

func newOfferingServer() (http.Handler, *inmem.OfferingRepo) {
	repo := inmem.NewOfferingRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:   inmem.NewCourseRepo(),
		Bookings:  inmem.NewBookingRepo(),
		Offerings: repo,
	})
	return srv, repo
}

func doOffering(t *testing.T, h http.Handler, method, path, body, tenantID, gcid, roles string) *httptest.ResponseRecorder {
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

func createOfferingBody(deliveryType string) string {
	return `{
		"course_id": "` + offTestCourseID + `",
		"delivery_type": "` + deliveryType + `",
		"label": "2026 Spring Cohort",
		"capacity": 30
	}`
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings
// -----------------------------------------------------------------------------

func TestOfferings_Post_AsAdmin_201(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("graduate"), offTestTenantID, offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "DRAFT" {
		t.Errorf("state=%v want DRAFT", got["state"])
	}
	if got["delivery_type"] != "graduate" {
		t.Errorf("delivery_type=%v want graduate", got["delivery_type"])
	}
	if got["tenant_id"] != offTestTenantID {
		t.Errorf("tenant_id=%v want %s", got["tenant_id"], offTestTenantID)
	}
	if got["id"] == nil || got["id"] == "" {
		t.Errorf("expected generated id")
	}
}

func TestOfferings_Post_MultipleCourseIds_201(t *testing.T) {
	srv, _ := newOfferingServer()
	const courseB = "019e2f93-d586-71b5-8c3d-e2b0d0d60201"
	body := `{
		"course_ids": ["` + offTestCourseID + `", "` + courseB + `"],
		"delivery_type": "graduate",
		"label": "Bundle Cohort",
		"capacity": 30
	}`
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		body, offTestTenantID, offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	ids, ok := got["course_ids"].([]interface{})
	if !ok || len(ids) != 2 || ids[0] != offTestCourseID || ids[1] != courseB {
		t.Fatalf("course_ids=%v want [%s %s]", got["course_ids"], offTestCourseID, courseB)
	}
	if got["course_id"] != offTestCourseID {
		t.Errorf("course_id (primary)=%v want %s", got["course_id"], offTestCourseID)
	}
}

func TestOfferings_Post_LegacyCourseId_BackfillsCourseIds(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("short"), offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	ids, ok := got["course_ids"].([]interface{})
	if !ok || len(ids) != 1 || ids[0] != offTestCourseID {
		t.Fatalf("legacy course_id must backfill course_ids=[%s], got %v", offTestCourseID, got["course_ids"])
	}
}

func TestOfferings_Post_NoCourses_400(t *testing.T) {
	srv, _ := newOfferingServer()
	body := `{"course_ids": [], "delivery_type": "graduate", "label": "X", "capacity": 0}`
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		body, offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (at least one course required) body=%s", rec.Code, rec.Body.String())
	}
}

func TestOfferings_Post_AsInstructor_201(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("short"), offTestTenantID, offTestAdminGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
}

func TestOfferings_Post_AsyncDeliveryType_201(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("async"), offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
}

func TestOfferings_Post_NoTenant_400(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("graduate"), "", offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestOfferings_Post_NoGCID_401(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("graduate"), offTestTenantID, "", "training-admin")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestOfferings_Post_AsLearner_403(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("graduate"), offTestTenantID, offTestLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
}

func TestOfferings_Post_InvalidDeliveryType_400(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("weekend"), offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestOfferings_Post_BadJSON_400(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		`{"course_id": "x", "bogus": true`, offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings
// -----------------------------------------------------------------------------

func TestOfferings_List_Empty_200(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "GET", "/api/v1/offerings", "", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, ok := got["items"].([]interface{})
	if !ok || len(items) != 0 {
		t.Fatalf("want empty items array; got %v", got["items"])
	}
}

func TestOfferings_List_ReturnsCreated(t *testing.T) {
	srv, _ := newOfferingServer()
	_ = doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("graduate"), offTestTenantID, offTestAdminGCID, "admin")
	rec := doOffering(t, srv, "GET", "/api/v1/offerings", "", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("want 1 item; got %d", len(items))
	}
}

func TestOfferings_List_CrossTenantIsolated(t *testing.T) {
	srv, _ := newOfferingServer()
	_ = doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("graduate"), offTestTenantID, offTestAdminGCID, "admin")
	rec := doOffering(t, srv, "GET", "/api/v1/offerings", "", offTestOtherTenant, offTestAdminGCID, "admin")
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]interface{})
	if len(items) != 0 {
		t.Fatalf("cross-tenant must see 0 offerings; got %d", len(items))
	}
}

func TestOfferings_List_NoTenant_400(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "GET", "/api/v1/offerings", "", "", offTestAdminGCID, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}
// -----------------------------------------------------------------------------

func TestOfferings_GetByID_200(t *testing.T) {
	srv, _ := newOfferingServer()
	postRec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("graduate"), offTestTenantID, offTestAdminGCID, "admin")
	var created map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &created)
	id, _ := created["id"].(string)

	rec := doOffering(t, srv, "GET", "/api/v1/offerings/"+id, "", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["id"] != id {
		t.Errorf("id=%v want %s", got["id"], id)
	}
}

func TestOfferings_GetByID_NotFound_404(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "GET", "/api/v1/offerings/01970000-0000-7000-9999-fffffffffff0",
		"", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
}

func TestOfferings_GetByID_CrossTenant_404(t *testing.T) {
	srv, _ := newOfferingServer()
	postRec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody("graduate"), offTestTenantID, offTestAdminGCID, "admin")
	var created map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &created)
	id, _ := created["id"].(string)

	rec := doOffering(t, srv, "GET", "/api/v1/offerings/"+id, "", offTestOtherTenant, offTestAdminGCID, "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 (cross-tenant)", rec.Code)
	}
}

func TestOfferings_Delete_405(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := doOffering(t, srv, "DELETE", "/api/v1/offerings", "", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want 405", rec.Code)
	}
}
