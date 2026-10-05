// project_group_sv6_test.go — statement-coverage battery for
// internal/adapter/http/project_group_handler.go. Supplements
// project_group_handler_test.go with the remaining reachable branches:
//
//   - projectGroupsRootHandler non-GET/POST 405
//   - projectGroupsSubHandler method guards + route 404 shapes (trailing
//     slash, empty segments, unknown action, non-members 3-segment path,
//     over-deep paths)
//   - the unwired-repo 503 on every per-endpoint handler (routes are always
//     mounted; only the handler checks deps.ProjectGroups)
//   - handleProjectGroupCreate decode-error 400
//   - handleProjectGroupList empty-result 200
//   - handleProjectGroupGet cross-tenant 404
//   - handleProjectGroupSubmit unknown-group 404
//   - handleProjectGroupGrade decode-error 400 + unknown-group 404
//   - handleProjectGroupAddMember decode-error 400
//   - handleProjectGroupRemoveMember unknown-group 404
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
)

// -----------------------------------------------------------------------------
// Dispatchers — root + sub handler guards
// -----------------------------------------------------------------------------

func TestSv6_ProjectGroups_Root_MethodNotAllowed(t *testing.T) {
	srv, _ := newPGServer()
	rec := doPG(t, srv, "PUT", "/api/v1/project-groups", "", pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_ProjectGroupsSub_DispatchGuards(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)

	// trailing slash → rest == "" → 404
	if rec := doPG(t, srv, "GET", "/api/v1/project-groups/", "", pgTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /: status=%d want 404", rec.Code)
	}
	// case 1 non-GET → 405
	if rec := doPG(t, srv, "PUT", "/api/v1/project-groups/"+id, "", pgTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /{id}: status=%d want 405", rec.Code)
	}
	// case 2 non-POST → 405
	if rec := doPG(t, srv, "GET", "/api/v1/project-groups/"+id+"/submit", "", pgTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /{id}/submit: status=%d want 405", rec.Code)
	}
	// case 2 unknown action → 404
	if rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/dance", "", pgTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusNotFound {
		t.Errorf("POST /{id}/dance: status=%d want 404", rec.Code)
	}
	// case 3 second segment != "members" → 404
	if rec := doPG(t, srv, "GET", "/api/v1/project-groups/a/b/c", "", pgTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /a/b/c: status=%d want 404", rec.Code)
	}
	// case 3 non-DELETE → 405
	if rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/members/"+pgTestLearner1ID, "", pgTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /{id}/members/{gcid}: status=%d want 405", rec.Code)
	}
	// 4+ segments → 404
	if rec := doPG(t, srv, "GET", "/api/v1/project-groups/a/b/c/d", "", pgTestAuthorHTTPGCID, "instructor"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /a/b/c/d: status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Unwired repo → 503 on every per-endpoint handler
// -----------------------------------------------------------------------------

func TestSv6_ProjectGroups_UnwiredRepo_503(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{}) // ProjectGroups nil
	const gid = "01970000-0000-0000-0000-000000000001"
	cases := []struct {
		name, method, path, body string
	}{
		{"create", "POST", "/api/v1/project-groups", `{"course_id":"c","name":"n"}`},
		{"list", "GET", "/api/v1/project-groups", ""},
		{"get", "GET", "/api/v1/project-groups/" + gid, ""},
		{"submit", "POST", "/api/v1/project-groups/" + gid + "/submit", ""},
		{"grade", "POST", "/api/v1/project-groups/" + gid + "/grade", `{"score_pct":80}`},
		{"remove", "DELETE", "/api/v1/project-groups/" + gid + "/members/01970000-0000-7000-9000-000000000002", ""},
	}
	for _, tc := range cases {
		rec := doPG(t, srv, tc.method, tc.path, tc.body, pgTestAuthorHTTPGCID, "instructor")
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status=%d want 503 body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

// -----------------------------------------------------------------------------
// Create — decode-error 400
// -----------------------------------------------------------------------------

func TestSv6_ProjectGroupCreate_BadJSON_400(t *testing.T) {
	srv, _ := newPGServer()
	rec := doPG(t, srv, "POST", "/api/v1/project-groups", `{not-json`, pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// List — empty result renders []
// -----------------------------------------------------------------------------

func TestSv6_ProjectGroupList_Empty_200(t *testing.T) {
	srv, _ := newPGServer()
	rec := doPG(t, srv, "GET", "/api/v1/project-groups", "", pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	items, _ := resp["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("items len=%d want 0", len(items))
	}
}

// -----------------------------------------------------------------------------
// Get — cross-tenant 404
// -----------------------------------------------------------------------------

func TestSv6_ProjectGroupGet_CrossTenant_404(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)

	req := httptest.NewRequest("GET", "/api/v1/project-groups/"+id, nil)
	req.Header.Set("X-Tenant-Id", "other-tenant")
	req.Header.Set("gcid", pgTestAuthorHTTPGCID)
	req.Header.Set("x-mesh-user-roles", "instructor")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Submit / Grade / AddMember / RemoveMember — remaining error branches
// -----------------------------------------------------------------------------

func TestSv6_ProjectGroupSubmit_Unknown_404(t *testing.T) {
	srv, _ := newPGServer()
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/no-such/submit", "", pgTestLearner1ID, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_ProjectGroupGrade_Guards(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)

	// decode error → 400
	if rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/grade", `{oops`, pgTestGraderHTTPGCID, "instructor"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	// unknown group → 404
	if rec := doPG(t, srv, "POST", "/api/v1/project-groups/no-such/grade", `{"score_pct":80}`, pgTestGraderHTTPGCID, "instructor"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown group: status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_ProjectGroupAddMember_BadJSON_400(t *testing.T) {
	srv, _ := newPGServer()
	id := createProjectGroup(t, srv)
	rec := doPG(t, srv, "POST", "/api/v1/project-groups/"+id+"/members", `{oops`, pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSv6_ProjectGroupRemoveMember_Unknown_404(t *testing.T) {
	srv, _ := newPGServer()
	rec := doPG(t, srv, "DELETE", "/api/v1/project-groups/no-such/members/"+pgTestLearner1ID, "", pgTestAuthorHTTPGCID, "instructor")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}
