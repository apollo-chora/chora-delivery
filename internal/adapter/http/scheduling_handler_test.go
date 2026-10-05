// scheduling_handler_test.go — tests for the R+ Stage 3 week-view scheduling
// list endpoint.
//
// Endpoint: GET /v1/scheduling/classes?week_iso=YYYY-MM-DD
//
// The handler returns the tenant's ScheduledClass aggregates that fall inside
// the ISO-8601 week containing the supplied `week_iso` date. The default
// (when `week_iso` is absent) is the ISO week containing time.Now() so the
// R+ /r/scheduling page renders the current week without the FE having to
// compute it.
//
// Strict TDD per `.claude/rules/development-execution.md`: tests authored
// before the handler. Coverage targets:
//
//   - 200 happy (week_iso supplied, matches Mon-of-week, week has classes)
//   - 200 happy (week_iso supplied, mid-week date, same ISO week)
//   - 200 happy (no week_iso → defaults to current ISO week)
//   - 200 empty list (week with no classes, NOT a 404 — empty week is valid)
//   - 200 tenant isolation (cross-tenant classes filtered)
//   - 200 soft-deleted classes filtered
//   - 400 missing X-Tenant-Id (tenantRequired middleware)
//   - 422 malformed week_iso (not YYYY-MM-DD shape)
//   - 422 malformed week_iso (impossible date)
//   - 405 non-GET method
//
// Per `feedback_no_stubs_real_wiring` we seed REAL ScheduledClass aggregates
// into the SchedulingRepo and assert on the real DTO shape — no faked items.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-delivery/internal/domain/campusops"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

const (
	// tenantOther is used to assert cross-tenant isolation in the week list.
	tenantOther = "01970000-0000-7000-8000-0000000000ff"
	// schedInstructor is the canonical instructor gcid for R+ scheduling seed.
	schedInstructor = "01970000-0000-7000-9000-000000000901"
	// schedCourse is the canonical course id for R+ scheduling seed.
	schedCourse = "01970000-0000-7000-7000-000000000901"
)

// newSchedulingServer wires a fresh server with a SchedulingRepo seeded by
// the supplied callback. Mirrors the newCatalogueServer pattern in
// catalogue_handlers_test.go.
const schedRoomID = "01985e7f-6666-7abc-8def-000000000401"

// newSchedulingServerWithRepo wires the same deps but injects a caller-supplied
// SchedulingStore, so a test can drive the Save error paths (CHO-2299).
func newSchedulingServerWithRepo(t *testing.T, repo scheduling.SchedulingStore) http.Handler {
	t.Helper()
	rooms := repoinmem.NewRoomRepo()
	seedRoom, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantA, Name: "Fixture Room 401", Capacity: 100,
	})
	if err != nil {
		t.Fatalf("seed room: %v", err)
	}
	seedRoom.ID = schedRoomID
	if err := rooms.Save(context.Background(), seedRoom); err != nil {
		t.Fatalf("seed room save: %v", err)
	}
	return httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      events.NewInMemoryPublisher("chora-489812", "chora-delivery"),
		CampusOps:      repoinmem.NewCampusRepo(),
		Rooms:          rooms,
		Scheduling:     repo,
	})
}

func newSchedulingServer(t *testing.T, seed func(*repoinmem.SchedulingRepo)) http.Handler {
	t.Helper()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	schedRepo := repoinmem.NewSchedulingRepo()
	if seed != nil {
		seed(schedRepo)
	}
	// CHO-2299: a room is booked by room_id, and the handler resolves it through
	// the Rooms port, so the fixture needs a REAL room to book. Seeded with the
	// id createSchedBody() books and a capacity above its max_capacity.
	rooms := repoinmem.NewRoomRepo()
	seedRoom, err := campusops.NewRoom(campusops.NewRoomInput{
		TenantID: tenantA,
		Name:     "Fixture Room 401",
		Capacity: 100,
	})
	if err != nil {
		t.Fatalf("seed room: %v", err)
	}
	seedRoom.ID = schedRoomID
	if err := rooms.Save(context.Background(), seedRoom); err != nil {
		t.Fatalf("seed room save: %v", err)
	}
	return httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      pub,
		CampusOps:      repoinmem.NewCampusRepo(),
		Rooms:          rooms,
		Scheduling:     schedRepo,
	})
}

// mustNewScheduledClass is a t.Helper-aware constructor for tests.
func mustNewScheduledClass(t *testing.T, tenantID, room string, starts, ends time.Time) *scheduling.ScheduledClass {
	t.Helper()
	// CHO-2299: `room` is the stable RoomID now. A bare display name is no
	// longer a booking, so fixtures key on the id.
	cls, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID: tenantID, CourseID: schedCourse, InstructorGCID: schedInstructor,
		RoomID: room, StartsAt: starts, EndsAt: ends, MaxCapacity: 25,
	})
	if err != nil {
		t.Fatalf("NewScheduledClass: %v", err)
	}
	return cls
}

