// Tests for the /v1/ path-prefix consolidation + new endpoints.
//
// Per S4.3 brief (A-Content-Delivery): all routes consolidate under /v1/
// for platform consistency. Legacy /api/* and the bare /courses path
// continue to respond (deprecated alias) until one release after the
// migration window per .claude/rules/git-workflow.md.
//
// New endpoints:
//
//	POST   /v1/courses             — create
//	GET    /v1/courses             — list with cursor pagination
//	GET    /v1/courses/{id}        — detail
//	PATCH  /v1/courses/{id}        — update metadata + visibility
//	POST   /v1/courses/{id}/enrolments
//	DELETE /v1/courses/{id}/enrolments/{enrolment_id}
//	GET    /v1/me/enrolments
//	POST   /v1/campus              — create campus
//	GET    /v1/campus/{id}         — get campus
package httpapi_test

import (
	"context"
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

// newV1Server wires the full S4.3 deps including Campus.
func newV1Server() (http.Handler, *events.InMemoryPublisher) {
	return newV1ServerWithEnrollments(domain.NewInMemEnrollmentStore())
}

// newV1ServerWithEnrollments wires the full S4.3 deps but lets the caller
// inject the EnrollmentPort — used to assert the cancel handler drives the
// port's persist call (a spy), not just the loaded aggregate.
func newV1ServerWithEnrollments(enroll domain.EnrollmentPort) (http.Handler, *events.InMemoryPublisher) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    enroll,
		Publisher:      pub,
		CampusOps:      repoinmem.NewCampusRepo(),
	})
	return srv, pub
}

// cancelSpyStore wraps the in-memory EnrollmentPort and records Cancel calls so
// a test can prove handleV1EnrolmentCancel drives the port's PERSIST path. The
// old code mutated the loaded aggregate via SoftDelete() and never called the
// port — a silent no-op on Postgres. Embedding InMemEnrollmentStore supplies
// the other five methods; Cancel is overridden to record then delegate.
type cancelSpyStore struct {
	*domain.InMemEnrollmentStore
	cancelCalls  int
	lastTenantID string
	lastEnrollID string
}

func (s *cancelSpyStore) Cancel(ctx context.Context, tenantID, enrollmentID string) error {
	s.cancelCalls++
	s.lastTenantID = tenantID
	s.lastEnrollID = enrollmentID
	return s.InMemEnrollmentStore.Cancel(ctx, tenantID, enrollmentID)
}

// -----------------------------------------------------------------------------
// /v1/ path-prefix CRUD (POST + GET)
// -----------------------------------------------------------------------------

func TestV1Courses_PostCreatesCourse(t *testing.T) {
	srv, pub := newV1Server()
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":            "CSPO Fundamentals",
		"syllabus_outline": []string{"M1", "M2"},
		"tags":             []string{"agile"},
		"price_sgd_cents":  20000,
		"visibility":       "private",
		"instructor_name":  "Phyllis",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /v1/courses: %d body=%q", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["visibility"] != "private" {
		t.Fatalf("visibility: got %v", got["visibility"])
	}
	if pub.History()[0].Topic != "chora.delivery.course.created.v1" {
		t.Fatalf("expected course.created event")
	}
}

func TestV1Courses_GetListWithCursorPagination(t *testing.T) {
	srv, _ := newV1Server()
	// Seed 5 public courses.
	for i := 0; i < 5; i++ {
		req := map[string]interface{}{
			"title":           "course-" + string(rune('A'+i)),
			"price_sgd_cents": 1000,
			"visibility":      "public",
			"instructor_name": "x",
		}
		w := reqJSON(t, srv, http.MethodPost, "/v1/courses", req)
		if w.Code != http.StatusCreated {
			t.Fatalf("seed %d: %d", i, w.Code)
		}
	}
	// First page: first=2.
	w := reqGET(t, srv, "/v1/courses?visibility=public&first=2")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/courses: %d body=%q", w.Code, w.Body.String())
	}
	var page1 map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &page1)
	items := page1["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("expected 2 items in page 1, got %d", len(items))
	}
	pageInfo := page1["page_info"].(map[string]interface{})
	if pageInfo["has_next_page"] != true {
		t.Fatalf("expected has_next_page=true on page 1")
	}
	endCursor := pageInfo["end_cursor"].(string)
	if endCursor == "" {
		t.Fatalf("expected non-empty end_cursor")
	}
	// Second page using cursor.
	w2 := reqGET(t, srv, "/v1/courses?visibility=public&first=2&after="+endCursor)
	if w2.Code != http.StatusOK {
		t.Fatalf("GET cursor page: %d", w2.Code)
	}
	var page2 map[string]interface{}
	_ = json.Unmarshal(w2.Body.Bytes(), &page2)
	if len(page2["items"].([]interface{})) != 2 {
		t.Fatalf("expected 2 items in page 2")
	}
}

