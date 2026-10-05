// assessment_encoders.go — binary-protobuf encoders for the assessment +
// submission + grading event payloads per ADR-155. Mirrors the conventions
// in protomarshal.go (single top-level message; envelope + Timestamp +
// transitive deps nested inline; field numbers pinned to
// chora-contracts/proto/events-flat/delivery/{assessment,submission,grading}/*.proto).
//
// Topics emitted by chora-delivery on the assessments + grading surface:
//
//   - chora.delivery.assessment.created.v1
//   - chora.delivery.assessment.published.v1
//   - chora.delivery.assessment.opened.v1            (E2E-BE-2a)
//   - chora.delivery.assessment.closed.v1
//   - chora.delivery.assessment.grading_started.v1   (E2E-BE-2a)
//   - chora.delivery.assessment.graded.v1            (E2E-BE-2a)
//   - chora.delivery.assessment.released.v1
//   - chora.delivery.assessment.archived.v1          (E2E-BE-2a)
//   - chora.delivery.submission.started.v1
//   - chora.delivery.submission.submitted.v1
//   - chora.delivery.submission.graded.v1
//   - chora.delivery.submission.released.v1
//   - chora.delivery.grading.oe_batch_requested.v1
//   - chora.delivery.grading.failed.v1               (E2E-BE-8)
//   - chora.delivery.grading.mcq_completed.v1        (LEG4-A)
//
// The completion event (chora.delivery.grading.oe_batch_completed.v1) is
// consumed via protodecode in the inbox subscriber — not encoded here.
package protomarshal

import (
	"fmt"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// protowireNumber aliases protowire.Number for terser call sites.
type protowireNumber = protowire.Number

// MarshalAssessmentPayload extends the topic-to-encoder dispatcher with
// assessment + submission + grading topics. Callers MAY use this directly
// (the outbox dispatcher routes through MarshalPayload first; this is the
// fallback for the 9 new topics).
//
// Returns ErrUnsupportedTopic for unknown topics.
func MarshalAssessmentPayload(topic string, env Envelope, payload map[string]any) ([]byte, error) {
	switch topic {
	case "chora.delivery.assessment.created.v1":
		return encodeAssessmentCreated(env, payload)
	case "chora.delivery.assessment.published.v1":
		return encodeAssessmentPublished(env, payload)
	case "chora.delivery.assessment.opened.v1":
		return encodeAssessmentOpened(env, payload)
	case "chora.delivery.assessment.closed.v1":
		return encodeAssessmentClosed(env, payload)
	case "chora.delivery.assessment.grading_started.v1":
		return encodeAssessmentGradingStarted(env, payload)
	case "chora.delivery.assessment.graded.v1":
		return encodeAssessmentGraded(env, payload)
	case "chora.delivery.assessment.released.v1":
		return encodeAssessmentReleased(env, payload)
	case "chora.delivery.assessment.archived.v1":
		return encodeAssessmentArchived(env, payload)
	case "chora.delivery.submission.started.v1":
		return encodeSubmissionStarted(env, payload)
	case "chora.delivery.submission.submitted.v1":
		return encodeSubmissionSubmitted(env, payload)
	case "chora.delivery.submission.graded.v1":
		return encodeSubmissionGraded(env, payload)
	case "chora.delivery.submission.released.v1":
		return encodeSubmissionReleased(env, payload)
	case "chora.delivery.grading.oe_batch_requested.v1":
		return encodeGradingOEBatchRequested(env, payload)
	case "chora.delivery.grading.failed.v1":
		return encodeGradingFailed(env, payload)
	case "chora.delivery.grading.mcq_completed.v1":
		return encodeGradingMCQCompleted(env, payload)
	case "chora.delivery.grading.mcq_snapshot_missing.v1":
		return encodeGradingMcqSnapshotMissing(env, payload)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedTopic, topic)
	}
}

