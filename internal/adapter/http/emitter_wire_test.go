// Tests that the booking + certification HTTP handlers wire to the
// publisher emitters per S4.3 brief.
//
// AsyncAPI contracts referenced:
//   - chora-contracts/asyncapi/delivery/booking.confirmed.v1.yaml
//   - chora-contracts/asyncapi/delivery/certification.issued.v1.yaml
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	repoinmem "github.com/apollo-chora/chora-delivery/internal/adapter/repo/inmem"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// newWiredServer returns the full S4.3 server with publisher in place so
// emitter assertions are non-trivial. The durable ScheduledClass store is
// returned too, so a test can seed a bookable class (ADR-236 D2).
func newWiredServer() (http.Handler, *events.InMemoryPublisher, *repoinmem.SchedulingRepo) {
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	sched := repoinmem.NewSchedulingRepo()
	srv := httpapi.NewServer(httpapi.Deps{
		Courses:        inmem.NewCourseRepo(),
		Bookings:       inmem.NewBookingRepo(),
		Certifications: domain.NewCertificationRegistry(),
		Catalogue:      domain.NewInMemCatalogue(),
		Enrollments:    domain.NewInMemEnrollmentStore(),
		Publisher:      pub,
		CampusOps:      repoinmem.NewCampusRepo(),
		Scheduling:     sched,
	})
	return srv, pub, sched
}

func TestBookingConfirm_EmitsBookingConfirmedEvent(t *testing.T) {
	srv, pub, sched := newWiredServer()
	// Seed a durable ScheduledClass, then book it (ADR-236 D2 re-point).
	classID := seedScheduledClass(t, sched, tenantA, 10)

	wB := reqJSON(t, srv, http.MethodPost, "/api/bookings", map[string]interface{}{
		"class_id":     classID,
		"learner_gcid": gcidB,
	})
	if wB.Code != http.StatusCreated {
		t.Fatalf("booking: %d body=%q", wB.Code, wB.Body.String())
	}
	var b map[string]interface{}
	_ = json.Unmarshal(wB.Body.Bytes(), &b)
	bookingID := b["id"].(string)

	// PATCH status -> confirmed
	wU := reqJSON(t, srv, http.MethodPatch, "/api/bookings/"+bookingID+"/status", map[string]interface{}{
		"status": "confirmed",
	})
	if wU.Code != http.StatusOK {
		t.Fatalf("PATCH confirmed: %d body=%q", wU.Code, wU.Body.String())
	}

	// Assert the booking.confirmed event was published.
	confirmed := 0
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.booking.confirmed.v1" {
			confirmed++
		}
	}
	if confirmed != 1 {
		t.Fatalf("expected 1 booking.confirmed event, got %d", confirmed)
	}

	// Transitioning further should NOT re-emit booking.confirmed.
	wA := reqJSON(t, srv, http.MethodPatch, "/api/bookings/"+bookingID+"/status", map[string]interface{}{
		"status": "attended",
	})
	if wA.Code != http.StatusOK {
		t.Fatalf("PATCH attended: %d", wA.Code)
	}
	confirmed = 0
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.booking.confirmed.v1" {
			confirmed++
		}
	}
	if confirmed != 1 {
		t.Fatalf("expected NO further booking.confirmed events, total=%d", confirmed)
	}
}

func TestCertificationIssue_EmitsCertificationIssuedEvent(t *testing.T) {
	srv, pub, _ := newWiredServer()
	wC := reqJSON(t, srv, http.MethodPost, "/api/certifications", map[string]interface{}{
		"learner_gcid":    gcidA,
		"course_id":       "01970000-0000-7000-c000-000000000099",
		"accomplishments": []string{"Sprint planning", "Backlog refinement"},
	})
	if wC.Code != http.StatusCreated {
		t.Fatalf("cert issue: %d body=%q", wC.Code, wC.Body.String())
	}
	issued := 0
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.certification.issued.v1" {
			issued++
		}
	}
	if issued != 1 {
		t.Fatalf("expected 1 certification.issued event, got %d", issued)
	}
}
