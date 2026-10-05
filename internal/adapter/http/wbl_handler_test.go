// wbl_handler_test.go — table-driven tests for the WblPlacement HTTP surface
// per the M15c brief.
//
// 5 endpoints:
//
//	GET    /api/v1/wbl-placements?state=
//	POST   /api/v1/wbl-placements
//	GET    /api/v1/wbl-placements/{id}
//	PATCH  /api/v1/wbl-placements/{id}
//	DELETE /api/v1/wbl-placements/{id}      (soft-delete via state=WITHDRAWN)
//
// TDD: written FIRST then drove the handler in wbl_handler.go.
// Coverage target: ≥60% adapter (per .claude/rules/development-execution.md).
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/directory"
)

const (
	wblTestTenantID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50100"
	wblTestAuthorGCID  = "019e2f93-d586-71b5-8c3d-e2b0d0d50101"
	wblTestLearnerGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d50103"
	wblTestCourseID    = "019e2f93-d586-71b5-8c3d-e2b0d0d50200"
	wblTestLearnerName = "Alice Tan"
	wblTestCourseTitle = "Intro to Robotics"
)

// newWblServer wires the M15c routes against fresh in-mem adapters per test.
func newWblServer() (http.Handler, *inmem.WblRepo) {
	repo := inmem.NewWblRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Wbl: repo,
	})
	return srv, repo
}

