// Package protomarshal encodes chora-delivery outbox event payloads to
// canonical binary protobuf wire format so the Schema Registry
// validation (BINARY encoding) passes at publish time.
//
// Why hand-rolled
// ---------------
// Generated Go bindings exist in chora-contracts/gen/go/chora/delivery/v1/*.pb.go,
// but the producer publishes via a loose map[string]any payload shape that the
// generated bindings don't accept directly. Routing the map through the
// generated structs would require a per-topic adapter layer that's still
// schema-fragile (each generated struct version-bumps separately as
// chora-contracts regens).
//
// Instead — mirror the canonical fix at services/chora-consumption/internal/
// adapter/events/protomarshal: use google.golang.org/protobuf/encoding/protowire
// to emit canonical wire bytes for the exact subset of fields each Schema
// Registry schema expects, with zero dependency on the generated bindings.
//
// Field numbers + wire types are pinned to chora-contracts/proto/events-flat/
// delivery/* — those flat protos ARE the Schema Registry schemas.
//
// Schema-Registry-attached topics (16) the encoder fully implements:
//
//   - chora.delivery.course.created.v1
//   - chora.delivery.booking.confirmed.v1
//   - chora.delivery.certification.issued.v1
//   - chora.delivery.enrollment.completed.v1
//   - chora.delivery.application.submitted.v1
//   - chora.delivery.application.offer_made.v1
//   - chora.delivery.application.accepted.v1
//   - chora.delivery.application.withdrawn.v1
//   - chora.delivery.application.rejected.v1
//   - chora.delivery.application.paid.v1
//   - chora.delivery.application.enrolled.v1
//   - chora.delivery.test_set.created.v1
//   - chora.delivery.test_set.question_added.v1
//   - chora.delivery.test_set.question_updated.v1
//   - chora.delivery.test_set.question_removed.v1
//   - chora.delivery.test_set.published.v1
//
// Topics chora-delivery currently emits but the Schema Registry does NOT have
// a binary schema for (4) — return ErrUnsupportedTopic so the caller logs
// once + falls back to JSON. When schemas land, add encoders + drop fallback:
//
//   - chora.delivery.course.updated.v1
//   - chora.delivery.enrollment.created.v1
//   - chora.delivery.enrollment.cancelled.v1
//   - chora.delivery.application.under_review.v1
//
// Invariants per the Schema Registry binary-encoded protos:
//
//   - Field 1 = envelope (length-delimited nested message)
//   - Envelope nested fields 1..15 follow chora.common.v1.EventEnvelope layout
//   - Timestamps are nested messages: int64 seconds (field 1) + int32 nanos
//     (field 2)
//   - Unknown topics fail loud (ErrUnsupportedTopic) so the dispatcher
//     dead-letters rather than retrying forever against a schema mismatch
//
// Per CLAUDE.md §6 — wire format MUST be binary protobuf for Pub/Sub-attached
// topics. JSON encoding is rejected at publish time with "Invalid binary
// proto message".
package protomarshal

