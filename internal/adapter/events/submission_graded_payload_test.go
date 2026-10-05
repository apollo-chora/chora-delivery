// submission_graded_payload_test — the canonical builder must carry the
// assessment title (ADR-205 WS-6, CHO-1958) so chora-consumption's derived
// Growth-Edge projector has a concept to key a learner_weakness on. The title
// is the submission's ONLY concept-bearing signal (a whole-submission grade has
// no per-atom topic). An empty title omits the field entirely — proto3 default
// omission — so the downstream projector acks unmappable evidence cleanly.
package events_test

import (
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func gradedSubmissionFixture() *domain.Submission {
	graded := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	passed := true
	return &domain.Submission{
		ID:             "sub-1",
		AssessmentID:   "ass-1",
		TenantID:       "tenant-1",
		LearnerGCID:    "gcid-learner",
		TotalScore:     4,
		MaxScore:       10,
		PassingPercent: 70,
		Passed:         &passed,
		GradedAt:       &graded,
		UpdatedAt:      graded,
	}
}

func TestSubmissionGradedPayload_IncludesAssessmentTitle(t *testing.T) {
	payload := events.SubmissionGradedPayload(gradedSubmissionFixture(), "Algebra Midterm", "graduate", "tp-1")
	if payload == nil {
		t.Fatal("payload nil")
	}
	if got := payload["assessment_title"]; got != "Algebra Midterm" {
		t.Errorf("assessment_title = %v want %q", got, "Algebra Midterm")
	}
}

func TestSubmissionGradedPayload_OmitsEmptyAssessmentTitle(t *testing.T) {
	payload := events.SubmissionGradedPayload(gradedSubmissionFixture(), "   ", "graduate", "tp-1")
	if _, present := payload["assessment_title"]; present {
		t.Errorf("assessment_title must be absent for a blank title, got %v", payload["assessment_title"])
	}
}

// -----------------------------------------------------------------------------
// delivery_type (field 14) — CHO-2224, §10.6 capstone criterion 1.
// -----------------------------------------------------------------------------

func TestSubmissionGradedPayload_IncludesDeliveryType(t *testing.T) {
	payload := events.SubmissionGradedPayload(gradedSubmissionFixture(), "Algebra Midterm", "graduate", "tp-1")
	if got := payload["delivery_type"]; got != "graduate" {
		t.Errorf("delivery_type = %v want %q", got, "graduate")
	}
}

// The other half of the ">=2 modes" proof: a builder that pinned one value would
// pass the test above and still leave the capstone unprovable.
func TestSubmissionGradedPayload_IncludesShortDeliveryType(t *testing.T) {
	payload := events.SubmissionGradedPayload(gradedSubmissionFixture(), "Intro Workshop", "short", "tp-1")
	if got := payload["delivery_type"]; got != "short" {
		t.Errorf("delivery_type = %v want %q", got, "short")
	}
}

// TestSubmissionGradedPayload_OmitsEmptyDeliveryType — a freestanding assessment
// resolves no mode. The key must be ABSENT (not ""), so the encoder leaves field
// 14 off the wire: a populated field 14 is rejected 400 AT PUBLISH until the
// Pub/Sub schema revision is committed.
func TestSubmissionGradedPayload_OmitsEmptyDeliveryType(t *testing.T) {
	payload := events.SubmissionGradedPayload(gradedSubmissionFixture(), "Algebra Midterm", "", "tp-1")
	if _, present := payload["delivery_type"]; present {
		t.Errorf("delivery_type must be absent when unresolved, got %v", payload["delivery_type"])
	}
}

func TestSubmissionGradedPayload_OmitsWhitespaceDeliveryType(t *testing.T) {
	payload := events.SubmissionGradedPayload(gradedSubmissionFixture(), "Algebra Midterm", "   ", "tp-1")
	if _, present := payload["delivery_type"]; present {
		t.Errorf("delivery_type must be absent when blank, got %v", payload["delivery_type"])
	}
}

// TestSubmissionGradedPayload_DeliveryTypeIndependentOfTitle — the two adjacent
// string params must not be conflated: a blank title must not suppress the mode,
// and vice versa.
func TestSubmissionGradedPayload_DeliveryTypeIndependentOfTitle(t *testing.T) {
	payload := events.SubmissionGradedPayload(gradedSubmissionFixture(), "", "async", "tp-1")
	if _, present := payload["assessment_title"]; present {
		t.Errorf("assessment_title must be absent, got %v", payload["assessment_title"])
	}
	if got := payload["delivery_type"]; got != "async" {
		t.Errorf("delivery_type = %v want %q (a blank title must not suppress the mode)", got, "async")
	}
}
