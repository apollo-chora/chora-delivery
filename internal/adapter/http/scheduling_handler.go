// scheduling_handler.go — GET /v1/scheduling/classes?week_iso=YYYY-MM-DD
//
// R+ Stage 3 week-view calendar real-BFF wiring. The R+ /r/scheduling page
// (chora-web/src/app/features/surfaces/rplus/scheduling/scheduling.component.ts)
// previously held an in-memory CSPO fixture; this endpoint surfaces the real
// ScheduledClass aggregates owned by `internal/domain/scheduling`.
//
// Behaviour:
//
//   - `week_iso` query param is a YYYY-MM-DD date. The handler resolves the
//     ISO-8601 (year, week) containing that date and asks
//     SchedulingRepo.ListByTenantWeek to return all non-soft-deleted
//     ScheduledClass rows for the caller's tenant whose StartsAt falls
//     inside that ISO week.
//   - When `week_iso` is absent the handler defaults to the ISO week that
//     contains time.Now().UTC() so the FE can render the current week
//     without computing the date itself.
//   - The week list is sorted by ID (UUIDv7 ⇒ lexicographic == creation
//     order); ordering is stable and the FE can re-sort on the client.
//   - Cross-tenant isolation is enforced by SchedulingRepo (matches
//     scheduled_class.TenantID against the caller's X-Tenant-Id mesh claim).
//   - Soft-deleted rows are filtered by the repo.
//
// Per `feedback_no_stubs_real_wiring`: when the SchedulingRepo is empty the
// response is `{"items": []}` with status 200 — the UI renders an empty-state
// instead of faking data.
//
// Per `.claude/rules/development-execution.md` TDD: this implementation is
// authored AFTER `scheduling_handler_test.go` (RED first).
//
// Hexagonal: ADAPTER. Domain code never imports this file.
package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

// Request DTOs for the scheduling write-path (R+ Wave-2 follow-up).
type createScheduledClassReq struct {
	CourseID       string `json:"course_id"`
	InstructorGCID string `json:"instructor_gcid"`
	// CHO-2299: a room is booked by room_id ONLY; `room` is a display name.
	RoomID      string `json:"room_id"`
	Room        string `json:"room"`
	StartsAt    string `json:"starts_at"` // RFC3339
	EndsAt      string `json:"ends_at"`   // RFC3339
	MaxCapacity int    `json:"max_capacity"`
}

type rescheduleClassReq struct {
	StartsAt string `json:"starts_at"` // RFC3339
	EndsAt   string `json:"ends_at"`   // RFC3339
	RoomID   string `json:"room_id"`
	Room     string `json:"room"`
}

