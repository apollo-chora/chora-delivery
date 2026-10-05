// Tests for GET /v1/campus — list campuses by current tenant.
//
// R+ M15a /r/campusops route — adds the missing list endpoint to the
// existing /v1/campus resource group (POST + GET-by-id already shipped
// in v1_handlers.go). The list handler reads the tenant from the
// X-Tenant-Id mesh-claim header and returns
// `{items: [CampusDTO]}` containing only non-soft-deleted rows whose
// `tenant_id` matches the caller.
//
// TDD strict: these tests are written before the handler exists; the
// suite MUST fail with a 4xx (method-not-allowed on the existing
// campusHandler) until the wrapper dispatcher lands in handlers.go and
// the new list handler is implemented.
package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// -----------------------------------------------------------------------------
// Local helpers (kept in this file so we don't touch handlers_test.go)
// -----------------------------------------------------------------------------

func newGETRequest(path string) *http.Request {
	return httptest.NewRequest(http.MethodGet, path, nil)
}

func serveAndRecord(srv http.Handler, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// mustPostCampus seeds a campus under tenantA via the live POST handler so
// the list tests exercise the same write path real callers use.
func mustPostCampus(t *testing.T, srv http.Handler, name, country string) {
	t.Helper()
	body := map[string]interface{}{"name": name, "country": country}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/campus", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed POST /v1/campus failed: %d body=%q", w.Code, w.Body.String())
	}
}

func TestV1Campus_GetListEmpty(t *testing.T) {
	srv, _ := newV1Server()
	w := reqGET(t, srv, "/v1/campus")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/campus: %d body=%q", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, ok := got["items"].([]interface{})
	if !ok {
		t.Fatalf("expected items array, got %T", got["items"])
	}
	if len(items) != 0 {
		t.Fatalf("expected empty items, got %d", len(items))
	}
}

func TestV1Campus_GetListReturnsTenantCampuses(t *testing.T) {
	srv, _ := newV1Server()
	// Seed 2 campuses under tenantA via POST.
	mustPostCampus(t, srv, "MTM SG — Bras Basah", "SG")
	mustPostCampus(t, srv, "MTM SG — Bishan", "SG")

	w := reqGET(t, srv, "/v1/campus")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/campus: %d body=%q", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// Verify DTO shape on first row.
	row := items[0].(map[string]interface{})
	for _, key := range []string{"id", "tenant_id", "name", "country", "created_at"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("DTO missing key %q in %v", key, row)
		}
	}
	if row["tenant_id"] != tenantA {
		t.Fatalf("expected tenant_id=%q, got %v", tenantA, row["tenant_id"])
	}
}

func TestV1Campus_GetListScopedByTenant(t *testing.T) {
	srv, _ := newV1Server()
	mustPostCampus(t, srv, "MTM SG — Bras Basah", "SG")

	// GET under a different tenant — the seeded row is invisible.
	req := newGETRequest("/v1/campus")
	req.Header.Set("X-Tenant-Id", "01970000-0000-7000-8000-000000000099")
	req.Header.Set("gcid", gcidA)
	w := serveAndRecord(srv, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/campus: %d", w.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 0 {
		t.Fatalf("expected 0 items for other tenant, got %d", len(items))
	}
}

func TestV1Campus_GetListRequiresTenant(t *testing.T) {
	srv, _ := newV1Server()
	req := newGETRequest("/v1/campus")
	// No X-Tenant-Id header.
	w := serveAndRecord(srv, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without tenant header, got %d", w.Code)
	}
}

func TestV1Campus_PostStillWorksAfterListWrapper(t *testing.T) {
	// Guard: registering the GET list handler MUST NOT break the existing
	// POST behaviour — the method-dispatch wrapper preserves both verbs.
	srv, _ := newV1Server()
	w := reqJSON(t, srv, http.MethodPost, "/v1/campus", map[string]interface{}{
		"name":    "PostStillWorks",
		"country": "SG",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /v1/campus after list wrapper: %d body=%q", w.Code, w.Body.String())
	}
}
