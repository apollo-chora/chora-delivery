// offering_roster_enroll_test.go — R+ Phase-2 S3: admin roster enrol.
// POST /api/v1/offerings/{id}/roster — enrol a learner (by GCID) into one of
// the offering's attached courses, via the canonical EnrollmentPort. Capacity
// gate applied per attached course (0 = unbounded); idempotent re-enrol.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/apollo-chora/chora-delivery/internal/adapter/http"
	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	delivery "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	enrollOfferingID = "01985e7f-5555-7abc-8def-0000000000d1"
	enrollCourseA    = "01985e7f-5555-7abc-8def-0000000000e1"
	enrollCourseX    = "01985e7f-5555-7abc-8def-0000000000e9"
	enrollLearner1   = "00000000-0000-7000-8000-000000000a01"
	enrollLearner2   = "00000000-0000-7000-8000-000000000a02"
)

func newOfferingEnrollTestServer(t *testing.T) (http.Handler, *inmem.OfferingRepo, delivery.EnrollmentPort) {
	t.Helper()
	oRepo := inmem.NewOfferingRepo()
	enroll := delivery.NewInMemEnrollmentStore()
	srv := httpapi.NewServer(httpapi.Deps{Offerings: oRepo, Enrollments: enroll})
	return srv, oRepo, enroll
}

func seedEnrollOffering(t *testing.T, oRepo *inmem.OfferingRepo, id, courseID string, capacity int) {
	t.Helper()
	o, err := delivery.NewOffering(delivery.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{courseID},
		DeliveryType: delivery.DeliveryTypeGraduate,
		Label:        "S3 Enroll Run",
		Capacity:     capacity,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	o.ID = id
	if err := oRepo.Save(context.Background(), o); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func postEnroll(t *testing.T, srv http.Handler, offeringID, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := reqWithHeaders(http.MethodPost, "/api/v1/offerings/"+offeringID+"/roster", b, instructor, role)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestOfferingRoster_Enroll_201AndPersists(t *testing.T) {
	srv, oRepo, enroll := newOfferingEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)

	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["enrollment_id"] == "" || got["gcid"] != enrollLearner1 || got["status"] != "active" {
		t.Fatalf("enrol DTO: %#v", got)
	}
	if _, ok, _ := enroll.GetByCourseAndGCID(context.Background(), tenantID, enrollCourseA, enrollLearner1); !ok {
		t.Fatalf("enrolment not persisted")
	}
}

func TestOfferingRoster_Enroll_409AtCapacity(t *testing.T) {
	srv, oRepo, _ := newOfferingEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 1) // cap 1

	if w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1}); w.Code != http.StatusCreated {
		t.Fatalf("first enrol: want 201, got %d", w.Code)
	}
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner2})
	if w.Code != http.StatusConflict {
		t.Fatalf("second enrol at capacity: want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Enroll_ReenrolIdempotentEvenAtCapacity(t *testing.T) {
	srv, oRepo, _ := newOfferingEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 1)
	if w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1}); w.Code != http.StatusCreated {
		t.Fatalf("first enrol: want 201, got %d", w.Code)
	}
	// Re-enrol the SAME learner at capacity → idempotent 201, never 409.
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusCreated {
		t.Fatalf("re-enrol same learner at cap: want 201 (idempotent), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Enroll_400WhenCourseNotAttached(t *testing.T) {
	srv, oRepo, _ := newOfferingEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseX, "gcid": enrollLearner1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("course not attached: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Enroll_400WhenMissingGCID(t *testing.T) {
	srv, oRepo, _ := newOfferingEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": "  "})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing gcid: want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Enroll_403WithoutRole(t *testing.T) {
	srv, oRepo, _ := newOfferingEnrollTestServer(t)
	seedEnrollOffering(t, oRepo, enrollOfferingID, enrollCourseA, 0)
	w := postEnroll(t, srv, enrollOfferingID, "", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusForbidden {
		t.Fatalf("no role: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOfferingRoster_Enroll_404WhenOfferingMissing(t *testing.T) {
	srv, _, _ := newOfferingEnrollTestServer(t)
	w := postEnroll(t, srv, enrollOfferingID, "instructor", map[string]any{"course_id": enrollCourseA, "gcid": enrollLearner1})
	if w.Code != http.StatusNotFound {
		t.Fatalf("offering missing: want 404, got %d body=%s", w.Code, w.Body.String())
	}
}
