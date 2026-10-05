// skillsfutures_handler_test.go — table-driven coverage of the
// SkillsFutures Claims HTTP surface. TDD: written FIRST and verified RED
// before skillsfutures_handler.go landed.
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
	sfTestTenantID    = "01970000-1111-7000-8000-000000000001"
	sfTestLearnerGCID = "01970000-1111-7000-9000-000000000001"
	sfTestAdminGCID   = "01970000-1111-7000-9000-000000000002"
	sfTestCourseID    = "01970000-1111-7000-9000-000000000003"
	sfTestNRICHash    = "sha256:0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa"
)

// newSFServer wires the bare minimum Deps needed for the skillsfutures
// routes to mount. The handler is opt-in via the SkillsFutures repo on
// Deps — when nil, the routes are not mounted.
func newSFServer() (http.Handler, *inmem.SkillsFuturesRepo) {
	repo := inmem.NewSkillsFuturesRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		SkillsFutures: repo,
	})
	return srv, repo
}

// doSF issues a request with the standard SkillsFutures headers.
func doSF(t *testing.T, h http.Handler, method, path, body, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("X-Tenant-Id", sfTestTenantID)
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

// validSFBody returns a JSON body that satisfies all NewClaim validation.
func validSFBody() string {
	return `{
		"course_id": "` + sfTestCourseID + `",
		"nric_hash": "` + sfTestNRICHash + `",
		"requested_amount_sgd_cents": 50000
	}`
}

// -----------------------------------------------------------------------------
// POST /api/v1/skillsfutures-claims (learner submits)
// -----------------------------------------------------------------------------

func TestSF_PostClaim_AsLearner_201(t *testing.T) {
	srv, _ := newSFServer()
	rec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["state"] != "PENDING" {
		t.Errorf("state=%v want PENDING", got["state"])
	}
	if got["gcid"] != sfTestLearnerGCID {
		t.Errorf("gcid=%v want %s", got["gcid"], sfTestLearnerGCID)
	}
	if got["course_id"] != sfTestCourseID {
		t.Errorf("course_id=%v want %s", got["course_id"], sfTestCourseID)
	}
	if int64(got["requested_amount_sgd_cents"].(float64)) != 50000 {
		t.Errorf("requested_amount_sgd_cents=%v want 50000", got["requested_amount_sgd_cents"])
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Errorf("id missing")
	}
}

func TestSF_PostClaim_NoTenant_400(t *testing.T) {
	srv, _ := newSFServer()
	req := httptest.NewRequest("POST", "/api/v1/skillsfutures-claims",
		bytes.NewReader([]byte(validSFBody())))
	req.Header.Set("gcid", sfTestLearnerGCID)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestSF_PostClaim_NoGCID_401(t *testing.T) {
	srv, _ := newSFServer()
	rec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSF_PostClaim_InvalidBody_400(t *testing.T) {
	srv, _ := newSFServer()
	body := `{"course_id":"` + sfTestCourseID + `","nric_hash":"` + sfTestNRICHash + `","requested_amount_sgd_cents":0}`
	rec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", body, sfTestLearnerGCID, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSF_PostClaim_BadJSON_400(t *testing.T) {
	srv, _ := newSFServer()
	rec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", `{not-json}`, sfTestLearnerGCID, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/skillsfutures-claims (training-admin lists)
// -----------------------------------------------------------------------------

func TestSF_ListClaims_AsTrainingAdmin_EmptyOK(t *testing.T) {
	srv, _ := newSFServer()
	rec := doSF(t, srv, "GET", "/api/v1/skillsfutures-claims", "", sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("items len=%d want 0", len(items))
	}
}

func TestSF_ListClaims_AsTrainingAdmin_AfterSubmission(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	if postRec.Code != http.StatusCreated {
		t.Fatalf("post failed: %d body=%s", postRec.Code, postRec.Body.String())
	}
	rec := doSF(t, srv, "GET", "/api/v1/skillsfutures-claims", "", sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]interface{})
	if len(items) != 1 {
		t.Errorf("items len=%d want 1", len(items))
	}
}

func TestSF_ListClaims_StateFilter(t *testing.T) {
	srv, _ := newSFServer()
	// submit two
	_ = doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	postRec2 := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	if postRec2.Code != http.StatusCreated {
		t.Fatalf("post 2 failed: %d", postRec2.Code)
	}
	var c2 map[string]interface{}
	_ = json.Unmarshal(postRec2.Body.Bytes(), &c2)
	id2 := c2["id"].(string)
	// reject one
	rejRec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id2+"/reject",
		`{"rejection_reason":"missing docs"}`,
		sfTestAdminGCID, "training-admin")
	if rejRec.Code != http.StatusOK {
		t.Fatalf("reject failed: %d body=%s", rejRec.Code, rejRec.Body.String())
	}

	pendRec := doSF(t, srv, "GET",
		"/api/v1/skillsfutures-claims?state=PENDING", "",
		sfTestAdminGCID, "training-admin")
	if pendRec.Code != http.StatusOK {
		t.Fatalf("list pending status=%d", pendRec.Code)
	}
	var pendBody map[string]interface{}
	_ = json.Unmarshal(pendRec.Body.Bytes(), &pendBody)
	pendItems, _ := pendBody["items"].([]interface{})
	if len(pendItems) != 1 {
		t.Errorf("pending len=%d want 1", len(pendItems))
	}

	rejListRec := doSF(t, srv, "GET",
		"/api/v1/skillsfutures-claims?state=REJECTED", "",
		sfTestAdminGCID, "training-admin")
	var rejBody map[string]interface{}
	_ = json.Unmarshal(rejListRec.Body.Bytes(), &rejBody)
	rejItems, _ := rejBody["items"].([]interface{})
	if len(rejItems) != 1 {
		t.Errorf("rejected len=%d want 1", len(rejItems))
	}
}

func TestSF_ListClaims_InvalidStateFilter_400(t *testing.T) {
	srv, _ := newSFServer()
	rec := doSF(t, srv, "GET",
		"/api/v1/skillsfutures-claims?state=BOGUS", "",
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSF_ListClaims_AsLearner_403(t *testing.T) {
	srv, _ := newSFServer()
	rec := doSF(t, srv, "GET", "/api/v1/skillsfutures-claims", "", sfTestLearnerGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/skillsfutures-claims/{id}
// -----------------------------------------------------------------------------

func TestSF_GetClaim_AsTrainingAdmin(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	rec := doSF(t, srv, "GET", "/api/v1/skillsfutures-claims/"+id, "",
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var fetched map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &fetched)
	if fetched["id"] != id {
		t.Errorf("id=%v want %s", fetched["id"], id)
	}
}

func TestSF_GetClaim_AsOwnerLearner(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	rec := doSF(t, srv, "GET", "/api/v1/skillsfutures-claims/"+id, "",
		sfTestLearnerGCID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSF_GetClaim_AsAnotherLearner_404(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)
	otherLearner := "01970000-1111-7000-9000-000000099999"

	rec := doSF(t, srv, "GET", "/api/v1/skillsfutures-claims/"+id, "",
		otherLearner, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

func TestSF_GetClaim_NotFound_404(t *testing.T) {
	srv, _ := newSFServer()
	rec := doSF(t, srv, "GET",
		"/api/v1/skillsfutures-claims/missing-id", "",
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/skillsfutures-claims/{id}/approve
// -----------------------------------------------------------------------------

func TestSF_Approve_AsTrainingAdmin_200(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	rec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/approve",
		`{"approved_amount_sgd_cents":40000}`,
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var approved map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &approved)
	if approved["state"] != "APPROVED" {
		t.Errorf("state=%v want APPROVED", approved["state"])
	}
	if int64(approved["approved_amount_sgd_cents"].(float64)) != 40000 {
		t.Errorf("approved_amount_sgd_cents=%v want 40000", approved["approved_amount_sgd_cents"])
	}
	if approved["decided_by_gcid"] != sfTestAdminGCID {
		t.Errorf("decided_by_gcid=%v want %s", approved["decided_by_gcid"], sfTestAdminGCID)
	}
}

func TestSF_Approve_AsLearner_403(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	rec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/approve",
		`{"approved_amount_sgd_cents":40000}`,
		sfTestLearnerGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestSF_Approve_OverRequested_400(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	rec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/approve",
		`{"approved_amount_sgd_cents":99999}`,
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestSF_Approve_AfterReject_409(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	_ = doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/reject",
		`{"rejection_reason":"first"}`, sfTestAdminGCID, "training-admin")

	rec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/approve",
		`{"approved_amount_sgd_cents":40000}`,
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/skillsfutures-claims/{id}/reject
// -----------------------------------------------------------------------------

func TestSF_Reject_AsTrainingAdmin_200(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	rec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/reject",
		`{"rejection_reason":"missing supporting docs"}`,
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var rejected map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &rejected)
	if rejected["state"] != "REJECTED" {
		t.Errorf("state=%v want REJECTED", rejected["state"])
	}
	if rejected["rejection_reason"] != "missing supporting docs" {
		t.Errorf("rejection_reason=%v want missing supporting docs", rejected["rejection_reason"])
	}
}

func TestSF_Reject_AsLearner_403(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	rec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/reject",
		`{"rejection_reason":"trying"}`,
		sfTestLearnerGCID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rec.Code)
	}
}

func TestSF_Reject_EmptyReason_400(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	rec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/reject",
		`{"rejection_reason":""}`,
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rec.Code)
	}
}

func TestSF_Reject_AfterApprove_409(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	_ = doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/approve",
		`{"approved_amount_sgd_cents":40000}`,
		sfTestAdminGCID, "training-admin")

	rec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/reject",
		`{"rejection_reason":"too late"}`,
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusConflict {
		t.Errorf("status=%d want 409", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Method-not-allowed paths
// -----------------------------------------------------------------------------

func TestSF_RootHandler_PUT_405(t *testing.T) {
	srv, _ := newSFServer()
	rec := doSF(t, srv, "PUT", "/api/v1/skillsfutures-claims", "", sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d want 405", rec.Code)
	}
}

func TestSF_SubHandler_UnknownAction_404(t *testing.T) {
	srv, _ := newSFServer()
	postRec := doSF(t, srv, "POST", "/api/v1/skillsfutures-claims", validSFBody(), sfTestLearnerGCID, "")
	var got map[string]interface{}
	_ = json.Unmarshal(postRec.Body.Bytes(), &got)
	id := got["id"].(string)

	rec := doSF(t, srv, "POST",
		"/api/v1/skillsfutures-claims/"+id+"/bogus", "",
		sfTestAdminGCID, "training-admin")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rec.Code)
	}
}
