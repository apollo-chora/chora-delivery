// offering_curriculum_write_test.go — handler-level verification of the R+
// Phase-2 S1 offering-nested Curriculum AUTHORING surface (making the read-only
// W2.D outline write-capable so an admin can actually build out a course from
// the offering workspace):
//
//	POST /api/v1/offerings/{id}/curriculum          add one content item
//	POST /api/v1/offerings/{id}/curriculum/reorder  reorder a course's items
//	POST /api/v1/offerings/{id}/curriculum/remove   remove one content item
//
// The offering endpoint is a VALIDATED PROXY: it enforces course_id ∈
// offering.CourseIDs then delegates to the existing CourseContent service
// (AddItem/Reorder/RemoveItem, CHO-1612). The Course still OWNS its content
// (atom-centric: items reference atoms by UUID, never own them) — the offering
// does not. Intra-chora_delivery, POST-only (edge-safe, no DELETE), admin-gated
// (hasOfferingAdminRole). Reuses the offering_curriculum_handler_test.go in-mem
// harness (newOfferingCurriculumTestServer / seed* / reqWithHeaders).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	cc "github.com/apollo-chora/chora-delivery/internal/domain/course_content"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// Second atom ref for multi-item ordering tests (curAtomRef lives in the read test).
const curAtomRef2 = "01985e7f-2222-7abc-8def-0000000000f3"

// curriculumWriteItem is the wire shape of one item row in a write response
// (superset of the read row — includes `ref` so the editor can render/edit it).
type curriculumWriteItem struct {
	ItemID   string `json:"item_id"`
	Kind     string `json:"kind"`
	Ref      string `json:"ref"`
	Title    string `json:"title"`
	Position int    `json:"position"`
}

// curriculumWriteResp is the envelope returned by the three write endpoints:
// the affected course's full, freshly-ordered outline.
type curriculumWriteResp struct {
	CourseID string                `json:"course_id"`
	Items    []curriculumWriteItem `json:"items"`
}

// postCurriculum POSTs a JSON body to a curriculum (sub)path and returns the recorder.
func postCurriculum(t *testing.T, srv http.Handler, path, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPost, path, b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/curriculum  — add item
// -----------------------------------------------------------------------------

func TestOfferingCurriculum_Add_AppendsItemToAttachedCourse(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseA,
		"kind":      "atom",
		"ref":       curAtomRef,
		"title":     "  Limits  ",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp curriculumWriteResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if resp.CourseID != curCourseA {
		t.Fatalf("course_id: want %s, got %s", curCourseA, resp.CourseID)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items: want 1, got %d body=%s", len(resp.Items), w.Body.String())
	}
	it := resp.Items[0]
	if it.ItemID == "" {
		t.Fatalf("added item must carry an item_id, body=%s", w.Body.String())
	}
	if it.Kind != "atom" || it.Ref != curAtomRef || it.Title != "Limits" || it.Position != 0 {
		t.Fatalf("item: want atom/%s/Limits(trimmed)/0, got %s/%s/%s/%d", curAtomRef, it.Kind, it.Ref, it.Title, it.Position)
	}

	// Persisted → the read surface now lists it.
	_, read := getCurriculum(t, srv, curOfferingID, "instructor")
	if len(read.Courses) != 1 || len(read.Courses[0].Items) != 1 || read.Courses[0].Items[0].Title != "Limits" {
		t.Fatalf("added item not visible via GET, got %#v", read.Courses)
	}
}

// -----------------------------------------------------------------------------
// B1.2 — edit-lock: a shared course's curriculum may NOT be edited while any
// offering attaching it is LAUNCHED or RUNNING (a live cohort would see its
// content shift under it). Fail-loud 409.
// -----------------------------------------------------------------------------

// seedCurriculumOfferingState stores an offering in an explicit FSM state.
func seedCurriculumOfferingState(t *testing.T, oRepo *inmem.OfferingRepo, id string, state delivery.OfferingState, courseIDs ...string) {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    courseIDs,
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "B1.2 lock run",
		Capacity:     0,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	o.State = state
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save offering: %v", err)
	}
}

