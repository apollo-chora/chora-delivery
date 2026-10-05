// submission_graded_canonical_test — proves the hand-rolled encoder emits the
// canonical chora.delivery.submission.graded.v1 wire layout: the bytes decode
// cleanly into the generated deliveryv1.SubmissionGraded struct. WS1.c3 Phase 2
// closes the §3 producer drift (the encoder previously emitted state/total_score
// /max_score/graded_at at drifted field numbers 5-8) so chora-consumption can
// decode the event via the canonical gen struct instead of a hand-rolled mirror.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
)

func TestSubmissionGraded_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	gradedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"submission_id":             "sub-1",
		"assessment_id":             "ass-1",
		"learner_gcid":              "gcid-learner",
		"grading_job_id":            "job-1",
		"state":                     int32(4), // SUBMISSION_STATE_GRADED
		"total_points_earned":       float64(8.5),
		"total_points_possible":     int32(10),
		"passing_threshold_percent": int32(70),
		"passed":                    true,
		"graded_at":                 gradedAt.Format(time.RFC3339Nano),
		"assessment_title":          "Algebra Midterm",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.submission.graded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}

	var m deliveryv1.SubmissionGraded
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen SubmissionGraded: %v", err)
	}
	if m.GetSubmissionId() != "sub-1" {
		t.Errorf("submission_id (2) = %q", m.GetSubmissionId())
	}
	if m.GetAssessmentId() != "ass-1" {
		t.Errorf("assessment_id (3) = %q", m.GetAssessmentId())
	}
	if m.GetLearnerGcid() != "gcid-learner" {
		t.Errorf("learner_gcid (4) = %q", m.GetLearnerGcid())
	}
	if m.GetGradingJobId() != "job-1" {
		t.Errorf("grading_job_id (5) = %q", m.GetGradingJobId())
	}
	if m.GetState() != deliveryv1.SubmissionState_SUBMISSION_STATE_GRADED {
		t.Errorf("state (6) = %v want GRADED", m.GetState())
	}
	if got := m.GetTotalPointsEarned(); got != float32(8.5) {
		t.Errorf("total_points_earned (7, fixed32) = %v want 8.5", got)
	}
	if m.GetTotalPointsPossible() != 10 {
		t.Errorf("total_points_possible (8) = %v want 10", m.GetTotalPointsPossible())
	}
	if m.GetPassingThresholdPercent() != 70 {
		t.Errorf("passing_threshold_percent (9) = %v want 70", m.GetPassingThresholdPercent())
	}
	if !m.GetPassed() {
		t.Errorf("passed (10) = false want true")
	}
	if m.GetGradedAt() == nil || !m.GetGradedAt().AsTime().Equal(gradedAt) {
		t.Errorf("graded_at (11) = %v want %v", m.GetGradedAt(), gradedAt)
	}
	if m.GetAssessmentTitle() != "Algebra Midterm" {
		t.Errorf("assessment_title (13) = %q want %q", m.GetAssessmentTitle(), "Algebra Midterm")
	}
	// Envelope (field 1) must still decode — chora-notifications reads
	// envelope.gcid via protofield to resolve the email recipient.
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() == "" {
		t.Errorf("envelope (1) missing/empty")
	}
}
