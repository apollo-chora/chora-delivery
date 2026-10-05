// roster_handler_test.go — TDD coverage for GET /api/v1/rosters/{courseId}.
//
// R+ M4 — course-centric Roster READ VIEW. Pinned invariants:
//
//   - 400 missing X-Tenant-Id (tenantRequired middleware)
//   - 404 empty path segment / no leaf
//   - 405 non-GET methods
//   - 200 empty learners array when no enrolments exist (NOT a placeholder
//     per `feedback_no_stubs_real_wiring` + the M4 directive)
//   - 200 populated learners when enrolments exist; tenant isolation
//     guard rejects cross-tenant rows
//   - Response shape: { "course_id": "...", "learners": [...] }
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	rosterCourseA = "01970000-0000-7000-6000-000000000001"
	rosterCourseB = "01970000-0000-7000-6000-000000000002"
	rosterTenantA = "01970000-0000-7000-8000-000000000001"
	rosterTenantB = "01970000-0000-7000-8000-000000000002"
	learnerGCIDA  = "00000000-0000-7000-9000-00000000aaaa"
	learnerGCIDB  = "00000000-0000-7000-9000-00000000bbbb"
	learnerGCIDC  = "00000000-0000-7000-9000-00000000cccc"
)

// newRosterServer wires a fresh server with an in-mem enrolment store and the
// course-roster repo bound to it. Returns the server + the underlying
// enrollments port so individual tests can seed rows.
func newRosterServer(t *testing.T) (http.Handler, domain.EnrollmentPort) {
	t.Helper()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	enrollments := domain.NewInMemEnrollmentStore()
	rosterRepo := inmem.NewCourseRosterRepo(enrollments)
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    enrollments,
		Publisher:      pub,
		CampusOps:      repoinmem.NewCampusRepo(),
		Rosters:        rosterRepo,
	})
	return srv, enrollments
}