func TestOfferingCurriculum_Add_409WhenCourseInLiveOffering(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	// The course is attached to a LAUNCHED offering — editing its curriculum
	// (even via this same offering) must be refused.
	seedCurriculumOfferingState(t, oRepo, curOfferingID, delivery.OfferingStateLaunched, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseA, "kind": "atom", "ref": curAtomRef, "title": "Late edit",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status: want 409 (course in live offering), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCurriculum_Add_409WhenCourseInOTHERLiveOffering(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	// Edit via a DRAFT offering, but the SAME shared course is also attached to a
	// separate RUNNING offering → still locked (the shared-canonical guardrail).
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA) // DRAFT (edit target)
	seedCurriculumOfferingState(t, oRepo, "01985e7f-2222-7abc-8def-00000000d9c2", delivery.OfferingStateRunning, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseA, "kind": "atom", "ref": curAtomRef, "title": "Late edit",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status: want 409 (course in OTHER live offering), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCurriculum_Add_OKWhenOnlyDraftOfferings(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	// Only DRAFT offerings attach the course → editing is allowed (200).
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")
	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseA, "kind": "atom", "ref": curAtomRef, "title": "Fine edit",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201 (only draft offerings), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCurriculum_Add_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA) // only A attached
	seedCJ2Course(t, courseStore, curCourseB, "Unrelated")

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseB, // NOT attached to this offering
		"kind":      "atom",
		"ref":       curAtomRef,
		"title":     "Nope",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (course not attached), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCurriculum_Add_400WhenInvalidKind(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseA,
		"kind":      "bogus", // cc.ErrInvalidArgument → 400 via writeContentErr
		"ref":       curAtomRef,
		"title":     "X",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (invalid kind), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCurriculum_Add_404WhenOfferingMissing(t *testing.T) {
	srv, _, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCJ2Course(t, courseStore, curCourseA, "Orphan") // offering NOT seeded

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "instructor", map[string]any{
		"course_id": curCourseA, "kind": "atom", "ref": curAtomRef, "title": "X",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingCurriculum_Add_403WithoutRole(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum", "", map[string]any{
		"course_id": curCourseA, "kind": "atom", "ref": curAtomRef, "title": "X",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/curriculum/reorder
// -----------------------------------------------------------------------------

func TestOfferingCurriculum_Reorder_SwapsOrder(t *testing.T) {
	srv, oRepo, courseStore, contentSvc := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")
	a1, err := contentSvc.AddItem(context.Background(), tenantID, curCourseA, instructor, cc.AddItemParams{Kind: cc.KindAtom, Ref: curAtomRef, Title: "First"})
	if err != nil {
		t.Fatalf("seed item 1: %v", err)
	}
	id1 := a1.Items[len(a1.Items)-1].ItemID
	a2, err := contentSvc.AddItem(context.Background(), tenantID, curCourseA, instructor, cc.AddItemParams{Kind: cc.KindAtom, Ref: curAtomRef2, Title: "Second"})
	if err != nil {
		t.Fatalf("seed item 2: %v", err)
	}
	id2 := a2.Items[len(a2.Items)-1].ItemID

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/reorder", "instructor", map[string]any{
		"course_id":        curCourseA,
		"ordered_item_ids": []string{id2, id1},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp curriculumWriteResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 2 || resp.Items[0].ItemID != id2 || resp.Items[1].ItemID != id1 {
		t.Fatalf("reorder: want [%s,%s], got %#v", id2, id1, resp.Items)
	}
	if resp.Items[0].Position != 0 || resp.Items[1].Position != 1 {
		t.Fatalf("positions not compacted after reorder: %#v", resp.Items)
	}
}

func TestOfferingCurriculum_Reorder_405OnGet(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")

	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+curOfferingID+"/curriculum/reorder", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/curriculum/remove
// -----------------------------------------------------------------------------

func TestOfferingCurriculum_Remove_DropsItem(t *testing.T) {
	srv, oRepo, courseStore, contentSvc := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")
	agg, err := contentSvc.AddItem(context.Background(), tenantID, curCourseA, instructor, cc.AddItemParams{Kind: cc.KindAtom, Ref: curAtomRef, Title: "Doomed"})
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	itemID := agg.Items[len(agg.Items)-1].ItemID

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/remove", "instructor", map[string]any{
		"course_id": curCourseA,
		"item_id":   itemID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp curriculumWriteResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(resp.Items) != 0 {
		t.Fatalf("remove: want empty outline, got %#v", resp.Items)
	}

	// Confirmed gone via GET.
	_, read := getCurriculum(t, srv, curOfferingID, "instructor")
	if len(read.Courses) != 1 || len(read.Courses[0].Items) != 0 {
		t.Fatalf("removed item still visible via GET, got %#v", read.Courses)
	}
}

func TestOfferingCurriculum_Remove_404WhenItemMissing(t *testing.T) {
	srv, oRepo, courseStore, _ := newOfferingCurriculumTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCJ2Course(t, courseStore, curCourseA, "Calculus I")

	w := postCurriculum(t, srv, "/api/v1/offerings/"+curOfferingID+"/curriculum/remove", "instructor", map[string]any{
		"course_id": curCourseA,
		"item_id":   "01985e7f-2222-7abc-8def-000000000fff", // not a member
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404 (item not found), got %d body=%s", w.Code, w.Body.String())
	}
}
