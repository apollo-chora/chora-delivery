// offering_curriculum_om4_test.go — om4 coverage top-ups for
// offering_curriculum_handler.go. The pre-existing read + write suites cover
// the happy paths and the edit-lock 409s; these tests drive the remaining
// reachable branches: the unwired fail-loud 503s (offerings nil / course-content
// nil) on both the read and the three write endpoints, the 401 no-gcid gate, the
// decodeBody 400s, the reorder/remove offering-attach + edit-lock guards, the
// reorder non-permutation 400 through writeContentErr, and curriculumEditLocked's
// soft-deleted-offering skip (an archived offering no longer locks its course).
package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// om4NoGCID builds a request with X-Tenant-Id + role but NO gcid so the
// callerTenantGCID 401 gate in each offering handler fires.
func om4NoGCID(method, path string, body []byte, role string) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("X-Tenant-Id", tenantID)
	if role != "" {
		r.Header.Set("x-mesh-user-roles", role)
	}
	return r
}

// om4RawCurriculumPost POSTs a raw byte body (unmarshalable on purpose) to a
// curriculum sub-path so decodeBody's 400 guard fires.
func om4RawCurriculumPost(t *testing.T, srv http.Handler, path, raw string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodPost, path, []byte(raw), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// -----------------------------------------------------------------------------
// GET /curriculum — wiring + identity gates
// -----------------------------------------------------------------------------

// TestOm4Curriculum_Get_503WhenOfferingsUnwired drives the read endpoint's
// first unwired guard.
func TestOm4Curriculum_Get_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+curOfferingID+"/curriculum", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Get_503WhenContentUnwired drives the read endpoint's
// course-content service guard (offerings wired, CourseCJ2 empty).
func TestOm4Curriculum_Get_503WhenContentUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo})
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+curOfferingID+"/curriculum", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Get_401WithoutGCID drives callerTenantGCID's 401 for the
// curriculum read.
func TestOm4Curriculum_Get_401WithoutGCID(t *testing.T) {
	srv, oRepo, _, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	r := om4NoGCID(http.MethodGet, "/api/v1/offerings/"+curOfferingID+"/curriculum", nil, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Get_EmptyTitleWhenCourseMissingFromCJ2 drives the
// `else if found && c != nil` FALSE branch: an attached course with NO CJ#2 row
// renders with an empty title (not an error) — the suite always seeded CJ2
// shells, so this degradation branch was uncovered.
func TestOm4Curriculum_Get_EmptyTitleWhenCourseMissingFromCJ2(t *testing.T) {
	srv, oRepo, _, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA) // courseA has NO CJ2 shell

	w, resp := getCurriculum(t, srv, curOfferingID, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	if len(resp.Courses) != 1 || resp.Courses[0].ID != curCourseA || resp.Courses[0].Title != "" {
		t.Fatalf("row: want [%s] with empty title, got %#v", curCourseA, resp.Courses)
	}
}

// -----------------------------------------------------------------------------
// POST /curriculum — decodeBody 400 + write-preamble 503s
// -----------------------------------------------------------------------------

// TestOm4Curriculum_Add_401WithoutGCID drives offeringForCurriculumWrite's
// callerTenantGCID 401 (the write preamble's tenant=="" early return).
func TestOm4Curriculum_Add_401WithoutGCID(t *testing.T) {
	srv, oRepo, _, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	r := om4NoGCID(http.MethodPost, "/api/v1/offerings/"+curOfferingID+"/curriculum", []byte(`{"course_id":"`+curCourseA+`","kind":"atom","ref":"`+curAtomRef+`","title":"X"}`), "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Add_400MalformedJSON drives decodeBody on the add endpoint
// (the suite's 400s were all domain/value refusals).
func TestOm4Curriculum_Add_400MalformedJSON(t *testing.T) {
	srv, oRepo, _, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := om4RawCurriculumPost(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", `{"course_id":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Add_503WhenOfferingsUnwired drives offeringForCurriculumWrite's
// offerings-nil guard + the add handler's `if !ok` early return.
func TestOm4Curriculum_Add_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{})
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseA, "kind": "atom", "ref": curAtomRef, "title": "X",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Add_503WhenContentUnwired drives offeringForCurriculumWrite's
// course-content-nil guard (offerings wired only).
func TestOm4Curriculum_Add_503WhenContentUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo})
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseA, "kind": "atom", "ref": curAtomRef, "title": "X",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /curriculum/reorder — guards left uncovered by the suite
// -----------------------------------------------------------------------------

// TestOm4Curriculum_Reorder_404WhenOfferingMissing drives the reorder write
// preamble miss (the suite's 404 only covered add).
func TestOm4Curriculum_Reorder_404WhenOfferingMissing(t *testing.T) {
	srv, _, _, _ := newOfferingCurriculumTestServer(t)
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/reorder", "instructor", map[string]any{
		"course_id": curCourseA, "ordered_item_ids": []string{},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Reorder_400MalformedJSON drives decodeBody on reorder.
func TestOm4Curriculum_Reorder_400MalformedJSON(t *testing.T) {
	srv, oRepo, _, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := om4RawCurriculumPost(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/reorder", `{"course_id": "x"`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Reorder_400WhenCourseNotAttached drives reorder's
// offeringHasCourse guard.
func TestOm4Curriculum_Reorder_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, _, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA) // only A attached
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/reorder", "instructor", map[string]any{
		"course_id": curCourseB, "ordered_item_ids": []string{},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Reorder_409WhenCourseLocked drives reorder's edit-lock
// (the suite's 409s only covered add).
func TestOm4Curriculum_Reorder_409WhenCourseLocked(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOfferingState(t, oRepo, curOfferingID, delivery.OfferingStateLaunched, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Locked Course")
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/reorder", "instructor", map[string]any{
		"course_id": curCourseA, "ordered_item_ids": []string{},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Reorder_400BadPermutation drives the Reorder service error
// → writeContentErr 400 (cc.ErrInvalidReorder) — a non-permutation id list.
func TestOm4Curriculum_Reorder_400BadPermutation(t *testing.T) {
	srv, oRepo, courseStore, contentSvc := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Course")
	seedCourseContentItem(t, contentSvc, curCourseA, cc.KindAtom, curAtomRef, "Only Item")
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/reorder", "instructor", map[string]any{
		"course_id":        curCourseA,
		"ordered_item_ids": []string{"01985e7f-2222-7abc-8def-000000000fff"}, // not a member
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /curriculum/remove — guards left uncovered by the suite
// -----------------------------------------------------------------------------

// TestOm4Curriculum_Remove_404WhenOfferingMissing drives the remove write
// preamble miss.
func TestOm4Curriculum_Remove_404WhenOfferingMissing(t *testing.T) {
	srv, _, _, _ := newOfferingCurriculumTestServer(t)
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/remove", "instructor", map[string]any{
		"course_id": curCourseA, "item_id": curAtomRef,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Remove_400MalformedJSON drives decodeBody on remove.
func TestOm4Curriculum_Remove_400MalformedJSON(t *testing.T) {
	srv, oRepo, _, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := om4RawCurriculumPost(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/remove", `{"item_id":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Remove_400WhenCourseNotAttached drives remove's
// offeringHasCourse guard.
func TestOm4Curriculum_Remove_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA) // only A attached
	seedCJ2Course(t, courseStore, curCourseB, "Unrelated")
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/remove", "instructor", map[string]any{
		"course_id": curCourseB, "item_id": curAtomRef,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Curriculum_Remove_409WhenCourseLocked drives remove's edit-lock.
func TestOm4Curriculum_Remove_409WhenCourseLocked(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOfferingState(t, oRepo, curOfferingID, delivery.OfferingStateRunning, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Locked Course")
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/remove", "instructor", map[string]any{
		"course_id": curCourseA, "item_id": curAtomRef,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// curriculumEditLocked — soft-deleted offering skip
// -----------------------------------------------------------------------------

// TestOm4Curriculum_Edit_OKWhenLiveOfferingArchived drives curriculumEditLocked's
// `o == nil || o.DeletedAt != nil` skip: a LAUNCHED offering that was since
// ARCHIVED no longer locks its shared course, so the edit proceeds.
func TestOm4Curriculum_Edit_OKWhenLiveOfferingArchived(t *testing.T) {
	srv, oRepo, courseStore, contentSvc := newOfferingCurriculumTestServer(t)
	// The SAME course is attached to (a) the edit-target DRAFT offering and (b) a
	// once-LAUNCHED offering that has since been archived (soft-deleted).
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCurriculumOfferingState(t, oRepo, "01985e7f-2222-7abc-8def-00000000d9c3", delivery.OfferingStateLaunched, curCourseA)
	live, ok, err := oRepo.Get(context.Background(), "01985e7f-2222-7abc-8def-00000000d9c3")
	if err != nil || !ok {
		t.Fatalf("load launched offering: ok=%v err=%v", ok, err)
	}
	live.Archive()
	if err := oRepo.Save(context.Background(), live); err != nil {
		t.Fatalf("Save archived offering: %v", err)
	}
	seedCJ2Course(t, courseStore, curCourseA, "Unlocked Course")
	seedCourseContentItem(t, contentSvc, curCourseA, cc.KindAtom, curAtomRef, "Lesson 1")

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseA, "kind": "atom", "ref": curAtomRef2, "title": "Lesson 2",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201 (archived offering no longer locks), got %d body=%s", w.Code, w.Body.String())
	}
}
