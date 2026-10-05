// offering_om5_schedule_attendance_test.go — statement-coverage battery
// (TestOm5*) for handleOfferingListSchedule / handleOfferingCreateSession
// (offering_schedule_handler.go) + handleOfferingListAttendance /
// handleOfferingMarkAttendance (offering_attendance_handler.go): the wiring
// 503 / repo-error 500 branches the sibling happy-path suites leave out.
//
// Reuses the shared helpers (seedScheduleOffering, postSession, getSchedule,
// postMark, getAttendance, markBody, validSessionBody) from the existing
// offering_schedule_handler_test.go / offering_attendance_handler_test.go and
// the om5 failing doubles from offering_om5_fakes_test.go — it adds NO new
// helper that collides with anything in the package.
package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
)

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/schedule — wiring + repo-error branches
// -----------------------------------------------------------------------------

func TestOm5Schedule_Get_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{OfferingSessions: &om5ErrSessionStore{}})
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+oschOfferingID+"/schedule", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Schedule_Get_500OnOfferingLookup(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:        &om5ErrOfferingRepo{failGet: true},
		OfferingSessions: &om5ErrSessionStore{},
	})
	w, _ := getSchedule(t, srv, oschOfferingID, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Schedule_Get_500OnListError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedScheduleOffering(t, oRepo, oschOfferingID)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:        oRepo,
		OfferingSessions: &om5ErrSessionStore{failList: true},
	})
	w, _ := getSchedule(t, srv, oschOfferingID, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (session list failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/schedule — wiring + save-error branches
// -----------------------------------------------------------------------------

func TestOm5Schedule_Post_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{OfferingSessions: &om5ErrSessionStore{}})
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	w := postSession(t, srv, oschOfferingID, "instructor", validSessionBody("A", start))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Schedule_Post_503WhenSessionsUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedScheduleOffering(t, oRepo, oschOfferingID)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // OfferingSessions nil
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	w := postSession(t, srv, oschOfferingID, "instructor", validSessionBody("A", start))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (sessions unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Schedule_Post_500OnSaveError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedScheduleOffering(t, oRepo, oschOfferingID)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:        oRepo,
		OfferingSessions: &om5ErrSessionStore{failSave: true},
	})
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	w := postSession(t, srv, oschOfferingID, "instructor", validSessionBody("A", start))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (session save failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /api/v1/offerings/{id}/attendance/{sid} — wiring + repo-error branches
// -----------------------------------------------------------------------------

func TestOm5Attendance_Get_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{OfferingAttendance: &om5AttendanceStore{}})
	w, _ := getAttendance(t, srv, oattOfferingID, oattSessionID, "instructor")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Attendance_Get_500OnOfferingLookup(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:          &om5ErrOfferingRepo{failGet: true},
		OfferingAttendance: &om5AttendanceStore{},
	})
	w, _ := getAttendance(t, srv, oattOfferingID, oattSessionID, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Attendance_Get_500OnListError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedScheduleOffering(t, oRepo, oattOfferingID)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:          oRepo,
		OfferingAttendance: &om5AttendanceStore{failList: true},
	})
	w, _ := getAttendance(t, srv, oattOfferingID, oattSessionID, "instructor")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (attendance list failed), got %d body=%s", w.Code, w.Body.String())
	}
}

// -----------------------------------------------------------------------------
// POST /api/v1/offerings/{id}/attendance — wiring + guard/upsert branches
// -----------------------------------------------------------------------------

func TestOm5Attendance_Mark_503WhenOfferingsUnwired(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{OfferingAttendance: &om5AttendanceStore{}})
	w := postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "manual"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (offerings unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Attendance_Mark_503WhenAttendanceUnwired(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedScheduleOffering(t, oRepo, oattOfferingID)
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo}) // OfferingAttendance nil
	w := postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "manual"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (attendance unwired), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Attendance_Mark_500OnUpsertError(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedScheduleOffering(t, oRepo, oattOfferingID)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:          oRepo,
		OfferingAttendance: &om5AttendanceStore{failUpsert: true},
	})
	w := postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "manual"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (upsert failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Attendance_Mark_400OnBlankSession(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedScheduleOffering(t, oRepo, oattOfferingID)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:          oRepo,
		OfferingAttendance: &om5AttendanceStore{},
	})
	// Blank session_id → NewRecord domain guard → 400 (never reaches Upsert).
	w := postMark(t, srv, oattOfferingID, "instructor", markBody("", oattG1, "present", "manual"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 (blank session), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Schedule_Create_500OnOfferingLookup(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:        &om5ErrOfferingRepo{failGet: true},
		OfferingSessions: &om5ErrSessionStore{},
	})
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	w := postSession(t, srv, oschOfferingID, "instructor", validSessionBody("A", start))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (POST offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Attendance_Mark_500OnOfferingLookup(t *testing.T) {
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:          &om5ErrOfferingRepo{failGet: true},
		OfferingAttendance: &om5AttendanceStore{},
	})
	w := postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "manual"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (POST offering lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Schedule_Post_404WhenOfferingMissing(t *testing.T) {
	oRepo := inmem.NewOfferingRepo() // nothing seeded
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:        oRepo,
		OfferingSessions: &om5ErrSessionStore{},
	})
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	w := postSession(t, srv, oschOfferingID, "instructor", validSessionBody("A", start))
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 (POST offering missing), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Schedule_Post_500OnRoomLookup(t *testing.T) {
	oRepo := inmem.NewOfferingRepo()
	seedScheduleOffering(t, oRepo, oschOfferingID)
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:        oRepo,
		OfferingSessions: &om5ErrSessionStore{},
		Rooms:            &om5ErrRoomStore{},
	})
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	w := postSession(t, srv, oschOfferingID, "instructor",
		sessionBodyRoomID("A", "01985e7f-6666-7abc-8def-0000000000ff", start, start.Add(2*time.Hour)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 (room lookup failed), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOm5Attendance_Mark_404WhenOfferingMissing(t *testing.T) {
	oRepo := inmem.NewOfferingRepo() // nothing seeded
	srv := httpapi.NewServer(httpapi.Deps{
		Offerings:          oRepo,
		OfferingAttendance: &om5AttendanceStore{},
	})
	w := postMark(t, srv, oattOfferingID, "instructor", markBody(oattSessionID, oattG1, "present", "manual"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 (POST offering missing), got %d body=%s", w.Code, w.Body.String())
	}
}
