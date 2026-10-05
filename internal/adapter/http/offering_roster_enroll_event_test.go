// offering_roster_enroll_event_test.go — CHO-2152.
//
// The R+ admin roster-enrol (POST /api/v1/offerings/{id}/roster) wrote the
// course_enrollments row but never published chora.delivery.enrollment.created.v1.
// chora-consumption bootstraps the learner's LearningPath off that topic, so an
// admin-enrolled learner could NEVER open the course — A+ span forever on
// "Setting up your learning path…".
//
// The domain layer states the obligation the handler skipped
// (internal/domain/delivery/enrollment.go): chora-delivery is responsible for
// (1) storing the enrolment row, (2) EMITTING enrollment.created.v1,
// (3) enforcing idempotency on (course_id, gcid). The roster path did 1 and 3.
//
// The sibling handleOfferingRosterRemove in the same file already publishes
// enrollment.cancelled.v1 — so admin unenrol was observable downstream while
// admin enrol was not. These tests close that asymmetry.
package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// newOfferingEnrollEventTestServer mirrors newOfferingRemoveTestServer: the
// enrol harness deliberately wires a Publisher (the pre-existing enrol harness
// does not — which is precisely why this gap was never caught).
func newOfferingEnrollEventTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, delivery.EnrollmentPort, *events.InMemoryPublisher) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	enroll := delivery.NewInMemEnrollmentStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: enroll, Publisher: pub})
	return srv, oRepo, enroll, pub
}

func enrollmentCreatedEvents(pub *events.InMemoryPublisher) []events.PublishedEvent {
	var out []events.PublishedEvent
	for _, ev := range pub.History() {
		if ev.Topic == events.TopicEnrollmentCreated {
			out = append(out, ev)
		}
	}
	return out
}

func TestOfferingRoster_Enroll_PublishesEnrollmentCreated(t *testing.T) {
	srv, oRepo, _, pub := newOfferingEnrollEventTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)

	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}

	got := enrollmentCreatedEvents(pub)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 %s, got %d", events.TopicEnrollmentCreated, len(got))
	}
	ev := got[0]

	// The payload must carry what chora-consumption needs to bootstrap the path.
	if ev.Payload["course_id"] != enrollCourseA {
		t.Errorf("payload course_id: want %q, got %v", enrollCourseA, ev.Payload["course_id"])
	}
	if ev.Payload["learner_gcid"] != enrollLearner1 {
		t.Errorf("payload learner_gcid: want %q, got %v", enrollLearner1, ev.Payload["learner_gcid"])
	}
	if id, _ := ev.Payload["enrollment_id"].(string); id == "" {
		t.Errorf("payload enrollment_id must be set, got %v", ev.Payload["enrollment_id"])
	}

	// Envelope: tenant-scoped, and the idempotency key is deterministic on
	// (course_id, learner_gcid) so a replay dedupes at the bus.
	if ev.Envelope.TenantID != tenantID {
		t.Errorf("envelope tenant_id: want %q, got %q", tenantID, ev.Envelope.TenantID)
	}
	if want := "enrollment:" + enrollCourseA + ":" + enrollLearner1; ev.Envelope.IdempotencyKey != want {
		t.Errorf("envelope idempotency_key: want %q, got %q", want, ev.Envelope.IdempotencyKey)
	}
}

// A re-enrol of an already-enrolled learner is idempotent (the handler already
// computes `existed` for the capacity gate) — it must NOT emit a second event.
// Mirrors v1_handlers.go's self-enrol (`if !existed`) and the payments
// subscriber (`if !created … skip emit`).
func TestOfferingRoster_Enroll_ReenrolDoesNotRepublish(t *testing.T) {
	srv, oRepo, _, pub := newOfferingEnrollEventTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)

	body := map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1}
	if w := postEnroll(t, srv, enrollOfferingID, "instructor", body); w.Code != http.StatusCreated {
		t.Fatalf("first enrol: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	if w := postEnroll(t, srv, enrollOfferingID, "instructor", body); w.Code != http.StatusCreated {
		t.Fatalf("re-enrol: want 201 (idempotent), got %d body=%s", w.Code, w.Body.String())
	}

	if got := enrollmentCreatedEvents(pub); len(got) != 1 {
		t.Fatalf("re-enrol must not republish: want exactly 1 %s, got %d", events.TopicEnrollmentCreated, len(got))
	}
}

// A refused enrol (course not attached to the offering) must emit nothing —
// no event may precede or outlive a rejected write.
func TestOfferingRoster_Enroll_NoEventWhenRejected(t *testing.T) {
	srv, oRepo, _, pub := newOfferingEnrollEventTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)

	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseX, "gcid": enrollLearner1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("course not attached: want 400, got %d body=%s", w.Code, w.Body.String())
	}
	if got := enrollmentCreatedEvents(pub); len(got) != 0 {
		t.Fatalf("rejected enrol must emit nothing, got %d %s", len(got), events.TopicEnrollmentCreated)
	}
}
