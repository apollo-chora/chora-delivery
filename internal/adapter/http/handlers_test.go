// Package httpapi_test exercises the HTTP adapter against the in-memory
// repos. These tests verify endpoint contracts (status codes + JSON
// envelope) without standing up the whole service — wiring is done via
// NewServer().
package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	gcidA   = "01970000-0000-7000-9000-000000000001"
	gcidB   = "01970000-0000-7000-9000-000000000002"
	gcidC   = "01970000-0000-7000-9000-000000000003"
)

func newTestServer() http.Handler {
	return httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
	})
}

// reqJSON wraps a JSON POST/PATCH with the mandatory tenant + gcid headers.
func reqJSON(t *testing.T, srv http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// reqGET wraps a GET with the mandatory tenant + gcid headers.
func reqGET(t *testing.T, srv http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// -----------------------------------------------------------------------------
// Health
// -----------------------------------------------------------------------------

func TestHealth_OK(t *testing.T) {
	srv := newTestServer()
	w := reqGET(t, srv, "/healthz")
	if w.Code != http.StatusOK {
		t.Fatalf("/healthz: status %d, body %q", w.Code, w.Body.String())
	}
}

func TestReady_OK(t *testing.T) {
	srv := newTestServer()
	w := reqGET(t, srv, "/readyz")
	if w.Code != http.StatusOK {
		t.Fatalf("/readyz: status %d, body %q", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Courses
// -----------------------------------------------------------------------------

func TestCreateCourse_201(t *testing.T) {
	srv := newTestServer()
	w := reqJSON(t, srv, http.MethodPost, "/api/courses", map[string]interface{}{
		"title":        "Intro to Architecture",
		"atom_ids":     []string{"a1", "a2"},
		"max_capacity": 10,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/courses: status %d, body %q", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["id"] == nil || got["id"].(string) == "" {
		t.Fatalf("expected id in response, got %v", got)
	}
}

func TestCreateCourse_RejectsMissingTenant(t *testing.T) {
	srv := newTestServer()
	body, _ := json.Marshal(map[string]interface{}{
		"title": "x", "max_capacity": 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/courses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Missing X-Tenant-Id
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreateCourse_RejectsBadJSON(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/api/courses", strings.NewReader("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGetCourse_404(t *testing.T) {
	srv := newTestServer()
	w := reqGET(t, srv, "/api/courses/does-not-exist")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGetCourse_200(t *testing.T) {
	srv := newTestServer()
	createW := reqJSON(t, srv, http.MethodPost, "/api/courses", map[string]interface{}{
		"title": "x", "max_capacity": 1,
	})
	var created map[string]interface{}
	_ = json.Unmarshal(createW.Body.Bytes(), &created)
	id := created["id"].(string)

	getW := reqGET(t, srv, "/api/courses/"+id)
	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", getW.Code, getW.Body.String())
	}
}

// ADR-236 D1 — deps.Courses.Get is now tenant-scoped (mirrors
// pg.CourseRepo.Get(ctx, tenantID, courseID)); a caller in a DIFFERENT
// tenant must never read the course, even holding its exact id. Before D1
// this call site never threaded tenant into ctx and Get had no tenant
// param at all, so a cross-tenant read of the in-memory store was possible
// (caught only by a manual post-fetch header compare this test also
// guards stays correct after that check is removed as redundant).
func TestGetCourse_404OnCrossTenant(t *testing.T) {
	srv := newTestServer()
	createW := reqJSON(t, srv, http.MethodPost, "/api/courses", map[string]interface{}{
		"title": "tenantA's course", "max_capacity": 1,
	})
	var created map[string]interface{}
	_ = json.Unmarshal(createW.Body.Bytes(), &created)
	id := created["id"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/courses/"+id, nil)
	req.Header.Set("X-Tenant-Id", "01970000-0000-7000-8000-0000000000bb") // tenantB
	req.Header.Set("gcid", gcidA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant GET /api/courses/{id}: expected 404, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestListCourses_FiltersByTenant(t *testing.T) {
	srv := newTestServer()
	_ = reqJSON(t, srv, http.MethodPost, "/api/courses", map[string]interface{}{
		"title": "x", "max_capacity": 1,
	})
	_ = reqJSON(t, srv, http.MethodPost, "/api/courses", map[string]interface{}{
		"title": "y", "max_capacity": 2,
	})
	w := reqGET(t, srv, "/api/courses")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Items) != 2 || resp.Total != 2 {
		t.Fatalf("expected 2 items + total=2, got %d / %d", len(resp.Items), resp.Total)
	}
}

// -----------------------------------------------------------------------------
// Certifications
// -----------------------------------------------------------------------------

func TestIssueCertification_201_AndAppendOnly(t *testing.T) {
	srv := newTestServer()
	w := reqJSON(t, srv, http.MethodPost, "/api/certifications", map[string]interface{}{
		"learner_gcid":    gcidB,
		"course_id":       "01970000-0000-7000-c000-000000000001",
		"accomplishments": []string{"atom-1:passed", "exam:passed"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%q", w.Code, w.Body.String())
	}
	var cert map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &cert)
	if cert["hash"] == nil || cert["hash"].(string) == "" {
		t.Fatalf("expected hash in response")
	}

	// Re-issue must fail with 409.
	w2 := reqJSON(t, srv, http.MethodPost, "/api/certifications", map[string]interface{}{
		"learner_gcid":    gcidB,
		"course_id":       "01970000-0000-7000-c000-000000000001",
		"accomplishments": []string{"atom-1:passed", "exam:passed"},
	})
	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409 on re-issue, got %d", w2.Code)
	}
}

func TestGetCertification_200(t *testing.T) {
	srv := newTestServer()
	w := reqJSON(t, srv, http.MethodPost, "/api/certifications", map[string]interface{}{
		"learner_gcid":    gcidB,
		"course_id":       "01970000-0000-7000-c000-000000000002",
		"accomplishments": []string{"x"},
	})
	var cert map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &cert)
	id := cert["id"].(string)

	getW := reqGET(t, srv, "/api/certifications/"+id)
	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getW.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(getW.Body.Bytes(), &got)
	if got["hash"] != cert["hash"] {
		t.Fatalf("hash mismatch on GET: %v vs %v", got["hash"], cert["hash"])
	}
}

func TestGetCertification_404OnUnknown(t *testing.T) {
	srv := newTestServer()
	w := reqGET(t, srv, "/api/certifications/does-not-exist")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