// -----------------------------------------------------------------------------
// AssessmentCreated — chora.delivery.assessment.created.v1
// Schema — events-flat/delivery/assessment/created.proto
// -----------------------------------------------------------------------------
//
//	1  Envelope envelope
//	2  string   assessment_id
//	3  string   test_set_id
//	4  int32    test_set_revision_snapshot
//	5  string   instructor_gcid
//	6  string   class_id
//	7  enum     state
//	8  string   title
//	9  Timestamp scheduled_open_at
//	10 Timestamp scheduled_close_at
//	11 int32    max_attempts
//	12 int32    question_count
//	13 int32    total_points
//	14 Timestamp created_at
func encodeAssessmentCreated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "assessment_id", 2)
	out = encodeStringField(out, payload, "test_set_id", 3)
	if v, ok := asInt32(payload["test_set_revision_snapshot"]); ok && v != 0 {
		out = appendVarint(out, 4, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "instructor_gcid", 5)
	out = encodeStringField(out, payload, "class_id", 6)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 7, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "title", 8)
	if err := encodeTimestampInto(&out, payload, "scheduled_open_at", 9); err != nil {
		return nil, err
	}
	if err := encodeTimestampInto(&out, payload, "scheduled_close_at", 10); err != nil {
		return nil, err
	}
	if v, ok := asInt32(payload["max_attempts"]); ok && v != 0 {
		out = appendVarint(out, 11, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["question_count"]); ok && v != 0 {
		out = appendVarint(out, 12, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["total_points"]); ok && v != 0 {
		out = appendVarint(out, 13, uint64(uint32(v)))
	}
	if err := encodeTimestampInto(&out, payload, "created_at", 14); err != nil {
		return nil, err
	}
	return out, nil
}

// AssessmentPublished — chora.delivery.assessment.published.v1.
// Schema — events-flat/delivery/assessment/published.proto.
//
//	1  Envelope envelope
//	2  string   assessment_id
//	3  string   test_set_id
//	4  string   instructor_gcid
//	5  string   class_id
//	6  enum     state            (SCHEDULED)
//	7  string   title
//	8  Timestamp scheduled_open_at
//	9  Timestamp scheduled_close_at
//	10 int32    max_attempts
//	11 Timestamp published_at
//
// LEG2B-F bug-fix: the prior encoder put `published_at` at field 5, which
// the schema reserves for `class_id` (string). Pub/Sub Schema Registry
// rejected those bytes as INVALID_BINARY_PROTO_MESSAGE. Field numbers now
// match the proto.
func encodeAssessmentPublished(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "assessment_id", 2)
	out = encodeStringField(out, payload, "test_set_id", 3)
	out = encodeStringField(out, payload, "instructor_gcid", 4)
	out = encodeStringField(out, payload, "class_id", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "title", 7)
	if err := encodeTimestampInto(&out, payload, "scheduled_open_at", 8); err != nil {
		return nil, err
	}
	if err := encodeTimestampInto(&out, payload, "scheduled_close_at", 9); err != nil {
		return nil, err
	}
	if v, ok := asInt32(payload["max_attempts"]); ok && v != 0 {
		out = appendVarint(out, 10, uint64(uint32(v)))
	}
	if err := encodeTimestampInto(&out, payload, "published_at", 11); err != nil {
		return nil, err
	}
	return out, nil
}

// AssessmentOpened — chora.delivery.assessment.opened.v1.
// Schema — events-flat/delivery/assessment/opened.proto.
//
//	1  Envelope envelope
//	2  string   assessment_id
//	3  string   test_set_id
//	4  string   instructor_gcid
//	5  string   class_id
//	6  enum     state            (OPEN)
//	7  string   open_trigger     ("scheduled_auto_open" | "instructor_force_open")
//	8  Timestamp opens_at
//	9  Timestamp closes_at
//	10 int32    total_invited
//	11 Timestamp opened_at
func encodeAssessmentOpened(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "assessment_id", 2)
	out = encodeStringField(out, payload, "test_set_id", 3)
	out = encodeStringField(out, payload, "instructor_gcid", 4)
	out = encodeStringField(out, payload, "class_id", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "open_trigger", 7)
	if err := encodeTimestampInto(&out, payload, "opens_at", 8); err != nil {
		return nil, err
	}
	if err := encodeTimestampInto(&out, payload, "closes_at", 9); err != nil {
		return nil, err
	}
	if v, ok := asInt32(payload["total_invited"]); ok && v != 0 {
		out = appendVarint(out, 10, uint64(uint32(v)))
	}
	if err := encodeTimestampInto(&out, payload, "opened_at", 11); err != nil {
		return nil, err
	}
	return out, nil
}

