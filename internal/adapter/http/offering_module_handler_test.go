// offering_module_handler_test.go — handler-level verification of the R+
// Phase-2 W7 (WS-A) offering-nested Module course-structure surface:
//
//	GET  /api/v1/offerings/{id}/modules?course_id=X
//	POST /api/v1/offerings/{id}/modules                {course_id,title}
//	POST /api/v1/offerings/{id}/modules/add-item       {course_id,module_id,content_item_id}
//	POST /api/v1/offerings/{id}/modules/remove-item    {course_id,module_id,item_id}
//	POST /api/v1/offerings/{id}/modules/reorder-items  {course_id,module_id,ordered_item_ids}
//	POST /api/v1/offerings/{id}/modules/set-requirement {course_id,module_id,kind,threshold_n,required_item_ids}
//	POST /api/v1/offerings/{id}/modules/remove         {course_id,module_id}
//
// VALIDATED PROXY: enforces course_id ∈ offering.CourseIDs, admin-gated writes,
// module↔course ownership, and the B1.2 edit-lock on LAUNCHED/RUNNING offerings.
// Reuses the curriculum-test harness helpers (seedCurriculumOffering /
// seedCurriculumOfferingState / reqWithHeaders + tenantID/instructor constants).
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/module"
)

// -----------------------------------------------------------------------------
// Wire shapes + harness
// -----------------------------------------------------------------------------

type moduleItemWire struct {
	ItemID        string `json:"item_id"`
	ContentItemID string `json:"content_item_id"`
	Position      int    `json:"position"`
}

type moduleReqWire struct {
	Kind            string   `json:"kind"`
	ThresholdN      int      `json:"threshold_n"`
	RequiredItemIDs []string `json:"required_item_ids"`
}

type moduleWire struct {
	ID          string           `json:"id"`
	CourseID    string           `json:"course_id"`
	Title       string           `json:"title"`
	Position    int              `json:"position"`
	Requirement moduleReqWire    `json:"requirement"`
	Items       []moduleItemWire `json:"items"`
}

type moduleListWire struct {
	CourseID string       `json:"course_id"`
	Modules  []moduleWire `json:"modules"`
}

// newOfferingModuleTestServer wires Offerings + Modules with in-mem stores so the
// module routes resolve DB-free (module ops need no CourseCJ2 / content service).
func newOfferingModuleTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, *module.InMemModuleStore) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	modStore := module.NewInMemModuleStore()
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings: oRepo,
		Modules:   modStore,
	})
	return srv, oRepo, modStore
}

