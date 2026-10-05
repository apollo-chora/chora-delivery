// Package httpapi_test — exercises the GET /api/v1/certifications list
// handler. ADD-ONLY 2026-05-26 for the R+ /r/certifications buildout.
//
// The test stands up its OWN mux mounting only certificationsListHandler
// (parallel-safe — does NOT depend on handlers.go's NewServer mux that
// the orchestrator wires the new route into). The orchestrator's
// integration step adds the route to NewServer at integration time; until
// then this test covers the handler in isolation.
package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// listMux mounts only the new list handler so the test is decoupled
// from NewServer's mux registration (which the orchestrator owns).
func listMux(deps httpapi.Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/certifications", httpapi.CertificationsListHandler(deps))
	return mux
}

func seedCerts(t *testing.T, registry *domain.CertificationRegistry) (idA, idB string) {
	t.Helper()
	a, err := registry.Issue(tenantA, gcidA, "01970000-0000-7000-c000-000000000010", []string{"atom-1:passed"})
	if err != nil {
		t.Fatalf("seed A: %v", err)
	}
	b, err := registry.Issue(tenantA, gcidB, "01970000-0000-7000-c000-000000000011", []string{"atom-2:passed", "exam:passed"})
	if err != nil {
		t.Fatalf("seed B: %v", err)
	}
	return a.ID, b.ID
}

func TestCertificationsList_Returns200WithItemsEnvelope(t *testing.T) {
	registry := domain.NewCertificationRegistry()
	seedCerts(t, registry)
	deps := httpapi.Deps{Certifications: registry}
	srv := listMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/certifications", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", w.Code, w.Body.String())
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 items, got %d (%v)", len(resp.Items), resp.Items)
	}
	// Items must include the canonical certDTO shape — id + hash + learner_gcid + course_id + issued_at.
	for i, it := range resp.Items {
		for _, key := range []string{"id", "tenant_id", "learner_gcid", "course_id", "hash", "issued_at"} {
			if it[key] == nil {
				t.Fatalf("item %d missing %q (got %v)", i, key, it)
			}
		}
	}
}

func TestCertificationsList_RejectsMissingTenant(t *testing.T) {
	registry := domain.NewCertificationRegistry()
	deps := httpapi.Deps{Certifications: registry}
	srv := listMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/certifications", nil)
	// Intentionally no X-Tenant-Id.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (missing tenant), got %d body=%q", w.Code, w.Body.String())
	}
}

func TestCertificationsList_RejectsNonGet(t *testing.T) {
	registry := domain.NewCertificationRegistry()
	deps := httpapi.Deps{Certifications: registry}
	srv := listMux(deps)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/certifications", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestCertificationsList_FiltersTenantIsolation(t *testing.T) {
	registry := domain.NewCertificationRegistry()
	// tenantA cert
	if _, err := registry.Issue(tenantA, gcidA, "course-A", []string{"x"}); err != nil {
		t.Fatalf("seed tenantA: %v", err)
	}
	// Different tenant cert — MUST NOT appear in tenantA's list.
	otherTenant := "01970000-0000-7000-8000-000000000999"
	if _, err := registry.Issue(otherTenant, gcidA, "course-B", []string{"y"}); err != nil {
		t.Fatalf("seed otherTenant: %v", err)
	}

	srv := listMux(httpapi.Deps{Certifications: registry})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/certifications", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item (tenant isolation), got %d (%v)", len(resp.Items), resp.Items)
	}
	if resp.Items[0]["course_id"] != "course-A" {
		t.Fatalf("expected the tenantA cert (course-A), got %v", resp.Items[0]["course_id"])
	}
}

func TestCertificationsList_FilterByLearnerGCID(t *testing.T) {
	registry := domain.NewCertificationRegistry()
	if _, err := registry.Issue(tenantA, gcidA, "course-1", []string{"x"}); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if _, err := registry.Issue(tenantA, gcidB, "course-1", []string{"y"}); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	srv := listMux(httpapi.Deps{Certifications: registry})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/certifications?learner_gcid=%s", gcidB), nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item (filtered by learner), got %d", len(resp.Items))
	}
	if resp.Items[0]["learner_gcid"] != gcidB {
		t.Fatalf("expected learner_gcid=%s, got %v", gcidB, resp.Items[0]["learner_gcid"])
	}
}

func TestCertificationsList_FilterByCourseID(t *testing.T) {
	registry := domain.NewCertificationRegistry()
	if _, err := registry.Issue(tenantA, gcidA, "course-X", []string{"x"}); err != nil {
		t.Fatalf("seed X: %v", err)
	}
	if _, err := registry.Issue(tenantA, gcidA, "course-Y", []string{"y"}); err != nil {
		t.Fatalf("seed Y: %v", err)
	}

	srv := listMux(httpapi.Deps{Certifications: registry})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/certifications?course_id=course-Y", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	if resp.Items[0]["course_id"] != "course-Y" {
		t.Fatalf("expected course_id=course-Y, got %v", resp.Items[0]["course_id"])
	}
}

func TestCertificationsList_EmptyOnNoMatches(t *testing.T) {
	registry := domain.NewCertificationRegistry()
	srv := listMux(httpapi.Deps{Certifications: registry})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/certifications", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (empty list), got %d", w.Code)
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Items field MUST be present (non-nil) even when empty — never a nil slice
	// in the JSON wire shape, so the FE can iterate without a null-check.
	if resp.Items == nil {
		t.Fatalf("expected non-nil items slice in JSON, got nil — DTO must marshal as []")
	}
	if len(resp.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(resp.Items))
	}
}
