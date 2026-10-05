// offering_schedule_handler.go — HTTP handlers for the R+ offering-nested
// Schedule & Rooms surface (four-mode short delivery_type):
//
//	GET  /api/v1/offerings/{id}/schedule  — list this offering's sessions
//	POST /api/v1/offerings/{id}/schedule  — create one session
//
// An OfferingSession (offering_session domain, chora_delivery.offering_sessions
// — mig 0036) is a scheduled delivery meeting (title + room + time) belonging to
// an Offering. Unlike Sections (JSONB-on-offering), sessions get their OWN table
// because an Attendance record references a stable session_id. offering_id is a
// cross-aggregate UUID reference (no FK, ddd-enforcement #3) — intra-delivery,
// NO cross-DB query, NO new event. There is NO DELETE (soft-delete is a future
// POST /cancel, edge-safe).
//
// Authorisation mirrors the Sections surface: instructor / admin / training-admin
// (hasOfferingAdminRole) + X-Tenant-Id + gcid (callerTenantGCID). Tenant-scoped
// throughout; the dispatcher 405s any method other than GET/POST.
package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	offeringsession "github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
)

// createSessionReq is the POST body (snake_case wire). `title` +
// `starts_at`/`ends_at` are load-bearing. A room is booked by `room_id` (a Room
// aggregate the picker selects, SP3); `room` free-text is the LEGACY field and,
// supplied WITHOUT a room_id, is rejected 400 (the ratified no-free-text-bypass
// rule) — it is never stored. instructor is optional.
type createSessionReq struct {
	Title          string    `json:"title"`
	RoomID         string    `json:"room_id"`
	Room           string    `json:"room"`
	InstructorGCID string    `json:"instructor_gcid"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
}

// handleOfferingListSchedule — GET /api/v1/offerings/{id}/schedule.
func handleOfferingListSchedule(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.OfferingSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "offering-sessions repo not wired")
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
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	sessions, err := deps.OfferingSessions.ListByOffering(ctx, tenantID, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session list failed: "+err.Error())
		return
	}
	dtos := make([]map[string]interface{}, 0, len(sessions))
	for _, s := range sessions {
		dtos = append(dtos, offeringSessionDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": dtos})
}

// handleOfferingCreateSession — POST /api/v1/offerings/{id}/schedule.
//
// Steps: (a) wiring 503; (b) tenant 400 + gcid 401 + admin 403; (c) offering
// must exist for this tenant (404); (d) decode body (400 malformed); (e) the
// ratified room_id gate (CHO-2191 SP2): a room is booked by room_id ONLY —
// free-text `room` without a room_id is 400 room_required (no typo-bypass); a
// room_id that resolves to no Room is 400 room_not_found (no phantom
// auto-provision); the offering's seat budget exceeding the room's capacity is
// 409 room_over_capacity; a roomless request (no room_id, no free text) is
// allowed (room_id NULL, exempt from the gate); (f) NewOfferingSession domain
// guards (400 on blank title / ends<=starts); (g) Save — a room_id double-book
// (DB EXCLUDE, mig 0052) is 409 room_double_booked, any other write error 500;
// (h) 201 + the created session.
func handleOfferingCreateSession(deps Deps, offeringID string, w http.ResponseWriter, r *http.Request) {
	if deps.Offerings == nil {
		writeError(w, http.StatusServiceUnavailable, "offerings repo not wired")
		return
	}
	if deps.OfferingSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "offering-sessions repo not wired")
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
	o, ok, err := deps.Offerings.Get(ctx, offeringID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offering lookup failed: "+err.Error())
		return
	}
	if !ok || o == nil || o.TenantID != tenantID || o.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "offering not found")
		return
	}
	var req createSessionReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// (e) The ratified room_id gate. A room is booked by room_id ONLY.
	roomID := strings.TrimSpace(req.RoomID)
	roomName := ""
	if roomID == "" {
		// Free-text room without a room_id is the typo-bypass the owner rejected
		// ("Room A " vs "Room A" would fork a phantom room and re-open the clash).
		if strings.TrimSpace(req.Room) != "" {
			writeErrorCode(w, http.StatusBadRequest, "room_required",
				"a room must be booked by room_id (select one from GET /api/v1/rooms), not free text")
			return
		}
		// else: a genuinely roomless session — allowed, room_id stays NULL.
	} else {
		if deps.Rooms == nil {
			writeError(w, http.StatusServiceUnavailable, "rooms store not wired")
			return
		}
		room, ok, err := deps.Rooms.GetForTenant(ctx, tenantID, roomID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "room lookup failed: "+err.Error())
			return
		}
		if !ok || room == nil {
			// No auto-provision — a room_id must resolve to a real Room.
			writeErrorCode(w, http.StatusBadRequest, "room_not_found",
				"no such room for this tenant; create it via POST /api/v1/rooms first")
			return
		}
		// Over-capacity: the offering's seat budget must not exceed the room's
		// capacity. Offering.Capacity == 0 means unbounded (async) and is exempt.
		if o.Capacity > 0 && o.Capacity > room.Capacity {
			writeErrorCode(w, http.StatusConflict, "room_over_capacity",
				fmt.Sprintf("offering seat budget %d exceeds room %q capacity %d", o.Capacity, room.Name, room.Capacity))
			return
		}
		// Display name is DERIVED from the Room aggregate, never free text.
		roomName = room.Name
	}

	sess, err := offeringsession.NewOfferingSession(offeringsession.NewOfferingSessionInput{
		TenantID:       tenantID,
		OfferingID:     offeringID,
		Title:          req.Title,
		RoomID:         roomID,
		Room:           roomName,
		InstructorGCID: req.InstructorGCID,
		StartsAt:       req.StartsAt,
		EndsAt:         req.EndsAt,
	})
	if err != nil {
		// Domain guard failure (blank title, ends<=starts) → bad request.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := deps.OfferingSessions.Save(ctx, sess); err != nil {
		// Ratified CHO-2191 / ADR-237: a room_id double-book (DB EXCLUDE, mig
		// 0052) is a client conflict, not our fault — fail loud with 409 + a
		// typed code, not a masked 500 (the gateway launders 5xx and erases the
		// reason). Any OTHER Save error is a genuine outage → 5xx.
		if errors.Is(err, offeringsession.ErrRoomDoubleBooked) {
			writeErrorCode(w, http.StatusConflict, "room_double_booked", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, offeringSessionDTO(sess))
}

// offeringSessionDTO renders an OfferingSession for the JSON wire (snake_case).
func offeringSessionDTO(s *offeringsession.OfferingSession) map[string]interface{} {
	return map[string]interface{}{
		"id":              s.ID,
		"title":           s.Title,
		"room_id":         s.RoomID,
		"room":            s.Room,
		"instructor_gcid": s.InstructorGCID,
		"starts_at":       s.StartsAt.UTC().Format(time.RFC3339),
		"ends_at":         s.EndsAt.UTC().Format(time.RFC3339),
		"created_at":      s.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":      s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}