func TestV1Courses_GetDetail(t *testing.T) {
	srv, _ := newV1Server()
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "x",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "x",
	})
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	id := c["id"].(string)

	w2 := reqGET(t, srv, "/v1/courses/"+id)
	if w2.Code != http.StatusOK {
		t.Fatalf("GET /v1/courses/{id}: %d", w2.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	if got["id"] != id {
		t.Fatalf("id round-trip")
	}
}

// -----------------------------------------------------------------------------
// PATCH /v1/courses/{id}
// -----------------------------------------------------------------------------

func TestV1Courses_PatchUpdateMetadata(t *testing.T) {
	srv, pub := newV1Server()
	// Create as private.
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "CSPO",
		"price_sgd_cents": 20000,
		"visibility":      "private",
		"instructor_name": "Phyllis",
	})
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	id := c["id"].(string)
	// Drain the create event so the assertion below is unambiguous.
	_ = pub.History()

	// PATCH title only.
	wP := reqJSON(t, srv, http.MethodPatch, "/v1/courses/"+id, map[string]interface{}{
		"title": "CSPO Mastery",
	})
	if wP.Code != http.StatusOK {
		t.Fatalf("PATCH /v1/courses/{id}: %d body=%q", wP.Code, wP.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(wP.Body.Bytes(), &got)
	if got["title"] != "CSPO Mastery" {
		t.Fatalf("title not updated: %v", got["title"])
	}
	if got["visibility"] != "private" {
		t.Fatalf("visibility unchanged should remain private; got %v", got["visibility"])
	}
	// CHO-2247: course.updated.v1 must NOT be emitted — the topic has never
	// existed (describe -> NOT_FOUND) and the lane emitted 0 events in its
	// lifetime. This assertion is INVERTED from its original form, which
	// demanded >=1 such event: it was green against an event that could never
	// leave the process.
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.course.updated.v1" {
			t.Fatalf("course.updated.v1 emitted, but the lane is DEAD (no topic exists) — a publish here is a phantom that discards its own error")
		}
	}
}

func TestV1Courses_PatchVisibilityFlipEmitsPublishedEvent(t *testing.T) {
	srv, pub := newV1Server()
	// Create as private.
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "CSPO",
		"price_sgd_cents": 0,
		"visibility":      "private",
		"instructor_name": "Phyllis",
	})
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	id := c["id"].(string)

	// Flip to public via PATCH.
	wP := reqJSON(t, srv, http.MethodPatch, "/v1/courses/"+id, map[string]interface{}{
		"visibility": "public",
	})
	if wP.Code != http.StatusOK {
		t.Fatalf("PATCH visibility flip: %d body=%q", wP.Code, wP.Body.String())
	}

	// CHO-2247: emits course.published.v1 ONLY. The paired course.updated.v1
	// assertion was removed with its dead producer — that topic does not exist.
	// course.published.v1 is live (chora-sharing.delivery-course-published).
	var sawPublished bool
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.course.updated.v1" {
			t.Fatalf("course.updated.v1 emitted on visibility flip; the lane is DEAD")
		}
		if ev.Topic == "chora.delivery.course.published.v1" {
			sawPublished = true
		}
	}
	if !sawPublished {
		t.Fatalf("expected course.published.v1 on private->public flip")
	}
}