import (
	"errors"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Envelope is the producer-side flat shape of chora.common.v1.EventEnvelope
// that the encoder needs. Mirrors services/chora-delivery/internal/adapter/
// events.EventEnvelope; defined locally to keep this package
// import-cycle-free (both events.CloudPublisher and outbox.Publisher depend
// on protomarshal).
type Envelope struct {
	EventID        string
	IdempotencyKey string
	TenantID       string
	GCID           string
	OccurredAt     time.Time
	PublishedAt    time.Time
	Traceparent    string
	Tracestate     string
	SourceProject  string
	SourceService  string
	SchemaVersion  int32
	CorrelationID  string
	CausationID    string
}

// ErrUnsupportedTopic is returned by MarshalPayload when the topic has no
// registered binary encoder. The dispatcher should fall back to JSON for
// these (the topics they reference have no Schema Registry schema attached).
var ErrUnsupportedTopic = errors.New("protomarshal: topic has no binary encoder; falling back to JSON")

// IsUnsupportedTopic reports whether err is (or wraps) ErrUnsupportedTopic.
func IsUnsupportedTopic(err error) bool { return errors.Is(err, ErrUnsupportedTopic) }

// MarshalPayload converts a topic + envelope + loose payload map into the
// canonical binary protobuf wire bytes for that topic's Schema Registry
// schema. Returns ErrUnsupportedTopic if no encoder is registered for the
// supplied topic.
func MarshalPayload(topic string, env Envelope, payload map[string]any) ([]byte, error) {
	switch topic {
	case "chora.delivery.course.created.v1":
		return encodeCourseCreated(env, payload)
	case "chora.delivery.course.released.v1":
		// E2E-BE-CJ2 — Customer Journey #2 course release event.
		return encodeCourseReleased(env, payload)
	case "chora.delivery.course.published.v1":
		// Fabric-repair 2026-07-01 (Class D) — course visibility → public.
		return encodeCoursePublished(env, payload)
	case "chora.delivery.booking.confirmed.v1":
		return encodeBookingConfirmed(env, payload)
	case "chora.delivery.certification.issued.v1":
		return encodeCertIssued(env, payload)
	case "chora.delivery.exam_result.released.v1":
		// W4 Exam BC (ADR-190 D1) — outcome-spine seam. Producer: the
		// ExamResultPublisher; no consumer yet.
		return encodeExamResultReleased(env, payload)
	case "chora.delivery.enrollment.completed.v1":
		return encodeEnrollmentCompleted(env, payload)
	case "chora.delivery.application.submitted.v1":
		return encodeApplicationSubmitted(env, payload)
	case "chora.delivery.application.offer_made.v1":
		return encodeApplicationOfferMade(env, payload)
	case "chora.delivery.application.accepted.v1":
		return encodeApplicationAccepted(env, payload)
	case "chora.delivery.application.withdrawn.v1":
		return encodeApplicationWithdrawn(env, payload)
	case "chora.delivery.application.rejected.v1":
		return encodeApplicationRejected(env, payload)
	case "chora.delivery.application.paid.v1":
		return encodeApplicationPaid(env, payload)
	case "chora.delivery.application.enrolled.v1":
		return encodeApplicationEnrolled(env, payload)
	case "chora.delivery.test_set.created.v1":
		return encodeTestSetCreated(env, payload)
	case "chora.delivery.test_set.question_added.v1":
		return encodeTestSetQuestionAdded(env, payload)
	case "chora.delivery.test_set.question_updated.v1":
		return encodeTestSetQuestionUpdated(env, payload)
	case "chora.delivery.test_set.question_removed.v1":
		return encodeTestSetQuestionRemoved(env, payload)
	case "chora.delivery.test_set.published.v1":
		return encodeTestSetPublished(env, payload)
	case "chora.delivery.live_quiz_session.started.v1":
		return encodeLiveQuizSessionStarted(env, payload)
	case "chora.delivery.live_quiz_session.score_awarded.v1":
		return encodeLiveQuizScoreAwarded(env, payload)
	case "chora.delivery.live_quiz_session.ended.v1":
		return encodeLiveQuizSessionEnded(env, payload)
	case "chora.delivery.assessment.created.v1",
		"chora.delivery.assessment.published.v1",
		"chora.delivery.assessment.opened.v1",
		"chora.delivery.assessment.closed.v1",
		"chora.delivery.assessment.grading_started.v1",
		"chora.delivery.assessment.graded.v1",
		"chora.delivery.assessment.released.v1",
		"chora.delivery.assessment.archived.v1",
		"chora.delivery.submission.started.v1",
		"chora.delivery.submission.submitted.v1",
		"chora.delivery.submission.graded.v1",
		"chora.delivery.submission.released.v1",
		"chora.delivery.grading.oe_batch_requested.v1",
		"chora.delivery.grading.failed.v1",
		"chora.delivery.grading.mcq_completed.v1",
		"chora.delivery.grading.mcq_snapshot_missing.v1":
		return MarshalAssessmentPayload(topic, env, payload)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedTopic, topic)
	}
}

