// Package httpapi_test exercises the Phyllis-MVP catalogue + enrollment
// HTTP routes (Comic Ch5 P10 + Ch4 P8).
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
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// newCatalogueServer wires the Phyllis-MVP routes against fresh in-mem
// adapters so each test gets isolated state.
func newCatalogueServer() (http.Handler, *events.InMemoryPublisher) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      pub,
	})
	return srv, pub
}

// -----------------------------------------------------------------------------
// POST /courses
// -----------------------------------------------------------------------------

func TestCatalogue_PostCourse_201_AndPublishesEvent(t *testing.T) {
	srv, pub := newCatalogueServer()
	w := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title":            "CSPO Fundamentals",
		"syllabus_outline": []string{"M1", "M2"},
		"tags":             []string{"agile", "scrum"},
		"price_sgd_cents":  20000,
		"public":           true,
		"sf_eligible":      false,
		"instructor_name":  "Phyllis Tan",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /courses: status %d body=%q", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["id"] == nil || got["id"].(string) == "" {
		t.Fatalf("expected id in response")
	}
	if got["public"] != true {
		t.Fatalf("expected public=true, got %v", got["public"])
	}
	if got["instructor_gcid"] != gcidA {
		t.Fatalf("instructor_gcid should equal request gcid; got %v", got["instructor_gcid"])
	}
	if got["enrolled_count"].(float64) != 0 {
		t.Fatalf("fresh course should have 0 enrolled, got %v", got["enrolled_count"])
	}
	if got["syllabus_outline_count"].(float64) != 2 {
		t.Fatalf("syllabus count: got %v", got["syllabus_outline_count"])
	}

	// Event must be published (Comic Ch5 P10).
	hist := pub.History()
	if len(hist) != 1 {
		t.Fatalf("expected 1 event published, got %d", len(hist))
	}
	if hist[0].Topic != "chora.delivery.course.created.v1" {
		t.Fatalf("topic: got %q", hist[0].Topic)
	}
}

func TestCatalogue_PostCourse_RejectsMissingGCID(t *testing.T) {
	srv, _ := newCatalogueServer()
	// Build the request without the gcid header.
	body := strings.NewReader(`{"title":"x","price_sgd_cents":0}`)
	req := httptest.NewRequest(http.MethodPost, "/courses", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing gcid, got %d", w.Code)
	}
}

func TestCatalogue_PostCourse_RejectsBadInput(t *testing.T) {
	srv, _ := newCatalogueServer()
	w := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title":           "", // empty title
		"price_sgd_cents": 0,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /courses
// -----------------------------------------------------------------------------

func TestCatalogue_GetCourses_FilterPublicTrue(t *testing.T) {
	srv, _ := newCatalogueServer()
	// seed: 1 public + 1 private
	if w := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "CSM Prep", "price_sgd_cents": 0, "public": true, "tags": []string{"scrum"},
	}); w.Code != http.StatusCreated {
		t.Fatalf("seed public: %d %s", w.Code, w.Body.String())
	}
	if w := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "CSPO", "price_sgd_cents": 20000, "public": false, "tags": []string{"agile"},
	}); w.Code != http.StatusCreated {
		t.Fatalf("seed private: %d %s", w.Code, w.Body.String())
	}

	w := reqGET(t, srv, "/courses?public=true")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /courses?public=true: status %d", w.Code)
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Fatalf("expected 1 public course, got total=%d", resp.Total)
	}
	if resp.Items[0]["title"] != "CSM Prep" {
		t.Fatalf("expected CSM Prep as the public course, got %v", resp.Items[0]["title"])
	}
}

func TestCatalogue_GetCourses_SearchQ(t *testing.T) {
	srv, _ := newCatalogueServer()
	_ = reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "CSM Prep", "price_sgd_cents": 0, "public": true,
	})
	_ = reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "PMP Crash", "price_sgd_cents": 15000, "public": true,
	})

	w := reqGET(t, srv, "/courses?q=csm")
	var resp struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Fatalf("q=csm: expected 1 match, got %d", resp.Total)
	}
}

