// instructor_courses_handler_test.go — tests for the by-instructor course
// list endpoint that closes debt #4 / A6 (FE instructor-roster surface).
//
// Endpoint: GET /api/v1/instructors/{instructor_gcid}/courses
//
// Strict TDD: tests authored before the handler implementation. Coverage:
//   - 401 on missing X-Tenant-Id
//   - 422 on malformed UUID path param
//   - 403 when caller is neither the instructor nor instructor/admin role
//   - 200 self-read (caller_gcid == path.instructor_gcid)
//   - 200 admin-read (caller has admin role, different gcid)
//   - 200 instructor-role-read (caller has instructor role, different gcid)
//   - 200 empty list (no courses authored)
//   - 200 with pagination
//   - 405 on non-GET method
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	// phyllisInstructorGCID is the gcid used in handoff-fe-to-be docs +
	// chora-infra/seed/phyllis/04_delivery.sql for Phyllis's instructor row.
	phyllisInstructorGCID = "00000000-0000-7000-8000-000000001999"
	otherGCID             = "00000000-0000-7000-8000-000000002222"
)

// newInstructorRosterServer wires a fresh server with a seeded in-memory
// catalogue that has Phyllis as the instructor on a single course inside
// tenantA. tenantB has a different course (cross-tenant isolation guard).
func newInstructorRosterServer(t *testing.T) (http.Handler, *domain.Catalogue) {
	t.Helper()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	cat := domain.NewCatalogue()
	// Seed: Phyllis authors 1 course in tenantA.
	pc, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       tenantA,
		Title:          "MTM Tuition Math Bootcamp",
		InstructorGCID: phyllisInstructorGCID,
		InstructorName: "Phyllis",
		Visibility:     domain.VisibilityTenantOnly,
	})
	if err != nil {
		t.Fatalf("seed: NewPublicCourse: %v", err)
	}
	cat.Save(pc)
	// Cross-tenant guard: a course in some OTHER tenant authored by the same
	// gcid must NEVER surface in the tenantA listing.
	pcOther, err := domain.NewPublicCourse(domain.NewPublicCourseInput{
		TenantID:       "01970000-0000-7000-8000-0000000000bb",
		Title:          "OTHER tenant course",
		InstructorGCID: phyllisInstructorGCID,
		InstructorName: "Phyllis",
		Visibility:     domain.VisibilityTenantOnly,
	})
	if err != nil {
		t.Fatalf("seed: NewPublicCourse other: %v", err)
	}
	cat.Save(pcOther)

	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogueFrom(cat),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      pub,
		CampusOps:      repoinmem.NewCampusRepo(),
	})
	return srv, cat
}