// -----------------------------------------------------------------------------
// CourseCreated — chora.delivery.course.created.v1
// Schema — chora-contracts/proto/events-flat/delivery/course/created.proto
// -----------------------------------------------------------------------------
//
//	1  Envelope envelope
//	2  string   course_id
//	3  string   title
//	4  string   instructor_gcid
//	5  string   atom_path_id
//	6  Modality modality (enum varint)
//	7  Timestamp starts_at
//	8  Timestamp ends_at
//	9  int32    capacity
//	10 Timestamp created_at
func encodeCourseCreated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	if v, err := requireString(payload, "course_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "title", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "instructor_gcid", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	if v, err := requireString(payload, "atom_path_id", 5); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 5, v)
	}
	// modality is an int enum; producer payload often omits.
	if v, ok := asInt32(payload["modality"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	// capacity int32
	if v, ok := asInt32(payload["capacity"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	// created_at — accepts time.Time, *time.Time, or RFC3339Nano string.
	tsBz, err := encodeTimestampField(payload, "created_at", 10)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 10, tsBz)
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// BookingConfirmed — chora.delivery.booking.confirmed.v1
// Schema — events-flat/delivery/booking/confirmed.proto
// -----------------------------------------------------------------------------
//
//	1  Envelope envelope
//	2  string   booking_id
//	3  string   course_id
//	4  string   learner_gcid
//	5  BookingStatus status (enum varint)
//	6  int32    seat_number
//	7  Timestamp confirmed_at
func encodeBookingConfirmed(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	if v, err := requireString(payload, "booking_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "course_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "learner_gcid", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	// seat_number int32 (field 6) — REQUIRED type-check
	if raw, present := payload["seat_number"]; present {
		v, ok := asInt32(raw)
		if !ok {
			return nil, fmt.Errorf("field seat_number: expected int32-convertible, got %T", raw)
		}
		if v != 0 {
			out = appendVarint(out, 6, uint64(uint32(v)))
		}
	}
	tsBz, err := encodeTimestampField(payload, "confirmed_at", 7)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 7, tsBz)
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// CertIssued — chora.delivery.certification.issued.v1
// Schema — events-flat/delivery/certification/issued.proto
// -----------------------------------------------------------------------------
//
//	1  Envelope envelope
//	2  string   cert_id              (publisher key: certification_id)
//	3  string   learner_gcid
//	4  string   course_id
//	5  CertType cert_type (enum varint)
//	6  Timestamp issued_at
//	7  Timestamp expires_at
func encodeCertIssued(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	// Producer side uses key "certification_id"; schema field is cert_id.
	if v, err := readString(payload, "certification_id"); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	} else if v2, err2 := readString(payload, "cert_id"); err2 != nil {
		return nil, err2
	} else if v2 != "" {
		out = appendString(out, 2, v2)
	}
	if v, err := requireString(payload, "learner_gcid", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "course_id", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	if v, ok := asInt32(payload["cert_type"]); ok && v != 0 {
		out = appendVarint(out, 5, uint64(uint32(v)))
	}
	tsBz, err := encodeTimestampField(payload, "issued_at", 6)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 6, tsBz)
	}
	tsBz2, err := encodeTimestampField(payload, "expires_at", 7)
	if err != nil {
		return nil, err
	}
	if tsBz2 != nil {
		out = appendLengthDelimited(out, 7, tsBz2)
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// EnrollmentCompleted — chora.delivery.enrollment.completed.v1 (WS1.c2, ADR-200)
// Schema — events-flat/delivery/enrollment/completed.proto
// -----------------------------------------------------------------------------
//
//	1 Envelope  envelope
//	2 string    enrollment_id
//	3 string    learner_gcid
//	4 string    course_id
//	5 bool      passed
//	6 Timestamp completed_at
//
// The per-learner course-completion fact consumed by the Familiar Global
// LearnerProfile read-model (ADR-200) + familiar verified-EXP (ADR-203, tier S).
// Unlike score_awarded, learner_gcid is a top-level payload field here (field 3)
// per the contract — it is NOT folded into the envelope gcid on the wire.
func encodeEnrollmentCompleted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	if v, err := requireString(payload, "enrollment_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "learner_gcid", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "course_id", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	// passed (field 5, bool) — proto3 omits false on the wire (a completion
	// without passing decodes back to the default false).
	if v, ok := payload["passed"].(bool); ok && v {
		out = appendBool(out, 5, true)
	}
	tsBz, err := encodeTimestampField(payload, "completed_at", 6)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 6, tsBz)
	}

	return out, nil
}

// -----------------------------------------------------------------------------
// Application{Submitted,OfferMade,Accepted,Withdrawn,Rejected,Paid,Enrolled}
// — chora.delivery.application.*.v1
//
// Producer payload (per application.CanonicalEventPayload):
//
//	application_id        (string) → schema field 2
//	tenant_id             (string) → envelope field 3 (NOT a top-level payload slot)
//	course_id             (string) → schema field 4
//	learner_gcid          (string) → schema field 3 (renamed applicant_gcid)
//	status                (string) → NOT in schema (status is implied by topic)
//	created_at            (string) → NOT in any schema as a top-level slot
//	updated_at            (string) → mapped to per-state terminal timestamp
//	class_id              (string,opt) → NOT in schema; ignored
//	offer_expires_at      (string,opt) → schema field 6 (offer_made only)
//	stripe_payment_intent_id (string,opt) → schema field 5 (paid only)
//	invoice_id            (string,opt) → ignored (not in event-flat schema)
//	singpass_session_id   (string,opt) → ignored
//	rejected_reason       (string,opt) → schema field 6 (rejected only)
//	withdrawn_reason      (string,opt) → schema field 6 (withdrawn only)
// -----------------------------------------------------------------------------

// encodeApplicationCore handles fields 1..4 (envelope, application_id,
// applicant_gcid, course_id) shared across the 7 ApplicationXxx schemas.
func encodeApplicationCore(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)

	if payload == nil {
		return out, nil
	}

	if v, err := requireString(payload, "application_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	// learner_gcid → applicant_gcid (field 3)
	if v, err := requireString(payload, "learner_gcid", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "course_id", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}

	return out, nil
}

// encodeApplicationSubmitted — schema fields 5 myinfo_retrieved_at, 6
// supporting_docs_uri, 7 submitted_at. Producer payload doesn't carry the
// first two yet; we encode `updated_at` as submitted_at.
func encodeApplicationSubmitted(env Envelope, payload map[string]any) ([]byte, error) {
	out, err := encodeApplicationCore(env, payload)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return out, nil
	}
	tsBz, err := encodeTimestampField(payload, "updated_at", 7)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 7, tsBz)
	}
	return out, nil
}

