// rooms_handler_test.go — handler-level verification of the R+ Rooms surface
// (CHO-2191 SP1):
//
//	POST /api/v1/rooms  — create a room (admin-gated)
//	GET  /api/v1/rooms  — list this tenant's rooms
//
// Mirrors offering_schedule_handler_test.go's admin-gated write harness; reuses
// the http_test package tenantID / instructor / reqWithHeaders helpers.
package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

type roomDTOResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Capacity  int    `json:"capacity"`
	CampusID  string `json:"campus_id"`
	BranchID  string `json:"branch_id"`
	CreatedAt string `json:"created_at"`
}

type roomsListResp struct {
	Rooms []roomDTOResp `json:"rooms"`
}

func newRoomsTestServer() (http.Handler, *repoinmem.RoomRepo) {
	repo := repoinmem.NewRoomRepo()
	srv := httpapi.NewServer(httpapi.Deps{Rooms: repo})
	return srv, repo
}

func postRoom(srv http.Handler, role, body string) *httptest.ResponseRecorder {
	r := reqWithHeaders(http.MethodPost, "/api/v1/rooms", []byte(body), instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func getRooms(srv http.Handler, role string) (*httptest.ResponseRecorder, roomsListResp) {
	r := reqWithHeaders(http.MethodGet, "/api/v1/rooms", nil, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	var resp roomsListResp
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

func TestRooms_Post_Creates201(t *testing.T) {
	srv, _ := newRoomsTestServer()
	w := postRoom(srv, "instructor", `{"name":"Lab A","capacity":30}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var created roomDTOResp
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" || created.Name != "Lab A" || created.Capacity != 30 {
		t.Fatalf("created room: %+v", created)
	}
	if created.CreatedAt == "" {
		t.Fatalf("created_at must be set; got %+v", created)
	}
	// Blank campus/branch round-trip as empty strings in the DTO.
	if created.CampusID != "" || created.BranchID != "" {
		t.Fatalf("expected blank campus/branch; got %+v", created)
	}
}

func TestRooms_Post_ThenListedForTenant(t *testing.T) {
	srv, _ := newRoomsTestServer()
	w := postRoom(srv, "instructor", `{"name":"Lab A","capacity":30}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var created roomDTOResp
	_ = json.Unmarshal(w.Body.Bytes(), &created)

	lw, resp := getRooms(srv, "instructor")
	if lw.Code != http.StatusOK {
		t.Fatalf("GET status: want 200, got %d", lw.Code)
	}
	if len(resp.Rooms) != 1 || resp.Rooms[0].ID != created.ID {
		t.Fatalf("list after create: want [%s], got %+v", created.ID, resp.Rooms)
	}
}

func TestRooms_Post_WithCampusAndBranch(t *testing.T) {
	srv, _ := newRoomsTestServer()
	body := `{"name":"Room 12","capacity":12,"campus_id":"01985e7f-6666-7abc-8def-000000000c01","branch_id":"01985e7f-6666-7abc-8def-000000000b01"}`
	w := postRoom(srv, "instructor", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var created roomDTOResp
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.CampusID != "01985e7f-6666-7abc-8def-000000000c01" || created.BranchID != "01985e7f-6666-7abc-8def-000000000b01" {
		t.Fatalf("campus/branch not echoed; got %+v", created)
	}
}

func TestRooms_GetEmptyList(t *testing.T) {
	srv, _ := newRoomsTestServer()
	w, resp := getRooms(srv, "instructor")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if len(resp.Rooms) != 0 {
		t.Fatalf("rooms: want 0, got %d", len(resp.Rooms))
	}
	if !strings.Contains(w.Body.String(), `"rooms":[]`) {
		t.Fatalf("body must contain rooms=[], got %s", w.Body.String())
	}
}

func TestRooms_403WithoutRole(t *testing.T) {
	srv, _ := newRoomsTestServer()
	w := postRoom(srv, "", `{"name":"Lab A","capacity":30}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST status: want 403, got %d", w.Code)
	}
	gw, _ := getRooms(srv, "")
	if gw.Code != http.StatusForbidden {
		t.Fatalf("GET status: want 403, got %d", gw.Code)
	}
}

func TestRooms_Post_400OnBadCapacity(t *testing.T) {
	srv, _ := newRoomsTestServer()
	w := postRoom(srv, "instructor", `{"name":"Lab A","capacity":0}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (capacity=0), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_Post_400OnMalformedBody(t *testing.T) {
	srv, _ := newRoomsTestServer()
	w := postRoom(srv, "instructor", `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (malformed), got %d", w.Code)
	}
}

func TestRooms_405OnPut(t *testing.T) {
	srv, _ := newRoomsTestServer()
	r := reqWithHeaders(http.MethodPut, "/api/v1/rooms", []byte("{}"), instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: want 405, got %d", w.Code)
	}
}

func TestRooms_503WhenUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{}) // Rooms nil
	w, _ := getRooms(srv, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// CHO-2332 - Edit (PATCH/PUT) + soft-Delete (DELETE) /api/v1/rooms/{id}
// -----------------------------------------------------------------------------

// itemReq drives /api/v1/rooms/{id} with an explicit method + optional body.
func itemReq(srv http.Handler, method, role, id, body string) *httptest.ResponseRecorder {
	var b []byte
	if body != "" {
		b = []byte(body)
	}
	r := reqWithHeaders(method, "/api/v1/rooms/"+id, b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// seedRoom creates a room and returns its id.
func seedRoom(t *testing.T, srv http.Handler) string {
	t.Helper()
	w := postRoom(srv, "instructor", `{"name":"Lab A","capacity":30}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var created roomDTOResp
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("seed decode: %v", err)
	}
	return created.ID
}

func TestRooms_Patch_UpdatesName(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodPatch, "instructor", id, `{"name":"Lab Renamed"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got roomDTOResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != id || got.Name != "Lab Renamed" || got.Capacity != 30 {
		t.Fatalf("patched DTO: %+v", got)
	}
	// The rename must be visible in the list (pickers read the list).
	_, list := getRooms(srv, "instructor")
	if len(list.Rooms) != 1 || list.Rooms[0].Name != "Lab Renamed" {
		t.Fatalf("list after rename: %+v", list.Rooms)
	}
}

func TestRooms_Patch_UpdatesCapacity(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodPatch, "instructor", id, `{"capacity":50}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got roomDTOResp
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Capacity != 50 || got.Name != "Lab A" {
		t.Fatalf("patched DTO: %+v", got)
	}
}

func TestRooms_Put_UpdatesBothFields(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodPut, "instructor", id, `{"name":"Hall","capacity":200}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status: want 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got roomDTOResp
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Name != "Hall" || got.Capacity != 200 {
		t.Fatalf("put DTO: %+v", got)
	}
}

func TestRooms_Patch_400OnNoFields(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodPatch, "instructor", id, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (no fields), got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "room_no_fields") {
		t.Fatalf("body must name room_no_fields, got %s", w.Body.String())
	}
}

func TestRooms_Patch_400OnBlankName(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodPatch, "instructor", id, `{"name":"   "}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (blank name), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_Patch_400OnBadCapacity(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodPatch, "instructor", id, `{"capacity":0}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (capacity=0), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_Patch_400OnMalformedBody(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodPatch, "instructor", id, `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (malformed), got %d", w.Code)
	}
}

func TestRooms_Patch_400OnUnknownField(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	// campus_id/branch_id are NOT editable through this endpoint (spec: name+capacity).
	w := itemReq(srv, http.MethodPatch, "instructor", id, `{"campus_id":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (unknown field), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_Patch_404OnMissing(t *testing.T) {
	srv, _ := newRoomsTestServer()
	w := itemReq(srv, http.MethodPatch, "instructor", "01985e7f-6666-7abc-8def-0000000000ff", `{"name":"x"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404 (missing), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_Patch_403WithoutRole(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodPatch, "", id, `{"name":"x"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d", w.Code)
	}
}

// Tenant isolation: a caller from another tenant cannot see or edit the room.
func TestRooms_Patch_404CrossTenant(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	r := reqWithHeaders(http.MethodPatch, "/api/v1/rooms/"+id, []byte(`{"name":"Hijack"}`), instructor, "instructor")
	r.Header.Set("X-Tenant-Id", "01985e7f-9999-7abc-8def-000000000aaa") // different tenant
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant PATCH: want 404, got %d body=%s", w.Code, w.Body.String())
	}
	// Original room must be untouched for its real tenant.
	_, list := getRooms(srv, "instructor")
	if len(list.Rooms) != 1 || list.Rooms[0].Name != "Lab A" {
		t.Fatalf("cross-tenant PATCH must not mutate: %+v", list.Rooms)
	}
}

func TestRooms_Delete_SoftDeletes204(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodDelete, "instructor", id, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE status: want 204, got %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.Len() != 0 {
		t.Fatalf("204 must have empty body, got %s", w.Body.String())
	}
	// Gone from the list (pickers).
	_, list := getRooms(srv, "instructor")
	if len(list.Rooms) != 0 {
		t.Fatalf("deleted room must vanish from list, got %+v", list.Rooms)
	}
}

func TestRooms_Delete_RepeatReturns404(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	if w := itemReq(srv, http.MethodDelete, "instructor", id, ""); w.Code != http.StatusNoContent {
		t.Fatalf("first DELETE: want 204, got %d", w.Code)
	}
	// GetForTenant filters deleted_at IS NULL, so a repeat is a genuine miss.
	if w := itemReq(srv, http.MethodDelete, "instructor", id, ""); w.Code != http.StatusNotFound {
		t.Fatalf("repeat DELETE: want 404, got %d", w.Code)
	}
	// And a PATCH of the deleted room is likewise 404.
	if w := itemReq(srv, http.MethodPatch, "instructor", id, `{"name":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("PATCH deleted: want 404, got %d", w.Code)
	}
}

func TestRooms_Delete_404OnMissing(t *testing.T) {
	srv, _ := newRoomsTestServer()
	w := itemReq(srv, http.MethodDelete, "instructor", "01985e7f-6666-7abc-8def-0000000000ff", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", w.Code)
	}
}

func TestRooms_Delete_403WithoutRole(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodDelete, "", id, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d", w.Code)
	}
}

func TestRooms_ItemRoute_405OnGet(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	w := itemReq(srv, http.MethodGet, "instructor", id, "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET item: want 405, got %d", w.Code)
	}
}

func TestRooms_ItemRoute_404OnEmptyID(t *testing.T) {
	srv, _ := newRoomsTestServer()
	// "/api/v1/rooms/" with no id segment is not an item route leaf.
	w := itemReq(srv, http.MethodPatch, "instructor", "", `{"name":"x"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("empty-id PATCH: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_ItemRoute_404OnDeeperPath(t *testing.T) {
	srv, _ := newRoomsTestServer()
	id := seedRoom(t, srv)
	// A nested path under a room id is not a route leaf.
	w := itemReq(srv, http.MethodPatch, "instructor", id+"/schedule", `{"name":"x"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("deeper path: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_ItemRoute_503WhenUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{}) // Rooms nil
	for _, m := range []string{http.MethodPatch, http.MethodDelete} {
		w := itemReq(srv, m, "instructor", "01985e7f-6666-7abc-8def-0000000000ff", `{"name":"x"}`)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status: want 503, got %d body=%s", m, w.Code, w.Body.String())
		}
	}
}

// -----------------------------------------------------------------------------
// Fail-loud (CHO-2184): an infra/RLS failure on read or write must surface a
// 500; NEVER collapse to a silent 404 (miss) or 200 (success). Exercised with
// a RoomStore double that errors, since the inmem repo never fails.
// -----------------------------------------------------------------------------

type failingRoomStore struct {
	getErr  error
	saveErr error
	room    *campusops.Room // returned by GetForTenant when getErr == nil
}

func (f *failingRoomStore) Save(_ context.Context, _ *campusops.Room) error { return f.saveErr }
func (f *failingRoomStore) GetForTenant(_ context.Context, _, _ string) (*campusops.Room, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return f.room, f.room != nil, nil
}
func (f *failingRoomStore) ListByTenant(_ context.Context, _ string) ([]*campusops.Room, error) {
	return nil, nil
}

func mustRoom(t *testing.T) *campusops.Room {
	t.Helper()
	rm, err := campusops.NewRoom(campusops.NewRoomInput{TenantID: tenantID, Name: "Lab A", Capacity: 30})
	if err != nil {
		t.Fatalf("NewRoom: %v", err)
	}
	return rm
}

func TestRooms_Update_500OnReadError(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{Rooms: &failingRoomStore{getErr: errors.New("boom: rls apply failed")}})
	w := itemReq(srv, http.MethodPatch, "instructor", "01985e7f-6666-7abc-8def-0000000000ff", `{"name":"x"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("PATCH read-error: want 500, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_Update_500OnSaveError(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{Rooms: &failingRoomStore{room: mustRoom(t), saveErr: errors.New("boom: write failed")}})
	w := itemReq(srv, http.MethodPatch, "instructor", "01985e7f-6666-7abc-8def-0000000000ff", `{"capacity":42}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("PATCH save-error: want 500, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_Delete_500OnReadError(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{Rooms: &failingRoomStore{getErr: errors.New("boom: rls apply failed")}})
	w := itemReq(srv, http.MethodDelete, "instructor", "01985e7f-6666-7abc-8def-0000000000ff", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE read-error: want 500, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestRooms_Delete_500OnSaveError(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{Rooms: &failingRoomStore{room: mustRoom(t), saveErr: errors.New("boom: write failed")}})
	w := itemReq(srv, http.MethodDelete, "instructor", "01985e7f-6666-7abc-8def-0000000000ff", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE save-error: want 500, got %d body=%s", w.Code, w.Body.String())
	}
}