func TestV1Courses_PatchPrivateToTenantOnlyDoesNotPublish(t *testing.T) {
	srv, pub := newV1Server()
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "x",
		"price_sgd_cents": 0,
		"visibility":      "private",
		"instructor_name": "x",
	})
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	id := c["id"].(string)

	wP := reqJSON(t, srv, http.MethodPatch, "/v1/courses/"+id, map[string]interface{}{
		"visibility": "tenant_only",
	})
	if wP.Code != http.StatusOK {
		t.Fatalf("PATCH tenant_only: %d", wP.Code)
	}
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.course.published.v1" {
			t.Fatalf("course.published must NOT fire on private->tenant_only")
		}
	}
}

func TestV1Courses_PatchUnknownIDReturns404(t *testing.T) {
	srv, _ := newV1Server()
	w := reqJSON(t, srv, http.MethodPatch, "/v1/courses/01970000-0000-7000-8000-DEADBEEFDEAD", map[string]interface{}{
		"title": "x",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// /v1/courses/{id}/enrolments
// -----------------------------------------------------------------------------

func TestV1Enrolments_PostCreatesIdempotent(t *testing.T) {
	srv, pub := newV1Server()
	// Seed a public course.
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "CSM Prep",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "Mr. Chen",
	})
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	id := c["id"].(string)

	// First enrol -> 201.
	w1 := reqJSON(t, srv, http.MethodPost, "/v1/courses/"+id+"/enrolments", map[string]interface{}{})
	if w1.Code != http.StatusCreated {
		t.Fatalf("first enrol: %d body=%q", w1.Code, w1.Body.String())
	}
	// Repeat enrol -> 200 (idempotent).
	w2 := reqJSON(t, srv, http.MethodPost, "/v1/courses/"+id+"/enrolments", map[string]interface{}{})
	if w2.Code != http.StatusOK {
		t.Fatalf("idempotent second enrol expected 200, got %d", w2.Code)
	}
	// Exactly 1 enrollment.created event.
	created := 0
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.enrollment.created.v1" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("expected exactly 1 enrollment.created (idempotent); got %d", created)
	}
}

func TestV1Enrolments_PostReturnsEnrolmentBody(t *testing.T) {
	srv, _ := newV1Server()
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "x",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "x",
	})
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	id := c["id"].(string)

	w1 := reqJSON(t, srv, http.MethodPost, "/v1/courses/"+id+"/enrolments", map[string]interface{}{})
	if w1.Code != http.StatusCreated {
		t.Fatalf("post enrol: %d", w1.Code)
	}
	var e map[string]interface{}
	_ = json.Unmarshal(w1.Body.Bytes(), &e)
	if e["course_id"] != id {
		t.Fatalf("course_id round-trip")
	}
	if e["gcid"] == nil || e["gcid"].(string) == "" {
		t.Fatalf("expected gcid in response")
	}
}

func TestV1Enrolments_DeleteCancelsAndPublishesEvent(t *testing.T) {
	srv, pub := newV1Server()
	// Seed + enrol.
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "x",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "x",
	})
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	courseID := c["id"].(string)

	wE := reqJSON(t, srv, http.MethodPost, "/v1/courses/"+courseID+"/enrolments", map[string]interface{}{})
	var e map[string]interface{}
	_ = json.Unmarshal(wE.Body.Bytes(), &e)
	enrolID := e["id"].(string)

	// DELETE.
	wD := reqDELETE(t, srv, "/v1/courses/"+courseID+"/enrolments/"+enrolID)
	if wD.Code != http.StatusNoContent {
		t.Fatalf("DELETE: expected 204, got %d body=%q", wD.Code, wD.Body.String())
	}
	// enrollment.cancelled emitted exactly once.
	cancelled := 0
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.enrollment.cancelled.v1" {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("expected exactly 1 enrollment.cancelled, got %d", cancelled)
	}

	// Idempotent: second DELETE on same enrolment is a no-op (404 acceptable;
	// some clients re-issue). We accept either 204 OR 404.
	wD2 := reqDELETE(t, srv, "/v1/courses/"+courseID+"/enrolments/"+enrolID)
	if wD2.Code != http.StatusNoContent && wD2.Code != http.StatusNotFound {
		t.Fatalf("second DELETE: expected 204 or 404, got %d", wD2.Code)
	}
}

