// offering_prerequisites_handler_test.go — handler-level verification of the
// R+ Phase-2 offering-nested Prerequisite DAG surface (ADR-226):
//
//	GET  /api/v1/offerings/{id}/prerequisites          read edges + notes per course
//	POST /api/v1/offerings/{id}/prerequisites          add a course→course edge
//	POST /api/v1/offerings/{id}/prerequisites/remove   drop an edge
//
// VALIDATED PROXY: enforces course_id ∈ offering.CourseIDs, then delegates to
// the CoursePrerequisiteService (cycle-checked, catalogue-existence-guarded).
// Admin-gated, POST-only writes (edge-safe, no DELETE). Domain graph refusals
// (cycle / self / cap / unknown course) map to 422.
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

const (
	preqOfferingID = "01985e7f-2222-7abc-8def-0000000000c1"
	preqCourseA    = "01985e7f-2222-7abc-8def-0000000000c2"
	preqCourseB    = "01985e7f-2222-7abc-8def-0000000000c3"
	preqCourseC    = "01985e7f-2222-7abc-8def-0000000000c4"
)

// prereqEdgeRow is one wire prerequisite in a response.
type prereqEdgeRow struct {
	PrerequisiteCourseID    string `json:"prerequisite_course_id"`
	PrerequisiteCourseTitle string `json:"prerequisite_course_title"`
	Kind                    string `json:"kind"`
}

// prereqWriteResp is the POST add/remove envelope.
type prereqWriteResp struct {
	CourseID      string          `json:"course_id"`
	Prerequisites []prereqEdgeRow `json:"prerequisites"`
}

// prereqCourseBlock is one course row in the GET envelope.
type prereqCourseBlock struct {
	CourseID          string          `json:"course_id"`
	Title             string          `json:"title"`
	PrerequisiteNotes []string        `json:"prerequisite_notes"`
	Prerequisites     []prereqEdgeRow `json:"prerequisites"`
}
type prereqGetResp struct {
	Courses []prereqCourseBlock `json:"courses"`
}

// newOfferingPrereqTestServer wires Offerings + CourseCJ2 (Courses + Prerequisites)
// with in-mem repos so the prerequisites route resolves DB-free.
func newOfferingPrereqTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *delivery.InMemCourseCJ2Store) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	courseStore := delivery.NewInMemCourseCJ2Store()
	edges := delivery.NewInMemCoursePrerequisiteStore()
	svc := delivery.NewCoursePrerequisiteService(edges, courseStore, 32)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		CourseCJ2: &httpapi.CourseCJ2Deps{
			Courses:       courseStore,
			Prerequisites: svc,
		},
	})
	return srv, oRepo, courseStore
}