// schedulingClassesHandler serves the /v1/scheduling/classes collection:
//
//	GET  → week-view list (?week_iso=YYYY-MM-DD; defaults to current ISO week)
//	POST → create a ScheduledClass (instructor / admin / training-admin)
//
// Method dispatch is handled inside so unsupported methods yield a clean 405.
func schedulingClassesHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 503 fail-loud when the repo has not been wired. Per
		// feedback_no_stubs_real_wiring the handler does NOT fake an
		// empty week — boot-time should always supply Scheduling.
		if deps.Scheduling == nil {
			writeError(w, http.StatusServiceUnavailable, "scheduling repo not wired")
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleSchedulingWeekList(deps, w, r)
		case http.MethodPost:
			handleCreateScheduledClass(deps, w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// handleSchedulingWeekList serves GET /v1/scheduling/classes[?week_iso=...].
func handleSchedulingWeekList(deps Deps, w http.ResponseWriter, r *http.Request) {
	year, week, err := resolveISOWeekFromQuery(r.URL.Query().Get("week_iso"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	tenantID := r.Header.Get("X-Tenant-Id")
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	items, err := deps.Scheduling.ListByTenantWeek(ctx, tenantID, year, week)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, c := range items {
		out = append(out, scheduledClassDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": out})
}

// handleCreateScheduledClass serves POST /v1/scheduling/classes. Gated to
// instructor / admin / training-admin (mirrors the survey-create RBAC). The
// domain constructor enforces the guard clauses (required ids, capacity > 0,
// ends_at after starts_at) → 400 on violation.
func handleCreateScheduledClass(deps Deps, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid // captured for future audit / event emission
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req createScheduledClassReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	starts, ends, perr := parseSchedWindow(req.StartsAt, req.EndsAt)
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.Error())
		return
	}
	c, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID:       tenantID,
		CourseID:       req.CourseID,
		InstructorGCID: req.InstructorGCID,
		RoomID:         req.RoomID,
		Room:           req.Room,
		StartsAt:       starts,
		EndsAt:         ends,
		MaxCapacity:    req.MaxCapacity,
	})
	// CHO-2299: the no-free-text-bypass rule is a DOMAIN invariant now, so the
	// adapter's only job is mapping it onto the ratified wire code. Mirrors
	// offering_schedule_handler.go.
	if errors.Is(err, campusops.ErrFreeTextRoom) {
		writeErrorCode(w, http.StatusBadRequest, "room_required",
			"a room must be booked by room_id (select one from GET /api/v1/rooms), not free text")
		return
	}

	// Cross-aggregate validation: a ScheduledClass cannot reach the Room
	// aggregate to prove it exists, so existence is resolved here through the
	// Rooms port. No auto-provision: a room_id must resolve to a REAL Room for
	// this tenant (the owner rejected minting rooms from typed names). Mirrors
	// offering_schedule_handler.go.
	if err == nil && c != nil && c.RoomID != "" {
		if deps.Rooms == nil {
			writeError(w, http.StatusServiceUnavailable, "rooms store not wired")
			return
		}
		// The tenant MUST be in the CONTEXT, not just the header: pg.RoomRepo
		// calls rls.ApplySession, which reads it from the context and fails with
		// ErrNoTenantContext before issuing any SQL (the CHO-2293 lesson).
		ctx := tracing.WithTenantID(r.Context(), tenantID)
		room, ok, lookupErr := deps.Rooms.GetForTenant(ctx, tenantID, c.RoomID)
		if lookupErr != nil {
			writeError(w, http.StatusInternalServerError, "room lookup failed: "+lookupErr.Error())
			return
		}
		if !ok || room == nil {
			writeErrorCode(w, http.StatusBadRequest, "room_not_found",
				"no such room for this tenant; create it via POST /api/v1/rooms first")
			return
		}
		if req.MaxCapacity > 0 && req.MaxCapacity > room.Capacity {
			writeErrorCode(w, http.StatusConflict, "room_over_capacity",
				"this room is too small for the class capacity")
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := deps.Scheduling.Save(tracing.WithTenantID(r.Context(), tenantID), c); err != nil {
		if errors.Is(err, scheduling.ErrRoomDoubleBooked) {
			writeErrorCode(w, http.StatusConflict, "room_double_booked",
				"that room is already booked for an overlapping time; pick another room or time")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, scheduledClassDTO(c))
}

// schedulingClassByIDHandler serves /v1/scheduling/classes/{id} sub-routes:
//
//	GET  /v1/scheduling/classes/{id}             → detail (tenant-scoped 404)
//	POST /v1/scheduling/classes/{id}/reschedule  → reschedule (instructor/admin)
//	POST /v1/scheduling/classes/{id}/cancel      → soft-delete (instructor/admin)
func schedulingClassByIDHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Scheduling == nil {
			writeError(w, http.StatusServiceUnavailable, "scheduling repo not wired")
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/v1/scheduling/classes/")
		rest = strings.TrimSuffix(rest, "/")
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		id := parts[0]
		switch len(parts) {
		case 1:
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handleGetScheduledClass(deps, id, w, r)
		case 2:
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			switch parts[1] {
			case "reschedule":
				handleRescheduleScheduledClass(deps, id, w, r)
			case "cancel":
				handleCancelScheduledClass(deps, id, w, r)
			default:
				writeError(w, http.StatusNotFound, "not found")
			}
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	}
}

// handleGetScheduledClass serves GET /v1/scheduling/classes/{id} (tenant-scoped;
// cancelled = 404).
func handleGetScheduledClass(deps Deps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Tenant-Id")
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	c, ok, err := deps.Scheduling.Get(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scheduled class lookup failed: "+err.Error())
		return
	}
	if !ok || c == nil || c.TenantID != tenantID || c.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "scheduled class not found")
		return
	}
	writeJSON(w, http.StatusOK, scheduledClassDTO(c))
}

// handleRescheduleScheduledClass serves POST /v1/scheduling/classes/{id}/reschedule.
func handleRescheduleScheduledClass(deps Deps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	var req rescheduleClassReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	starts, ends, perr := parseSchedWindow(req.StartsAt, req.EndsAt)
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.Error())
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	c, ok, err := deps.Scheduling.Get(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scheduled class lookup failed: "+err.Error())
		return
	}
	if !ok || c == nil || c.TenantID != tenantID || c.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "scheduled class not found")
		return
	}
	if err := c.Reschedule(starts, ends, req.RoomID, req.Room); err != nil {
		if errors.Is(err, campusops.ErrFreeTextRoom) {
			writeErrorCode(w, http.StatusBadRequest, "room_required",
				"a room must be booked by room_id (select one from GET /api/v1/rooms), not free text")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := deps.Scheduling.Save(ctx, c); err != nil {
		if errors.Is(err, scheduling.ErrRoomDoubleBooked) {
			writeErrorCode(w, http.StatusConflict, "room_double_booked",
				"that room is already booked for an overlapping time; pick another room or time")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, scheduledClassDTO(c))
}

// handleCancelScheduledClass serves POST /v1/scheduling/classes/{id}/cancel
// (soft-delete; idempotent at the domain level).
func handleCancelScheduledClass(deps Deps, id string, w http.ResponseWriter, r *http.Request) {
	tenantID, gcid := callerTenantGCID(w, r)
	if tenantID == "" {
		return
	}
	_ = gcid
	if !hasInstructorOrAdmin(r) {
		writeError(w, http.StatusForbidden, "caller lacks instructor/admin/training-admin role")
		return
	}
	ctx := tracing.WithTenantID(r.Context(), tenantID)
	c, ok, err := deps.Scheduling.Get(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scheduled class lookup failed: "+err.Error())
		return
	}
	if !ok || c == nil || c.TenantID != tenantID || c.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "scheduled class not found")
		return
	}
	c.SoftDelete()
	if err := deps.Scheduling.Save(ctx, c); err != nil {
		if errors.Is(err, scheduling.ErrRoomDoubleBooked) {
			writeErrorCode(w, http.StatusConflict, "room_double_booked",
				"that room is already booked for an overlapping time; pick another room or time")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, scheduledClassDTO(c))
}

// parseSchedWindow parses the RFC3339 starts_at / ends_at pair. The domain
// constructor / Reschedule re-validate ends_at > starts_at, but we surface a
// clear 400 on malformed timestamps here (before the domain sees them).
func parseSchedWindow(startsRaw, endsRaw string) (time.Time, time.Time, error) {
	starts, err := time.Parse(time.RFC3339, strings.TrimSpace(startsRaw))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid starts_at: expected RFC3339")
	}
	ends, err := time.Parse(time.RFC3339, strings.TrimSpace(endsRaw))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid ends_at: expected RFC3339")
	}
	return starts, ends, nil
}