// AssessmentClosed — chora.delivery.assessment.closed.v1.
// Schema — events-flat/delivery/assessment/closed.proto.
//
//	1  Envelope envelope
//	2  string   assessment_id
//	3  string   test_set_id
//	4  string   instructor_gcid
//	5  string   class_id
//	6  enum     state            (CLOSED)
//	7  string   close_reason
//	8  int32    total_invited
//	9  int32    total_started
//	10 int32    total_submitted
//	11 Timestamp closed_at
//
// LEG2B-F bug-fix companion: the prior encoder put `close_reason` at field 3
// (schema: test_set_id) + `closed_at` at field 4 (schema: instructor_gcid).
// Field numbers now match the proto.
func encodeAssessmentClosed(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "assessment_id", 2)
	out = encodeStringField(out, payload, "test_set_id", 3)
	out = encodeStringField(out, payload, "instructor_gcid", 4)
	out = encodeStringField(out, payload, "class_id", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "close_reason", 7)
	if v, ok := asInt32(payload["total_invited"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["total_started"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["total_submitted"]); ok && v != 0 {
		out = appendVarint(out, 10, uint64(uint32(v)))
	}
	if err := encodeTimestampInto(&out, payload, "closed_at", 11); err != nil {
		return nil, err
	}
	return out, nil
}

// AssessmentGradingStarted — chora.delivery.assessment.grading_started.v1.
// Schema — events-flat/delivery/assessment/grading_started.proto.
//
//	1 Envelope envelope
//	2 string   assessment_id
//	3 string   test_set_id
//	4 string   instructor_gcid
//	5 string   class_id
//	6 enum     state                          (GRADING)
//	7 int32    submissions_dispatched
//	8 int32    oe_question_submission_count
//	9 Timestamp grading_started_at
func encodeAssessmentGradingStarted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "assessment_id", 2)
	out = encodeStringField(out, payload, "test_set_id", 3)
	out = encodeStringField(out, payload, "instructor_gcid", 4)
	out = encodeStringField(out, payload, "class_id", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["submissions_dispatched"]); ok && v != 0 {
		out = appendVarint(out, 7, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["oe_question_submission_count"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(uint32(v)))
	}
	if err := encodeTimestampInto(&out, payload, "grading_started_at", 9); err != nil {
		return nil, err
	}
	return out, nil
}

