// submission_released_emit_test.go: CHO-2349.
//
// The auto-issue certificate engine (CHO-2157) rides
// chora.delivery.submission.released.v1 and NOTHING else. Two producer defects
// found on a live walk (2026-07-23) meant a learner who completed a graduate
// course and passed its assessment at 100% received no certificate:
//
//  1. The grading queue's "Approve & release" button (POST .../approve-all with
//     {"release":true}) flipped the assessment AND every submission to RELEASED
//     and then emitted NOTHING. It discarded ReleaseAllSubmissions' returned ids
//     into `_`. The dedicated release route emits both events off exactly those
//     ids, so the two release paths disagreed and only one fed the engine.
//     Live evidence: at 12:20:51 only grading.assessment_approved.v1,
//     submission.graded.v1 and grading.submission_approved.v1 published, while
//     the assessment carried results_released_at from the same second.
//
//  2. emitSubmissionReleasedEvent stamped a.InstructorGCID into the
//     learner_gcid field (and into the envelope gcid). The certificate is minted
//     off the submission row's own learner, so it did not mis-issue, but every
//     released event on the wire was attributed to the wrong person.
//
// These drive the REAL routes through the REAL server and assert on the REAL
// emitted events, because that is exactly the seam that was unproven.
package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

const (
	releasedTopic           = "chora.delivery.submission.released.v1"
	assessmentReleasedTopic = "chora.delivery.assessment.released.v1"
)

// eventsOfTopic filters the publisher history to one topic.
func eventsOfTopic(pub *events.InMemoryPublisher, topic string) []events.PublishedEvent {
	out := make([]events.PublishedEvent, 0)
	for _, e := range pub.History() {
		if e.Topic == topic {
			out = append(out, e)
		}
	}
	return out
}

// postJSON drives a route as the instructor, reusing the package's canonical
// header shape (reqWithHeaders) so the auth seam matches every other test.
func postJSON(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := reqWithHeaders(http.MethodPost, path, []byte(body), instructor, "instructor")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestCHO2349_ApproveAll_WithRelease_EmitsReleaseEvents: the grading queue's
// bulk "Approve & release" must feed the certificate engine exactly as the
// dedicated release route does. Without this the credential never issues and
// nothing anywhere says why.
func TestCHO2349_ApproveAll_WithRelease_EmitsReleaseEvents(t *testing.T) {
	off, err := domain.NewOffering(domain.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{"01970000-0000-7000-a000-0000000000d1"},
		Label:        "MSc Cohort 2026",
		DeliveryType: domain.DeliveryTypeGraduate,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	srv, aRepo, sRepo, pub := newAssessmentServerWithOfferings(t, &dtOfferings{offering: off, ok: true})
	a, sub := seedOfferingScopedSubmission(t, aRepo, sRepo, off.ID)

	rec := postJSON(t, srv, "/api/v1/assessments/"+a.ID+"/approve-all", `{"release":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve-all: status %d, body %s", rec.Code, rec.Body.String())
	}

	// The submission really did flip, so the state and the events must agree.
	got, ok, err := sRepo.Get(context.Background(), tenantID, sub.ID)
	if err != nil || !ok {
		t.Fatalf("reload submission: ok=%v err=%v", ok, err)
	}
	if got.State != domain.SubmissionStateReleased {
		t.Fatalf("fixture precondition: submission state = %q, want RELEASED", got.State)
	}

	released := eventsOfTopic(pub, releasedTopic)
	if len(released) != 1 {
		t.Fatalf("approve-all with release=true must emit exactly ONE %s (one per released submission); got %d. "+
			"The certificate engine rides this topic and NOTHING else, so a missing event is a credential the "+
			"learner earned and will never receive.", releasedTopic, len(released))
	}
	p := payloadOf(t, released[0])
	if p["submission_id"] != sub.ID {
		t.Errorf("submission_id = %v; want %q", p["submission_id"], sub.ID)
	}
	if p["assessment_id"] != a.ID {
		t.Errorf("assessment_id = %v; want %q", p["assessment_id"], a.ID)
	}

	if n := len(eventsOfTopic(pub, assessmentReleasedTopic)); n != 1 {
		t.Errorf("approve-all with release=true must also emit exactly one %s (the dedicated route does); got %d",
			assessmentReleasedTopic, n)
	}
}

// TestCHO2349_ApproveAll_WithoutRelease_EmitsNoReleaseEvents: the negative
// control. "Approve all" alone must NOT announce a release, or results leak to
// the learner ahead of their instructor.
func TestCHO2349_ApproveAll_WithoutRelease_EmitsNoReleaseEvents(t *testing.T) {
	off, err := domain.NewOffering(domain.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{"01970000-0000-7000-a000-0000000000d2"},
		Label:        "MSc Cohort 2026",
		DeliveryType: domain.DeliveryTypeGraduate,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	srv, aRepo, sRepo, pub := newAssessmentServerWithOfferings(t, &dtOfferings{offering: off, ok: true})
	a, _ := seedOfferingScopedSubmission(t, aRepo, sRepo, off.ID)

	rec := postJSON(t, srv, "/api/v1/assessments/"+a.ID+"/approve-all", `{"release":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve-all: status %d, body %s", rec.Code, rec.Body.String())
	}
	if n := len(eventsOfTopic(pub, releasedTopic)); n != 0 {
		t.Errorf("approve-all with release=false emitted %d %s; want 0, an unreleased grade must not reach the learner",
			n, releasedTopic)
	}
}

// TestCHO2349_SubmissionReleased_CarriesLearnerGCID: the released event must
// name the LEARNER. It stamped the instructor, so every consumer of this topic
// (the certificate engine's envelope gcid included) saw the wrong person.
func TestCHO2349_SubmissionReleased_CarriesLearnerGCID(t *testing.T) {
	off, err := domain.NewOffering(domain.NewOfferingInput{
		TenantID:     tenantID,
		CourseIDs:    []string{"01970000-0000-7000-a000-0000000000d3"},
		Label:        "MSc Cohort 2026",
		DeliveryType: domain.DeliveryTypeGraduate,
	})
	if err != nil {
		t.Fatalf("NewOffering: %v", err)
	}
	srv, aRepo, sRepo, pub := newAssessmentServerWithOfferings(t, &dtOfferings{offering: off, ok: true})
	a, sub := seedOfferingScopedSubmission(t, aRepo, sRepo, off.ID)

	// Approve first (the HITL gate), then release via the dedicated route.
	approveSubmission(t, srv, a, sub)
	rec := postJSON(t, srv, "/api/v1/assessments/"+a.ID+"/release-results", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("release-results: status %d, body %s", rec.Code, rec.Body.String())
	}

	released := eventsOfTopic(pub, releasedTopic)
	if len(released) != 1 {
		t.Fatalf("want exactly 1 %s, got %d", releasedTopic, len(released))
	}
	p := payloadOf(t, released[0])
	if p["learner_gcid"] != learner {
		t.Errorf("learner_gcid = %v; want the LEARNER %q (it stamped the instructor %q). "+
			"Consumers key attribution off this field.", p["learner_gcid"], learner, instructor)
	}
	if p["learner_gcid"] == instructor {
		t.Errorf("learner_gcid is the INSTRUCTOR gcid, the released event names the wrong person")
	}
}