// encodeApplicationOfferMade — schema fields 5 reviewer_gcid, 6
// offer_expires_at, 7 price_micros, 8 currency, 9 notes, 10 offered_at.
func encodeApplicationOfferMade(env Envelope, payload map[string]any) ([]byte, error) {
	out, err := encodeApplicationCore(env, payload)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return out, nil
	}
	// offer_expires_at
	tsBz, err := encodeTimestampField(payload, "offer_expires_at", 6)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 6, tsBz)
	}
	// offered_at — mapped from `updated_at`
	tsBz2, err := encodeTimestampField(payload, "updated_at", 10)
	if err != nil {
		return nil, err
	}
	if tsBz2 != nil {
		out = appendLengthDelimited(out, 10, tsBz2)
	}
	return out, nil
}

// encodeApplicationAccepted — schema field 5 accepted_at.
func encodeApplicationAccepted(env Envelope, payload map[string]any) ([]byte, error) {
	out, err := encodeApplicationCore(env, payload)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return out, nil
	}
	// accepted_at — mapped from `updated_at`.
	tsBz, err := encodeTimestampField(payload, "updated_at", 5)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 5, tsBz)
	}
	return out, nil
}

// encodeApplicationWithdrawn — schema fields 5 prior_status (enum), 6 reason,
// 7 withdrawn_at.
func encodeApplicationWithdrawn(env Envelope, payload map[string]any) ([]byte, error) {
	out, err := encodeApplicationCore(env, payload)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return out, nil
	}
	// prior_status — int32 enum if present
	if v, ok := asInt32(payload["prior_status"]); ok && v != 0 {
		out = appendVarint(out, 5, uint64(uint32(v)))
	}
	// reason — sourced from `withdrawn_reason` in producer payload.
	if v, err := readString(payload, "withdrawn_reason"); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 6, v)
	} else if v2, err2 := readString(payload, "reason"); err2 != nil {
		return nil, err2
	} else if v2 != "" {
		out = appendString(out, 6, v2)
	}
	// withdrawn_at — mapped from `updated_at`.
	tsBz, err := encodeTimestampField(payload, "updated_at", 7)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 7, tsBz)
	}
	return out, nil
}

