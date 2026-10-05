// submission_graded_payload.go — single canonical builder for the
// chora.delivery.submission.graded.v1 event payload (WS1.c3 Phase 2).
//
// The event has two emit sites (the OE/batch grading-inbox subscriber and the
// per-submission HTTP grading handler). Before this builder each constructed
// the payload inline with drifting keys (total_score/max_score vs the canonical
// total_points_*), which is how the producer diverged from the schema. Routing
// both through ONE builder keyed to the proto/events SubmissionGraded schema
// keeps them aligned; protomarshal.encodeSubmissionGraded reads exactly these
// keys and emits the canonical wire layout.
package events

import (
	"strings"
	"time"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// SubmissionGradedPayload builds the canonical submission.graded.v1 payload from
// a graded Submission aggregate. Returns nil for a nil submission. grading_job_id
// is out-of-band (the orchestrator owns it) and intentionally absent; hint_count
// has no source in chora-delivery (hints are a consumption concern) and is left
// unset so the field stays absent.
//
// assessmentTitle (ADR-205 WS-6, CHO-1958) is the human-readable title of the
// parent assessment, threaded in by the emit site that holds the Assessment
// aggregate. It is the whole-submission grade's ONLY concept-bearing signal, so
// chora-consumption keys a DERIVED Growth Edge on it. A blank title omits the
// field (proto3 default omission) so the projector acks unmappable evidence.
// deliveryType is the parent Offering's mode ({graduate|short|async}), already
// resolved + validated by delivery.ResolveDeliveryType. Blank means genuinely
// unattributable (a freestanding assessment has no Offering), and the key is
// then OMITTED rather than written empty.
//
// ⚠⚠ That omission is a SAFETY property, not tidiness: chora.delivery.submission
// .graded.v1 has a Pub/Sub schema attached, so a populated field 14 is rejected
// 400 AT PUBLISH (never reaching a DLQ) until the matching schema revision is
// committed.
func SubmissionGradedPayload(sub *domain.Submission, assessmentTitle, deliveryType, traceparent string) map[string]any {
	if sub == nil {
		return nil
	}
	gradedAt := sub.UpdatedAt
	if sub.GradedAt != nil {
		gradedAt = *sub.GradedAt
	}
	payload := map[string]any{
		"submission_id":             sub.ID,
		"assessment_id":             sub.AssessmentID,
		"learner_gcid":              sub.LearnerGCID,
		"state":                     int32(4), // SUBMISSION_STATE_GRADED (the event's meaning)
		"total_points_earned":       sub.TotalScore,
		"total_points_possible":     int32(sub.MaxScore),
		"passing_threshold_percent": int32(sub.PassingPercent),
		"graded_at":                 gradedAt.Format(time.RFC3339Nano),
		"traceparent":               traceparent,
		"chora_imda_dimension":      "accountability",
	}
	if sub.Passed != nil {
		payload["passed"] = *sub.Passed
	}
	if title := strings.TrimSpace(assessmentTitle); title != "" {
		payload["assessment_title"] = title
	}
	if dt := strings.TrimSpace(deliveryType); dt != "" {
		payload["delivery_type"] = dt
	}
	return payload
}