// reqGETScheduling wraps a GET with X-Tenant-Id (gcid optional — listing is
// tenant-scoped only, no per-user filter).
func reqGETScheduling(t *testing.T, srv http.Handler, path, tenantID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if tenantID != "" {
		req.Header.Set("X-Tenant-Id", tenantID)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// -----------------------------------------------------------------------------
// 200 happy — week_iso supplied, Monday-of-week, returns the seeded class
// -----------------------------------------------------------------------------

func TestSchedulingClasses_HappyMonday_ReturnsWeekClasses(t *testing.T) {
	// 2026-05-18 is the Monday of ISO week 2026-W21. Seed one class on
	// Tuesday 2026-05-19 inside the same ISO week.
	classStarts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)
	classEnds := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	srv := newSchedulingServer(t, func(r *repoinmem.SchedulingRepo) {
		r.Save(context.Background(), mustNewScheduledClass(t, tenantA, "MTM HQ Room 401", classStarts, classEnds))
	})
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-05-18", tenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	items, ok := resp["items"].([]any)
	if !ok {
		t.Fatalf("missing items; body=%s", w.Body.String())
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1; body=%s", len(items), w.Body.String())
	}
	first, _ := items[0].(map[string]any)
	if first["tenant_id"] != tenantA {
		t.Errorf("tenant_id = %v, want %s", first["tenant_id"], tenantA)
	}
	if first["course_id"] != schedCourse {
		t.Errorf("course_id = %v, want %s", first["course_id"], schedCourse)
	}
	if first["instructor_gcid"] != schedInstructor {
		t.Errorf("instructor_gcid = %v, want %s", first["instructor_gcid"], schedInstructor)
	}
	if first["room_id"] != "MTM HQ Room 401" {
		t.Errorf("room_id = %v", first["room_id"])
	}
	if first["max_capacity"].(float64) != 25 {
		t.Errorf("max_capacity = %v, want 25", first["max_capacity"])
	}
	if first["starts_at"] == nil || first["ends_at"] == nil {
		t.Errorf("starts_at / ends_at missing; body=%s", w.Body.String())
	}
	if _, ok := first["id"].(string); !ok {
		t.Errorf("id missing or not a string")
	}
}

// -----------------------------------------------------------------------------
// 200 happy — week_iso supplied as a mid-week date, same ISO week resolves
// -----------------------------------------------------------------------------

func TestSchedulingClasses_HappyMidweek_ReturnsSameWeek(t *testing.T) {
	// 2026-05-21 is a Thursday inside ISO week 2026-W21. Seeded class on
	// Tuesday 2026-05-19 should still match.
	classStarts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)
	classEnds := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	srv := newSchedulingServer(t, func(r *repoinmem.SchedulingRepo) {
		r.Save(context.Background(), mustNewScheduledClass(t, tenantA, "MTM HQ Room 401", classStarts, classEnds))
	})
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-05-21", tenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (mid-week date should resolve same ISO week)", len(items))
	}
}

// -----------------------------------------------------------------------------
// 200 happy — no week_iso → defaults to current ISO week
// -----------------------------------------------------------------------------

func TestSchedulingClasses_NoWeekIso_DefaultsToCurrentISOWeek(t *testing.T) {
	// Seed a class for the current week's Tuesday at 09:00 UTC.
	now := time.Now().UTC()
	// Walk back to Monday of the current ISO week, then +1 day to Tuesday.
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday treated as 7 in ISO
	}
	monday := now.AddDate(0, 0, -(weekday - 1))
	tuesday := monday.AddDate(0, 0, 1)
	classStarts := time.Date(tuesday.Year(), tuesday.Month(), tuesday.Day(), 9, 0, 0, 0, time.UTC)
	classEnds := classStarts.Add(3 * time.Hour)

	srv := newSchedulingServer(t, func(r *repoinmem.SchedulingRepo) {
		r.Save(context.Background(), mustNewScheduledClass(t, tenantA, "Default-week room", classStarts, classEnds))
	})
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes", tenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (no week_iso ⇒ default to current ISO week)", len(items))
	}
}

// -----------------------------------------------------------------------------
// 200 empty — no classes in target week
// -----------------------------------------------------------------------------

func TestSchedulingClasses_EmptyWeek_Returns200WithZeroItems(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-05-18", tenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	items, ok := resp["items"].([]any)
	if !ok {
		t.Fatalf("missing items; body=%s", w.Body.String())
	}
	if len(items) != 0 {
		t.Errorf("items = %d, want 0 (empty week is 200 not 404)", len(items))
	}
}