func postModule(t *testing.T, srv http.Handler, path, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPost, path, b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func modulesPath(offeringID string) string { return "/api/v1/offerings/" + offeringID + "/modules" }

// createModule POSTs a module and returns its decoded wire object (fatal unless 201).
func createModule(t *testing.T, srv http.Handler, offeringID, courseID, title string) moduleWire {
	t.Helper()
	w := postModule(t, srv, modulesPath(offeringID), "instructor", map[string]any{
		"course_id": courseID, "title": title,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create module: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var m moduleWire
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode module: %v body=%s", err, w.Body.String())
	}
	return m
}

// -----------------------------------------------------------------------------
// Create + list
// -----------------------------------------------------------------------------

func TestOfferingModules_Create_DefaultsToAllItemsAndPositionZero(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	m := createModule(t, srv, curOfferingID, curCourseA, "  Foundations  ")
	if m.ID == "" {
		t.Fatalf("module must carry an id, body=%#v", m)
	}
	if m.CourseID != curCourseA || m.Title != "Foundations" || m.Position != 0 {
		t.Fatalf("module: want course=%s title=Foundations(trimmed) pos=0, got %s/%s/%d", curCourseA, m.CourseID, m.Title, m.Position)
	}
	if m.Requirement.Kind != "all_items" || len(m.Items) != 0 {
		t.Fatalf("fresh module: want all_items + 0 items, got %s + %d", m.Requirement.Kind, len(m.Items))
	}
	if m.Requirement.RequiredItemIDs == nil {
		t.Fatalf("required_item_ids must serialise as [] not null")
	}
}

func TestOfferingModules_Create_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	w := postModule(t, srv, modulesPath(curOfferingID), "instructor", map[string]any{
		"course_id": curCourseB, "title": "Orphan",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unattached course: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingModules_Create_403WhenNotAdmin(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	w := postModule(t, srv, modulesPath(curOfferingID), "learner", map[string]any{
		"course_id": curCourseA, "title": "Nope",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("learner: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingModules_Create_503WhenModulesUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // no Modules

	w := postModule(t, srv, modulesPath(curOfferingID), "instructor", map[string]any{
		"course_id": curCourseA, "title": "X",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired modules: want 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingModules_List_ReturnsCreatedInStructureOrder(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	createModule(t, srv, curOfferingID, curCourseA, "First")
	createModule(t, srv, curOfferingID, curCourseA, "Second")

	r := reqWithHeaders(http.MethodGet, modulesPath(curOfferingID)+"?course_id="+curCourseA, nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got moduleListWire
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list: %v body=%s", err, w.Body.String())
	}
	if len(got.Modules) != 2 || got.Modules[0].Title != "First" || got.Modules[1].Title != "Second" {
		t.Fatalf("list: want [First,Second] in order, got %#v", got.Modules)
	}
	if got.Modules[0].Position != 0 || got.Modules[1].Position != 1 {
		t.Fatalf("dense positions: want 0,1, got %d,%d", got.Modules[0].Position, got.Modules[1].Position)
	}
}

func TestOfferingModules_List_400WhenCourseIDMissing(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	r := reqWithHeaders(http.MethodGet, modulesPath(curOfferingID), nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing course_id: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Item ops
// -----------------------------------------------------------------------------

func TestOfferingModules_AddItem_AppendsAndPersists(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")

	w := postModule(t, srv, modulesPath(curOfferingID)+"/add-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "content_item_id": curAtomRef,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("add-item: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var updated moduleWire
	_ = json.Unmarshal(w.Body.Bytes(), &updated)
	if len(updated.Items) != 1 || updated.Items[0].ContentItemID != curAtomRef || updated.Items[0].Position != 0 {
		t.Fatalf("add-item: want 1 item ref=%s pos=0, got %#v", curAtomRef, updated.Items)
	}
}

func TestOfferingModules_AddItem_404WhenModuleMissing(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)

	w := postModule(t, srv, modulesPath(curOfferingID)+"/add-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": curAtomRef2 /* a UUID that is no module */, "content_item_id": curAtomRef,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing module: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingModules_AddItem_404WhenModuleBelongsToAnotherCourse(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA, curCourseB)
	m := createModule(t, srv, curOfferingID, curCourseA, "A-module")

	// courseB is attached, but module m belongs to courseA → cross-course reach is 404.
	w := postModule(t, srv, modulesPath(curOfferingID)+"/add-item", "instructor", map[string]any{
		"course_id": curCourseB, "module_id": m.ID, "content_item_id": curAtomRef,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-course module: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingModules_RemoveItem_RecompactsPositions(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef)
	after := addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef2)
	firstItemID := after.Items[0].ItemID

	w := postModule(t, srv, modulesPath(curOfferingID)+"/remove-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "item_id": firstItemID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("remove-item: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got moduleWire
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Items) != 1 || got.Items[0].ContentItemID != curAtomRef2 || got.Items[0].Position != 0 {
		t.Fatalf("after remove: want [ref2@0], got %#v", got.Items)
	}
}

func TestOfferingModules_SetRequirement_NOfMAndValidation(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef)
	addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef2)

	// n_of_m with a valid threshold (1 of 2) → 200.
	ok := postModule(t, srv, modulesPath(curOfferingID)+"/set-requirement", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "kind": "n_of_m", "threshold_n": 1,
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("valid n_of_m: want 200, got %d body=%s", ok.Code, ok.Body.String())
	}
	var got moduleWire
	_ = json.Unmarshal(ok.Body.Bytes(), &got)
	if got.Requirement.Kind != "n_of_m" || got.Requirement.ThresholdN != 1 {
		t.Fatalf("requirement not persisted: got %#v", got.Requirement)
	}

	// threshold > item count → 422 (fail-loud rule validation).
	bad := postModule(t, srv, modulesPath(curOfferingID)+"/set-requirement", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "kind": "n_of_m", "threshold_n": 5,
	})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over-cap n_of_m: want 422, got %d body=%s", bad.Code, bad.Body.String())
	}
}

func TestOfferingModules_Remove_SoftDeletesAndDropsFromList(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "Doomed")

	w := postModule(t, srv, modulesPath(curOfferingID)+"/remove", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("remove module: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	// List is now empty.
	r := reqWithHeaders(http.MethodGet, modulesPath(curOfferingID)+"?course_id="+curCourseA, nil, instructor, "instructor")
	lw := httptest.NewRecorder()
	srv.ServeHTTP(lw, r)
	var got moduleListWire
	_ = json.Unmarshal(lw.Body.Bytes(), &got)
	if len(got.Modules) != 0 {
		t.Fatalf("after soft-delete: want 0 modules, got %d", len(got.Modules))
	}
}

// -----------------------------------------------------------------------------
// B1.2 edit-lock — a course bundled by a LAUNCHED/RUNNING offering is locked (409)
// -----------------------------------------------------------------------------

func TestOfferingModules_Create_409WhenCourseInLiveOffering(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	// The offering we author THROUGH is DRAFT, but another offering attaching the
	// same course is LAUNCHED → the shared course's structure is locked.
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	seedCurriculumOfferingState(t, oRepo, "01985e7f-2222-7abc-8def-0000000000d9", delivery.OfferingStateLaunched, curCourseA)

	w := postModule(t, srv, modulesPath(curOfferingID), "instructor", map[string]any{
		"course_id": curCourseA, "title": "Should be locked",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("live-offering lock: want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// addModuleItem POSTs one content-item into a module and returns the updated
// module wire (fatal unless 200) — a helper for multi-item tests.
func addModuleItem(t *testing.T, srv http.Handler, offeringID, courseID, moduleID, contentItemID string) moduleWire {
	t.Helper()
	w := postModule(t, srv, modulesPath(offeringID)+"/add-item", "instructor", map[string]any{
		"course_id": courseID, "module_id": moduleID, "content_item_id": contentItemID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("add-item helper: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var m moduleWire
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode add-item: %v body=%s", err, w.Body.String())
	}
	return m
}
