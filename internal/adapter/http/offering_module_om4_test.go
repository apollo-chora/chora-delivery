// offering_module_om4_test.go — om4 coverage top-ups for
// offering_module_handler.go. The pre-existing offering_module_handler_test.go
// suite covers the happy paths + the course-attach/edit-lock 409s; these tests
// drive the branches it left behind: the fail-loud 503s (offerings unwired /
// modules unwired), the decodeBody 400s (malformed JSON), the module err-map
// classes reachable via the in-memory store (ErrInvalidArgument / ErrDuplicate
// Item → 409 / ErrCapExceeded → 422 / ErrRequirementViolation → 422 /
// ErrInvalidReorder → 400 / ErrItemNotFound → 404), the moduleForCourseInOffering
// guard branches, and the previously-0% handleOfferingReorderModuleItems in
// full (happy + each guard).
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/module"
)

// om4RawModulePost POSTs a raw byte body (unmarshalable on purpose) to a module
// sub-path — reqWithHeaders lets the caller own the exact wire bytes.
func om4RawModulePost(t *testing.T, srv http.Handler, path, raw string) *httptest.ResponseRecorder {
	t.Helper()
	r := reqWithHeaders(http.MethodPost, path, []byte(raw), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// om4SeedModuleStoreOnRepo pre-seeds a fully-formed module directly into the
// in-memory module store (bypassing the HTTP surface) so a handler test can
// start from an exotic aggregate shape — e.g. a module already AT the item cap.
func om4SeedModuleStoreOnRepo(t *testing.T, modStore *module.InMemModuleStore, tenantID, courseID string, items int) *module.Module {
	t.Helper()
	m, err := module.New(module.NewParams{TenantID: tenantID, CourseID: courseID, Title: "Cap Module"})
	if err != nil {
		t.Fatalf("module.New: %v", err)
	}
	for i := 0; i < items; i++ {
		m.Items = append(m.Items, &module.ModuleItem{
			ID:            string(rune('a'+i%26)) + string(rune('0'+i/26)) + "00000000-7000-8000-000000000000",
			ModuleID:      m.ID,
			ContentItemID: curAtomRef,
			Position:      i,
		})
	}
	if _, err := modStore.Create(context.Background(), m); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	return m
}

// -----------------------------------------------------------------------------
// LIST — wiring + authz + course guards
// -----------------------------------------------------------------------------

// TestOm4Module_List_503WhenOfferingsUnwired drives handleOfferingListModules'
// first unwired guard (offerings nil — the suites' 503 covered only Modules nil).
func TestOm4Module_List_503WhenOfferingsUnwired(t *testing.T) {
	modStore := module.NewInMemModuleStore()
	srv := httpapi.NewServer(httpapi.Deps{Modules: modStore})
	r := reqWithHeaders(http.MethodGet, modulesPath(curOfferingID)+"?course_id="+curCourseA, nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_List_403WithoutRole drives the hasInstructorRole gate (the
// suite never hit it for the list route).
func TestOm4Module_List_403WithoutRole(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	r := reqWithHeaders(http.MethodGet, modulesPath(curOfferingID)+"?course_id="+curCourseA, nil, instructor, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_List_503WhenModulesUnwired drives handleOfferingListModules'
// SECOND unwired guard (offerings wired, module store nil — the mirror of the
// Offerings-nil 503 above).
func TestOm4Module_List_503WhenModulesUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // Modules nil
	r := reqWithHeaders(http.MethodGet, modulesPath(curOfferingID)+"?course_id="+curCourseA, nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_Create_503WhenOfferingsUnwired drives the WRITE preamble's
// offerings-nil guard (offeringForModuleWrite — distinct from the list's own
// check).
func TestOm4Module_Create_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{}) // Offerings nil
	w := postModule(t, srv, modulesPath(curOfferingID), "instructor", map[string]any{
		"course_id": curCourseA, "title": "X",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_List_401WithoutGCID drives the list's callerTenantGCID 401
// (per-handler statement: the list route had no no-gcid test).
func TestOm4Module_List_401WithoutGCID(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	r := om4NoGCID(http.MethodGet, modulesPath(curOfferingID)+"?course_id="+curCourseA, nil, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_List_400WhenCourseNotAttached drives the list's
// offeringHasCourse guard with a course_id that is NOT attached (the suite's
// 400 covered only the missing-course_id case).
func TestOm4Module_List_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA) // only A attached
	r := reqWithHeaders(http.MethodGet, modulesPath(curOfferingID)+"?course_id="+curCourseB, nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_List_404WhenOfferingMissing drives the list's tenant-scoped
// 404 (the suite never listed over a missing offering).
func TestOm4Module_List_404WhenOfferingMissing(t *testing.T) {
	srv, _, _ := newOfferingModuleTestServer(t)
	r := reqWithHeaders(http.MethodGet, modulesPath(curOfferingID)+"?course_id="+curCourseA, nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// CREATE — decodeBody + domain-constructor errors
// -----------------------------------------------------------------------------

// TestOm4Module_Create_400MalformedJSON drives handleOfferingCreateModule's
// decodeBody guard.
func TestOm4Module_Create_400MalformedJSON(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := om4RawModulePost(t, srv, modulesPath(curOfferingID), `{"course_id": "x",`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_Create_400BlankTitle drives module.New's ErrInvalidArgument →
// writeModuleErr 400 (a trim-blank title is the only constructor refusal the
// in-memory store can surface on create).
func TestOm4Module_Create_400BlankTitle(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID), "instructor", map[string]any{
		"course_id": curCourseA, "title": "   ",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_Create_404WhenOfferingMissing drives the write-preamble 404
// (offeringForModuleWrite tenant-scoped miss) for the create handler.
func TestOm4Module_Create_404WhenOfferingMissing(t *testing.T) {
	srv, _, _ := newOfferingModuleTestServer(t)
	w := postModule(t, srv, modulesPath(curOfferingID), "instructor", map[string]any{
		"course_id": curCourseA, "title": "X",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// ADD-ITEM — moduleForCourseInOffering guards + error-map classes
// -----------------------------------------------------------------------------

// TestOm4Module_AddItem_400MalformedJSON drives decodeBody on add-item.
func TestOm4Module_AddItem_400MalformedJSON(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := om4RawModulePost(t, srv, modulesPath(curOfferingID)+"/add-item", `{"course_id":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_AddItem_400UnattachedCourse drives moduleForCourseInOffering's
// offeringHasCourse 400 (the suite only reached its 404 cross-course branch).
func TestOm4Module_AddItem_400UnattachedCourse(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA) // only A attached
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	w := postModule(t, srv, modulesPath(curOfferingID)+"/add-item", "instructor", map[string]any{
		"course_id": curCourseB, "module_id": m.ID, "content_item_id": curAtomRef,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_AddItem_400EmptyModuleID drives moduleForCourseInOffering's
// module_id-required 400.
func TestOm4Module_AddItem_400EmptyModuleID(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/add-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": "   ", "content_item_id": curAtomRef,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_AddItem_409Duplicate drives the store AddItem → ErrDuplicateItem
// → writeModuleErr 409 class.
func TestOm4Module_AddItem_409Duplicate(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/add-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "content_item_id": curAtomRef, // already a member
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("dup add-item: want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_AddItem_409WhenCourseLocked drives the add-item edit-lock (the
// suite covered the lock only for create). The module itself is created while
// only the DRAFT offering attaches the course; the LAUNCHED lock lands after.
func TestOm4Module_AddItem_409WhenCourseLocked(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	seedCurriculumOfferingState(t, oRepo, "01985e7f-2222-7abc-8def-0000000000e9", delivery.OfferingStateLaunched, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/add-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "content_item_id": curAtomRef,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("locked add-item: want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_AddItem_422WhenAtCap drives the store AddItem → ErrCapExceeded
// → writeModuleErr 422 class: the module is pre-seeded AT the 200-item cap so a
// single add-item trips the cap guard (no 200-request loop needed).
func TestOm4Module_AddItem_422WhenAtCap(t *testing.T) {
	srv, oRepo, modStore := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := om4SeedModuleStoreOnRepo(t, modStore, tenantID, curCourseA, module.MaxItemsPerModule)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/add-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "content_item_id": curAtomRef2,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("at-cap add-item: want 422, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// REMOVE-ITEM — requirement-violation + item-miss error classes
// -----------------------------------------------------------------------------

// TestOm4Module_RemoveItem_400MalformedJSON drives decodeBody on remove-item.
func TestOm4Module_RemoveItem_400MalformedJSON(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := om4RawModulePost(t, srv, modulesPath(curOfferingID)+"/remove-item", `{`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_RemoveItem_404UnknownItem drives the store RemoveItem →
// ErrItemNotFound → writeModuleErr 404 class.
func TestOm4Module_RemoveItem_404UnknownItem(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/remove-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "item_id": "01985e7f-2222-7abc-8def-000000000fff",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown item remove: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_RemoveItem_404UnknownModule drives the remove-item handler's
// moduleForCourseInOffering miss (closing the per-handler `!ok` statement).
func TestOm4Module_RemoveItem_404UnknownModule(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/remove-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": "01985e7f-2222-7abc-8def-000000000fff", "item_id": "x",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing module remove-item: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_RemoveItem_422RequirementViolation drives the store RemoveItem
// → ErrRequirementViolation → writeModuleErr 422 class: an n_of_m requirement
// that the removal would break fails loud.
func TestOm4Module_RemoveItem_422RequirementViolation(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef)
	after := addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef2)
	if w := postModule(t, srv, modulesPath(curOfferingID)+"/set-requirement", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "kind": "n_of_m", "threshold_n": 2,
	}); w.Code != http.StatusOK {
		t.Fatalf("seed n_of_m/2: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	w := postModule(t, srv, modulesPath(curOfferingID)+"/remove-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "item_id": after.Items[0].ItemID,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("violating remove: want 422, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_RemoveItem_409WhenCourseLocked drives the remove-item edit-lock
// (module created unlocked, then the RUNNING lock lands).
func TestOm4Module_RemoveItem_409WhenCourseLocked(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	seedCurriculumOfferingState(t, oRepo, "01985e7f-2222-7abc-8def-0000000000ea", delivery.OfferingStateRunning, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/remove-item", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "item_id": "x",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("locked remove-item: want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// REORDER-ITEMS (previously 0-coveraged) — full path + each guard
// -----------------------------------------------------------------------------

// TestOm4Module_Reorder_200SwapsOrder is the happy path: a full permutation of
// the active item ids re-stamps dense positions.
func TestOm4Module_Reorder_200SwapsOrder(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef)
	after := addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef2)
	first, second := after.Items[0].ItemID, after.Items[1].ItemID

	w := postModule(t, srv, modulesPath(curOfferingID)+"/reorder-items", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "ordered_item_ids": []string{second, first},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("reorder: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got moduleWire
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(got.Items) != 2 || got.Items[0].ItemID != second || got.Items[1].ItemID != first {
		t.Fatalf("reorder: want [%s,%s], got %#v", second, first, got.Items)
	}
	if got.Items[0].Position != 0 || got.Items[1].Position != 1 {
		t.Fatalf("positions not compacted: %#v", got.Items)
	}
}

// TestOm4Module_Reorder_400NotAPermutation drives the store Reorder →
// ErrInvalidReorder → writeModuleErr 400 class (wrong-length id list).
func TestOm4Module_Reorder_400NotAPermutation(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	addModuleItem(t, srv, curOfferingID, curCourseA, m.ID, curAtomRef)

	w := postModule(t, srv, modulesPath(curOfferingID)+"/reorder-items", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID,
		"ordered_item_ids": []string{"01985e7f-2222-7abc-8def-000000000fff"}, // length mismatch
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad reorder: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_Reorder_400MalformedJSON drives decodeBody on reorder-items.
func TestOm4Module_Reorder_400MalformedJSON(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := om4RawModulePost(t, srv, modulesPath(curOfferingID)+"/reorder-items", `{"course_id": "x"`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_Reorder_404ModuleMissing drives moduleForCourseInOffering's
// miss 404 on reorder-items.
func TestOm4Module_Reorder_404ModuleMissing(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/reorder-items", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": "01985e7f-2222-7abc-8def-000000000fff",
		"ordered_item_ids": []string{},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing module reorder: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_Reorder_409WhenCourseLocked drives the reorder edit-lock
// (module created unlocked, then the LAUNCHED lock lands).
func TestOm4Module_Reorder_409WhenCourseLocked(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	seedCurriculumOfferingState(t, oRepo, "01985e7f-2222-7abc-8def-0000000000eb", delivery.OfferingStateLaunched, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/reorder-items", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "ordered_item_ids": []string{},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("locked reorder: want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// SET-REQUIREMENT + REMOVE — remaining guard classes
// -----------------------------------------------------------------------------

// TestOm4Module_SetRequirement_400MalformedJSON drives decodeBody on
// set-requirement.
func TestOm4Module_SetRequirement_400MalformedJSON(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := om4RawModulePost(t, srv, modulesPath(curOfferingID)+"/set-requirement", `{"kind":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_SetRequirement_404UnknownModule drives the set-requirement
// handler's moduleForCourseInOffering miss (closing the per-handler `!ok`).
func TestOm4Module_SetRequirement_404UnknownModule(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/set-requirement", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": "01985e7f-2222-7abc-8def-000000000fff", "kind": "all_items",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing module set-requirement: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_SetRequirement_422BogusKind drives the SetRequirement →
// ErrInvalidRequirement → writeModuleErr 422 class via an unknown kind (the
// suite's 422 covered only the over-cap threshold).
func TestOm4Module_SetRequirement_422BogusKind(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	w := postModule(t, srv, modulesPath(curOfferingID)+"/set-requirement", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "kind": "bogus",
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bogus kind: want 422, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_SetRequirement_409WhenCourseLocked drives the set-requirement
// edit-lock (module created unlocked, then the RUNNING lock lands).
func TestOm4Module_SetRequirement_409WhenCourseLocked(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	seedCurriculumOfferingState(t, oRepo, "01985e7f-2222-7abc-8def-0000000000ec", delivery.OfferingStateRunning, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/set-requirement", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID, "kind": "all_items",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("locked set-requirement: want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_Remove_400MalformedJSON drives decodeBody on module remove.
func TestOm4Module_Remove_400MalformedJSON(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := om4RawModulePost(t, srv, modulesPath(curOfferingID)+"/remove", `!!!`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_Remove_404ModuleMissing drives the remove handler's
// moduleForCourseInOffering miss (the suite covered remove only on an existing
// module).
func TestOm4Module_Remove_404ModuleMissing(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/remove", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": "01985e7f-2222-7abc-8def-000000000fff",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing module remove: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestOm4Module_Remove_409WhenCourseLocked drives the module-remove edit-lock
// (module created unlocked, then the RUNNING lock lands).
func TestOm4Module_Remove_409WhenCourseLocked(t *testing.T) {
	srv, oRepo, _ := newOfferingModuleTestServer(t)
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	m := createModule(t, srv, curOfferingID, curCourseA, "M1")
	seedCurriculumOfferingState(t, oRepo, "01985e7f-2222-7abc-8def-0000000000ed", delivery.OfferingStateRunning, curCourseA)
	w := postModule(t, srv, modulesPath(curOfferingID)+"/remove", "instructor", map[string]any{
		"course_id": curCourseA, "module_id": m.ID,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("locked remove: want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// Write preamble fail-loud — every write handler's `if !ok { return }`
// -----------------------------------------------------------------------------

// TestOm4Module_AllWrites_503WhenModulesUnwired loops every module write
// sub-path against a server whose Modules port is nil — each handler's write
// preamble (`offeringForModuleWrite` → ok=false) returns 503, covering the
// `if !ok` statement in create/add-item/remove-item/reorder-items/
// set-requirement/remove.
func TestOm4Module_AllWrites_503WhenModulesUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedCurriculumOffering(t, oRepo, curOfferingID, curCourseA)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // Modules nil

	subs := []string{"", "/add-item", "/remove-item", "/reorder-items", "/set-requirement", "/remove"}
	for _, sub := range subs {
		w := postModule(t, srv, modulesPath(curOfferingID)+sub, "instructor", map[string]any{
			"course_id": curCourseA, "module_id": curAtomRef, "title": "X",
		})
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: want 503, got %d body=%s", sub, w.Code, w.Body.String())
		}
	}
}
