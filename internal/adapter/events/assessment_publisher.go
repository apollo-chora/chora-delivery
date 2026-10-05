// assessment_publisher.go — Lane B (ADR-155) assessments + submissions +
// grading event emission via the existing InMemoryPublisher append pipeline.
//
// The publisher extension exposes a single generic PublishCustom method
// that the HTTP handler uses for the 9 new topics. This keeps the
// events.Publisher interface unchanged (Lane B doesn't need 9 typed
// PublishX wrappers for events with mostly-identical envelope shapes).
//
// The CloudPublisher + TransactionalOutboxPublisher tee through this
// helper because they embed an inner Publisher.
package events

import (
	"errors"
	"fmt"
	"strings"
	"time"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// PublishCustom emits a generic Pub/Sub event for the 9 Lane B topics.
//
// Idempotency keys are derived per-topic:
//   - assessment.*       : assessment_id:topic-suffix
//   - submission.*       : submission_id:topic-suffix
//   - grading.oe_batch.* : oe_batch_id (caller MUST supply)
//
// The handler-side helper (chora-delivery/internal/adapter/http) uses a
// publishCustomer type-assertion to find this method — keeps the events
// package agnostic to the Lane B handler's wiring.
func (p *InMemoryPublisher) PublishCustom(topic, tenantID, gcid string, payload map[string]any) (PublishedEvent, error) {
	if strings.TrimSpace(tenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	if strings.TrimSpace(topic) == "" {
		return PublishedEvent{}, errors.New("topic required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: deriveIdempotencyKey(topic, payload),
		TenantID:       tenantID,
		GCID:           gcid,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(stringFromPayload(payload, "traceparent")),
		Tracestate:     stringFromPayload(payload, "tracestate"),
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	ev := PublishedEvent{Topic: topic, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// deriveIdempotencyKey selects a stable dedup key from the topic + payload.
func deriveIdempotencyKey(topic string, payload map[string]any) string {
	suffix := ""
	switch {
	case strings.HasPrefix(topic, "chora.delivery.assessment."):
		suffix = stringFromPayload(payload, "assessment_id")
	case strings.HasPrefix(topic, "chora.delivery.submission."):
		suffix = stringFromPayload(payload, "submission_id")
	case strings.HasPrefix(topic, "chora.delivery.exam_result."):
		// W4 Exam BC outcome event — stable key from the durable result_id so a
		// redelivered finalize dedups (never a time-fallback key).
		suffix = stringFromPayload(payload, "result_id")
	case strings.HasPrefix(topic, "chora.delivery.grading."):
		// ADR-155 batch events keyed on oe_batch_id; ADR-172 per-submission +
		// HITL events (submission_requested/completed, score_overridden,
		// model_answer_amended, submission/assessment_approved) carry NO
		// oe_batch_id — derive a STABLE key from submission_id (+ per-question
		// test_set_question_id when present) so the mandatory event_envelope
		// idempotency_key is deterministic across Pub/Sub redelivery rather than
		// falling through to the time.Now() fallback below.
		suffix = stringFromPayload(payload, "oe_batch_id")
		if suffix == "" {
			suffix = stringFromPayload(payload, "submission_id")
			if suffix == "" {
				suffix = stringFromPayload(payload, "assessment_id")
			}
			if tsq := stringFromPayload(payload, "test_set_question_id"); tsq != "" {
				suffix += ":" + tsq
			}
		}
	}
	if suffix == "" {
		// Fallback: derive from topic + occurred timestamp
		suffix = fmt.Sprintf("%d", time.Now().UnixMilli())
	}
	// Topic suffix gives idempotency granularity per event type
	parts := strings.Split(topic, ".")
	if len(parts) >= 4 {
		return suffix + ":" + parts[len(parts)-2]
	}
	return suffix + ":" + topic
}

// stringFromPayload returns the string value at the key (or "").
func stringFromPayload(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}
