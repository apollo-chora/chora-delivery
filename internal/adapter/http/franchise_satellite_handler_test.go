// franchise_satellite_handler_test.go: TDD for the write-restricted
// franchise_satellite admin path (ADR-192 D1 mapping authority, W5
// bypass-free slice, CHO-2230).
//
// The mapping is the authoritative, revocable scope of the future exam
// rollup, so writes are restricted to the platform operator or an admin of
// the OWNER tenant (ADR-192 O1 posture). Exercised THROUGH the mux.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

const (
	fsOwnerTenant = "019e2f93-d586-71b5-8c3d-e2b0d0d50001"
	fsOtherTenant = "019e2f93-d586-71b5-8c3d-e2b0d0d50002"
	fsSatelliteA  = "019e2f93-d586-71b5-8c3d-e2b0d0d50003"
	fsSatelliteB  = "019e2f93-d586-71b5-8c3d-e2b0d0d50004"
	fsAdminGCID   = "019e2f93-d586-71b5-8c3d-e2b0d0d5ad01"
)

func newFranchiseServer() (http.Handler, *inmem.FranchiseSatelliteRepo) {
	store := inmem.NewFranchiseSatelliteRepo()
	mux := http.NewServeMux()
	httpapi.RegisterFranchiseSatelliteRoutes(mux, httpapi.FranchiseSatelliteDeps{Store: store})
	return mux, store
}

func doFranchise(t *testing.T, srv http.Handler, method, path, body, tenantID, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
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
	srv.ServeHTTP(rec, req)
	return rec
}

const fsPath = "/api/v1/franchise-satellites"

// -----------------------------------------------------------------------------
// Create
// -----------------------------------------------------------------------------

func TestFranchise_Create_AsOwnerTenantAdmin_201(t *testing.T) {
	srv, _ := newFranchiseServer()
	rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteA+`"}`,
		fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["owner_tenant_id"] != fsOwnerTenant {
		t.Errorf("owner=%v want caller tenant %s", got["owner_tenant_id"], fsOwnerTenant)
	}
	if got["satellite_tenant_id"] != fsSatelliteA {
		t.Errorf("satellite=%v", got["satellite_tenant_id"])
	}
	if got["created_by_gcid"] != fsAdminGCID {
		t.Errorf("created_by=%v want %s", got["created_by_gcid"], fsAdminGCID)
	}
	if got["id"] == nil || got["id"] == "" {
		t.Error("id missing")
	}
}

func TestFranchise_Create_AdminCannotNameAnotherOwner_403(t *testing.T) {
	srv, _ := newFranchiseServer()
	rec := doFranchise(t, srv, "POST", fsPath,
		`{"owner_tenant_id":"`+fsOtherTenant+`","satellite_tenant_id":"`+fsSatelliteA+`"}`,
		fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestFranchise_Create_AsPlatformOperator_ForAnyOwner_201(t *testing.T) {
	srv, _ := newFranchiseServer()
	rec := doFranchise(t, srv, "POST", fsPath,
		`{"owner_tenant_id":"`+fsOtherTenant+`","satellite_tenant_id":"`+fsSatelliteA+`"}`,
		fsOwnerTenant, fsAdminGCID, "platform_operator")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["owner_tenant_id"] != fsOtherTenant {
		t.Errorf("owner=%v want %s", got["owner_tenant_id"], fsOtherTenant)
	}
}

func TestFranchise_Create_OperatorMustNameOwner_400(t *testing.T) {
	srv, _ := newFranchiseServer()
	rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteA+`"}`,
		fsOwnerTenant, fsAdminGCID, "platform_operator")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "owner_tenant_id") {
		t.Errorf("400 body must name owner_tenant_id: %s", rec.Body.String())
	}
}

func TestFranchise_Create_RoleGauntlet_403(t *testing.T) {
	srv, _ := newFranchiseServer()
	for _, roles := range []string{"instructor", "learner", "training-admin", ""} {
		rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteA+`"}`,
			fsOwnerTenant, fsAdminGCID, roles)
		if rec.Code != http.StatusForbidden {
			t.Errorf("roles=%q status=%d want 403", roles, rec.Code)
		}
	}
}