// -----------------------------------------------------------------------------
// 200 cross-tenant isolation — classes for a different tenant must NOT leak
// -----------------------------------------------------------------------------

func TestSchedulingClasses_CrossTenantIsolation_FiltersOtherTenant(t *testing.T) {
	classStarts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)
	classEnds := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	srv := newSchedulingServer(t, func(r *repoinmem.SchedulingRepo) {
		r.Save(context.Background(), mustNewScheduledClass(t, tenantA, "tenantA room", classStarts, classEnds))
		r.Save(context.Background(), mustNewScheduledClass(t, tenantOther, "tenantOther room", classStarts, classEnds))
	})
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-05-18", tenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (cross-tenant row must be filtered); body=%s", len(items), w.Body.String())
	}
	first, _ := items[0].(map[string]any)
	if first["tenant_id"] != tenantA {
		t.Errorf("tenant_id = %v, want %s (cross-tenant leak)", first["tenant_id"], tenantA)
	}
}

// -----------------------------------------------------------------------------
// 200 soft-deleted filter — SoftDelete()'d classes must NOT surface
// -----------------------------------------------------------------------------

func TestSchedulingClasses_SoftDeletedFilter_ExcludesDeleted(t *testing.T) {
	classStarts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)
	classEnds := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	srv := newSchedulingServer(t, func(r *repoinmem.SchedulingRepo) {
		live := mustNewScheduledClass(t, tenantA, "live room", classStarts, classEnds)
		r.Save(context.Background(), live)
		dead := mustNewScheduledClass(t, tenantA, "deleted room", classStarts, classEnds)
		dead.SoftDelete()
		r.Save(context.Background(), dead)
	})
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-05-18", tenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (soft-deleted must be filtered); body=%s", len(items), w.Body.String())
	}
	first, _ := items[0].(map[string]any)
	if first["room_id"] != "live room" {
		t.Errorf("room_id = %v, want \"live room\"", first["room_id"])
	}
}

// -----------------------------------------------------------------------------
// 400 — missing X-Tenant-Id (tenantRequired middleware)
// -----------------------------------------------------------------------------

func TestSchedulingClasses_MissingTenant_Returns400(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-05-18", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (body=%s); want 400 (tenantRequired)", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 422 — malformed week_iso (wrong shape)
// -----------------------------------------------------------------------------

func TestSchedulingClasses_MalformedWeekIso_Returns422(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=not-a-date", tenantA)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (body=%s); want 422", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 422 — impossible date (out of range)
// -----------------------------------------------------------------------------

func TestSchedulingClasses_ImpossibleDate_Returns422(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-13-40", tenantA)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (body=%s); want 422", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 405 — non-GET methods rejected
// -----------------------------------------------------------------------------

func TestSchedulingClasses_PutNotAllowed_Returns405(t *testing.T) {
	// GET (week-view) + POST (create) are the only collection verbs; PUT 405s.
	srv := newSchedulingServer(t, nil)
	req := httptest.NewRequest(http.MethodPut,
		"/v1/scheduling/classes",
		strings.NewReader(`{}`))
	req.Header.Set("X-Tenant-Id", tenantA)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d (body=%s); want 405", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 200 happy — ordering is stable (sorted by ID, UUIDv7 = creation order)
// -----------------------------------------------------------------------------

func TestSchedulingClasses_OrderedByID(t *testing.T) {
	// Seed 3 classes back-to-back. UUIDv7 ⇒ lexicographic == creation order,
	// so the DTO list MUST be in that order. We assert ascending id.
	classStarts := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC)
	classEnds := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	srv := newSchedulingServer(t, func(r *repoinmem.SchedulingRepo) {
		r.Save(context.Background(), mustNewScheduledClass(t, tenantA, "rm-1", classStarts, classEnds))
		// Sleep ~1ms between to guarantee monotonic UUIDv7 prefix.
		time.Sleep(2 * time.Millisecond)
		r.Save(context.Background(), mustNewScheduledClass(t, tenantA, "rm-2", classStarts, classEnds))
		time.Sleep(2 * time.Millisecond)
		r.Save(context.Background(), mustNewScheduledClass(t, tenantA, "rm-3", classStarts, classEnds))
	})
	w := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-05-18", tenantA)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s); want 200", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3; body=%s", len(items), w.Body.String())
	}
	last := ""
	for i, raw := range items {
		row, _ := raw.(map[string]any)
		id, _ := row["id"].(string)
		if id == "" {
			t.Fatalf("items[%d].id missing; body=%s", i, w.Body.String())
		}
		if i > 0 && id < last {
			t.Errorf("ordering: items[%d].id=%s < previous=%s (UUIDv7 should be ascending)", i, id, last)
		}
		last = id
	}
}

// -----------------------------------------------------------------------------
// Wave-2 follow-up (CHO-1626) — create / reschedule / cancel write path.
// -----------------------------------------------------------------------------

// postSched issues a POST with tenant + gcid + role headers.
func postSched(t *testing.T, srv http.Handler, path, body, gcid, roles string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-Tenant-Id", tenantA)
	if gcid != "" {
		req.Header.Set("gcid", gcid)
	}
	if roles != "" {
		req.Header.Set("x-mesh-user-roles", roles)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func createSchedBody() string {
	return `{
		"course_id": "` + schedCourse + `",
		"instructor_gcid": "` + schedInstructor + `",
		"room_id": "` + schedRoomID + `",
		"starts_at": "2026-05-19T09:00:00Z",
		"ends_at": "2026-05-19T12:00:00Z",
		"max_capacity": 25
	}`
}

func TestSchedulingCreate_AsInstructor_201_ThenVisibleInWeek(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	w := postSched(t, srv, "/v1/scheduling/classes", createSchedBody(), schedInstructor, "instructor")
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d want 201 body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create response missing id: %s", w.Body.String())
	}
	wl := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-05-18", tenantA)
	var resp map[string]any
	_ = json.Unmarshal(wl.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("week-view items=%d want 1 (created class); body=%s", len(items), wl.Body.String())
	}
}

func TestSchedulingCreate_AsLearner_403(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	w := postSched(t, srv, "/v1/scheduling/classes", createSchedBody(), schedInstructor, "learner")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", w.Code, w.Body.String())
	}
}

func TestSchedulingCreate_NoGCID_401(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	w := postSched(t, srv, "/v1/scheduling/classes", createSchedBody(), "", "instructor")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401 body=%s", w.Code, w.Body.String())
	}
}

func TestSchedulingCreate_EndsBeforeStarts_400(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	bad := `{"course_id":"` + schedCourse + `","instructor_gcid":"` + schedInstructor + `","room":"R","starts_at":"2026-05-19T12:00:00Z","ends_at":"2026-05-19T09:00:00Z","max_capacity":10}`
	w := postSched(t, srv, "/v1/scheduling/classes", bad, schedInstructor, "instructor")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
	}
}