// resolveISOWeekFromQuery turns the optional `week_iso=YYYY-MM-DD` query
// param into a (year, week) pair via the ISO-8601 week-numbering algorithm.
//
//   - An empty string defaults to the current UTC instant's ISO week so the
//     FE can hit /v1/scheduling/classes without computing the week itself.
//   - A non-empty string MUST match the strict `YYYY-MM-DD` shape; anything
//     else is a 422 (caller bug, not a not-found).
//   - The parsed date is then reduced to (ISO year, ISO week) — note that
//     these can differ from the calendar year for dates near year-ends.
func resolveISOWeekFromQuery(weekISO string) (int, int, error) {
	weekISO = strings.TrimSpace(weekISO)
	if weekISO == "" {
		y, w := time.Now().UTC().ISOWeek()
		return y, w, nil
	}
	// time.Parse with the exact `YYYY-MM-DD` layout rejects both wrong-shape
	// strings (e.g. "not-a-date") and impossible dates (e.g. "2026-13-40").
	t, err := time.Parse("2006-01-02", weekISO)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid week_iso: expected YYYY-MM-DD")
	}
	y, w := t.UTC().ISOWeek()
	return y, w, nil
}

// scheduledClassDTO renders a domain ScheduledClass as the JSON shape the
// FE consumes. Field names use the delivery DTO convention (snake_case +
// RFC3339 timestamps).
func scheduledClassDTO(c *scheduling.ScheduledClass) map[string]interface{} {
	out := map[string]interface{}{
		"id":              c.ID,
		"tenant_id":       c.TenantID,
		"course_id":       c.CourseID,
		"instructor_gcid": c.InstructorGCID,
		"room_id":         c.RoomID,
		"room":            c.Room,
		"starts_at":       c.StartsAt.Format(time.RFC3339),
		"ends_at":         c.EndsAt.Format(time.RFC3339),
		"max_capacity":    c.MaxCapacity,
		"created_at":      c.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":      c.UpdatedAt.Format(time.RFC3339Nano),
	}
	if c.DeletedAt != nil {
		out["deleted_at"] = c.DeletedAt.Format(time.RFC3339Nano)
	}
	return out
}