// encodeApplicationRejected — schema fields 5 reviewer_gcid, 6 reason, 7 rejected_at.
func encodeApplicationRejected(env Envelope, payload map[string]any) ([]byte, error) {
	out, err := encodeApplicationCore(env, payload)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return out, nil
	}
	// reason — sourced from `rejected_reason` in producer payload.
	if v, err := readString(payload, "rejected_reason"); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 6, v)
	} else if v2, err2 := readString(payload, "reason"); err2 != nil {
		return nil, err2
	} else if v2 != "" {
		out = appendString(out, 6, v2)
	}
	// rejected_at — mapped from `updated_at`.
	tsBz, err := encodeTimestampField(payload, "updated_at", 7)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 7, tsBz)
	}
	return out, nil
}

// encodeApplicationPaid — schema fields 5 stripe_payment_intent_id,
// 6 total_micros (int64), 7 currency, 8 funding_lines (repeated), 9 paid_at.
func encodeApplicationPaid(env Envelope, payload map[string]any) ([]byte, error) {
	out, err := encodeApplicationCore(env, payload)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return out, nil
	}
	if v, err := readString(payload, "stripe_payment_intent_id"); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 5, v)
	}
	if v, ok := asInt64(payload["total_micros"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(v))
	}
	if v, err := readString(payload, "currency"); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 7, v)
	}
	// paid_at — mapped from `updated_at`.
	tsBz, err := encodeTimestampField(payload, "updated_at", 9)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 9, tsBz)
	}
	return out, nil
}

// encodeApplicationEnrolled — schema fields 5 enrollment_id, 6 enrolled_at.
func encodeApplicationEnrolled(env Envelope, payload map[string]any) ([]byte, error) {
	out, err := encodeApplicationCore(env, payload)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return out, nil
	}
	if v, err := readString(payload, "enrollment_id"); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 5, v)
	}
	// enrolled_at — mapped from `updated_at`.
	tsBz, err := encodeTimestampField(payload, "updated_at", 6)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 6, tsBz)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// TestSetCreated — chora.delivery.test_set.created.v1
// Schema — events-flat/delivery/test_set/created.proto
// -----------------------------------------------------------------------------
//
//	1  Envelope envelope
//	2  string   test_set_id
//	3  string   tenant_id
//	4  string   author_gcid
//	5  string   title
//	6  Timestamp created_at
func encodeTestSetCreated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if v, err := requireString(payload, "test_set_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "tenant_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "author_gcid", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	if v, err := requireString(payload, "title", 5); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 5, v)
	}
	tsBz, err := encodeTimestampField(payload, "created_at", 6)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 6, tsBz)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// TestSetQuestionAdded — chora.delivery.test_set.question_added.v1
