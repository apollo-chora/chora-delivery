// offering_roster_remove_test.go — R+ Phase-2 WS-B: admin roster unenrol.
// POST /api/v1/offerings/{id}/roster/remove — cancel a learner's enrolment
// (by GCID) in one of the offering's attached courses, via the canonical
// EnrollmentPort.Cancel (soft-delete). Validated proxy mirroring the sibling
// enrol handler; the Course still owns the enrolment. No cross-DB, no migration.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func newOfferingRemoveTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, delivery.EnrollmentPort, *events.InMemoryPublisher) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	enroll := delivery.NewInMemEnrollmentStore()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: enroll, Publisher: pub})
	return srv, oRepo, enroll, pub
}

func postRemove(t *testing.T, srv http.Handler, offeringID, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringID+"/roster/remove", b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestOfferingRoster_Remove_204SoftDeletesAndPublishes(t *testing.T) {
	srv, oRepo, enroll, pub := newOfferingRemoveTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	// Seed a live enrolment on the attached course.
	if _, err := enroll.Register(context.Background(), tenantID, enrollCourseA, enrollLearner1); err != nil {
		t.Fatalf("seed enrol: %v", err)
	}

	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d body=%s", w.Code, w.Body.String())
	}
	// The enrolment must be gone from active reads (soft-delete persisted).
	if _, ok, _ := enroll.GetByCourseAndGCID(context.Background(), tenantID, enrollCourseA, enrollLearner1); ok {
		t.Fatalf("enrolment still active after remove")
	}
	// The cancellation event must fire exactly once.
	cancelled := 0
	for _, ev := range pub.History() {
		if ev.Topic == events.TopicEnrollmentCancelled {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("expected exactly 1 enrollment.cancelled, got %d", cancelled)
	}
}

func TestOfferingRoster_Remove_404WhenEnrolmentMissing(t *testing.T) {
	srv, oRepo, _, _ := newOfferingRemoveTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	// No enrolment seeded — the learner was never on the roster.
	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusNotFound {
		t.Fatalf("enrolment missing: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Remove_404WhenOfferingMissing(t *testing.T) {
	srv, _, _, _ := newOfferingRemoveTestServer(t)
	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusNotFound {
		t.Fatalf("offering missing: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Remove_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, _, _ := newOfferingRemoveTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseX, "gcid": enrollLearner1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("course not attached: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Remove_400WhenMissingGCID(t *testing.T) {
	srv, oRepo, _, _ := newOfferingRemoveTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postRemove(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": "  "})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing gcid: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Remove_403WithoutRole(t *testing.T) {
	srv, oRepo, _, _ := newOfferingRemoveTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postRemove(t, srv, enrollOfferingID, "", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusForbidden {
		t.Fatalf("no role: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Remove_405OnGet(t *testing.T) {
	srv, oRepo, _, _ := newOfferingRemoveTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	r := reqWithHeaders(http.MethodGet, "/api/v1/offerings/"+enrollOfferingID+"/roster/remove", nil, instructor, "instructor")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on remove: want 405, got %d body=%s", w.Code, w.Body.String())
	}
}