// AssessmentGraded — chora.delivery.assessment.graded.v1.
// Schema — events-flat/delivery/assessment/graded.proto.
//
//	1  Envelope envelope
//	2  string   assessment_id
//	3  string   test_set_id
//	4  string   instructor_gcid
//	5  string   class_id
//	6  enum     state            (GRADED)
//	7  int32    total_invited
//	8  int32    total_submitted
//	9  int32    total_graded
//	10 float    average_score_percent
//	11 float    median_score_percent
//	12 int32    passed_count
//	13 int32    failed_count
//	14 int64    total_grading_latency_ms
//	15 Timestamp graded_at
func encodeAssessmentGraded(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "assessment_id", 2)
	out = encodeStringField(out, payload, "test_set_id", 3)
	out = encodeStringField(out, payload, "instructor_gcid", 4)
	out = encodeStringField(out, payload, "class_id", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["total_invited"]); ok && v != 0 {
		out = appendVarint(out, 7, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["total_submitted"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["total_graded"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	// average_score_percent (10) + median_score_percent (11) are proto3
	// `float` (fixed32 LE). The shared protomarshal package has no helper
	// today; emit as plain int32 percent ×100 when the encoder grows a
	// fixed32 helper. Subscribers that need the precise float currently
	// derive from per-submission graded.v1 aggregates instead.
	if v, ok := asInt32(payload["passed_count"]); ok && v != 0 {
		out = appendVarint(out, 12, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["failed_count"]); ok && v != 0 {
		out = appendVarint(out, 13, uint64(uint32(v)))
	}
	if v, ok := payload["total_grading_latency_ms"].(int64); ok && v != 0 {
		out = appendVarint(out, 14, uint64(v))
	}
	if err := encodeTimestampInto(&out, payload, "graded_at", 15); err != nil {
		return nil, err
	}
	return out, nil
}

// AssessmentReleased — chora.delivery.assessment.released.v1.
// Schema — events-flat/delivery/assessment/released.proto.
func encodeAssessmentReleased(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "assessment_id", 2)
	out = encodeStringField(out, payload, "test_set_id", 3)
	out = encodeStringField(out, payload, "instructor_gcid", 4)
	out = encodeStringField(out, payload, "class_id", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "release_method", 7)
	if v, ok := asInt32(payload["total_invited"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["total_submitted"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["total_graded"]); ok && v != 0 {
		out = appendVarint(out, 10, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "release_announcement", 15)
	if err := encodeTimestampInto(&out, payload, "released_at", 16); err != nil {
		return nil, err
	}
	return out, nil
}

// AssessmentArchived — chora.delivery.assessment.archived.v1.
// Schema — events-flat/delivery/assessment/archived.proto.
//
//	1 Envelope envelope
//	2 string   assessment_id
//	3 string   test_set_id
//	4 string   instructor_gcid
//	5 string   class_id
//	6 enum     state            (ARCHIVED)
//	7 string   archive_trigger  ("instructor_manual" | "retention_sweep")
//	8 string   archive_note
//	9 Timestamp archived_at
func encodeAssessmentArchived(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "assessment_id", 2)
	out = encodeStringField(out, payload, "test_set_id", 3)
	out = encodeStringField(out, payload, "instructor_gcid", 4)
	out = encodeStringField(out, payload, "class_id", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "archive_trigger", 7)
	out = encodeStringField(out, payload, "archive_note", 8)
	if err := encodeTimestampInto(&out, payload, "archived_at", 9); err != nil {
		return nil, err
	}
	return out, nil
}

// SubmissionStarted — chora.delivery.submission.started.v1.
func encodeSubmissionStarted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "submission_id", 2)
	out = encodeStringField(out, payload, "assessment_id", 3)
	out = encodeStringField(out, payload, "test_set_id", 4)
	out = encodeStringField(out, payload, "learner_gcid", 5)
	out = encodeStringField(out, payload, "class_id", 6)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 7, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["attempt_number"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(uint32(v)))
	}
	if err := encodeTimestampInto(&out, payload, "opens_at", 9); err != nil {
		return nil, err
	}
	if err := encodeTimestampInto(&out, payload, "closes_at", 10); err != nil {
		return nil, err
	}
	if v, ok := asInt32(payload["time_limit_seconds"]); ok && v != 0 {
		out = appendVarint(out, 11, uint64(uint32(v)))
	}
	if err := encodeTimestampInto(&out, payload, "started_at", 12); err != nil {
		return nil, err
	}
	return out, nil
}

// SubmissionSubmitted — chora.delivery.submission.submitted.v1.
func encodeSubmissionSubmitted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "submission_id", 2)
	out = encodeStringField(out, payload, "assessment_id", 3)
	out = encodeStringField(out, payload, "test_set_id", 4)
	out = encodeStringField(out, payload, "learner_gcid", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 7, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["attempt_number"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["close_reason"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["total_questions"]); ok && v != 0 {
		out = appendVarint(out, 10, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["answered_count"]); ok && v != 0 {
		out = appendVarint(out, 11, uint64(uint32(v)))
	}
	if err := encodeTimestampInto(&out, payload, "submitted_at", 14); err != nil {
		return nil, err
	}
	return out, nil
}

// SubmissionGraded — chora.delivery.submission.graded.v1.
func encodeSubmissionGraded(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	// Canonical chora.delivery.submission.graded.v1 layout (proto/events/
	// delivery/assessment.proto SubmissionGraded). WS1.c3 Phase 2 closed the §3
	// drift — fields 5-11 were previously emitted as state/total_score*100/
	// max_score/graded_at at the WRONG numbers + wire types, so chora-consumption
	// could not decode via the generated struct. They now match the schema; and
	// chora-notifications (which reads only envelope.gcid) is unaffected.
	out = encodeStringField(out, payload, "submission_id", 2)
	out = encodeStringField(out, payload, "assessment_id", 3)
	out = encodeStringField(out, payload, "learner_gcid", 4)
	out = encodeStringField(out, payload, "grading_job_id", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	// total_points_earned (field 7) is a proto3 float → fixed32 wire type.
	if v, ok := payload["total_points_earned"].(float64); ok && v != 0 {
		out = protowire.AppendTag(out, 7, protowire.Fixed32Type)
		out = protowire.AppendFixed32(out, math.Float32bits(float32(v)))
	}
	if v, ok := asInt32(payload["total_points_possible"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["passing_threshold_percent"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	if v, ok := payload["passed"].(bool); ok && v {
		out = appendBool(out, 10, true)
	}
	if err := encodeTimestampInto(&out, payload, "graded_at", 11); err != nil {
		return nil, err
	}
	// hint_count (field 12, int32) — additive WS1.c2 field with no canonical
	// proto backing yet + no real source in chora-delivery (hints are a
	// consumption concern). Emitted only if a caller supplies it; omitted when
	// zero/absent so the gen struct (which lacks the field) ignores it cleanly.
	if v, ok := asInt32(payload["hint_count"]); ok && v != 0 {
		out = appendVarint(out, 12, uint64(uint32(v)))
	}
	// assessment_title (field 13, string) — ADR-205 WS-6 (CHO-1958). Snapshot of
	// the parent assessment's title; chora-consumption keys a derived Growth Edge
	// on it. Omitted when blank so the gen struct's default-omission holds.
	out = encodeStringField(out, payload, "assessment_title", 13)
	// delivery_type (field 14, string) — CHO-2224. Snapshot of the parent
	// Offering's delivery_type; chora-consumption's StudentTranscript keys the
	// entry's mode on it (its ONLY mode-bearing signal).
	//
	// ⚠⚠ Omitted when blank, and that is LOAD-BEARING, not tidiness: this topic
	// has a Pub/Sub schema ATTACHED, so a populated field 14 is rejected 400 AT
	// PUBLISH — never reaching a DLQ — until the matching revision is committed.
	// Blank also means genuinely unattributable (a freestanding assessment has
	// no Offering), so omission is the honest encoding either way.
	out = encodeStringField(out, payload, "delivery_type", 14)
	return out, nil
}

// SubmissionReleased — chora.delivery.submission.released.v1.
func encodeSubmissionReleased(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "submission_id", 2)
	out = encodeStringField(out, payload, "assessment_id", 3)
	out = encodeStringField(out, payload, "learner_gcid", 4)
	out = encodeStringField(out, payload, "grading_job_id", 5)
	if v, ok := asInt32(payload["state"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "release_method", 7)
	out = encodeStringField(out, payload, "release_announcement", 8)
	// Fabric-repair 2026-07-01: released_at is field 9, NOT 5. The prior
	// encoder emitted the Timestamp at field 5 (grading_job_id string) so
	// Pub/Sub Schema Registry rejected every submission.released as
	// INVALID_BINARY_PROTO_MESSAGE (Timestamp bytes fail UTF-8 string parse).
	// Guarded by fabric_canonical_test.TestSubmissionReleased_CanonicalRoundTrip.
	if err := encodeTimestampInto(&out, payload, "released_at", 9); err != nil {
		return nil, err
	}
	return out, nil
}

// GradingOEBatchRequested — chora.delivery.grading.oe_batch_requested.v1.
//
// The agentic-dispatch event — the chora-ai-kernel-orchestrator Python
// LangGraph subscribes; chora-delivery DOES NOT call the engine directly.
// See ADR-155 "Locked architectural rule".
//
// Per the flat schema, the batch_submissions repeated field is a length-
// delimited nested QuestionGradeBatchEntry; we encode each entry inline.
func encodeGradingOEBatchRequested(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 512)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, err
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "oe_batch_id", 2)
	out = encodeStringField(out, payload, "assessment_id", 3)
	out = encodeStringField(out, payload, "test_set_question_id", 4)
	out = encodeStringField(out, payload, "question_id", 5)
	out = encodeStringField(out, payload, "rubric_json", 6)
	out = encodeStringField(out, payload, "model_answer", 7)
	out = encodeStringField(out, payload, "prompt", 8)
	if v, ok := asInt32(payload["points_possible_per_submission"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	out = encodeStringField(out, payload, "model_tier", 10)
	if v, ok := payload["per_question_feedback_enabled"].(bool); ok && v {
		out = appendVarint(out, 11, 1)
	}
	// repeated batch_submissions — emit one length-delimited entry per item
	if raw, ok := payload["batch_submissions"].([]map[string]any); ok {
		for _, entry := range raw {
			entryBz := encodeOEBatchEntry(entry)
			out = appendLengthDelimited(out, 12, entryBz)
		}
	}
	if v, ok := asInt32(payload["estimated_mana_units"]); ok && v != 0 {
		out = appendVarint(out, 13, uint64(uint32(v)))
	}
	if err := encodeTimestampInto(&out, payload, "dispatched_at", 14); err != nil {
		return nil, err
	}
	return out, nil
}

// encodeOEBatchEntry emits a QuestionGradeBatchEntry nested wire bytes.
//
//	1 string submission_id
//	2 string test_set_question_id
//	3 string question_id
//	4 string learner_gcid
//	5 string response_text
//	6 string accommodations_json
func encodeOEBatchEntry(payload map[string]any) []byte {
	out := make([]byte, 0, 256)
	out = encodeStringField(out, payload, "submission_id", 1)
	out = encodeStringField(out, payload, "test_set_question_id", 2)
	out = encodeStringField(out, payload, "question_id", 3)
	out = encodeStringField(out, payload, "learner_gcid", 4)
	out = encodeStringField(out, payload, "response_text", 5)
	out = encodeStringField(out, payload, "accommodations_json", 6)
	return out
}

// GradingFailed — chora.delivery.grading.failed.v1 (E2E-BE-8).
// Schema — events-flat/delivery/grading/failed.proto.
//
//	1  Envelope envelope
//	2  string   grading_job_id
//	3  string   submission_id
//	4  string   assessment_id
//	5  string   learner_gcid
//	6  enum     failure_category (GradingFailureCategory varint)
//	7  string   failure_message
//	8  enum     status (GradingJobStatus varint — always FAILED on this topic)
//	9  int32    attempt_count
//	10 string   oe_batch_id
//	11 Timestamp failed_at
//
// Producer: services/chora-delivery/internal/adapter/subscribers/grading_inbox.go
// emits this when the OE-batch completion event arrives with batch_outcome
// != "ok". Consumed by O+ governance + R+ monitoring + Observability.
func encodeGradingFailed(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if v, err := requireString(payload, "grading_job_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "submission_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "assessment_id", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	if v, err := requireString(payload, "learner_gcid", 5); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 5, v)
	}
	// failure_category — GradingFailureCategory enum (varint). 0 = UNSPECIFIED
	// is the proto3 default; we omit it on the wire to keep the message
	// canonical. The producer always sets a non-zero category.
	if v, ok := asInt32(payload["failure_category"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	if v, err := requireString(payload, "failure_message", 7); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 7, v)
	}
	// status — GradingJobStatus enum (varint). Always FAILED on this topic;
	// included for filter-friendly subscribers.
	if v, ok := asInt32(payload["status"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["attempt_count"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	if v, err := requireString(payload, "oe_batch_id", 10); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 10, v)
	}
	if err := encodeTimestampInto(&out, payload, "failed_at", 11); err != nil {
		return nil, err
	}
	return out, nil
}

// GradingMCQCompleted — chora.delivery.grading.mcq_completed.v1 (LEG4-A).
// Schema — events-flat/delivery/grading/mcq_completed.proto.
//
//	1  Envelope envelope
//	2  string   grading_job_id
//	3  string   submission_id
//	4  string   assessment_id
//	5  string   learner_gcid
//	6  int32    mcq_correct_count
//	7  int32    mcq_incorrect_count
//	8  float    mcq_points_earned
//	9  int32    mcq_points_possible
//	10 bool     no_oe_pending
//	11 Timestamp mcq_completed_at
//
// Marker event per ADR-155 §line 70 IMDA D1 accountability — emitted by
// chora-delivery's inline deterministic MCQ scorer at /submit time AFTER
// gradeMCQAnswersInline persists per-answer points. Consumers:
//   - R+ monitoring (partial-progress UI: "MCQ scored, OE pending")
//   - Observability (submit→mcq_completed latency histogram, sub-200ms p99)
//   - chora-governance (audit trail — deterministic compare-to-key only)
func encodeGradingMCQCompleted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "grading_job_id", 2)
	out = encodeStringField(out, payload, "submission_id", 3)
	out = encodeStringField(out, payload, "assessment_id", 4)
	out = encodeStringField(out, payload, "learner_gcid", 5)
	if v, ok := asInt32(payload["mcq_correct_count"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["mcq_incorrect_count"]); ok && v != 0 {
		out = appendVarint(out, 7, uint64(uint32(v)))
	}
	// float (proto3 wire type 5 fixed32) — mcq_points_earned. We use the
	// protowire primitives to emit Tag(8, Fixed32Type) + 4 little-endian
	// bytes per the protobuf binary wire format.
	if v, ok := payload["mcq_points_earned"].(float64); ok && v != 0 {
		bits := math.Float32bits(float32(v))
		out = protowire.AppendTag(out, 8, protowire.Fixed32Type)
		out = protowire.AppendFixed32(out, bits)
	}
	if v, ok := asInt32(payload["mcq_points_possible"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	if v, ok := payload["no_oe_pending"].(bool); ok && v {
		out = appendVarint(out, 10, 1)
	}
	if err := encodeTimestampInto(&out, payload, "mcq_completed_at", 11); err != nil {
		return nil, err
	}
	return out, nil
}

// encodeGradingMcqSnapshotMissing — chora.delivery.grading.mcq_snapshot_missing.v1.
// Schema — events-flat/delivery/grading/mcq_snapshot_missing.proto.
//
//	1  Envelope  envelope
//	2  string    submission_id
//	3  string    assessment_id
//	4  string    learner_gcid
//	5  string    error_message
//	6  Timestamp detected_at
//
// Producer: services/chora-delivery/internal/adapter/http/assessment_handler.go
// emitMCQSnapshotMissingEvent. Fail-loud accountability signal (IMDA D1) when a
// submission's MCQ payload snapshot is missing at grade time. The envelope
// carries chora_imda_dimension="accountability"; traceparent is lifted into the
// envelope by PublishCustom. Contract landed by the 2026-07-01 event-fabric
// audit (Class D — the topic was previously unprovisioned).
func encodeGradingMcqSnapshotMissing(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "submission_id", 2)
	out = encodeStringField(out, payload, "assessment_id", 3)
	out = encodeStringField(out, payload, "learner_gcid", 4)
	out = encodeStringField(out, payload, "error_message", 5)
	if err := encodeTimestampInto(&out, payload, "detected_at", 6); err != nil {
		return nil, err
	}
	return out, nil
}

// encodeStringField is a tiny helper to keep the encoders flat.
func encodeStringField(out []byte, payload map[string]any, key string, fieldNum int) []byte {
	v, ok := payload[key].(string)
	if ok && v != "" {
		out = appendString(out, fieldNumWire(fieldNum), v)
	}
	return out
}

// encodeTimestampInto appends a Timestamp field into out (or no-op when absent).
func encodeTimestampInto(out *[]byte, payload map[string]any, key string, fieldNum int) error {
	tsBz, err := encodeTimestampField(payload, key, fieldNumWire(fieldNum))
	if err != nil {
		return err
	}
	if tsBz != nil {
		*out = appendLengthDelimited(*out, fieldNumWire(fieldNum), tsBz)
	}
	return nil
}

func fieldNumWire(n int) protowireNumber { return protowireNumber(n) }