func postPrereq(t *testing.T, srv http.Handler, path, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPost, path, b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// -----------------------------------------------------------------------------
// POST /prerequisites — add
// -----------------------------------------------------------------------------

func TestOfferingPrereq_Add_HappyPath(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	seedCJ2Course(t, courseStore, preqCourseA, "Calculus II")
	seedCJ2Course(t, courseStore, preqCourseB, "Calculus I")

	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id":              preqCourseA,
		"prerequisite_course_id": preqCourseB,
		"kind":                   "hard_gate",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp prereqWriteResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if resp.CourseID != preqCourseA || len(resp.Prerequisites) != 1 {
		t.Fatalf("resp = %#v, want course A with 1 prereq", resp)
	}
	p := resp.Prerequisites[0]
	if p.PrerequisiteCourseID != preqCourseB || p.Kind != "hard_gate" || p.PrerequisiteCourseTitle != "Calculus I" {
		t.Fatalf("prereq = %#v, want B/hard_gate/'Calculus I'", p)
	}
}

func TestOfferingPrereq_Add_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA) // only A attached
	seedCJ2Course(t, courseStore, preqCourseA, "A")
	seedCJ2Course(t, courseStore, preqCourseB, "B")

	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id":              preqCourseB, // NOT attached
		"prerequisite_course_id": preqCourseA,
		"kind":                   "hard_gate",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (source not attached), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingPrereq_Add_422OnSelfEdge(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	seedCJ2Course(t, courseStore, preqCourseA, "A")

	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id":              preqCourseA,
		"prerequisite_course_id": preqCourseA,
		"kind":                   "hard_gate",
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: want 422 (self edge), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingPrereq_Add_422OnUnknownTarget(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	seedCJ2Course(t, courseStore, preqCourseA, "A")
	// preqCourseC never seeded → unknown target

	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id":              preqCourseA,
		"prerequisite_course_id": preqCourseC,
		"kind":                   "hard_gate",
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: want 422 (unknown target course), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingPrereq_Add_422OnCycle(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	// Attach BOTH courses so either can be the source of an edge.
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA, preqCourseB)
	seedCJ2Course(t, courseStore, preqCourseA, "A")
	seedCJ2Course(t, courseStore, preqCourseB, "B")

	// A requires B (ok)
	if w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB, "kind": "hard_gate",
	}); w.Code != http.StatusOK {
		t.Fatalf("seed A->B: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	// B requires A would close a cycle → 422
	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id": preqCourseB, "prerequisite_course_id": preqCourseA, "kind": "hard_gate",
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: want 422 (cycle), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingPrereq_Add_403WithoutRole(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	seedCJ2Course(t, courseStore, preqCourseA, "A")
	seedCJ2Course(t, courseStore, preqCourseB, "B")

	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB, "kind": "hard_gate",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingPrereq_Add_404WhenOfferingMissing(t *testing.T) {
	srv, _, courseStore := newOfferingPrereqTestServer(t)
	seedCJ2Course(t, courseStore, preqCourseA, "A") // offering NOT seeded

	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB, "kind": "hard_gate",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /prerequisites
// -----------------------------------------------------------------------------

func TestOfferingPrereq_Get_ListsEdgesAndTitles(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	seedCJ2Course(t, courseStore, preqCourseA, "Calculus II")
	seedCJ2Course(t, courseStore, preqCourseB, "Calculus I")
	// add A requires B
	if w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB, "kind": "advisory",
	}); w.Code != http.StatusOK {
		t.Fatalf("seed edge: %d body=%s", w.Code, w.Body.String())
	}

	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp prereqGetResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(resp.Courses) != 1 {
		t.Fatalf("courses: want 1, got %d body=%s", len(resp.Courses), w.Body.String())
	}
	cb := resp.Courses[0]
	if cb.CourseID != preqCourseA || cb.Title != "Calculus II" {
		t.Fatalf("course block = %#v, want A/'Calculus II'", cb)
	}
	if len(cb.Prerequisites) != 1 || cb.Prerequisites[0].PrerequisiteCourseTitle != "Calculus I" || cb.Prerequisites[0].Kind != "advisory" {
		t.Fatalf("prereqs = %#v, want [B/'Calculus I'/advisory]", cb.Prerequisites)
	}
}

// -----------------------------------------------------------------------------
// POST /prerequisites/remove
// -----------------------------------------------------------------------------

func TestOfferingPrereq_Remove_DropsEdge(t *testing.T) {
	srv, oRepo, courseStore := newOfferingPrereqTestServer(t)
	seedCurriculumOffering(t, oRepo, preqOfferingID, preqCourseA)
	seedCJ2Course(t, courseStore, preqCourseA, "A")
	seedCJ2Course(t, courseStore, preqCourseB, "B")
	if w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB, "kind": "hard_gate",
	}); w.Code != http.StatusOK {
		t.Fatalf("seed edge: %d", w.Code)
	}

	w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites/remove", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("remove status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp prereqWriteResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Prerequisites) != 0 {
		t.Fatalf("after remove: want 0 prereqs, got %#v", resp.Prerequisites)
	}
	// idempotent remove
	if w := postPrereq(t, srv, "/api/v1/offerings/"+preqOfferingID+"/prerequisites/remove", "instructor", map[string]any{
		"course_id": preqCourseA, "prerequisite_course_id": preqCourseB,
	}); w.Code != http.StatusOK {
		t.Fatalf("idempotent remove: want 200, got %d", w.Code)
	}
}