// TestV1Enrolments_DeleteInvokesPortCancel is the regression for the latent pg
// no-op: the DELETE handler must drive the EnrollmentPort's Cancel (the persist
// call), not merely SoftDelete() the loaded aggregate. In-mem both look the
// same; on Postgres the mutation is dropped. A spy proves the port was called.
func TestV1Enrolments_DeleteInvokesPortCancel(t *testing.T) {
	spy := &cancelSpyStore{InMemEnrollmentStore: domain.NewInMemEnrollmentStore()}
	srv, _ := newV1ServerWithEnrollments(spy)

	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "x",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "x",
	})
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	courseID := c["id"].(string)

	wE := reqJSON(t, srv, http.MethodPost, "/v1/courses/"+courseID+"/enrolments", map[string]interface{}{})
	var e map[string]interface{}
	_ = json.Unmarshal(wE.Body.Bytes(), &e)
	enrolID := e["id"].(string)

	wD := reqDELETE(t, srv, "/v1/courses/"+courseID+"/enrolments/"+enrolID)
	if wD.Code != http.StatusNoContent {
		t.Fatalf("DELETE: expected 204, got %d body=%q", wD.Code, wD.Body.String())
	}
	if spy.cancelCalls != 1 {
		t.Fatalf("expected handler to call port.Cancel exactly once; got %d", spy.cancelCalls)
	}
	if spy.lastEnrollID != enrolID {
		t.Fatalf("Cancel called with wrong enrollment_id: want %q, got %q", enrolID, spy.lastEnrollID)
	}
	if spy.lastTenantID != tenantA {
		t.Fatalf("Cancel called with wrong tenant_id: want %q, got %q", tenantA, spy.lastTenantID)
	}
}

func TestV1MeEnrolments_ReturnsLearnerEnrolments(t *testing.T) {
	srv, _ := newV1Server()
	// Seed two courses + enrol in both.
	courseIDs := []string{}
	for i := 0; i < 2; i++ {
		w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
			"title":           "course",
			"price_sgd_cents": 0,
			"visibility":      "public",
			"instructor_name": "x",
		})
		var c map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &c)
		courseIDs = append(courseIDs, c["id"].(string))
	}
	for _, cid := range courseIDs {
		_ = reqJSON(t, srv, http.MethodPost, "/v1/courses/"+cid+"/enrolments", map[string]interface{}{})
	}

	w := reqGET(t, srv, "/v1/me/enrolments")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/me/enrolments: %d", w.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("expected 2 enrolments, got %d", len(items))
	}
}

// -----------------------------------------------------------------------------
// Cross-tenant public catalogue
// -----------------------------------------------------------------------------

func TestV1Courses_PublicCoursesVisibleCrossTenant(t *testing.T) {
	srv, _ := newV1Server()
	// Phyllis (tenantA) creates a public CSM course.
	w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "CSM Prep",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "Mr. Chen",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("seed: %d", w.Code)
	}

	// Different tenant queries the public catalogue — must see CSM.
	req := httptest.NewRequest(http.MethodGet, "/v1/courses?visibility=public", nil)
	req.Header.Set("X-Tenant-Id", "01970000-0000-7000-8000-DEADBEEFDEAD")
	req.Header.Set("gcid", "01970000-0000-7000-9000-CCCCCCCCCCCC")
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("cross-tenant GET: %d", w2.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	items := got["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("cross-tenant viewer must see 1 public course, got %d", len(items))
	}
	first := items[0].(map[string]interface{})
	if first["title"] != "CSM Prep" {
		t.Fatalf("title round-trip")
	}
}

