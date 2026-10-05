// offering_transition_handler_test.go — TDD coverage for the R+ Offering FSM
// transition HTTP surface (four-mode refactor W2.B, CHO-1850).
//
// Endpoints under test (PATCH-only sub-routes on /api/v1/offerings/{id}):
//
//	PATCH /api/v1/offerings/{id}/launch     DRAFT      → LAUNCHED
//	PATCH /api/v1/offerings/{id}/start      LAUNCHED   → RUNNING
//	PATCH /api/v1/offerings/{id}/conclude   RUNNING    → CONCLUDED
//	PATCH /api/v1/offerings/{id}/archive    any        → ARCHIVED (soft-delete)
//
// → 200 + offeringDTO on success; 409 on an illegal transition for the current
// state; 403 without an admin role; 401 without gcid; 400 without tenant; 404
// for an unknown / cross-tenant / soft-deleted offering or an unknown action.
//
// Drives offering_handler.go (offeringsSubHandler PATCH branch +
// handleOfferingTransition). The domain FSM (offering.go) is already covered by
// offering_search_test.go's sibling domain tests; this is the adapter wiring.
// TDD: written FIRST, RED before the handler PATCH branch existed.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mkOffering creates a DRAFT offering (as training-admin) and returns its id.
func mkOffering(t *testing.T, srv http.Handler, deliveryType string) string {
	t.Helper()
	rec := doOffering(t, srv, "POST", "/api/v1/offerings",
		createOfferingBody(deliveryType), offTestTenantID, offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusCreated {
		t.Fatalf("mkOffering: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var m map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("mkOffering: response carried no id: %s", rec.Body.String())
	}
	return id
}

// patchOffering PATCHes /api/v1/offerings/{id}/{action}.
func patchOffering(t *testing.T, srv http.Handler, id, action, tenant, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	return doOffering(t, srv, "PATCH", "/api/v1/offerings/"+id+"/"+action, "", tenant, gcid, roles)
}

// transitionState PATCHes a transition that must succeed and returns the
// resulting offering DTO.
func transitionState(t *testing.T, srv http.Handler, id, action string) map[string]interface{} {
	t.Helper()
	rec := patchOffering(t, srv, id, action, offTestTenantID, offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH %s: status=%d want 200 body=%s", action, rec.Code, rec.Body.String())
	}
	var m map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("PATCH %s: bad json: %v", action, err)
	}
	return m
}

// -----------------------------------------------------------------------------
// Happy-path transitions
// -----------------------------------------------------------------------------

func TestOfferingTransition_Launch_200(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	got := transitionState(t, srv, id, "launch")
	if got["state"] != "LAUNCHED" {
		t.Fatalf("state=%v want LAUNCHED", got["state"])
	}
	if got["launched_at"] == nil || got["launched_at"] == "" {
		t.Fatalf("launch must stamp launched_at; got %v", got["launched_at"])
	}
}

func TestOfferingTransition_Start_200(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "short")
	transitionState(t, srv, id, "launch")
	got := transitionState(t, srv, id, "start")
	if got["state"] != "RUNNING" {
		t.Fatalf("state=%v want RUNNING", got["state"])
	}
}

func TestOfferingTransition_Conclude_200(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	transitionState(t, srv, id, "launch")
	transitionState(t, srv, id, "start")
	got := transitionState(t, srv, id, "conclude")
	if got["state"] != "CONCLUDED" {
		t.Fatalf("state=%v want CONCLUDED", got["state"])
	}
	if got["concluded_at"] == nil || got["concluded_at"] == "" {
		t.Fatalf("conclude must stamp concluded_at; got %v", got["concluded_at"])
	}
}

func TestOfferingTransition_FullLifecycle(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	for _, step := range []struct{ action, want string }{
		{"launch", "LAUNCHED"},
		{"start", "RUNNING"},
		{"conclude", "CONCLUDED"},
	} {
		got := transitionState(t, srv, id, step.action)
		if got["state"] != step.want {
			t.Fatalf("after %s: state=%v want %s", step.action, got["state"], step.want)
		}
	}
}