func TestCatalogue_GetCourses_Pagination(t *testing.T) {
	srv, _ := newCatalogueServer()
	// seed 3 public courses
	for i := 0; i < 3; i++ {
		_ = reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
			"title": "course", "price_sgd_cents": 0, "public": true,
		})
	}
	w := reqGET(t, srv, "/courses?public=true&page=1&per=2")
	var resp struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
		Page  int                      `json:"page"`
		Per   int                      `json:"per"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 3 || len(resp.Items) != 2 || resp.Page != 1 || resp.Per != 2 {
		t.Fatalf("page=1 per=2: expected total=3 len=2 page=1 per=2; got total=%d len=%d page=%d per=%d",
			resp.Total, len(resp.Items), resp.Page, resp.Per)
	}
}

// -----------------------------------------------------------------------------
// GET /courses/{id}
// -----------------------------------------------------------------------------

func TestCatalogue_GetCourseByID_200(t *testing.T) {
	srv, _ := newCatalogueServer()
	createW := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "x", "price_sgd_cents": 0, "public": true,
	})
	var created map[string]interface{}
	_ = json.Unmarshal(createW.Body.Bytes(), &created)
	id := created["id"].(string)
	w := reqGET(t, srv, "/courses/"+id)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestCatalogue_GetCourseByID_404(t *testing.T) {
	srv, _ := newCatalogueServer()
	w := reqGET(t, srv, "/courses/does-not-exist")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// POST /enrollments
// -----------------------------------------------------------------------------

func TestEnrollments_Post_201_AndPublishesEvent(t *testing.T) {
	srv, pub := newCatalogueServer()
	createW := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "CSM Prep", "price_sgd_cents": 0, "public": true,
	})
	var created map[string]interface{}
	_ = json.Unmarshal(createW.Body.Bytes(), &created)
	courseID := created["id"].(string)

	// Phyllis (gcidB) enrols on Mr. Chen's CSM course.
	w := reqJSON(t, srv, http.MethodPost, "/enrollments", map[string]interface{}{
		"course_id": courseID,
		"gcid":      gcidB,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%q", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["gcid"] != gcidB {
		t.Fatalf("expected enrolled gcid=%s, got %v", gcidB, got["gcid"])
	}
	if got["course_id"] != courseID {
		t.Fatalf("course_id mismatch")
	}

	// Verify the catalogue's enrolled_count incremented.
	courseW := reqGET(t, srv, "/courses/"+courseID)
	var course map[string]interface{}
	_ = json.Unmarshal(courseW.Body.Bytes(), &course)
	if course["enrolled_count"].(float64) != 1 {
		t.Fatalf("enrolled_count: expected 1, got %v", course["enrolled_count"])
	}

	// 1 course-created + 1 enrollment-created = 2 events
	if got, want := len(pub.History()), 2; got != want {
		t.Fatalf("expected %d events, got %d", want, got)
	}
	last := pub.History()[1]
	if last.Topic != "chora.delivery.enrollment.created.v1" {
		t.Fatalf("expected enrollment topic, got %q", last.Topic)
	}
	if last.Envelope.IdempotencyKey == "" {
		t.Fatalf("envelope must carry idempotency_key")
	}
}

func TestEnrollments_Post_IsIdempotent(t *testing.T) {
	srv, pub := newCatalogueServer()
	createW := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "x", "price_sgd_cents": 0, "public": true,
	})
	var created map[string]interface{}
	_ = json.Unmarshal(createW.Body.Bytes(), &created)
	courseID := created["id"].(string)

	w1 := reqJSON(t, srv, http.MethodPost, "/enrollments", map[string]interface{}{
		"course_id": courseID, "gcid": gcidB,
	})
	if w1.Code != http.StatusCreated {
		t.Fatalf("first enrol: expected 201, got %d", w1.Code)
	}
	w2 := reqJSON(t, srv, http.MethodPost, "/enrollments", map[string]interface{}{
		"course_id": courseID, "gcid": gcidB,
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("idempotent retry: expected 200, got %d", w2.Code)
	}
	var e1, e2 map[string]interface{}
	_ = json.Unmarshal(w1.Body.Bytes(), &e1)
	_ = json.Unmarshal(w2.Body.Bytes(), &e2)
	if e1["id"] != e2["id"] {
		t.Fatalf("idempotency: expected same enrollment id; got %v vs %v", e1["id"], e2["id"])
	}

	// Second POST must NOT republish the event.
	enrolEvents := 0
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.enrollment.created.v1" {
			enrolEvents++
		}
	}
	if enrolEvents != 1 {
		t.Fatalf("idempotent retry must not republish; got %d enrollment events", enrolEvents)
	}
}

// Same-identity invariant (Comic Ch4 P8): Phyllis is the instructor on her
// CSPO course AND a learner on Mr. Chen's CSM course. Both enrolment rows
// must surface under HER gcid (no new account created).
func TestEnrollments_Post_SameIdentityAcrossCourses(t *testing.T) {
	srv, _ := newCatalogueServer()
	cspoW := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "CSPO Fundamentals", "price_sgd_cents": 20000, "public": false,
	})
	var cspo map[string]interface{}
	_ = json.Unmarshal(cspoW.Body.Bytes(), &cspo)
	csmW := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "CSM Prep", "price_sgd_cents": 0, "public": true,
	})
	var csm map[string]interface{}
	_ = json.Unmarshal(csmW.Body.Bytes(), &csm)

	// Phyllis (gcidA) enrols on her own CSPO course.
	enrolCSPO := reqJSON(t, srv, http.MethodPost, "/enrollments", map[string]interface{}{
		"course_id": cspo["id"], "gcid": gcidA,
	})
	if enrolCSPO.Code != http.StatusCreated {
		t.Fatalf("enrol CSPO: %d %s", enrolCSPO.Code, enrolCSPO.Body.String())
	}
	// Phyllis enrols on Mr. Chen's CSM course (same gcid!).
	enrolCSM := reqJSON(t, srv, http.MethodPost, "/enrollments", map[string]interface{}{
		"course_id": csm["id"], "gcid": gcidA,
	})
	if enrolCSM.Code != http.StatusCreated {
		t.Fatalf("enrol CSM: %d %s", enrolCSM.Code, enrolCSM.Body.String())
	}
	var e1, e2 map[string]interface{}
	_ = json.Unmarshal(enrolCSPO.Body.Bytes(), &e1)
	_ = json.Unmarshal(enrolCSM.Body.Bytes(), &e2)
	if e1["gcid"] != e2["gcid"] {
		t.Fatalf("same-identity invariant: gcids differ across courses")
	}
	if e1["gcid"] != gcidA {
		t.Fatalf("expected phyllis gcid (%s), got %v", gcidA, e1["gcid"])
	}
}

func TestEnrollments_Post_404OnUnknownCourse(t *testing.T) {
	srv, _ := newCatalogueServer()
	w := reqJSON(t, srv, http.MethodPost, "/enrollments", map[string]interface{}{
		"course_id": "does-not-exist", "gcid": gcidA,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown course, got %d", w.Code)
	}
}

func TestEnrollments_Post_400OnMissingGCID(t *testing.T) {
	srv, _ := newCatalogueServer()
	createW := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
		"title": "x", "price_sgd_cents": 0, "public": true,
	})
	var created map[string]interface{}
	_ = json.Unmarshal(createW.Body.Bytes(), &created)

	// Build request without gcid header AND without body gcid.
	body := strings.NewReader(`{"course_id":"` + created["id"].(string) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/enrollments", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// GET /me/enrollments
// -----------------------------------------------------------------------------

func TestMeEnrollments_Get_ListsCurrentUser(t *testing.T) {
	srv, _ := newCatalogueServer()
	// Create 2 courses + enrol Phyllis (gcidA) in both.
	for i := 0; i < 2; i++ {
		w := reqJSON(t, srv, http.MethodPost, "/courses", map[string]interface{}{
			"title": "x", "price_sgd_cents": 0, "public": true,
		})
		var c map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &c)
		if w := reqJSON(t, srv, http.MethodPost, "/enrollments", map[string]interface{}{
			"course_id": c["id"], "gcid": gcidA,
		}); w.Code != http.StatusCreated {
			t.Fatalf("seed enrol: %d %s", w.Code, w.Body.String())
		}
	}

	w := reqGET(t, srv, "/me/enrollments")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Fatalf("expected 2 enrollments for current user, got %d", resp.Total)
	}
}

func TestMeEnrollments_Get_RejectsMissingGCID(t *testing.T) {
	srv, _ := newCatalogueServer()
	req := httptest.NewRequest(http.MethodGet, "/me/enrollments", nil)
	req.Header.Set("X-Tenant-Id", tenantA)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// -----------------------------------------------------------------------------
// Tenant required across all Phyllis routes
// -----------------------------------------------------------------------------

func TestCatalogue_RoutesRequireTenant(t *testing.T) {
	srv, _ := newCatalogueServer()
	for _, p := range []string{"/courses", "/courses/abc", "/enrollments", "/me/enrollments"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		// no X-Tenant-Id
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("path=%s: expected 400 without tenant header, got %d", p, w.Code)
		}
	}
}
