// exam_result_publisher_test — the W4 Exam BC outcome-event producer seam
// (ADR-190 D1). Asserts a finalised ExamResult enqueues EXACTLY ONE idempotent
// outbox row through the transactional outbox (the D6.2 Pillar-2 durable-emit
// path), carrying the mandatory event envelope + a binary-protobuf payload that
// round-trips through the registered schema shape.
package events_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/outbox"
	"github.com/apollo-chora/chora-delivery/internal/domain/exam"
)

func sampleReleased() exam.ExamResultReleased {
	return exam.ExamResultReleased{
		ResultID:     "019e2f93-d586-71b5-8c3d-e2b0d0d5f001",
		TenantID:     "tenant-1",
		ExamID:       "019e2f93-d586-71b5-8c3d-e2b0d0d5e777",
		ExamFormID:   "019e2f93-d586-71b5-8c3d-e2b0d0d5a010",
		CandidateRef: "019e2f93-d586-71b5-8c3d-e2b0d0d5c777",
		RawScore:     72,
		MaxScore:     100,
		PassMark:     60,
		Outcome:      exam.OutcomePass,
		OccurredAt:   time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC),
	}
}

// newExamResultOutboxPublisher wires the same producer stack main() does:
// InMemoryPublisher (envelope mint) → TransactionalOutboxPublisher (tee to
// outbox) → ExamResultPublisher (domain-port adapter).
func newExamResultOutboxPublisher() (*events.ExamResultPublisher, *outbox.InMemoryStore) {
	store := outbox.NewInMemoryStore()
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	txp := outbox.NewTransactionalPublisher(outbox.PublisherConfig{Inner: inner, Store: store})
	return events.NewExamResultPublisher(txp), store
}

func TestPublishExamResultReleased_EnqueuesExactlyOneOutboxRow(t *testing.T) {
	pub, store := newExamResultOutboxPublisher()
	rel := sampleReleased()

	if err := pub.PublishExamResultReleased(context.Background(), "gcid-grader", rel); err != nil {
		t.Fatalf("PublishExamResultReleased: %v", err)
	}

	rows, err := store.FetchPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("FetchPending: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 outbox row, got %d", len(rows))
	}
	row := rows[0]

	if row.Topic != "chora.delivery.exam_result.released.v1" {
		t.Errorf("topic = %q", row.Topic)
	}
	if row.AggregateType != "exam_result" {
		t.Errorf("aggregate_type = %q want exam_result", row.AggregateType)
	}
	if row.AggregateID != rel.ResultID {
		t.Errorf("aggregate_id = %q want %q", row.AggregateID, rel.ResultID)
	}
	if row.TenantID != rel.TenantID {
		t.Errorf("tenant_id = %q want %q", row.TenantID, rel.TenantID)
	}
	// Stable idempotency key derived from result_id (NOT a time fallback) — this
	// is what makes a redelivered finalize a no-op.
	if want := rel.ResultID + ":released"; row.IdempotencyKey != want {
		t.Errorf("idempotency_key = %q want %q", row.IdempotencyKey, want)
	}

	// Mandatory event-envelope fields present on the outbox row.
	for _, k := range []string{
		"event_id", "idempotency_key", "tenant_id", "occurred_at", "published_at",
		"traceparent", "source_project", "source_service", "schema_version",
	} {
		if row.Envelope[k] == "" {
			t.Errorf("envelope missing mandatory field %q", k)
		}
	}
	if row.Envelope["source_service"] != "chora-delivery" {
		t.Errorf("source_service = %q", row.Envelope["source_service"])
	}

	// Payload is BINARY protobuf (delivery.* topics decode as deliveryv1.X — see
	// reusable_delivery_topics_binary_proto_consumers_must_proto_decode).
	var m deliveryv1.ExamResultReleased
	if err := proto.Unmarshal(row.Payload, &m); err != nil {
		t.Fatalf("payload is not binary ExamResultReleased proto: %v", err)
	}
	if m.GetResultId() != rel.ResultID {
		t.Errorf("payload result_id = %q", m.GetResultId())
	}
	if m.GetOutcome() != deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_PASS {
		t.Errorf("payload outcome = %v want PASS", m.GetOutcome())
	}
	if m.GetRawScore() != 72 || m.GetMaxScore() != 100 || m.GetCutScore() != 60 {
		t.Errorf("payload scores = raw %d max %d cut %d", m.GetRawScore(), m.GetMaxScore(), m.GetCutScore())
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() != rel.TenantID {
		t.Errorf("payload envelope tenant_id mismatch")
	}
}

// TestPublishExamResultReleased_Idempotent — re-publishing the SAME finalised
// result MUST NOT enqueue a second row (the outbox dedups on the stable
// result_id-derived idempotency key).
func TestPublishExamResultReleased_Idempotent(t *testing.T) {
	pub, store := newExamResultOutboxPublisher()
	rel := sampleReleased()

	if err := pub.PublishExamResultReleased(context.Background(), "gcid-grader", rel); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	// Second publish of the identical released value — the outbox rejects the
	// duplicate idempotency key (surfaced as an error; the row count is the
	// authoritative idempotency invariant).
	if err := pub.PublishExamResultReleased(context.Background(), "gcid-grader", rel); err == nil {
		t.Errorf("second publish: want duplicate-idempotency error, got nil")
	}

	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("after re-publish want exactly 1 outbox row, got %d", len(rows))
	}
}

// TestPublishExamResultReleased_RejectsMissingRequired — fail-loud on an
// incomplete released value (no tenant / no result id).
func TestPublishExamResultReleased_RejectsMissingRequired(t *testing.T) {
	pub, store := newExamResultOutboxPublisher()

	if err := pub.PublishExamResultReleased(context.Background(), "g", exam.ExamResultReleased{ResultID: "r"}); err == nil {
		t.Errorf("missing tenant_id: want error, got nil")
	}
	if err := pub.PublishExamResultReleased(context.Background(), "g", exam.ExamResultReleased{TenantID: "t"}); err == nil {
		t.Errorf("missing result_id: want error, got nil")
	}
	if rows, _ := store.FetchPending(context.Background(), 10); len(rows) != 0 {
		t.Errorf("rejected publishes must not enqueue any row, got %d", len(rows))
	}
}