func TestOfferingTransition_Archive_FromDraft_200(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "async")
	got := transitionState(t, srv, id, "archive")
	if got["state"] != "ARCHIVED" {
		t.Fatalf("state=%v want ARCHIVED", got["state"])
	}
	if got["archived_at"] == nil || got["deleted_at"] == nil {
		t.Fatalf("archive must stamp archived_at + deleted_at; got archived=%v deleted=%v",
			got["archived_at"], got["deleted_at"])
	}
}

func TestOfferingTransition_Archive_FromRunning_200(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	transitionState(t, srv, id, "launch")
	transitionState(t, srv, id, "start")
	got := transitionState(t, srv, id, "archive")
	if got["state"] != "ARCHIVED" {
		t.Fatalf("archive-from-RUNNING: state=%v want ARCHIVED", got["state"])
	}
}

// -----------------------------------------------------------------------------
// Illegal transitions → 409
// -----------------------------------------------------------------------------

func TestOfferingTransition_Start_FromDraft_409(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate") // DRAFT, never launched
	rec := patchOffering(t, srv, id, "start", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("start from DRAFT: status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
}

func TestOfferingTransition_Conclude_FromDraft_409(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	rec := patchOffering(t, srv, id, "conclude", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("conclude from DRAFT: status=%d want 409", rec.Code)
	}
}

func TestOfferingTransition_Launch_Twice_409(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	transitionState(t, srv, id, "launch")
	rec := patchOffering(t, srv, id, "launch", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-launch: status=%d want 409", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// AuthZ + tenancy
// -----------------------------------------------------------------------------

func TestOfferingTransition_AsLearner_403(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	rec := patchOffering(t, srv, id, "launch", offTestTenantID, offTestLearnerGCID, "learner")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("learner transition: status=%d want 403", rec.Code)
	}
}

func TestOfferingTransition_NoGCID_401(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	rec := patchOffering(t, srv, id, "launch", offTestTenantID, "", "training-admin")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-gcid transition: status=%d want 401", rec.Code)
	}
}

func TestOfferingTransition_NoTenant_400(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	rec := patchOffering(t, srv, id, "launch", "", offTestAdminGCID, "training-admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-tenant transition: status=%d want 400", rec.Code)
	}
}

func TestOfferingTransition_UnknownID_404(t *testing.T) {
	srv, _ := newOfferingServer()
	rec := patchOffering(t, srv, "01970000-0000-7000-9999-fffffffffff0", "launch",
		offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status=%d want 404", rec.Code)
	}
}

func TestOfferingTransition_CrossTenant_404(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate") // created under offTestTenantID
	rec := patchOffering(t, srv, id, "launch", offTestOtherTenant, offTestAdminGCID, "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant transition: status=%d want 404", rec.Code)
	}
}

// -----------------------------------------------------------------------------
// Routing edges
// -----------------------------------------------------------------------------

func TestOfferingTransition_UnknownAction_404(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	rec := patchOffering(t, srv, id, "frobnicate", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestOfferingTransition_NonPatchMethod_405(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	rec := doOffering(t, srv, "POST", "/api/v1/offerings/"+id+"/launch", "",
		offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST on action route: status=%d want 405", rec.Code)
	}
}

// Regression: the W1 GET /{id} leaf must still resolve after the sub-handler
// learns the {id}/{action} shape.
func TestOfferingTransition_GetByID_StillWorks(t *testing.T) {
	srv, _ := newOfferingServer()
	id := mkOffering(t, srv, "graduate")
	rec := doOffering(t, srv, "GET", "/api/v1/offerings/"+id, "", offTestTenantID, offTestAdminGCID, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /{id} regression: status=%d want 200", rec.Code)
	}
}
