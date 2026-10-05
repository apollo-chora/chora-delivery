// project_group_handler_test.go — table-driven tests for the ProjectGroup
// HTTP surface (M15b R+ build-out).
//
// Coverage target: ≥60% adapter (per .claude/rules/development-execution.md).
//
// TDD: written FIRST then drove the handler in project_group_handler.go.
//
// Test matrix:
//
//	POST   /api/v1/project-groups                — create FORMING
//	GET    /api/v1/project-groups?course_id=     — list (by course or tenant)
//	GET    /api/v1/project-groups/{id}           — single fetch
//	POST   /api/v1/project-groups/{id}/submit    — ACTIVE → SUBMITTED
//	POST   /api/v1/project-groups/{id}/grade     — SUBMITTED → GRADED
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

const (
	pgTestTenantHTTPID    = "019e3000-0000-7000-8000-000000000001"
	pgTestCourseHTTPID    = "019e3000-0000-7000-8000-000000000002"
	pgTestAuthorHTTPGCID  = "019e3000-0000-7000-9000-000000000001"
	pgTestLearnerHTTPGCID = "019e3000-0000-7000-9000-000000000005"
	pgTestLearner1ID      = "019e3000-0000-7000-9000-000000000002"
	pgTestLearner2ID      = "019e3000-0000-7000-9000-000000000003"
	pgTestGraderHTTPGCID  = "019e3000-0000-7000-9000-000000000004"
)

// newPGServer wires the ProjectGroup routes against fresh in-mem adapters.
func newPGServer() (http.Handler, *inmem.ProjectGroupRepo) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	repo := inmem.NewProjectGroupRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      pub,
		ProjectGroups:  repo,
	})
	return srv, repo
}

// doPG issues a request with the standard project-group headers.
func doPG(t *testing.T, h http.Handler, method, path, body, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("X-Tenant-Id", pgTestTenantHTTPID)
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

// createProjectGroup is a helper that POSTs a FORMING group and returns the id.
func createProjectGroup(t *testing.T, srv http.Handler) string {
	t.Helper()
	body := `{
		"course_id": "` + pgTestCourseHTTPID + `",
		"name": "Team Atlas",
		"members": [
			{"gcid": "` + pgTestLearner1ID + `", "role": "leader"},
			{"gcid": "` + pgTestLearner2ID + `", "role": "member"}
		]
	}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups", body, pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("id missing in create response: %s", rec.Body.String())
	}
	return id
}

// -----------------------------------------------------------------------------
// POST /api/v1/project-groups (create FORMING)
// -----------------------------------------------------------------------------

func TestPG_PostGroup_AsInstructor_201(t *testing.T) {
	srv, _ := newPGServer()
	body := `{
		"course_id": "` + pgTestCourseHTTPID + `",
		"name": "Team Atlas",
		"members": [
			{"gcid": "` + pgTestLearner1ID + `", "role": "leader"}
		]
	}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups", body, pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "FORMING" {
		t.Errorf("state=%v want FORMING", got["state"])
	}
	if got["course_id"] != pgTestCourseHTTPID {
		t.Errorf("course_id=%v want %s", got["course_id"], pgTestCourseHTTPID)
	}
	if got["name"] != "Team Atlas" {
		t.Errorf("name=%v want Team Atlas", got["name"])
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Errorf("id missing")
	}
	members, _ := got["members"].([]interface{})
	if len(members) != 1 {
		t.Errorf("members len=%d want 1", len(members))
	}
}

