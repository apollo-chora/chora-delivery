// offering_prerequisites_om4_test.go — om4 coverage top-ups for
// offering_prerequisites_handler.go. The pre-existing suite covers the happy
// add/remove/get paths + the 422 graph refusals (self/cycle/unknown); these
// tests drive the reachable leftovers: the unwired fail-loud 503s (offerings /
// service) on all three endpoints, the decodeBody 400s, the write-preamble 404
// for remove, the offering-attach 400 for remove, get on a missing offering,
// the writePrerequisiteErr 400 classes (ErrPrerequisiteCourseRequired /
// ErrPrerequisiteKindInvalid), and the empty-title courses-unwired GET row.
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// om4DecodeJSON unmarshals a response body into v (fatal on failure).
func om4DecodeJSON(t *testing.T, w *httptest.ResponseRecorder, v interface{}) error {
	t.Helper()
	return json.Unmarshal(w.Body.Bytes(), v)
}

// om4RawPrereqPost POSTs a raw byte body (unmarshalable on purpose) to a
// prerequisites sub-path so decodeBody's 400 guard fires.
func om4RawPrereqPost(t *testing.T, srv http.Handler, path, raw string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodPost, path, []byte(raw), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// -----------------------------------------------------------------------------
// POST /prerequisites — decodeBody + error-map 400 classes + unwired 503s
// -----------------------------------------------------------------------------

// TestOm4Prereq_Add_400MalformedJSON drives decodeBody on the add endpoint.
func TestOm4Prereq_Add_400MalformedJSON(t *testing.T) {
	srv, oRepo, _ := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	w := om4RawPrereqPost(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", `{"course_id":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Add_400BadKind drives writePrerequisiteErr's
// ErrPrerequisiteKindInvalid → 400 class.
func TestOm4Prereq_Add_400BadKind(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	seedCJ2Course(t, courseStore, preqCourseA, "A")
	seedCJ2Course(t, courseStore, preqCourseB, "B")
	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB, "kind": "bogus",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad kind: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Add_400EmptyTarget drives writePrerequisiteErr's
// ErrPrerequisiteCourseRequired → 400 class.
func TestOm4Prereq_Add_400EmptyTarget(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	seedCJ2Course(t, courseStore, preqCourseA, "A")
	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": "", "kind": "hard_gate",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty target: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Add_503WhenOfferingsUnwired drives offeringForPrerequisiteWrite's
// offerings-nil guard + the add handler's `if !ok` early return.
func TestOm4Prereq_Add_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB, "kind": "hard_gate",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Add_401WithoutGCID drives the write preamble's
// callerTenantGCID 401 (tenant present, gcid absent → early return).
func TestOm4Prereq_Add_401WithoutGCID(t *testing.T) {
	srv, oRepo, _ := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	r := om4NoGCID(http.MethodPost, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", []byte(`{"course_id":"`+preqCourseA+`","prerequisite_course_id":"`+preqCourseB+`","kind":"hard_gate"}`), "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Add_503WhenServiceUnwired drives offeringForPrerequisiteWrite's
// service-nil guard (offerings wired only).
func TestOm4Prereq_Add_503WhenServiceUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo})
	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB, "kind": "hard_gate",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /prerequisites/remove — guards left uncovered by the suite
// -----------------------------------------------------------------------------

// TestOm4Prereq_Remove_404WhenOfferingMissing drives the remove write preamble
// miss (the suite's 404 only covered add).
func TestOm4Prereq_Remove_404WhenOfferingMissing(t *testing.T) {
	srv, _, _ := newOfferingPrereqTestServer(t)
	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites/remove", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Remove_400MalformedJSON drives decodeBody on remove.
func TestOm4Prereq_Remove_400MalformedJSON(t *testing.T) {
	srv, oRepo, _ := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	w := om4RawPrereqPost(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites/remove", `{"course_id":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Remove_400WhenCourseNotAttached drives remove's
// offeringHasCourse guard.
func TestOm4Prereq_Remove_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, _ := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA) // only A attached
	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites/remove", "instructor", map[string]any{
		"course_id": preqCourseB, "prerequisite_course_id": preqCourseA,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Remove_400EmptyTarget drives the Remove service's
// ErrPrerequisiteCourseRequired → writePrerequisiteErr 400 class.
func TestOm4Prereq_Remove_400EmptyTarget(t *testing.T) {
	srv, oRepo, _ := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites/remove", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": "",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty target remove: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /prerequisites — wiring + miss + courses-unwired row
// -----------------------------------------------------------------------------

// TestOm4Prereq_Get_503WhenOfferingsUnwired drives handleOfferingGetPrerequisites'
// first unwired guard.
func TestOm4Prereq_Get_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Get_503WhenServiceUnwired drives the prerequisite-service guard
// (offerings wired only).
func TestOm4Prereq_Get_503WhenServiceUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo})
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Get_404WhenOfferingMissing drives the read's tenant-scoped
// 404 (the suite only 404'd the write path).
func TestOm4Prereq_Get_404WhenOfferingMissing(t *testing.T) {
	srv, _, _ := newOfferingPrereqTestServer(t)
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Prereq_Get_EmptyTitleWhenCoursesUnwired builds a server whose
// CourseCJ2 port has the prerequisite service but NO course store: the GET must
// still render each attached course with an empty title (the `Courses != nil`
// enrichment guard degrades gracefully).
func TestOm4Prereq_Get_EmptyTitleWhenCoursesUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	edges := delivery.NewInMemCoursePrerequisiteStore()
	svc := delivery.NewCoursePrerequisiteService(edges, nil, 32)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{Prerequisites: svc},
	})
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	var resp prereqGetResp
	if err := om4DecodeJSON(t, w, &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(resp.Courses) != 1 || resp.Courses[0].CourseID != preqCourseA || resp.Courses[0].Title != "" {
		t.Fatalf("row: want [%s] with empty title, got %#v", preqCourseA, resp.Courses)
	}
}