func TestV1Courses_PrivateCoursesHiddenCrossTenant(t *testing.T) {
	srv, _ := newV1Server()
	// tenantA creates a private course.
	_ = reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "Internal",
		"price_sgd_cents": 0,
		"visibility":      "private",
		"instructor_name": "x",
	})
	// Different tenant — public-only filter.
	req := httptest.NewRequest(http.MethodGet, "/v1/courses?visibility=public", nil)
	req.Header.Set("X-Tenant-Id", "01970000-0000-7000-8000-DEADBEEFDEAD")
	req.Header.Set("gcid", "01970000-0000-7000-9000-CCCCCCCCCCCC")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got["items"].([]interface{})) != 0 {
		t.Fatalf("private course must NOT leak cross-tenant")
	}
}

// -----------------------------------------------------------------------------
// Anonymous public catalogue browse (Phyllis Step 5 — UNAUTHED discovery)
//
// The Phyllis demo's public-discovery step hits GET /api/catalog UNAUTHED at
// the gateway, which fans out to chora-delivery GET /v1/courses?visibility=public
// with NO X-Tenant-Id header. The list route must NOT be tenantRequired-gated
// for a public-visibility browse — public courses are cross-tenant readable.
// Tenant isolation MUST still hold for tenant-scoped + private rows.
// -----------------------------------------------------------------------------

// reqGETNoTenant issues a GET with NO X-Tenant-Id and NO gcid header — the
// anonymous public-discovery shape.
func reqGETNoTenant(t *testing.T, srv http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestV1Courses_AnonymousPublicListNoTenant_200(t *testing.T) {
	srv, _ := newV1Server()
	// tenantA seeds a public course.
	if w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "CSPO Fundamentals",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "Mr. Chen",
	}); w.Code != http.StatusCreated {
		t.Fatalf("seed: %d body=%q", w.Code, w.Body.String())
	}
	// Anonymous (no X-Tenant-Id) public-visibility browse.
	w := reqGETNoTenant(t, srv, "/v1/courses?visibility=public")
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous GET /v1/courses?visibility=public: %d body=%q", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	items, _ := got["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("anonymous public browse must see 1 public course, got %d", len(items))
	}
	first := items[0].(map[string]interface{})
	if first["title"] != "CSPO Fundamentals" {
		t.Fatalf("title round-trip: got %v", first["title"])
	}
}

func TestV1Courses_AnonymousTenantOrPublicListNoTenant_200(t *testing.T) {
	srv, _ := newV1Server()
	if w := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "Public One",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "x",
	}); w.Code != http.StatusCreated {
		t.Fatalf("seed: %d", w.Code)
	}
	// visibility=tenant_or_public is also an allowed anonymous public-browse
	// shape (the gateway never sends it without a tenant, but the route must
	// not 400 — it should serve the public slice).
	w := reqGETNoTenant(t, srv, "/v1/courses?visibility=tenant_or_public")
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous GET /v1/courses?visibility=tenant_or_public: %d body=%q", w.Code, w.Body.String())
	}
}

// A list request with NO visibility filter (or any non-public filter) and NO
// tenant context MUST still 400 — that is a tenant-scoped browse and the
// tenant-isolation guarantee depends on X-Tenant-Id being present.
func TestV1Courses_ListNoTenantNoVisibility_400(t *testing.T) {
	srv, _ := newV1Server()
	w := reqGETNoTenant(t, srv, "/v1/courses")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("tenant-scoped GET /v1/courses with no tenant must be 400, got %d body=%q", w.Code, w.Body.String())
	}
}

// Anonymous detail lookup of a PUBLIC course must succeed (cross-tenant
// readable); the handler already gates non-public rows to the active tenant.
func TestV1CourseDetail_AnonymousPublicCourseNoTenant_200(t *testing.T) {
	srv, _ := newV1Server()
	cw := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "CSPO Detail",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "x",
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("seed: %d", cw.Code)
	}
	var created map[string]interface{}
	_ = json.Unmarshal(cw.Body.Bytes(), &created)
	id := created["id"].(string)

	w := reqGETNoTenant(t, srv, "/v1/courses/"+id)
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous GET /v1/courses/{id} (public): %d body=%q", w.Code, w.Body.String())
	}
}

