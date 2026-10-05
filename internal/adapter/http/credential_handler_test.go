// credential_handler_test.go — handler-level verification of the operator
// Credential-with-competencies catalogue surface (ADR-216 WS-1):
//
//	POST /api/v1/credentials       — operator create (admin-gated)
//	GET  /api/v1/credentials       — catalogue list (tenant-scoped read)
//	GET  /api/v1/credentials/{id}  — catalogue read one (tenant-scoped 404)
//
// Self-contained harness (own cred-prefixed consts + doCredential helper) in the
// offering_handler_test.go style, wiring just the Credentials port with the
// repo/inmem store so the routes resolve DB-free. Immutability-by-learners is a
// role property: a learner role is 403 on create, and there is no learner write
// path — the learner only ever READS the catalogue.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
)

const (
	credTenantA     = "019e2f93-d586-71b5-8c3d-e2b0d0d70100"
	credTenantB     = "019e2f93-d586-71b5-8c3d-e2b0d0d70199"
	credAdminGCID   = "019e2f93-d586-71b5-8c3d-e2b0d0d70101"
	credLearnerGCID = "019e2f93-d586-71b5-8c3d-e2b0d0d70102"
)

func newCredentialTestServer(t *testing.T) (http.Handler, *repoinmem.CredentialRepo) {
	t.Helper()
	repo := repoinmem.NewCredentialRepo()
	srv := httpapi.NewServer(httpapi.Deps{Credentials: repo})
	return srv, repo
}

// doCredential fires a request with the standard mesh headers and returns the
// recorder. roles empty ⇒ no x-mesh-user-roles header.
func doCredential(t *testing.T, h http.Handler, method, path, body, tenant, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if tenant != "" {
		r.Header.Set("X-Tenant-Id", tenant)
	}
	if gcid != "" {
		r.Header.Set("gcid", gcid)
	}
	if roles != "" {
		r.Header.Set("x-mesh-user-roles", roles)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

const credCreateBody = `{
  "title": "Project Management Professional",
  "code": "PMP",
  "issuing_body": "PMI",
  "description": "Globally recognised project-management credential.",
  "competencies": [
    {"code": "People", "name": "People", "weight": 42},
    {"code": "Process", "name": "Process", "weight": 50},
    {"code": "Business Environment", "name": "Business Environment", "weight": 8}
  ]
}`

func createOneCredential(t *testing.T, srv http.Handler) string {
	t.Helper()
	rec := doCredential(t, srv, http.MethodPost, "/api/v1/credentials", credCreateBody, credTenantA, credAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201; got %d (%s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("create: decode: %v", err)
	}
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("create: no id in response %s", rec.Body.String())
	}
	return id
}

func TestCredentialCreate_OK(t *testing.T) {
	srv, _ := newCredentialTestServer(t)
	rec := doCredential(t, srv, http.MethodPost, "/api/v1/credentials", credCreateBody, credTenantA, credAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201; got %d (%s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["title"] != "Project Management Professional" || got["code"] != "PMP" {
		t.Fatalf("header fields: got %v", got)
	}
	comps, ok := got["competencies"].([]any)
	if !ok || len(comps) != 3 {
		t.Fatalf("want 3 competencies; got %v", got["competencies"])
	}
	first, _ := comps[0].(map[string]any)
	if first["code"] != "people" || first["name"] != "People" {
		t.Fatalf("competency normalisation/shape: got %v", first)
	}
	if first["competency_id"] == nil || first["competency_id"] == "" {
		t.Fatalf("competency must carry a generated id; got %v", first)
	}
}

func TestCredentialCreate_ForbiddenForLearner(t *testing.T) {
	srv, _ := newCredentialTestServer(t)
	rec := doCredential(t, srv, http.MethodPost, "/api/v1/credentials", credCreateBody, credTenantA, credLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("learner create must be 403 (learner-immutable); got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestCredentialCreate_AuthGuards(t *testing.T) {
	srv, _ := newCredentialTestServer(t)
	// missing tenant → 400
	if rec := doCredential(t, srv, http.MethodPost, "/api/v1/credentials", credCreateBody, "", credAdminGCID, "admin"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant: want 400; got %d", rec.Code)
	}
	// missing gcid → 401
	if rec := doCredential(t, srv, http.MethodPost, "/api/v1/credentials", credCreateBody, credTenantA, "", "admin"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing gcid: want 401; got %d", rec.Code)
	}
}

func TestCredentialCreate_ValidationError(t *testing.T) {
	srv, _ := newCredentialTestServer(t)
	body := `{"title": "Empty", "competencies": []}`
	rec := doCredential(t, srv, http.MethodPost, "/api/v1/credentials", body, credTenantA, credAdminGCID, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-competency credential must be 400; got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestCredentialCreate_WiringUnavailable(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{}) // Credentials nil
	rec := doCredential(t, srv, http.MethodPost, "/api/v1/credentials", credCreateBody, credTenantA, credAdminGCID, "admin")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil Credentials repo must 503; got %d", rec.Code)
	}
}

func TestCredentialList_CatalogueIsTenantScoped(t *testing.T) {
	srv, _ := newCredentialTestServer(t)
	createOneCredential(t, srv)

	// Tenant A sees its catalogue.
	rec := doCredential(t, srv, http.MethodGet, "/api/v1/credentials", "", credTenantA, credLearnerGCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: want 200; got %d (%s)", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(listResp.Items) != 1 {
		t.Fatalf("tenant A catalogue want 1; got %d", len(listResp.Items))
	}
	// The catalogue read carries the competency breakdown.
	comps, _ := listResp.Items[0]["competencies"].([]any)
	if len(comps) != 3 {
		t.Fatalf("catalogue item must carry competencies; got %v", listResp.Items[0]["competencies"])
	}

	// Tenant B sees an empty catalogue (RLS/tenant scoping).
	recB := doCredential(t, srv, http.MethodGet, "/api/v1/credentials", "", credTenantB, credLearnerGCID, "learner")
	var listB struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(recB.Body.Bytes(), &listB)
	if len(listB.Items) != 0 {
		t.Fatalf("tenant B must see none of tenant A's credentials; got %d", len(listB.Items))
	}
}

func TestCredentialGet_OKAndNotFound(t *testing.T) {
	srv, _ := newCredentialTestServer(t)
	id := createOneCredential(t, srv)

	rec := doCredential(t, srv, http.MethodGet, "/api/v1/credentials/"+id, "", credTenantA, credLearnerGCID, "learner")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: want 200; got %d (%s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("get decode: %v", err)
	}
	if got["id"] != id {
		t.Fatalf("get: id mismatch; got %v want %s", got["id"], id)
	}

	// Unknown id → 404.
	if r := doCredential(t, srv, http.MethodGet, "/api/v1/credentials/does-not-exist", "", credTenantA, credLearnerGCID, "learner"); r.Code != http.StatusNotFound {
		t.Fatalf("unknown id: want 404; got %d", r.Code)
	}
	// Cross-tenant read → 404 (tenant B may not read tenant A's credential).
	if r := doCredential(t, srv, http.MethodGet, "/api/v1/credentials/"+id, "", credTenantB, credLearnerGCID, "learner"); r.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get: want 404; got %d", r.Code)
	}
}

func TestCredentialRoot_MethodNotAllowed(t *testing.T) {
	srv, _ := newCredentialTestServer(t)
	if rec := doCredential(t, srv, http.MethodDelete, "/api/v1/credentials", "", credTenantA, credAdminGCID, "admin"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE on collection: want 405; got %d", rec.Code)
	}
}