func TestPG_PostGroup_NoTenantHeader_400(t *testing.T) {
	srv, _ := newPGServer()
	req := httptest.NewRequest("POST", "/api/v1/project-groups", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("gcid", pgTestAuthorHTTPGCID)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestPG_PostGroup_NoGCIDHeader_401(t *testing.T) {
	srv, _ := newPGServer()
	body := `{"course_id":"` + pgTestCourseHTTPID + `","name":"T"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups", body, "", "instructor")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rec.Code)
	}
}

func TestPG_PostGroup_AsLearner_403(t *testing.T) {
	srv, _ := newPGServer()
	body := `{"course_id":"` + pgTestCourseHTTPID + `","name":"T"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups", body, pgTestLearnerHTTPGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestPG_PostGroup_MissingCourseID_400(t *testing.T) {
	srv, _ := newPGServer()
	body := `{"name":"T"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups", body, pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestPG_PostGroup_MissingName_400(t *testing.T) {
	srv, _ := newPGServer()
	body := `{"course_id":"` + pgTestCourseHTTPID + `"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups", body, pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/project-groups
// -----------------------------------------------------------------------------

func TestPG_ListByCourse_ReturnsItems(t *testing.T) {
	srv, _ := newPGServer()
	id1 := createProjectGroup(t, srv)
	id2 := createProjectGroup(t, srv)
	rec := doPG(t, srv, "GET", "/api/v1/project-groups?course_id="+pgTestCourseHTTPID, "",
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, ok := resp["items"].([]interface{})
	if !ok {
		t.Fatalf("items missing/wrong type body=%s", rec.Body.String())
	}
	if len(items) != 2 {
		t.Errorf("items len=%d want 2", len(items))
	}
	// Verify both IDs surface.
	ids := map[string]bool{}
	for _, it := range items {
		m := it.(map[string]interface{})
		ids[m["id"].(string)] = true
	}
	if !ids[id1] || !ids[id2] {
		t.Errorf("missing expected ids: got=%v", ids)
	}
}

func TestPG_ListByTenant_NoCourseID_ReturnsItems(t *testing.T) {
	srv, _ := newPGServer()
	_ = createProjectGroup(t, srv)
	rec := doPG(t, srv, "GET", "/api/v1/project-groups", "",
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, _ := resp["items"].([]interface{})
	if len(items) != 1 {
		t.Errorf("items len=%d want 1", len(items))
	}
}

func TestPG_List_NoTenantHeader_400(t *testing.T) {
	srv, _ := newPGServer()
	req := httptest.NewRequest("GET", "/api/v1/project-groups", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/project-groups/{id}
// -----------------------------------------------------------------------------

func TestPG_GetByID_ReturnsGroup(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)
	rec := doPG(t, srv, "GET", "/api/v1/project-groups/"+id, "",
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["id"] != id {
		t.Errorf("id=%v want %s", got["id"], id)
	}
}

func TestPG_GetByID_NotFound_404(t *testing.T) {
	srv, _ := newPGServer()
	rec := doPG(t, srv, "GET", "/api/v1/project-groups/01970000-0000-0000-0000-000000000000", "",
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/project-groups/{id}/submit
// -----------------------------------------------------------------------------

func TestPG_Submit_ActiveToSubmitted(t *testing.T) {
	srv, repo := newPGServer()
	id := createProjectGroup(t, srv)
	// Promote to ACTIVE directly via the repo (M15b doesn't expose a public
	// Activate route — instructors auto-activate on first submit attempt in
	// production. For now, activate via stored aggregate.)
	g, ok, _ := repo.Get(context.Background(), id)
	if !ok {
		t.Fatalf("setup: group %s missing", id)
	}
	if err := g.Activate(); err != nil {
		t.Fatalf("setup activate err=%v", err)
	}
	_ = repo.Save(context.Background(), g)

	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/submit", "",
		pgTestLearner1ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "SUBMITTED" {
		t.Errorf("state=%v want SUBMITTED", got["state"])
	}
	if got["submitted_at"] == nil {
		t.Errorf("submitted_at missing")
	}
}

func TestPG_Submit_NotActive_409(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)
	// state = FORMING; submit should 409.
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/submit", "",
		pgTestLearner1ID, "")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/project-groups/{id}/grade
// -----------------------------------------------------------------------------

func TestPG_Grade_SubmittedToGraded(t *testing.T) {
	srv, repo := newPGServer()
	id := createProjectGroup(t, srv)
	g, ok, _ := repo.Get(context.Background(), id)
	if !ok {
		t.Fatalf("setup: group %s missing", id)
	}
	if err := g.Activate(); err != nil {
		t.Fatalf("setup activate err=%v", err)
	}
	if err := g.Submit(); err != nil {
		t.Fatalf("setup submit err=%v", err)
	}
	_ = repo.Save(context.Background(), g)

	body := `{"score_pct": 85.5, "feedback": "well done"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/grade", body,
		pgTestGraderHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "GRADED" {
		t.Errorf("state=%v want GRADED", got["state"])
	}
	if got["score_pct"].(float64) != 85.5 {
		t.Errorf("score_pct=%v want 85.5", got["score_pct"])
	}
	if got["grader_gcid"] != pgTestGraderHTTPGCID {
		t.Errorf("grader_gcid=%v want %s", got["grader_gcid"], pgTestGraderHTTPGCID)
	}
	if got["graded_at"] == nil {
		t.Errorf("graded_at missing")
	}
	if got["feedback"] != "well done" {
		t.Errorf("feedback=%v want 'well done'", got["feedback"])
	}
}

func TestPG_Grade_AsLearner_403(t *testing.T) {
	srv, repo := newPGServer()
	id := createProjectGroup(t, srv)
	g, _, _ := repo.Get(context.Background(), id)
	_ = g.Activate()
	_ = g.Submit()
	_ = repo.Save(context.Background(), g)

	body := `{"score_pct": 80}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/grade", body,
		pgTestLearnerHTTPGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestPG_Grade_NotSubmitted_409(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)
	// state = FORMING; grade should 409.
	body := `{"score_pct": 80}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/grade", body,
		pgTestGraderHTTPGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestPG_Grade_ScoreOutOfRange_400(t *testing.T) {
	srv, repo := newPGServer()
	id := createProjectGroup(t, srv)
	g, _, _ := repo.Get(context.Background(), id)
	_ = g.Activate()
	_ = g.Submit()
	_ = repo.Save(context.Background(), g)

	body := `{"score_pct": 101}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/grade", body,
		pgTestGraderHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/project-groups/{id}/members  (add member — CHO-2131)
// -----------------------------------------------------------------------------

func TestPG_AddMember_AsInstructor_200(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv) // learner1 (leader) + learner2 (member)
	body := `{"gcid":"` + pgTestLearnerHTTPGCID + `","role":"member"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/members", body,
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	members, _ := got["members"].([]interface{})
	if len(members) != 3 {
		t.Errorf("members len=%d want 3", len(members))
	}
}

func TestPG_AddMember_Duplicate_409(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)
	body := `{"gcid":"` + pgTestLearner1ID + `","role":"member"}` // already present
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/members", body,
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestPG_AddMember_EmptyGCID_400(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)
	body := `{"gcid":"","role":"member"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/members", body,
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestPG_AddMember_AsLearner_403(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)
	body := `{"gcid":"` + pgTestLearnerHTTPGCID + `"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/members", body,
		pgTestLearnerHTTPGCID, "") // no role → learner
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestPG_AddMember_GroupNotFound_404(t *testing.T) {
	srv, _ := newPGServer()
	body := `{"gcid":"` + pgTestLearnerHTTPGCID + `"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/01970000-0000-0000-0000-000000000000/members", body,
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestPG_AddMember_Locked_409(t *testing.T) {
	srv, repo := newPGServer()
	id := createProjectGroup(t, srv)
	g, _, _ := repo.Get(context.Background(), id)
	_ = g.Activate()
	_ = g.Submit() // SUBMITTED — roster frozen
	_ = repo.Save(context.Background(), g)
	body := `{"gcid":"` + pgTestLearnerHTTPGCID + `"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/members", body,
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestPG_AddMember_RepoUnwired_503(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{}) // ProjectGroups nil
	body := `{"gcid":"` + pgTestLearnerHTTPGCID + `"}`
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/01970000-0000-0000-0000-000000000001/members", body,
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d want 503 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// DELETE /api/v1/project-groups/{id}/members/{gcid}  (remove member — CHO-2131)
// -----------------------------------------------------------------------------

func TestPG_RemoveMember_AsInstructor_200(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv) // learner1 (leader) + learner2 (member)
	rec := doPG(t, srv, "DELETE", "/api/v1/project-groups/"+id+"/members/"+pgTestLearner1ID, "",
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	members, _ := got["members"].([]interface{})
	if len(members) != 1 {
		t.Fatalf("members len=%d want 1", len(members))
	}
	if members[0].(map[string]interface{})["gcid"] != pgTestLearner2ID {
		t.Errorf("remaining gcid=%v want learner2", members[0])
	}
}

func TestPG_RemoveMember_NotFound_404(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)
	rec := doPG(t, srv, "DELETE", "/api/v1/project-groups/"+id+"/members/"+pgTestGraderHTTPGCID, "",
		pgTestAuthorHTTPGCID, "instructor") // grader is not a member
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestPG_RemoveMember_AsLearner_403(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)
	rec := doPG(t, srv, "DELETE", "/api/v1/project-groups/"+id+"/members/"+pgTestLearner1ID, "",
		pgTestLearnerHTTPGCID, "") // no role → learner
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestPG_RemoveMember_Locked_409(t *testing.T) {
	srv, repo := newPGServer()
	id := createProjectGroup(t, srv)
	g, _, _ := repo.Get(context.Background(), id)
	_ = g.Activate()
	_ = g.Submit() // SUBMITTED — roster frozen
	_ = repo.Save(context.Background(), g)
	rec := doPG(t, srv, "DELETE", "/api/v1/project-groups/"+id+"/members/"+pgTestLearner1ID, "",
		pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}
