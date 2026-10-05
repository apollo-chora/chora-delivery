// exam_result_publisher.go — adapter emitting the W4 Exam BC outcome event
// chora.delivery.exam_result.released.v1 (ADR-190 D1 "one event-fed seam out of
// Delivery"). Mirrors course_content_publisher.go: it wraps the delivery
// Publisher, asserts the optional PublishCustom capability, and rides the
// generic outbox lane so the finalize path enqueues a durable, idempotent
// outbox row (the TransactionalOutboxPublisher tees it; the Dispatcher drains
// it to Pub/Sub).
//
// The durable exam_results row remains the source of truth; there is NO
// consumer yet (cert-from-exam / outcome-spine W6 is unbuilt) — the topic +
// schema + subscription + DLQ are provisioned by the owner at integration.
//
// Wire format: BINARY protobuf via protomarshal.encodeExamResultReleased (the
// topic carries a Pub/Sub Schema Registry BINARY schema once provisioned).
package events

import (
	"context"
	"errors"
	"strings"

	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

// TopicExamResultReleased is the canonical topic for the finalised-exam-result
// outcome feed (ADR-190 D1).
const TopicExamResultReleased = "chora.delivery.exam_result.released.v1"

// ExamResultPublisher adapts a delivery Publisher (asserted to the optional
// PublishCustom capability) to the Exam BC's result-finalize seam.
type ExamResultPublisher struct {
	inner Publisher
}

// NewExamResultPublisher wraps the delivery Publisher. Both InMemoryPublisher
// and the TransactionalOutboxPublisher satisfy PublishCustom (the latter tees
// to the outbox so the event actually reaches Pub/Sub); the assertion happens
// at publish time so callers pass the events.Publisher interface value directly
// (mirrors NewCourseContentPublisher).
func NewExamResultPublisher(inner Publisher) *ExamResultPublisher {
	return &ExamResultPublisher{inner: inner}
}

// PublishExamResultReleased emits the finalised outcome of one ExamResult.
// actorGCID (the grader / recorder) is stamped as the envelope gcid. Refuses a
// released value missing its tenant or result id (fail-loud — never emit a
// half-formed outcome event).
func (p *ExamResultPublisher) PublishExamResultReleased(_ context.Context, actorGCID string, r exam.ExamResultReleased) error {
	if strings.TrimSpace(r.TenantID) == "" {
		return errors.New("exam_result_publisher: tenant_id required")
	}
	if strings.TrimSpace(r.ResultID) == "" {
		return errors.New("exam_result_publisher: result_id required")
	}
	payload := map[string]any{
		"result_id":     r.ResultID,
		"exam_id":       r.ExamID,
		"exam_form_id":  r.ExamFormID,
		"candidate_ref": r.CandidateRef,
		"tenant_id":     r.TenantID, // carried on the message (mirrors sibling outcome events)
		"outcome":       string(r.Outcome),
		"raw_score":     r.RawScore,
		"max_score":     r.MaxScore,
		"cut_score":     r.PassMark,
		"scored_at":     r.OccurredAt,
	}
	pc, ok := p.inner.(customPublisher)
	if !ok {
		return errors.New("exam_result_publisher: inner Publisher does not implement PublishCustom")
	}
	_, err := pc.PublishCustom(TopicExamResultReleased, r.TenantID, actorGCID, payload)
	return err
}
