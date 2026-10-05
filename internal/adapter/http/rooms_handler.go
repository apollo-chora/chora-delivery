// rooms_handler.go — HTTP handlers for the R+ Rooms surface (CHO-2191 SP1):
//
//	POST /api/v1/rooms  — create a room
//	GET  /api/v1/rooms  — list this tenant's rooms
//
// A Room (campusops.Room, chora_delivery.rooms — mig 0051) is a bookable unit
// the ratified room_id-keyed double-book/over-capacity gate keys on (SP2 builds
// the gate; SP1 only promotes Room to Postgres + exposes create/list). campus_id
// + branch_id are OPTIONAL (a scheduling room needs no campus).
//
// Authorisation mirrors the Schedule surface: instructor / admin / training-admin
// (hasOfferingAdminRole) + X-Tenant-Id + gcid (callerTenantGCID). Tenant-scoped
// throughout; the dispatcher 405s any method other than GET/POST.
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
)

// createRoomReq is the POST body (snake_case wire). name + capacity are
// load-bearing; campus_id + branch_id are optional.
type createRoomReq struct {
	Name     string `json:"name"`
	Capacity int    `json:"capacity"`
	CampusID string `json:"campus_id"`
	BranchID string `json:"branch_id"`
}

// updateRoomReq is the PATCH/PUT body (snake_case wire). Both fields are
// POINTERS so an omitted field (nil) is distinguishable from a zero value: a
// PATCH may carry name-only, capacity-only, or both. At least one must be
// present (else 400 room_no_fields). campus_id / branch_id are NOT editable
// through this endpoint; decodeBody's DisallowUnknownFields 400s them.
type updateRoomReq struct {
	Name     *string `json:"name"`
	Capacity *int    `json:"capacity"`
}

// roomsRootHandler dispatches /api/v1/rooms by method.
func roomsRootHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListRooms(deps, w, r)
		case http.MethodPost:
			handleCreateRoom(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// handleCreateRoom — POST /api/v1/rooms.
//
// Steps: (a) wiring 503; (b) tenant 400 + gcid 401 + admin 403; (c) decode body
// (400 malformed); (d) NewRoom domain guards (400 on blank name / capacity<=0);
// (e) Save (500 on write error); (f) 201 + the created room.
func handleCreateRoom(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Rooms == nil {
		writeError(w, http.StatusServiceUnavailable, "rooms store not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req createRoomReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	room, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantID,
		CampusID: req.CampusID,
		BranchID: req.BranchID,
		Name:     req.Name,
		Capacity: req.Capacity,
	})
	if err != nil {
		// Domain guard failure (blank name, capacity<=0) → bad request.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	if err := deps.Rooms.Save(ctx, room); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, roomDTO(room))
}

// handleListRooms — GET /api/v1/rooms (this tenant's active rooms).
func handleListRooms(deps Deps, w http.ResponseWriter, r *http.Request) {
	if deps.Rooms == nil {
		writeError(w, http.StatusServiceUnavailable, "rooms store not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	rooms, err := deps.Rooms.ListByTenant(ctx, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "room list failed: "+err.Error())
		return
	}
	dtos := make([]map[string]interface{}, 0, len(rooms))
	for _, rm := range rooms {
		dtos = append(dtos, roomDTO(rm))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"rooms": dtos})
}

// roomByIDHandler dispatches /api/v1/rooms/{id} (CHO-2332):
//
//	PUT | PATCH → update (rename / re-capacity)
//	DELETE      → soft-delete
//
// Deeper or blank-segment paths are 404 (mirrors credentialsSubHandler). The
// method 405 fires before auth, mirroring roomsRootHandler's dispatch order;
// the 503 / tenant / role gates live in the per-method handlers, identical to
// create.
func roomByIDHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/rooms/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")
		for _, p := range parts {
			if p == "" { // "", "id//", nested → not a valid leaf
				writeError(w, http.StatusNotFound, "not found")
				return
			}
		}
		if len(parts) != 1 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		id := parts[0]
		switch r.Method {
		case http.MethodPut, http.MethodPatch:
			handleUpdateRoom(deps, id, w, r)
		case http.MethodDelete:
			handleDeleteRoom(deps, id, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// handleUpdateRoom - PATCH/PUT /api/v1/rooms/{id}.
//
// Steps: (a) wiring 503; (b) tenant 400 + gcid 401 + admin 403; (c) decode body
// (400 malformed / unknown field); (d) require >=1 field (400 room_no_fields);
// (e) load the tenant-scoped room (404 miss / 500 infra); (f) apply mutators
// through the domain guards (400 on blank name / capacity<=0); (g) Save (500 on
// write error); (h) 200 + the created-handler roomDTO shape.
func handleUpdateRoom(deps Deps, id string, w http.ResponseWriter, r *http.Request) {
	if deps.Rooms == nil {
		writeError(w, http.StatusServiceUnavailable, "rooms store not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req updateRoomReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == nil && req.Capacity == nil {
		writeError(w, http.StatusBadRequest, "room_no_fields: at least one of name or capacity is required")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	room, ok, err := deps.Rooms.GetForTenant(ctx, tenantID, id)
	if err != nil {
		// Infra/RLS failure is LOUD; never collapses to a 404 miss (CHO-2184).
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "room not found")
		return
	}
	if req.Name != nil {
		if err := room.Rename(*req.Name); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Capacity != nil {
		if err := room.SetCapacity(*req.Capacity); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := deps.Rooms.Save(ctx, room); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, roomDTO(room))
}

// handleDeleteRoom - DELETE /api/v1/rooms/{id}.
//
// Soft-delete only (NEVER hard-delete): load the tenant-scoped room (404 miss /
// 500 infra), stamp deleted_at via the aggregate, persist through Save (the
// upsert writes deleted_at), and return 204. A repeat DELETE is 404 because
// GetForTenant filters deleted_at IS NULL. Existing bookings that reference the
// room keep their room_id (UUID without FK), so history renders unchanged; the
// room simply drops out of the booking picker (CHO-2332 "inert in history").
func handleDeleteRoom(deps Deps, id string, w http.ResponseWriter, r *http.Request) {
	if deps.Rooms == nil {
		writeError(w, http.StatusServiceUnavailable, "rooms store not wired")
		return
	}
	tenantID, _ := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	if !hasOfferingAdminRole(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	room, ok, err := deps.Rooms.GetForTenant(ctx, tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "room not found")
		return
	}
	room.SoftDelete()
	if err := deps.Rooms.Save(ctx, room); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// roomDTO renders a Room for the JSON wire (snake_case).
func roomDTO(rm *campusops.Room) map[string]interface{} {
	return map[string]interface{}{
		"id":         rm.ID,
		"name":       rm.Name,
		"capacity":   rm.Capacity,
		"campus_id":  rm.CampusID,
		"branch_id":  rm.BranchID,
		"created_at": rm.CreatedAt.UTC().Format(time.RFC3339),
	}
}
