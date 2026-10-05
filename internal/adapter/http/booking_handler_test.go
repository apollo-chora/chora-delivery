// booking_handler_test.go — ADR-236 D2: POST /api/bookings resolves the class
// from the DURABLE ScheduledClass store (the same canonical store the R+
// calendar writes), not the ephemeral in-memory delivery.Class. Before D2 the
// Class store had no reachable populator in prod, so every booking-create 404'd;
// these specs drive the re-pointed path end-to-end (seed a ScheduledClass →
// book it → 201/404/409) plus the durable over-capacity gate.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
	"github.com/apollo-chora/chora-delivery/internal/domain/scheduling"
)

const (
	bookTenantB = "01970000-0000-7000-8000-000000000002"
	bookGCIDD   = "01970000-0000-7000-9000-000000000004"
)

// newBookingServer wires a delivery HTTP server whose class lookups resolve
// from the durable ScheduledClass store (repoinmem.SchedulingRepo). seed
// pre-loads scheduled classes into that store.
func newBookingServer(t *testing.T, seed func(*repoinmem.SchedulingRepo)) http.Handler {
	t.Helper()
	sched := repoinmem.NewSchedulingRepo()
	if seed != nil {
		seed(sched)
	}
	return httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Scheduling:     sched,
	})
}

// seedScheduledClass stores a ScheduledClass with the given tenant + capacity in
// the durable store and returns its id. Bypasses the RBAC'd create route (same
// pattern as scheduling_handler_test.go's seed).
func seedScheduledClass(t *testing.T, sched *repoinmem.SchedulingRepo, tenantID string, capacity int) string {
	t.Helper()
	starts := time.Date(2027, 1, 1, 10, 0, 0, 0, time.UTC)
	ends := starts.Add(2 * time.Hour)
	cls, err := scheduling.NewScheduledClass(scheduling.NewScheduledClassInput{
		TenantID: tenantID, CourseID: schedCourse, InstructorGCID: schedInstructor,
		RoomID: "Room-1", StartsAt: starts, EndsAt: ends, MaxCapacity: capacity,
	})
	if err != nil {
		t.Fatalf("seed NewScheduledClass: %v", err)
	}
	if err := sched.Save(context.Background(), cls); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	return cls.ID
}

func postBooking(t *testing.T, srv http.Handler, classID, learner string) *httptest.ResponseRecorder {
	t.Helper()
	return reqJSON(t, srv, http.MethodPost, "/api/bookings", map[string]interface{}{
		"class_id":     classID,
		"learner_gcid": learner,
	})
}

func TestCreateBooking_OnScheduledClass_201(t *testing.T) {
	var classID string
	srv := newBookingServer(t, func(s *repoinmem.SchedulingRepo) {
		classID = seedScheduledClass(t, s, tenantA, 2)
	})
	w := postBooking(t, srv, classID, gcidB)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%q", w.Code, w.Body.String())
	}
	var booking map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &booking)
	if booking["status"] != "pending" {
		t.Errorf("expected status=pending, got %v", booking["status"])
	}
	if booking["class_id"] != classID {
		t.Errorf("expected class_id=%s, got %v", classID, booking["class_id"])
	}
	if booking["course_id"] != schedCourse {
		t.Errorf("expected course_id=%s (from the ScheduledClass), got %v", schedCourse, booking["course_id"])
	}
}

func TestCreateBooking_UnknownClass_404(t *testing.T) {
	srv := newBookingServer(t, nil)
	w := postBooking(t, srv, "01970000-0000-7000-8000-0000000dead0", gcidB)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown class, got %d body=%q", w.Code, w.Body.String())
	}
}

// A class scheduled under tenant B must be invisible to tenant A (RLS + the
// handler's tenant check) — booking it returns 404, never a cross-tenant book.
func TestCreateBooking_WrongTenant_404(t *testing.T) {
	var classID string
	srv := newBookingServer(t, func(s *repoinmem.SchedulingRepo) {
		classID = seedScheduledClass(t, s, bookTenantB, 5)
	})
	// reqJSON sends X-Tenant-Id = tenantA.
	w := postBooking(t, srv, classID, gcidB)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 cross-tenant, got %d body=%q", w.Code, w.Body.String())
	}
}