func TestFranchise_Create_NoGCID_401(t *testing.T) {
	srv, _ := newFranchiseServer()
	rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteA+`"}`,
		fsOwnerTenant, "", "tenant_admin")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestFranchise_Create_NoTenant_400(t *testing.T) {
	srv, _ := newFranchiseServer()
	rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteA+`"}`,
		"", fsAdminGCID, "tenant_admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestFranchise_Create_Duplicate_409(t *testing.T) {
	srv, _ := newFranchiseServer()
	body := `{"satellite_tenant_id":"` + fsSatelliteA + `"}`
	if rec := doFranchise(t, srv, "POST", fsPath, body, fsOwnerTenant, fsAdminGCID, "tenant_admin"); rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d", rec.Code)
	}
	rec := doFranchise(t, srv, "POST", fsPath, body, fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestFranchise_Create_Validation_400(t *testing.T) {
	srv, _ := newFranchiseServer()
	cases := map[string]string{
		"self mapping":       `{"satellite_tenant_id":"` + fsOwnerTenant + `"}`,
		"bad satellite uuid": `{"satellite_tenant_id":"not-a-uuid"}`,
		"missing satellite":  `{}`,
		"not json":           `nope`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := doFranchise(t, srv, "POST", fsPath, body, fsOwnerTenant, fsAdminGCID, "tenant_admin")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// -----------------------------------------------------------------------------
// List
// -----------------------------------------------------------------------------

func TestFranchise_List_ScopedToCallerTenant(t *testing.T) {
	srv, _ := newFranchiseServer()
	if rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteA+`"}`,
		fsOwnerTenant, fsAdminGCID, "tenant_admin"); rec.Code != http.StatusCreated {
		t.Fatalf("seed A: %d", rec.Code)
	}
	if rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteB+`"}`,
		fsOtherTenant, fsAdminGCID, "tenant_admin"); rec.Code != http.StatusCreated {
		t.Fatalf("seed B: %d", rec.Code)
	}

	rec := doFranchise(t, srv, "GET", fsPath, "", fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []map[string]interface{} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Items) != 1 {
		t.Fatalf("items=%d want 1 (own tenant only)", len(got.Items))
	}
	if got.Items[0]["satellite_tenant_id"] != fsSatelliteA {
		t.Errorf("item=%+v", got.Items[0])
	}
}

func TestFranchise_List_AdminCannotQueryAnotherOwner_403(t *testing.T) {
	srv, _ := newFranchiseServer()
	rec := doFranchise(t, srv, "GET", fsPath+"?owner_tenant_id="+fsOtherTenant, "",
		fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
}

func TestFranchise_List_OperatorQueriesAnyOwner_200(t *testing.T) {
	srv, _ := newFranchiseServer()
	if rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteB+`"}`,
		fsOtherTenant, fsAdminGCID, "tenant_admin"); rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d", rec.Code)
	}
	rec := doFranchise(t, srv, "GET", fsPath+"?owner_tenant_id="+fsOtherTenant, "",
		fsOwnerTenant, fsAdminGCID, "platform_operator")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []map[string]interface{} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Items) != 1 {
		t.Fatalf("items=%d want 1", len(got.Items))
	}
}

// -----------------------------------------------------------------------------
// Revoke (soft delete)
// -----------------------------------------------------------------------------

func TestFranchise_Revoke_200_ThenGone(t *testing.T) {
	srv, _ := newFranchiseServer()
	rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteA+`"}`,
		fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d", rec.Code)
	}
	var created map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	del := doFranchise(t, srv, "DELETE", fsPath+"/"+id, "", fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if del.Code != http.StatusOK {
		t.Fatalf("revoke: %d want 200 body=%s", del.Code, del.Body.String())
	}
	var revoked map[string]interface{}
	_ = json.Unmarshal(del.Body.Bytes(), &revoked)
	if revoked["revoked"] != true {
		t.Errorf("revoked=%v want true", revoked["revoked"])
	}

	// Gone from the list (rollup scope shrinks immediately).
	list := doFranchise(t, srv, "GET", fsPath, "", fsOwnerTenant, fsAdminGCID, "tenant_admin")
	var got struct {
		Items []map[string]interface{} `json:"items"`
	}
	_ = json.Unmarshal(list.Body.Bytes(), &got)
	if len(got.Items) != 0 {
		t.Fatalf("items=%d want 0 after revoke", len(got.Items))
	}

	// A second revoke is a genuine miss.
	again := doFranchise(t, srv, "DELETE", fsPath+"/"+id, "", fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if again.Code != http.StatusNotFound {
		t.Fatalf("second revoke: %d want 404", again.Code)
	}
}

func TestFranchise_Revoke_CrossTenant_404(t *testing.T) {
	srv, _ := newFranchiseServer()
	rec := doFranchise(t, srv, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteA+`"}`,
		fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d", rec.Code)
	}
	var created map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	// Another tenant's admin probes the id: must read as not-found, never 403
	// (existence must not leak across tenants).
	del := doFranchise(t, srv, "DELETE", fsPath+"/"+id, "", fsOtherTenant, fsAdminGCID, "tenant_admin")
	if del.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant revoke: %d want 404", del.Code)
	}
}

func TestFranchise_Revoke_RoleGauntlet_403(t *testing.T) {
	srv, _ := newFranchiseServer()
	del := doFranchise(t, srv, "DELETE", fsPath+"/some-id", "", fsOwnerTenant, fsAdminGCID, "instructor")
	if del.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", del.Code)
	}
}

// -----------------------------------------------------------------------------
// Wiring honesty
// -----------------------------------------------------------------------------

func TestFranchise_UnwiredStore_503(t *testing.T) {
	mux := http.NewServeMux()
	httpapi.RegisterFranchiseSatelliteRoutes(mux, httpapi.FranchiseSatelliteDeps{})
	rec := doFranchise(t, mux, "POST", fsPath, `{"satellite_tenant_id":"`+fsSatelliteA+`"}`,
		fsOwnerTenant, fsAdminGCID, "tenant_admin")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
}
