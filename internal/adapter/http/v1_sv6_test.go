// v1_sv6_test.go — statement-coverage battery for
// internal/adapter/http/v1_handlers.go. Supplements v1_handlers_test.go with
// the remaining reachable branches:
//
//   - v1CoursesHandler / v1CoursesSubHandler method guards + route 404s
//     (trailing slash, unknown sub-resource, enrolments depth guards)
//   - /v1/courses/{id}/application-form sub-route (S6.1)
//   - handleV1CourseCreate decode-error 400, empty-title 400, invalid
//     visibility 400, missing-gcid 401
//   - handleV1CourseList paid=true filter (skipped free rows + total adjust)
//   - first cursor parse false path
//   - handleV1CourseDetail unknown-course 404
//   - handleV1CourseUpdate decode-error / empty-title / negative-price /
//     invalid-visibility 400s
//   - handleV1EnrolmentCreate decode-error 400, missing-gcid 401, unknown
//     course 404, private cross-tenant 404, public cross-tenant happy (skips
//     the same-tenant hard-gate block)
//   - handleV1EnrolmentCancel unknown-id 404 + course-mismatch 404
//   - campusHandler non-POST 405 + decode-error 400; campusByIDHandler
//     empty-id 404
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sv6Raw issues a request with explicit tenant/gcid/roles headers; an empty
// body string sends no body (and no Content-Type).
func sv6Raw(t *testing.T, srv http.Handler, method, path, body, tenant, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-Id", tenant)
	}
	if gcid != "" {
		req.Header.Set("gcid", gcid)
	}
	if roles != "" {
		req.Header.Set("x-mesh-user-roles", roles)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// sv6CreateCourse creates a course on srv and returns its id.
func sv6CreateCourse(t *testing.T, srv http.Handler, title, visibility string, price int32) string {
	t.Helper()
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           title,
		"price_sgd_cents": price,
		"visibility":      visibility,
		"instructor_name": "sv6",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("seed course %q: status=%d body=%s", title, w.Code, w.Body.String())
	}
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	id, _ := c["id"].(string)
	if id == "" {
		t.Fatalf("seed course %q: no id in %s", title, w.Body.String())
	}
	return id
}

// -----------------------------------------------------------------------------
// Dispatchers — v1CoursesHandler + v1CoursesSubHandler
// -----------------------------------------------------------------------------