// reqGETAs builds a GET with X-Tenant-Id + custom gcid + optional roles
// header. roles="" omits the header.
func reqGETAs(t *testing.T, srv http.Handler, path, tenantID, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if tenantID != "" {
		req.Header.Set("X-Tenant-Id", tenantID)
	}
	if gcid != "" {
		req.Header.Set("gcid", gcid)
	}
	if roles != "" {
		req.Header.Set("x-mesh-user-roles", roles)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// -----------------------------------------------------------------------------
// 200 self-read — caller is the instructor themselves
// -----------------------------------------------------------------------------

func TestInstructorCourses_SelfRead_ReturnsAuthoredCourses(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses",
		tenantA, phyllisInstructorGCID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	items, ok := resp["items"].([]any)
	if !ok {
		t.Fatalf("missing items; body=%s", w.Body.String())
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (cross-tenant row must be filtered); body=%s", len(items), w.Body.String())
	}
	first, _ := items[0].(map[string]any)
	if first["instructor_gcid"] != phyllisInstructorGCID {
		t.Fatalf("instructor_gcid = %v, want %s", first["instructor_gcid"], phyllisInstructorGCID)
	}
	if first["tenant_id"] != tenantA {
		t.Fatalf("tenant_id = %v, want %s", first["tenant_id"], tenantA)
	}
	if first["title"] != "MTM Tuition Math Bootcamp" {
		t.Fatalf("title = %v", first["title"])
	}
	if total, _ := resp["total"].(float64); total != 1 {
		t.Fatalf("total = %v, want 1", resp["total"])
	}
}

// -----------------------------------------------------------------------------
// 200 admin-read — caller has admin role, different gcid
// -----------------------------------------------------------------------------

func TestInstructorCourses_AdminRoleRead_ReturnsCourses(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses",
		tenantA, otherGCID, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 200 instructor-role-read — caller has instructor role, different gcid
// (the instructor role is canonically able to query any instructor's roster
// inside their own tenant — used by team-of-instructors admin pages)
// -----------------------------------------------------------------------------

func TestInstructorCourses_InstructorRoleRead_ReturnsCourses(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses",
		tenantA, otherGCID, "learner,instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 200 empty list — instructor authored no courses (404 would be wrong — the
// gcid is valid auth-wise; the answer is "no courses" not "not found")
// -----------------------------------------------------------------------------

func TestInstructorCourses_EmptyList_Returns200WithZeroItems(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	emptyInstructor := "00000000-0000-7000-8000-000000003333"
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+emptyInstructor+"/courses",
		tenantA, emptyInstructor, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	items, _ := resp["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("items = %d, want 0", len(items))
	}
	if total, _ := resp["total"].(float64); total != 0 {
		t.Fatalf("total = %v, want 0", resp["total"])
	}
}

// -----------------------------------------------------------------------------
// 403 — caller is not the instructor and lacks instructor/admin role
// -----------------------------------------------------------------------------

func TestInstructorCourses_Learner_Returns403(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	// otherGCID is a different user; "learner" role only.
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses",
		tenantA, otherGCID, "learner")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d (body=%s); want 403", w.Code, w.Body.String())
	}
}

func TestInstructorCourses_NoRoleHeader_NonOwner_Returns403(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	// No roles header at all — non-owner GCID — must 403.
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses",
		tenantA, otherGCID, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d (body=%s); want 403", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 401 — missing tenant context (tenantRequired middleware enforced)
// -----------------------------------------------------------------------------

func TestInstructorCourses_MissingTenant_Returns400(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses",
		"", phyllisInstructorGCID, "")
	// tenantRequired middleware returns 400 (mirrors existing handler convention).
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (body=%s); want 400 (tenantRequired)", w.Code, w.Body.String())
	}
}

func TestInstructorCourses_MissingGCID_Returns401(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses",
		tenantA, "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d (body=%s); want 401", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 422 — malformed instructor_gcid (non-UUID)
// -----------------------------------------------------------------------------

func TestInstructorCourses_MalformedInstructorGCID_Returns422(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	w := reqGETAs(t, srv,
		"/api/v1/instructors/not-a-uuid/courses",
		tenantA, phyllisInstructorGCID, "admin")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (body=%s); want 422", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 405 — non-GET methods rejected
// -----------------------------------------------------------------------------

func TestInstructorCourses_PostNotAllowed_Returns405(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses",
		strings.NewReader(`{}`))
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", phyllisInstructorGCID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d (body=%s); want 405", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Pagination — page / per query params
// -----------------------------------------------------------------------------

func TestInstructorCourses_Pagination_PageBeyondTotal_ReturnsEmpty(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses?page=99&per=10",
		tenantA, phyllisInstructorGCID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("items = %d on page=99, want 0", len(items))
	}
	if total, _ := resp["total"].(float64); total != 1 {
		t.Fatalf("total = %v, want 1 (total is pre-pagination)", resp["total"])
	}
}

func TestInstructorCourses_Pagination_InvalidPer_Returns422(t *testing.T) {
	srv, _ := newInstructorRosterServer(t)
	w := reqGETAs(t, srv,
		"/api/v1/instructors/"+phyllisInstructorGCID+"/courses?per=99999",
		tenantA, phyllisInstructorGCID, "")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (body=%s); want 422 (per > 200)", w.Code, w.Body.String())
	}
}