// Anonymous detail lookup of a PRIVATE course must 404 (not leak) — and must
// NOT 400 either: the route is reachable, the handler's visibility gate is
// what hides the row.
func TestV1CourseDetail_AnonymousPrivateCourseNoTenant_404(t *testing.T) {
	srv, _ := newV1Server()
	cw := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "Private Detail",
		"price_sgd_cents": 0,
		"visibility":      "private",
		"instructor_name": "x",
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("seed: %d", cw.Code)
	}
	var created map[string]interface{}
	_ = json.Unmarshal(cw.Body.Bytes(), &created)
	id := created["id"].(string)

	w := reqGETNoTenant(t, srv, "/v1/courses/"+id)
	if w.Code != http.StatusNotFound {
		t.Fatalf("anonymous GET /v1/courses/{id} (private) must 404, got %d body=%q", w.Code, w.Body.String())
	}
}

// POST /v1/courses (create) MUST still be tenantRequired-gated — the public
// exemption is read-only.
func TestV1Courses_PostNoTenant_400(t *testing.T) {
	srv, _ := newV1Server()
	req := httptest.NewRequest(http.MethodPost, "/v1/courses", strings.NewReader(`{"title":"x","visibility":"public"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("gcid", "01970000-0000-7000-9000-000000000001")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST /v1/courses with no tenant must 400, got %d body=%q", w.Code, w.Body.String())
	}
}

// PATCH /v1/courses/{id} MUST still be tenantRequired-gated.
func TestV1Courses_PatchNoTenant_400(t *testing.T) {
	srv, _ := newV1Server()
	req := httptest.NewRequest(http.MethodPatch, "/v1/courses/some-id", strings.NewReader(`{"title":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH /v1/courses/{id} with no tenant must 400, got %d body=%q", w.Code, w.Body.String())
	}
}

// POST /v1/courses/{id}/enrolments MUST still be tenantRequired-gated — only
// GET browse is exempt.
func TestV1Enrolments_PostNoTenant_400(t *testing.T) {
	srv, _ := newV1Server()
	req := httptest.NewRequest(http.MethodPost, "/v1/courses/some-id/enrolments", strings.NewReader(`{"gcid":"g"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST enrolments with no tenant must 400, got %d body=%q", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// /v1/campus
// -----------------------------------------------------------------------------

func TestV1Campus_PostCreatesCampus(t *testing.T) {
	srv, _ := newV1Server()
	w := reqJSON(t, srv, http.MethodPost, "/v1/campus", map[string]interface{}{
		"name":       "MTM SG — Bras Basah",
		"address_l1": "123 Bras Basah Rd",
		"city":       "Singapore",
		"country":    "SG",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /v1/campus: %d body=%q", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["id"] == nil || got["id"].(string) == "" {
		t.Fatalf("expected id")
	}
	if got["country"] != "SG" {
		t.Fatalf("country round-trip")
	}
}

func TestV1Campus_GetReturnsCampus(t *testing.T) {
	srv, _ := newV1Server()
	w := reqJSON(t, srv, http.MethodPost, "/v1/campus", map[string]interface{}{
		"name":    "x",
		"country": "SG",
	})
	var c map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	id := c["id"].(string)

	w2 := reqGET(t, srv, "/v1/campus/"+id)
	if w2.Code != http.StatusOK {
		t.Fatalf("GET: %d", w2.Code)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w2.Body.Bytes(), &got)
	if got["id"] != id {
		t.Fatalf("id round-trip")
	}
}

func TestV1Campus_GetUnknownReturns404(t *testing.T) {
	srv, _ := newV1Server()
	w := reqGET(t, srv, "/v1/campus/01970000-0000-7000-8000-DEADBEEFDEAD")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestV1Campus_PostRejectsBadCountry(t *testing.T) {
	srv, _ := newV1Server()
	w := reqJSON(t, srv, http.MethodPost, "/v1/campus", map[string]interface{}{
		"name":    "x",
		"country": "Singapore", // not ISO-2
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "country") {
		t.Fatalf("error should mention country, body=%q", w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Helper: DELETE request
// -----------------------------------------------------------------------------

func reqDELETE(t *testing.T, srv http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("gcid", gcidA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}
