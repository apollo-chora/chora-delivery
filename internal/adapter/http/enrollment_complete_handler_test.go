// Driver test for the enrollment-completion lifecycle: the instructor/admin
// "mark enrolment complete" command must transition the enrolment AND emit
// chora.delivery.enrollment.completed.v1 with the right per-learner payload.
//
// Mirrors emitter_wire_test.go / v1_handlers_test.go: a full wired server with
// an InMemoryPublisher so the emit assertion is non-trivial. The event is the
// per-learner course-completion fact consumed by the LearnerProfile read-model
// (ADR-200) + familiar verified-EXP (ADR-203).
package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// seedEnrolment creates a public course + enrols gcidA and returns
// (courseID, enrolmentID).
func seedEnrolment(t *testing.T, srv http.Handler) (string, string) {
	t.Helper()
	wC := reqJSON(t, srv, http.MethodPost, "/v1/courses", map[string]interface{}{
		"title":           "Completion Course",
		"price_sgd_cents": 0,
		"visibility":      "public",
		"instructor_name": "Mr. Chen",
	})
	if wC.Code != http.StatusCreated {
		t.Fatalf("seed course: %d body=%q", wC.Code, wC.Body.String())
	}
	var c map[string]interface{}
	_ = json.Unmarshal(wC.Body.Bytes(), &c)
	courseID := c["id"].(string)

	wE := reqJSON(t, srv, http.MethodPost, "/v1/courses/"+courseID+"/enrolments", map[string]interface{}{})
	if wE.Code != http.StatusCreated {
		t.Fatalf("seed enrol: %d body=%q", wE.Code, wE.Body.String())
	}
	var e map[string]interface{}
	_ = json.Unmarshal(wE.Body.Bytes(), &e)
	return courseID, e["id"].(string)
}

func TestV1Enrolment_Complete_EmitsEnrollmentCompletedEvent(t *testing.T) {
	srv, pub := newV1Server()
	courseID, enrolID := seedEnrolment(t, srv)

	w := reqJSON(t, srv, http.MethodPost,
		"/v1/courses/"+courseID+"/enrolments/"+enrolID+"/complete",
		map[string]interface{}{"passed": true})
	if w.Code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d body=%q", w.Code, w.Body.String())
	}

	// Exactly one enrollment.completed event with the right per-learner payload.
	var got int
	for _, ev := range pub.History() {
		if ev.Topic != "chora.delivery.enrollment.completed.v1" {
			continue
		}
		got++
		if ev.Payload["course_id"] != courseID {
			t.Fatalf("payload course_id: want %q, got %v", courseID, ev.Payload["course_id"])
		}
		if ev.Payload["learner_gcid"] != gcidA {
			t.Fatalf("payload learner_gcid: want %q, got %v", gcidA, ev.Payload["learner_gcid"])
		}
		if ev.Payload["passed"] != true {
			t.Fatalf("payload passed: want true, got %v", ev.Payload["passed"])
		}
		if ev.Payload["enrollment_id"] != enrolID {
			t.Fatalf("payload enrollment_id: want %q, got %v", enrolID, ev.Payload["enrollment_id"])
		}
	}
	if got != 1 {
		t.Fatalf("expected exactly 1 enrollment.completed event, got %d", got)
	}

	// Response body reflects the completed status.
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "completed" {
		t.Fatalf("response status: want completed, got %v", body["status"])
	}
}

func TestV1Enrolment_Complete_IsIdempotent_NoReEmit(t *testing.T) {
	srv, pub := newV1Server()
	courseID, enrolID := seedEnrolment(t, srv)
	path := "/v1/courses/" + courseID + "/enrolments/" + enrolID + "/complete"

	w1 := reqJSON(t, srv, http.MethodPost, path, map[string]interface{}{"passed": true})
	if w1.Code != http.StatusOK {
		t.Fatalf("complete #1: %d", w1.Code)
	}
	// Re-complete — still 200, but MUST NOT re-emit.
	w2 := reqJSON(t, srv, http.MethodPost, path, map[string]interface{}{"passed": false})
	if w2.Code != http.StatusOK {
		t.Fatalf("complete #2 (idempotent): %d", w2.Code)
	}

	var completed int
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.enrollment.completed.v1" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("idempotent re-complete must not re-emit; got %d events", completed)
	}
}

func TestV1Enrolment_Complete_RefusesCancelled(t *testing.T) {
	srv, pub := newV1Server()
	courseID, enrolID := seedEnrolment(t, srv)

	// Cancel the enrolment first.
	wD := reqDELETE(t, srv, "/v1/courses/"+courseID+"/enrolments/"+enrolID)
	if wD.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d", wD.Code)
	}

	// Completing a cancelled enrolment must be refused (4xx) + emit nothing.
	w := reqJSON(t, srv, http.MethodPost,
		"/v1/courses/"+courseID+"/enrolments/"+enrolID+"/complete",
		map[string]interface{}{"passed": true})
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("complete-cancelled: expected 4xx, got %d body=%q", w.Code, w.Body.String())
	}
	for _, ev := range pub.History() {
		if ev.Topic == "chora.delivery.enrollment.completed.v1" {
			t.Fatalf("cancelled enrolment must not emit enrollment.completed")
		}
	}
}