// Schema — events-flat/delivery/test_set/question_added.proto
// -----------------------------------------------------------------------------
//
//	1  Envelope envelope
//	2  string   test_set_id
//	3  string   test_set_question_id
//	4  string   question_atom_id
//	5  string   question_type
//	6  int32    display_order
//	7  double   points
//	8  Timestamp added_at
func encodeTestSetQuestionAdded(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if v, err := requireString(payload, "test_set_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "test_set_question_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "question_atom_id", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	if v, err := requireString(payload, "question_type", 5); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 5, v)
	}
	if v, ok := asInt32(payload["display_order"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	if v, ok := asFloat64(payload["points"]); ok && v != 0 {
		out = appendDouble(out, 7, v)
	}
	tsBz, err := encodeTimestampField(payload, "added_at", 8)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 8, tsBz)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// TestSetQuestionUpdated — chora.delivery.test_set.question_updated.v1
// Schema — events-flat/delivery/test_set/question_updated.proto
// -----------------------------------------------------------------------------
//
//	1  Envelope envelope
//	2  string   test_set_id
//	3  string   test_set_question_id
//	4  int32    display_order
//	5  double   points
//	6  Timestamp updated_at
func encodeTestSetQuestionUpdated(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if v, err := requireString(payload, "test_set_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "test_set_question_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, ok := asInt32(payload["display_order"]); ok && v != 0 {
		out = appendVarint(out, 4, uint64(uint32(v)))
	}
	if v, ok := asFloat64(payload["points"]); ok && v != 0 {
		out = appendDouble(out, 5, v)
	}
	tsBz, err := encodeTimestampField(payload, "updated_at", 6)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 6, tsBz)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// TestSetQuestionRemoved — chora.delivery.test_set.question_removed.v1
// Schema — events-flat/delivery/test_set/question_removed.proto
// -----------------------------------------------------------------------------
//
//	1  Envelope envelope
//	2  string   test_set_id
//	3  string   test_set_question_id
//	4  Timestamp removed_at
func encodeTestSetQuestionRemoved(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if v, err := requireString(payload, "test_set_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "test_set_question_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	tsBz, err := encodeTimestampField(payload, "removed_at", 4)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 4, tsBz)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// TestSetPublished — chora.delivery.test_set.published.v1
// Schema — events-flat/delivery/test_set/published.proto
// -----------------------------------------------------------------------------
//
//	1  Envelope envelope
//	2  string   test_set_id
//	3  string   tenant_id
//	4  string   author_gcid
//	5  int32    question_count
//	6  double   total_points
//	7  Timestamp published_at
func encodeTestSetPublished(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if v, err := requireString(payload, "test_set_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "tenant_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "author_gcid", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	if v, ok := asInt32(payload["question_count"]); ok && v != 0 {
		out = appendVarint(out, 5, uint64(uint32(v)))
	}
	if v, ok := asFloat64(payload["total_points"]); ok && v != 0 {
		out = appendDouble(out, 6, v)
	}
	tsBz, err := encodeTimestampField(payload, "published_at", 7)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 7, tsBz)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// LiveQuizSessionStarted — chora.delivery.live_quiz_session.started.v1
// Schema — events-flat/delivery/live_quiz_session/started.proto
// -----------------------------------------------------------------------------
//
//	1 Envelope envelope
//	2 string   session_id
//	3 string   live_quiz_id
//	4 string   instructor_gcid
//	5 Timestamp started_at
func encodeLiveQuizSessionStarted(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if v, err := requireString(payload, "session_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "live_quiz_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "instructor_gcid", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	tsBz, err := encodeTimestampField(payload, "started_at", 5)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 5, tsBz)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// LiveQuizScoreAwarded — chora.delivery.live_quiz_session.score_awarded.v1
// Schema — events-flat/delivery/live_quiz_session/score_awarded.proto
// -----------------------------------------------------------------------------
//
//	1 Envelope envelope
//	2 string   session_id
//	3 string   live_quiz_id
//	4 string   question_id
//	5 int32    awarded_points
//	6 int32    cumulative_score
//	7 bool     correct
//	8 int64    answer_millis
//	9 string   atom_id            (CR2-C3; empty for ad-hoc questions)
//	10 repeated string topic_tags (CR2-C3; denormalised from the linked atom)
//
// learner_gcid is carried redundantly in the producer payload but is the
// envelope.gcid on the wire — the encoder does not emit a separate field.
func encodeLiveQuizScoreAwarded(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if v, err := requireString(payload, "session_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "live_quiz_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, err := requireString(payload, "question_id", 4); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 4, v)
	}
	if v, ok := asInt32(payload["awarded_points"]); ok && v != 0 {
		out = appendVarint(out, 5, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["cumulative_score"]); ok && v != 0 {
		out = appendVarint(out, 6, uint64(uint32(v)))
	}
	if v, ok := payload["correct"].(bool); ok && v {
		out = appendBool(out, 7, true)
	}
	if v, ok := asInt64(payload["answer_millis"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(v))
	}
	// CR2-C3 additions (additive within v1): atoms-as-questions → mastery seam.
	// Field 9 atom_id (the linked LearningAtom UUID; empty for ad-hoc Qs);
	// field 10 topic_tags (repeated string — one wire entry per tag) so the
	// chora-consumption derived-weakness subscriber maps (atom, correct) →
	// topic_accuracy + Growth-Edge without a cross-DB read into chora_creation.
	if v, err := requireString(payload, "atom_id", 9); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 9, v)
	}
	for _, tag := range asStringSlice(payload["topic_tags"]) {
		if tag != "" {
			out = appendString(out, 10, tag)
		}
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// LiveQuizSessionEnded — chora.delivery.live_quiz_session.ended.v1
// Schema — events-flat/delivery/live_quiz_session/ended.proto
// -----------------------------------------------------------------------------
//
//	1 Envelope envelope
//	2 string   session_id
//	3 string   live_quiz_id
//	4 int32    total_responses
//	5 Timestamp ended_at
func encodeLiveQuizSessionEnded(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	if v, err := requireString(payload, "session_id", 2); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 2, v)
	}
	if v, err := requireString(payload, "live_quiz_id", 3); err != nil {
		return nil, err
	} else if v != "" {
		out = appendString(out, 3, v)
	}
	if v, ok := asInt32(payload["total_responses"]); ok && v != 0 {
		out = appendVarint(out, 4, uint64(uint32(v)))
	}
	tsBz, err := encodeTimestampField(payload, "ended_at", 5)
	if err != nil {
		return nil, err
	}
	if tsBz != nil {
		out = appendLengthDelimited(out, 5, tsBz)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Envelope (nested in every event message; field layout matches
// chora.common.v1.EventEnvelope as flattened by chora-contracts/internal/
// protoflatten + embedded as a NESTED type in every events-flat schema).
// -----------------------------------------------------------------------------
//
//	1  string event_id
//	2  string idempotency_key
//	3  string tenant_id
//	4  string gcid
//	5  bytes  Timestamp occurred_at
//	6  bytes  Timestamp published_at
//	7  string traceparent
//	8  string tracestate
//	9  string source_project
//	10 string source_service
//	11 varint int32 schema_version
//	12 string correlation_id
//	13 string causation_id
//	14 string chora_imda_dimension
//	15 string imda_lifecycle_stage
func encodeEnvelope(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)

	if env.EventID != "" {
		out = appendString(out, 1, env.EventID)
	}
	if env.IdempotencyKey != "" {
		out = appendString(out, 2, env.IdempotencyKey)
	}
	if env.TenantID != "" {
		out = appendString(out, 3, env.TenantID)
	}
	if env.GCID != "" {
		out = appendString(out, 4, env.GCID)
	}
	if !env.OccurredAt.IsZero() {
		out = appendLengthDelimited(out, 5, encodeTimestamp(env.OccurredAt))
	}
	if !env.PublishedAt.IsZero() {
		out = appendLengthDelimited(out, 6, encodeTimestamp(env.PublishedAt))
	}
	if env.Traceparent != "" {
		out = appendString(out, 7, env.Traceparent)
	}
	if env.Tracestate != "" {
		out = appendString(out, 8, env.Tracestate)
	}
	if env.SourceProject != "" {
		out = appendString(out, 9, env.SourceProject)
	}
	if env.SourceService != "" {
		out = appendString(out, 10, env.SourceService)
	}
	if env.SchemaVersion > 0 {
		out = appendVarint(out, 11, uint64(uint32(env.SchemaVersion)))
	}
	if env.CorrelationID != "" {
		out = appendString(out, 12, env.CorrelationID)
	}
	if env.CausationID != "" {
		out = appendString(out, 13, env.CausationID)
	}

	// Optional IMDA evidence fields sourced from the payload (per ADR-141
	// canonical mapping; chora-delivery sets `chora_imda_dimension` on
	// enrollment.created + application.* payloads).
	if payload != nil {
		if v, ok := payload["chora_imda_dimension"].(string); ok && v != "" {
			out = appendString(out, 14, v)
		}
		if v, ok := payload["imda_lifecycle_stage"].(string); ok && v != "" {
			out = appendString(out, 15, v)
		}
	}

	return out, nil
}

// encodeTimestamp emits the nested google.protobuf.Timestamp wire shape:
//
//	1 varint int64  seconds
//	2 varint int32  nanos
func encodeTimestamp(t time.Time) []byte {
	out := make([]byte, 0, 16)
	t = t.UTC()
	secs := t.Unix()
	nanos := int32(t.Nanosecond())
	if secs != 0 {
		out = appendVarint(out, 1, uint64(secs))
	}
	if nanos != 0 {
		out = appendVarint(out, 2, uint64(uint32(nanos)))
	}
	return out
}

// encodeTimestampField reads a key from the payload — accepts time.Time,
// *time.Time, or RFC3339Nano string — and returns the encoded Timestamp
// nested bytes. Returns (nil, nil) when the key is absent; returns an error
// when the key exists but the type can't be coerced (fail loud).
func encodeTimestampField(payload map[string]any, key string, fieldNum protowire.Number) ([]byte, error) {
	raw, present := payload[key]
	if !present {
		return nil, nil
	}
	t, ok := asTime(raw)
	if !ok {
		return nil, fmt.Errorf("field %s: expected time.Time / *time.Time / RFC3339Nano string, got %T", key, raw)
	}
	_ = fieldNum // unused; caller appends the tag
	return encodeTimestamp(t), nil
}

// -----------------------------------------------------------------------------
// Wire-format helpers (thin protowire wrappers; reuse keeps callers tidy).
// -----------------------------------------------------------------------------

func appendString(b []byte, field protowire.Number, v string) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendString(b, v)
	return b
}

func appendVarint(b []byte, field protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, field, protowire.VarintType)
	b = protowire.AppendVarint(b, v)
	return b
}

func appendLengthDelimited(b []byte, field protowire.Number, payload []byte) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = protowire.AppendBytes(b, payload)
	return b
}

// appendBool emits a proto3 bool field (wire type varint; true → 1). Callers
// skip the call when the value is false (proto3 default, omitted on the wire).
func appendBool(b []byte, field protowire.Number, v bool) []byte {
	n := uint64(0)
	if v {
		n = 1
	}
	return appendVarint(b, field, n)
}

// appendDouble emits a double-typed field (proto3 `double` = fixed64 IEEE-754
// little-endian, wire type 1).
func appendDouble(b []byte, field protowire.Number, v float64) []byte {
	bits := math.Float64bits(v)
	b = protowire.AppendTag(b, field, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, bits)
	return b
}

// -----------------------------------------------------------------------------
// Loose-typed payload coercion (in/out: map[string]any).
// -----------------------------------------------------------------------------

// requireString returns the string value at `key` from `payload`. Returns
// ("", nil) when the key is absent; ("", error) when present with the wrong
// type. `field` is the schema field number used in error messages.
func requireString(payload map[string]any, key string, field int) (string, error) {
	raw, present := payload[key]
	if !present {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("field %s (#%d): expected string, got %T", key, field, raw)
	}
	return s, nil
}

// readString is requireString without the field-number context — used by
// helpers that don't know their own field number.
func readString(payload map[string]any, key string) (string, error) {
	raw, present := payload[key]
	if !present {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("field %s: expected string, got %T", key, raw)
	}
	return s, nil
}

// asInt32 converts the value to int32. Returns false (not 0) if v is nil OR
// is a non-numeric type — caller decides whether to fail or skip.
func asInt32(v any) (int32, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int32(n), true
	case int32:
		return n, true
	case int64:
		return int32(n), true
	case float32:
		return int32(n), true
	case float64:
		return int32(n), true
	case uint:
		return int32(n), true
	case uint32:
		return int32(n), true
	case uint64:
		return int32(n), true
	default:
		return 0, false
	}
}

// asFloat64 converts the value to float64. Returns false (not 0) if v is nil
// OR is a non-numeric type — caller decides whether to fail or skip.
func asFloat64(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// asInt64 converts the value to int64 (used for total_micros).
func asInt64(v any) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float32:
		return int64(n), true
	case float64:
		return int64(n), true
	case uint:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	default:
		return 0, false
	}
}

// asTime coerces a value to time.Time. Accepts time.Time directly, *time.Time,
// or RFC3339Nano-format strings (the producer payload commonly stores
// timestamps as ISO strings).
func asTime(v any) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	case string:
		if t == "" {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339Nano, t)
		if err == nil {
			return parsed, true
		}
		// Some producers format RFC3339 (no nanos) — try that too.
		parsed2, err2 := time.Parse(time.RFC3339, t)
		if err2 == nil {
			return parsed2, true
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}