func TestSchedulingReschedule_200(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	cw := postSched(t, srv, "/v1/scheduling/classes", createSchedBody(), schedInstructor, "instructor")
	var created map[string]any
	_ = json.Unmarshal(cw.Body.Bytes(), &created)
	id, _ := created["id"].(string)

	body := `{"starts_at":"2026-05-20T14:00:00Z","ends_at":"2026-05-20T16:00:00Z","room_id":"` + schedRoomID + `"}`
	w := postSched(t, srv, "/v1/scheduling/classes/"+id+"/reschedule", body, schedInstructor, "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("reschedule status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["room_id"] != schedRoomID {
		t.Fatalf("room_id=%v want the rescheduled room key", got["room_id"])
	}
}

func TestSchedulingCancel_200_ThenDetail404(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	cw := postSched(t, srv, "/v1/scheduling/classes", createSchedBody(), schedInstructor, "instructor")
	var created map[string]any
	_ = json.Unmarshal(cw.Body.Bytes(), &created)
	id, _ := created["id"].(string)

	w := postSched(t, srv, "/v1/scheduling/classes/"+id+"/cancel", "", schedInstructor, "training-admin")
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status=%d want 200 body=%s", w.Code, w.Body.String())
	}
	d := reqGETScheduling(t, srv, "/v1/scheduling/classes/"+id, tenantA)
	if d.Code != http.StatusNotFound {
		t.Fatalf("detail-after-cancel status=%d want 404 body=%s", d.Code, d.Body.String())
	}
	wl := reqGETScheduling(t, srv, "/v1/scheduling/classes?week_iso=2026-05-18", tenantA)
	var resp map[string]any
	_ = json.Unmarshal(wl.Body.Bytes(), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("week-view after cancel items=%d want 0", len(items))
	}
}

func TestSchedulingDetail_Get_200(t *testing.T) {
	srv := newSchedulingServer(t, nil)
	cw := postSched(t, srv, "/v1/scheduling/classes", createSchedBody(), schedInstructor, "instructor")
	var created map[string]any
	_ = json.Unmarshal(cw.Body.Bytes(), &created)
	id, _ := created["id"].(string)

	d := reqGETScheduling(t, srv, "/v1/scheduling/classes/"+id, tenantA)
	if d.Code != http.StatusOK {
		t.Fatalf("detail status=%d want 200 body=%s", d.Code, d.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(d.Body.Bytes(), &got)
	if got["id"] != id {
		t.Fatalf("detail id=%v want %s", got["id"], id)
	}
}