func doWbl(t *testing.T, h http.Handler, method, path, body, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("X-Tenant-Id", wblTestTenantID)
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

func validWblCreateBody() string {
	start := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	return `{
		"gcid": "` + wblTestLearnerGCID + `",
		"course_id": "` + wblTestCourseID + `",
		"host_org_name": "Acme Robotics Pte Ltd",
		"supervisor_name": "Ms. Tan",
		"supervisor_email": "tan@acme.example",
		"start_date": "` + start + `",
		"end_date": "` + end + `",
		"hours_required": 160
	}`
}

func createWblFixture(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := doWbl(t, h, "POST", "/api/v1/wbl-placements", validWblCreateBody(),
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed POST status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("seed POST returned no id")
	}
	return id
}

// -----------------------------------------------------------------------------
// POST /api/v1/wbl-placements (create)
// -----------------------------------------------------------------------------

func TestWbl_PostPlacement_AsInstructor_201(t *testing.T) {
	srv, _ := newWblServer()
	rec := doWbl(t, srv, "POST", "/api/v1/wbl-placements", validWblCreateBody(),
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "SCHEDULED" {
		t.Errorf("state=%v want SCHEDULED", got["state"])
	}
	if got["host_org_name"] != "Acme Robotics Pte Ltd" {
		t.Errorf("host_org_name=%v", got["host_org_name"])
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Errorf("id missing")
	}
	if hr, _ := got["hours_required"].(float64); hr != 160 {
		t.Errorf("hours_required=%v want 160", got["hours_required"])
	}
}

func TestWbl_PostPlacement_NoTenant_400(t *testing.T) {
	srv, _ := newWblServer()
	req := httptest.NewRequest("POST", "/api/v1/wbl-placements",
		bytes.NewReader([]byte(validWblCreateBody())))
	req.Header.Set("gcid", wblTestAuthorGCID)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestWbl_PostPlacement_NoGCID_401(t *testing.T) {
	srv, _ := newWblServer()
	rec := doWbl(t, srv, "POST", "/api/v1/wbl-placements", validWblCreateBody(), "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestWbl_PostPlacement_NoRole_403(t *testing.T) {
	srv, _ := newWblServer()
	rec := doWbl(t, srv, "POST", "/api/v1/wbl-placements", validWblCreateBody(),
		wblTestAuthorGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestWbl_PostPlacement_InvalidEmail_400(t *testing.T) {
	srv, _ := newWblServer()
	body := `{
		"gcid": "` + wblTestLearnerGCID + `",
		"course_id": "` + wblTestCourseID + `",
		"host_org_name": "Acme",
		"supervisor_name": "Ms. Tan",
		"supervisor_email": "not-an-email",
		"start_date": "2026-06-01T09:00:00Z",
		"end_date": "2026-08-01T17:00:00Z",
		"hours_required": 80
	}`
	rec := doWbl(t, srv, "POST", "/api/v1/wbl-placements", body,
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestWbl_PostPlacement_BadJSON_400(t *testing.T) {
	srv, _ := newWblServer()
	rec := doWbl(t, srv, "POST", "/api/v1/wbl-placements", `{"not_valid_json`,
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/wbl-placements
// -----------------------------------------------------------------------------

func TestWbl_ListPlacements_OK(t *testing.T) {
	srv, _ := newWblServer()
	createWblFixture(t, srv)
	createWblFixture(t, srv)

	rec := doWbl(t, srv, "GET", "/api/v1/wbl-placements", "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, ok := got["items"].([]interface{})
	if !ok {
		t.Fatalf("items not array: %v", got["items"])
	}
	if len(items) != 2 {
		t.Errorf("items=%d want 2", len(items))
	}
}

func TestWbl_ListPlacements_FilterByState(t *testing.T) {
	srv, _ := newWblServer()
	createWblFixture(t, srv)

	rec := doWbl(t, srv, "GET", "/api/v1/wbl-placements?state=COMPLETED", "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("filtered items=%d want 0", len(items))
	}
}

func TestWbl_ListPlacements_BadState_400(t *testing.T) {
	srv, _ := newWblServer()
	rec := doWbl(t, srv, "GET", "/api/v1/wbl-placements?state=BANANA", "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestWbl_ListPlacements_NoTenant_400(t *testing.T) {
	srv, _ := newWblServer()
	req := httptest.NewRequest("GET", "/api/v1/wbl-placements", nil)
	req.Header.Set("gcid", wblTestAuthorGCID)
	req.Header.Set("x-mesh-user-roles", "instructor")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/wbl-placements/{id}
// -----------------------------------------------------------------------------

func TestWbl_GetPlacement_OK(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)
	rec := doWbl(t, srv, "GET", "/api/v1/wbl-placements/"+id, "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["id"] != id {
		t.Errorf("id=%v want %s", got["id"], id)
	}
}

func TestWbl_GetPlacement_NotFound_404(t *testing.T) {
	srv, _ := newWblServer()
	rec := doWbl(t, srv, "GET", "/api/v1/wbl-placements/nope", "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestWbl_GetPlacement_CrossTenant_404(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)

	// Probe with a different tenant header — must 404.
	req := httptest.NewRequest("GET", "/api/v1/wbl-placements/"+id, nil)
	req.Header.Set("X-Tenant-Id", "different-tenant")
	req.Header.Set("gcid", wblTestAuthorGCID)
	req.Header.Set("x-mesh-user-roles", "instructor")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// PATCH /api/v1/wbl-placements/{id}
// -----------------------------------------------------------------------------

func TestWbl_PatchPlacement_HoursCompleted_OK(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)
	rec := doWbl(t, srv, "PATCH", "/api/v1/wbl-placements/"+id,
		`{"hours_completed": 80}`, wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if hc, _ := got["hours_completed"].(float64); hc != 80 {
		t.Errorf("hours_completed=%v want 80", got["hours_completed"])
	}
}

func TestWbl_PatchPlacement_EvaluatorNotes_OK(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)
	rec := doWbl(t, srv, "PATCH", "/api/v1/wbl-placements/"+id,
		`{"evaluator_notes": "Strong technical aptitude."}`,
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["evaluator_notes"] != "Strong technical aptitude." {
		t.Errorf("evaluator_notes=%v", got["evaluator_notes"])
	}
}

func TestWbl_PatchPlacement_HoursOverflow_409(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)
	rec := doWbl(t, srv, "PATCH", "/api/v1/wbl-placements/"+id,
		`{"hours_completed": 9999}`, wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409", rec.Code)
	}
}

func TestWbl_PatchPlacement_NoRole_403(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)
	rec := doWbl(t, srv, "PATCH", "/api/v1/wbl-placements/"+id,
		`{"hours_completed": 10}`, wblTestAuthorGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// DELETE /api/v1/wbl-placements/{id} (soft-delete via state=WITHDRAWN)
// -----------------------------------------------------------------------------

func TestWbl_DeletePlacement_SoftWithdraw_200(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)
	rec := doWbl(t, srv, "DELETE", "/api/v1/wbl-placements/"+id, "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "WITHDRAWN" {
		t.Errorf("state=%v want WITHDRAWN", got["state"])
	}
}

func TestWbl_DeletePlacement_Twice_409(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)
	_ = doWbl(t, srv, "DELETE", "/api/v1/wbl-placements/"+id, "",
		wblTestAuthorGCID, "instructor")
	rec := doWbl(t, srv, "DELETE", "/api/v1/wbl-placements/"+id, "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 (already withdrawn)", rec.Code)
	}
}

func TestWbl_DeletePlacement_NotFound_404(t *testing.T) {
	srv, _ := newWblServer()
	rec := doWbl(t, srv, "DELETE", "/api/v1/wbl-placements/nope", "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestWbl_DeletePlacement_NoRole_403(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)
	rec := doWbl(t, srv, "DELETE", "/api/v1/wbl-placements/"+id, "",
		wblTestAuthorGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Method dispatch
// -----------------------------------------------------------------------------

func TestWbl_RootMethodNotAllowed(t *testing.T) {
	srv, _ := newWblServer()
	rec := doWbl(t, srv, "PUT", "/api/v1/wbl-placements", "{}",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}

func TestWbl_SubMethodNotAllowed(t *testing.T) {
	srv, _ := newWblServer()
	id := createWblFixture(t, srv)
	rec := doWbl(t, srv, "POST", "/api/v1/wbl-placements/"+id, "{}",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/wbl-placements display enrichment (CHO-2335)
//
// The list handler enriches each placement with a resolved learner_name
// (chora_delivery.user_directory projection) + course_title (chora_delivery
// courses) when the injected store carries the enrichment capability. Both are
// omitted (FE falls back to the raw gcid / course_id) when unresolved.
// -----------------------------------------------------------------------------

// newEnrichedWblServer wires the WBL routes against an enriched store that
// composes the user_directory name projection + the course-title repo, so the
// list path resolves learner_name + course_title. seedName / seedTitle control
// whether the fixture's learner / course are resolvable (else the id fallback).
func newEnrichedWblServer(t *testing.T, seedName, seedTitle bool) http.Handler {
	t.Helper()
	dir := directory.NewInMemUserDirectory()
	if seedName {
		if err := dir.Upsert(context.Background(), directory.UserDirectoryEntry{
			GCID:        wblTestLearnerGCID,
			DisplayName: wblTestLearnerName,
			UpdatedAt:   time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed directory: %v", err)
		}
	}
	courses := inmem.NewCourseRepo()
	if seedTitle {
		c, err := delivery.NewCourse(wblTestTenantID, wblTestCourseTitle, nil, 30)
		if err != nil {
			t.Fatalf("seed course: %v", err)
		}
		c.ID = wblTestCourseID
		if err := courses.Save(context.Background(), c); err != nil {
			t.Fatalf("save course: %v", err)
		}
	}
	enriched := inmem.NewEnrichedWblStore(inmem.NewWblRepo(), dir, courses)
	return httpapi.NewServer(httpapi.Deps{Wbl: enriched})
}

// firstWblItem unmarshals the list envelope + returns the first item map,
// failing the test when the list is empty.
func firstWblItem(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal list: %v body=%s", err, rec.Body.String())
	}
	items, _ := got["items"].([]interface{})
	if len(items) == 0 {
		t.Fatalf("list has no items: %s", rec.Body.String())
	}
	m, ok := items[0].(map[string]interface{})
	if !ok {
		t.Fatalf("item[0] not an object: %v", items[0])
	}
	return m
}

func TestWbl_ListPlacements_EnrichesLearnerNameAndCourseTitle(t *testing.T) {
	srv := newEnrichedWblServer(t, true, true)
	createWblFixture(t, srv)

	rec := doWbl(t, srv, "GET", "/api/v1/wbl-placements", "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	item := firstWblItem(t, rec)
	if item["learner_name"] != wblTestLearnerName {
		t.Errorf("learner_name=%v want %q", item["learner_name"], wblTestLearnerName)
	}
	if item["course_title"] != wblTestCourseTitle {
		t.Errorf("course_title=%v want %q", item["course_title"], wblTestCourseTitle)
	}
	// Backward-compat: the raw ids MUST still ride the envelope.
	if item["gcid"] != wblTestLearnerGCID {
		t.Errorf("gcid=%v want %q", item["gcid"], wblTestLearnerGCID)
	}
	if item["course_id"] != wblTestCourseID {
		t.Errorf("course_id=%v want %q", item["course_id"], wblTestCourseID)
	}
}

func TestWbl_ListPlacements_UnresolvedFallsBackToIDs(t *testing.T) {
	// Enriched store, but nothing seeded (both lookups miss), so the display
	// fields are omitted and the FE falls back to the raw ids.
	srv := newEnrichedWblServer(t, false, false)
	createWblFixture(t, srv)

	rec := doWbl(t, srv, "GET", "/api/v1/wbl-placements", "",
		wblTestAuthorGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	item := firstWblItem(t, rec)
	if v, ok := item["learner_name"]; ok {
		t.Errorf("learner_name should be omitted when unresolved, got %v", v)
	}
	if v, ok := item["course_title"]; ok {
		t.Errorf("course_title should be omitted when unresolved, got %v", v)
	}
	if item["gcid"] != wblTestLearnerGCID {
		t.Errorf("gcid=%v want %q", item["gcid"], wblTestLearnerGCID)
	}
	if item["course_id"] != wblTestCourseID {
		t.Errorf("course_id=%v want %q", item["course_id"], wblTestCourseID)
	}
}