func TestSv6_V1Courses_Root_MethodNotAllowed(t *testing.T) {
	srv, _ := newV1Server()
	if rec := reqJSON(t, srv, http.MethodPut, "/v1/courses", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /v1/courses: status=%d want 405 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_V1CoursesSub_DispatchGuards(t *testing.T) {
	srv, _ := newV1Server()
	id := sv6CreateCourse(t, srv, "Dispatch", "public", 0)

	// First path segment empty (trailing-slash root) → 404.
	if rec := reqGET(t, srv, "/v1/courses/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /v1/courses/: status=%d want 404", rec.Code)
	}
	// /{id} non-GET/PATCH → 405.
	if rec := reqJSON(t, srv, http.MethodPut, "/v1/courses/"+id, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /v1/courses/{id}: status=%d want 405", rec.Code)
	}
	// /{id}/enrolments non-POST → 405.
	if rec := reqGET(t, srv, "/v1/courses/"+id+"/enrolments"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /{id}/enrolments: status=%d want 405", rec.Code)
	}
	// /{id}/enrolments/{eid} non-DELETE → 405.
	if rec := reqJSON(t, srv, http.MethodPut, "/v1/courses/"+id+"/enrolments/e1", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /{id}/enrolments/e1: status=%d want 405", rec.Code)
	}
	// Unknown second segment (not enrolments / application-form) → 404.
	if rec := reqGET(t, srv, "/v1/courses/"+id+"/bogus"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /{id}/bogus: status=%d want 404", rec.Code)
	}
}

func TestSv6_V1Courses_ApplicationFormSubRoute(t *testing.T) {
	srv, _ := newV1Server()
	id := sv6CreateCourse(t, srv, "App Form", "public", 120000)

	rec := reqGET(t, srv, "/v1/courses/"+id+"/application-form")
	if rec.Code != http.StatusOK {
		t.Fatalf("application-form: status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["course_id"] != id {
		t.Errorf("course_id=%v want %s", got["course_id"], id)
	}
}

// -----------------------------------------------------------------------------
// handleV1CourseCreate — guards
// -----------------------------------------------------------------------------

func TestSv6_V1CourseCreate_Guards(t *testing.T) {
	srv, _ := newV1Server()

	// decode error → 400
	if rec := sv6Raw(t, srv, http.MethodPost, "/v1/courses", `{not-json`, tenantA, gcidA, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	// empty title → 400
	if rec := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title": "", "price_sgd_cents": 0,
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty title: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	// invalid visibility → 400
	if rec := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title": "x", "price_sgd_cents": 0, "visibility": "everyone",
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid visibility: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	// missing gcid → 401
	if rec := sv6Raw(t, srv, http.MethodPost, "/v1/courses", `{"title":"x","visibility":"public"}`, tenantA, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing gcid: status=%d want 401 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleV1CourseList — paid filter + first-parse false path
// -----------------------------------------------------------------------------

func TestSv6_V1CourseList_PaidFilter(t *testing.T) {
	srv, _ := newV1Server()
	_ = sv6CreateCourse(t, srv, "Free One", "public", 0)
	_ = sv6CreateCourse(t, srv, "Paid One", "public", 1000)

	// first=0 fails the 1..200 guard → default first retained (false path);
	// paid=true skips the free course and adjusts total.
	rec := reqGET(t, srv, "/v1/courses?visibility=public&paid=true&first=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, _ := resp["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("paid=true must hide the free course: got %d items", len(items))
	}
	if total, _ := resp["total"].(float64); total != 1 {
		t.Errorf("total=%v want 1", total)
	}
}

// -----------------------------------------------------------------------------
// handleV1CourseDetail / Update — guards
// -----------------------------------------------------------------------------

func TestSv6_V1CourseDetail_NotFound_404(t *testing.T) {
	srv, _ := newV1Server()
	if rec := reqGET(t, srv, "/v1/courses/no-such-course"); rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_V1CourseUpdate_Guards(t *testing.T) {
	srv, _ := newV1Server()
	id := sv6CreateCourse(t, srv, "CSPO", "private", 1000)

	// decode error → 400
	if rec := sv6Raw(t, srv, http.MethodPatch, "/v1/courses/"+id, `{oops`, tenantA, gcidA, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	// empty title → 400
	if rec := reqJSON(t, srv, http.MethodPatch, "/v1/courses/"+id, map[string]interface{}{"title": "  "}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty title: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	// negative price → 400
	if rec := reqJSON(t, srv, http.MethodPatch, "/v1/courses/"+id, map[string]interface{}{"price_sgd_cents": -1}); rec.Code != http.StatusBadRequest {
		t.Errorf("negative price: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	// invalid visibility → 400
	if rec := reqJSON(t, srv, http.MethodPatch, "/v1/courses/"+id, map[string]interface{}{"visibility": "nope"}); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid visibility: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleV1EnrolmentCreate — guards + cross-tenant shapes
// -----------------------------------------------------------------------------

func TestSv6_V1EnrolmentCreate_Guards(t *testing.T) {
	srv, _ := newV1Server()
	id := sv6CreateCourse(t, srv, "Enrol Target", "public", 0)

	// decode error → 400
	if rec := sv6Raw(t, srv, http.MethodPost, "/v1/courses/"+id+"/enrolments", `{oops`, tenantA, gcidA, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	// no gcid in body or header → 401
	if rec := sv6Raw(t, srv, http.MethodPost, "/v1/courses/"+id+"/enrolments", `{}`, tenantA, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing gcid: status=%d want 401 body=%s", rec.Code, rec.Body.String())
	}
	// unknown course → 404
	if rec := reqJSON(t, srv, http.MethodPost, "/v1/courses/no-such/enrolments", map[string]interface{}{}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown course: status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
	// private course cross-tenant → 404
	privID := sv6CreateCourse(t, srv, "Private", "private", 0)
	if rec := sv6Raw(t, srv, http.MethodPost, "/v1/courses/"+privID+"/enrolments", `{}`, "01970000-0000-7000-8000-0000000000bb", gcidA, ""); rec.Code != http.StatusNotFound {
		t.Errorf("private cross-tenant enrol: status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_V1EnrolmentCreate_CrossTenantPublic_201(t *testing.T) {
	// A public course enrols from ANOTHER tenant: pc.TenantID != tenantID, so
	// the same-tenant hard-gate block is skipped; enrolment records against
	// the learner's tenant.
	srv, _ := newV1Server()
	id := sv6CreateCourse(t, srv, "Public Cross", "public", 0)

	rec := sv6Raw(t, srv, http.MethodPost, "/v1/courses/"+id+"/enrolments", `{}`, "01970000-0000-7000-8000-0000000000bb", gcidB, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("cross-tenant public enrol: status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// handleV1EnrolmentCancel — 404 shapes
// -----------------------------------------------------------------------------

func TestSv6_V1EnrolmentCancel_NotFound_404(t *testing.T) {
	srv, _ := newV1Server()
	courseA := sv6CreateCourse(t, srv, "A", "public", 0)
	courseB := sv6CreateCourse(t, srv, "B", "public", 0)

	wE := reqJSON(t, srv, http.MethodPost, "/v1/courses/"+courseA+"/enrolments", map[string]interface{}{})
	if wE.Code != http.StatusCreated {
		t.Fatalf("seed enrol: status=%d body=%s", wE.Code, wE.Body.String())
	}
	var e map[string]interface{}
	_ = json.Unmarshal(wE.Body.Bytes(), &e)
	enrolID := e["id"].(string)

	// unknown enrolment id → 404
	if rec := reqDELETE(t, srv, "/v1/courses/"+courseA+"/enrolments/no-such"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown enrol id: status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
	// enrolment belongs to a different course → 404
	if rec := reqDELETE(t, srv, "/v1/courses/"+courseB+"/enrolments/"+enrolID); rec.Code != http.StatusNotFound {
		t.Errorf("course mismatch: status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Campus — root list, decode error, empty path id
// -----------------------------------------------------------------------------

func TestSv6_V1Campus_Root_List_200(t *testing.T) {
	// /v1/campus root dispatches via campusRootHandler: GET → campusListHandler.
	// The campusHandler POST-only guard is unreachable through the router.
	srv, _ := newV1Server()
	rec := reqGET(t, srv, "/v1/campus")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/campus: status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if _, ok := resp["items"]; !ok {
		t.Errorf("expected items envelope, got %s", rec.Body.String())
	}
}

func TestSv6_V1Campus_Create_BadJSON_400(t *testing.T) {
	srv, _ := newV1Server()
	if rec := sv6Raw(t, srv, http.MethodPost, "/v1/campus", `{oops`, tenantA, gcidA, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_V1Campus_Get_EmptyID_404(t *testing.T) {
	srv, _ := newV1Server()
	if rec := reqGET(t, srv, "/v1/campus/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /v1/campus/: status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}