// rosterGET builds a GET to /api/v1/rosters/{courseId} with X-Tenant-Id.
func rosterGET(t *testing.T, srv http.Handler, courseID, tenantID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/rosters/"+courseID, nil)
	if tenantID != "" {
		req.Header.Set("X-Tenant-Id", tenantID)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// -----------------------------------------------------------------------------
// 400 — tenantRequired middleware
// -----------------------------------------------------------------------------

func TestRoster_MissingTenant_Returns400(t *testing.T) {
	srv, _ := newRosterServer(t)
	w := rosterGET(t, srv, rosterCourseA, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (body=%s); want 400", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 404 — empty path segment
// -----------------------------------------------------------------------------

func TestRoster_EmptyCourseID_Returns404(t *testing.T) {
	srv, _ := newRosterServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/rosters/", nil)
	req.Header.Set("X-Tenant-Id", rosterTenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d (body=%s); want 404", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 405 — non-GET method
// -----------------------------------------------------------------------------

func TestRoster_PostNotAllowed_Returns405(t *testing.T) {
	srv, _ := newRosterServer(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/rosters/"+rosterCourseA, strings.NewReader(`{}`))
	req.Header.Set("X-Tenant-Id", rosterTenantA)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d (body=%s); want 405", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 200 — empty learners (no enrolments)
// -----------------------------------------------------------------------------

func TestRoster_EmptyCourse_Returns200EmptyArray(t *testing.T) {
	srv, _ := newRosterServer(t)
	w := rosterGET(t, srv, rosterCourseA, rosterTenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if resp["course_id"] != rosterCourseA {
		t.Errorf("course_id = %v, want %s", resp["course_id"], rosterCourseA)
	}
	learners, ok := resp["learners"].([]any)
	if !ok {
		t.Fatalf("missing/non-array learners; body=%s", w.Body.String())
	}
	if len(learners) != 0 {
		t.Errorf("learners = %d, want 0", len(learners))
	}
	// CRITICAL: the wire must say `"learners":[]` not `"learners":null`
	// (per M4 directive — empty learners array, NOT a placeholder).
	if !strings.Contains(w.Body.String(), `"learners":[]`) {
		t.Errorf("body must contain learners=[], got %s", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 200 — populated learners
// -----------------------------------------------------------------------------

func TestRoster_PopulatedCourse_ReturnsLearners(t *testing.T) {
	srv, enrollments := newRosterServer(t)
	// Seed 3 enrolments on rosterCourseA in rosterTenantA.
	for _, g := range []string{learnerGCIDA, learnerGCIDB, learnerGCIDC} {
		if _, err := enrollments.Register(context.Background(), rosterTenantA, rosterCourseA, g); err != nil {
			t.Fatalf("seed: Register(%s): %v", g, err)
		}
	}
	w := rosterGET(t, srv, rosterCourseA, rosterTenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if resp["course_id"] != rosterCourseA {
		t.Errorf("course_id = %v, want %s", resp["course_id"], rosterCourseA)
	}
	learners, _ := resp["learners"].([]any)
	if len(learners) != 3 {
		t.Fatalf("learners = %d, want 3; body=%s", len(learners), w.Body.String())
	}
	// First row must carry GCID + display_name (= GCID for now per
	// fail-loud projection placeholder) + progress_pct (= 0 default)
	// + enrolled_at (RFC3339).
	first, _ := learners[0].(map[string]any)
	if first["gcid"] == "" || first["gcid"] == nil {
		t.Errorf("first row missing gcid; body=%s", w.Body.String())
	}
	if first["display_name"] != first["gcid"] {
		t.Errorf("display_name fallback expected = gcid, got %v vs %v",
			first["display_name"], first["gcid"])
	}
	if progress, _ := first["progress_pct"].(float64); progress != 0 {
		t.Errorf("progress_pct = %v, want 0 (unwired projection)", first["progress_pct"])
	}
	if _, ok := first["enrolled_at"].(string); !ok {
		t.Errorf("enrolled_at missing or non-string; body=%s", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 200 — tenant isolation
// -----------------------------------------------------------------------------

func TestRoster_TenantIsolation_RejectsCrossTenantRows(t *testing.T) {
	srv, enrollments := newRosterServer(t)
	// Seed 2 enrolments on rosterCourseA in rosterTenantA.
	for _, g := range []string{learnerGCIDA, learnerGCIDB} {
		if _, err := enrollments.Register(context.Background(), rosterTenantA, rosterCourseA, g); err != nil {
			t.Fatalf("seed A: %v", err)
		}
	}
	// Seed 1 enrolment on the SAME courseID but a DIFFERENT tenant — must
	// be invisible to a tenantA caller.
	if _, err := enrollments.Register(context.Background(), rosterTenantB, rosterCourseA, learnerGCIDC); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	w := rosterGET(t, srv, rosterCourseA, rosterTenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	learners, _ := resp["learners"].([]any)
	if len(learners) != 2 {
		t.Fatalf("learners = %d, want 2 (cross-tenant row must be filtered); body=%s",
			len(learners), w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 200 — different course, same tenant: course isolation
// -----------------------------------------------------------------------------

func TestRoster_CourseIsolation_FiltersOtherCourses(t *testing.T) {
	srv, enrollments := newRosterServer(t)
	// 1 enrolment on courseA, 1 on courseB (same tenant).
	if _, err := enrollments.Register(context.Background(), rosterTenantA, rosterCourseA, learnerGCIDA); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if _, err := enrollments.Register(context.Background(), rosterTenantA, rosterCourseB, learnerGCIDB); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	w := rosterGET(t, srv, rosterCourseA, rosterTenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	learners, _ := resp["learners"].([]any)
	if len(learners) != 1 {
		t.Fatalf("learners = %d, want 1 (other-course row must be filtered); body=%s",
			len(learners), w.Body.String())
	}
	first, _ := learners[0].(map[string]any)
	if first["gcid"] != learnerGCIDA {
		t.Errorf("gcid = %v, want %s", first["gcid"], learnerGCIDA)
	}
}

// -----------------------------------------------------------------------------
// tenant context propagation — prod RLS regression guard
// -----------------------------------------------------------------------------

// ctxTenantCapturePort is a fake EnrollmentListByCoursePort that records the
// tenant carried on the ctx it is handed. The production pg EnrollmentRepo
// reads the tenant off ctx (rls.ApplySession → tracing.TenantIDFromContext)
// to SET LOCAL chora.tenant_id before the SELECT; a tenant-less ctx makes
// ApplySession return ErrNoTenantContext and the roster route 500s. The
// in-mem store used by the other tests ignores ctx, so it cannot catch this —
// this fake asserts the handler threads tracing.WithTenantID(r.Context(), …)
// all the way through the CourseRosterRepo port.
type ctxTenantCapturePort struct{ sawTenant string }

func (p *ctxTenantCapturePort) ListByCourse(ctx context.Context, _, _ string) ([]*domain.Enrollment, error) {
	p.sawTenant = tracing.TenantIDFromContext(ctx)
	return nil, nil
}

// TestRoster_ThreadsTenantContextToEnrollmentPort pins that the tenant from
// X-Tenant-Id reaches the EnrollmentListByCoursePort via ctx — the prod 500
// was the inmem CourseRosterRepo passing context.Background() (no tenant).
func TestRoster_ThreadsTenantContextToEnrollmentPort(t *testing.T) {
	port := &ctxTenantCapturePort{}
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      pub,
		CampusOps:      repoinmem.NewCampusRepo(),
		Rosters:        inmem.NewCourseRosterRepo(port),
	})
	w := rosterGET(t, srv, rosterCourseA, rosterTenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if port.sawTenant != rosterTenantA {
		t.Errorf("EnrollmentListByCoursePort saw tenant %q on ctx, want %q — the roster handler + inmem repo must thread tracing.WithTenantID(r.Context(), tenant) through CourseRosterRepo.ListByCourse (prod RLS reads tenant from ctx; context.Background() → ErrNoTenantContext → 500)",
			port.sawTenant, rosterTenantA)
	}
}