// The durable over-capacity gate: a 3rd booking on a 2-seat class is 409.
func TestCreateBooking_AtCapacity_409(t *testing.T) {
	var classID string
	srv := newBookingServer(t, func(s *repoinmem.SchedulingRepo) {
		classID = seedScheduledClass(t, s, tenantA, 2)
	})
	for _, learner := range []string{gcidB, gcidC} {
		if w := postBooking(t, srv, classID, learner); w.Code != http.StatusCreated {
			t.Fatalf("seed booking %s: status=%d body=%q", learner, w.Code, w.Body.String())
		}
	}
	w := postBooking(t, srv, classID, bookGCIDD)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 at capacity, got %d body=%q", w.Code, w.Body.String())
	}
}

// bookOne creates one pending booking on classID and returns its id.
func bookOne(t *testing.T, srv http.Handler, classID, learner string) string {
	t.Helper()
	w := postBooking(t, srv, classID, learner)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed booking %s: status=%d body=%q", learner, w.Code, w.Body.String())
	}
	var b map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	return b["id"].(string)
}

func TestUpdateBookingStatus_200_AndStateMachine(t *testing.T) {
	var classID string
	srv := newBookingServer(t, func(s *repoinmem.SchedulingRepo) {
		classID = seedScheduledClass(t, s, tenantA, 2)
	})
	bookingID := bookOne(t, srv, classID, gcidB)

	// pending → confirmed
	confirmW := reqJSON(t, srv, http.MethodPatch, "/api/bookings/"+bookingID+"/status", map[string]interface{}{"status": "confirmed"})
	if confirmW.Code != http.StatusOK {
		t.Fatalf("pending->confirmed: status %d body=%q", confirmW.Code, confirmW.Body.String())
	}
	// confirmed → attended
	attendedW := reqJSON(t, srv, http.MethodPatch, "/api/bookings/"+bookingID+"/status", map[string]interface{}{"status": "attended"})
	if attendedW.Code != http.StatusOK {
		t.Fatalf("confirmed->attended: status %d body=%q", attendedW.Code, attendedW.Body.String())
	}
	// attended → confirmed must be rejected
	rejW := reqJSON(t, srv, http.MethodPatch, "/api/bookings/"+bookingID+"/status", map[string]interface{}{"status": "confirmed"})
	if rejW.Code != http.StatusConflict {
		t.Fatalf("attended->confirmed: expected 409, got %d", rejW.Code)
	}
}

func TestUpdateBookingStatus_404OnUnknown(t *testing.T) {
	srv := newBookingServer(t, nil)
	w := reqJSON(t, srv, http.MethodPatch, "/api/bookings/does-not-exist/status", map[string]interface{}{"status": "confirmed"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestUpdateBookingStatus_400OnUnknownStatus(t *testing.T) {
	var classID string
	srv := newBookingServer(t, func(s *repoinmem.SchedulingRepo) {
		classID = seedScheduledClass(t, s, tenantA, 1)
	})
	bookingID := bookOne(t, srv, classID, gcidB)
	w := reqJSON(t, srv, http.MethodPatch, "/api/bookings/"+bookingID+"/status", map[string]interface{}{"status": "made-up-status"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// GET /api/bookings returns the tenant's bookings as a {items,total} envelope
// (BookingList contract). Bookings are now created against the durable
// ScheduledClass store (ADR-236 D2).
func TestListBookings_200_TenantScoped(t *testing.T) {
	var classID string
	srv := newBookingServer(t, func(s *repoinmem.SchedulingRepo) {
		classID = seedScheduledClass(t, s, tenantA, 5)
	})
	for _, learner := range []string{gcidB, gcidC} {
		bookOne(t, srv, classID, learner)
	}
	w := reqGET(t, srv, "/api/bookings")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", w.Code, w.Body.String())
	}
	var list struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode BookingList: %v body=%q", err, w.Body.String())
	}
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("expected 2 bookings; got total=%d items=%d", list.Total, len(list.Items))
	}
}
