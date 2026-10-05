// assessment_publisher_test.go — coverage for the Lane B (ADR-155)
// PublishCustom emitter + its idempotency-key derivation, exercised through
// the InMemoryPublisher (the payments/course/exam tests already drive the
// CloudPublisher + audit paths).
package events_test

import (
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
)

func TestPublishCustom_DerivesIdempotencyKeyPerTopic(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")

	tt := []struct {
		name        string
		topic       string
		payload     map[string]any
		mustContain []string
	}{
		{
			name:        "assessment prefix keys on assessment_id",
			topic:       "chora.delivery.assessment.created.v1",
			payload:     map[string]any{"assessment_id": "ass-1"},
			mustContain: []string{"ass-1", "created"},
		},
		{
			name:        "submission prefix keys on submission_id",
			topic:       "chora.delivery.submission.graded.v1",
			payload:     map[string]any{"submission_id": "sub-1"},
			mustContain: []string{"sub-1", "graded"},
		},
		{
			name:        "exam_result keys on result_id",
			topic:       "chora.delivery.exam_result.released.v1",
			payload:     map[string]any{"result_id": "res-1"},
			mustContain: []string{"res-1", "released"},
		},
		{
			name:        "grading keys on oe_batch_id",
			topic:       "chora.delivery.grading.oe_batch_completed.v1",
			payload:     map[string]any{"oe_batch_id": "batch-1"},
			mustContain: []string{"batch-1", "oe_batch_completed"},
		},
		{
			name:        "grading without batch falls back to submission_id",
			topic:       "chora.delivery.grading.failed.v1",
			payload:     map[string]any{"submission_id": "sub-9"},
			mustContain: []string{"sub-9", "failed"},
		},
		{
			name:        "grading submission + per-question id composes stable key",
			topic:       "chora.delivery.grading.submission_completed.v1",
			payload:     map[string]any{"submission_id": "sub-9", "test_set_question_id": "tsq-1"},
			mustContain: []string{"sub-9:tsq-1", "submission_completed"},
		},
		{
			name:        "grading falls back to assessment_id",
			topic:       "chora.delivery.grading.score_overridden.v1",
			payload:     map[string]any{"assessment_id": "ass-7"},
			mustContain: []string{"ass-7", "score_overridden"},
		},
		{
			name:        "topic without 4 parts uses verbatim topic suffix",
			topic:       "a.b",
			payload:     map[string]any{"submission_id": "s"},
			mustContain: []string{":a.b"}, // time-fallback suffix + verbatim topic
		},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := pub.PublishCustom(tc.topic, tenantA, gcidA, tc.payload)
			if err != nil {
				t.Fatalf("PublishCustom: %v", err)
			}
			for _, want := range tc.mustContain {
				if !strings.Contains(ev.Envelope.IdempotencyKey, want) {
					t.Errorf("idempotency_key %q missing %q", ev.Envelope.IdempotencyKey, want)
				}
			}
		})
	}
}

func TestPublishCustom_TimeFallbackKeyAndNilPayload(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")

	// A grading topic with no batch/submission/assessment ids -> time fallback.
	ev, err := pub.PublishCustom("chora.delivery.grading.oe_batch_completed.v1", tenantA, gcidA, map[string]any{})
	if err != nil {
		t.Fatalf("PublishCustom: %v", err)
	}
	if ev.Envelope.IdempotencyKey == "" || ev.Envelope.IdempotencyKey == ":oe_batch_completed" {
		t.Fatalf("expected time-fallback key, got %q", ev.Envelope.IdempotencyKey)
	}

	// Nil payload exercises the stringFromPayload nil-guard for the envelope
	// traceparent/tracestate reads.
	ev2, err := pub.PublishCustom("chora.delivery.grading.mcq_completed.v1", tenantA, gcidA, nil)
	if err != nil {
		t.Fatalf("PublishCustom(nil payload): %v", err)
	}
	if ev2.Envelope.Traceparent == "" {
		t.Fatal("publisher must mint a traceparent even for a nil payload")
	}
}

func TestPublishCustom_RejectsMissingArguments(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	if _, err := pub.PublishCustom("chora.delivery.assessment.created.v1", "", gcidA, map[string]any{}); err == nil {
		t.Fatal("want error for empty tenant_id")
	}
	if _, err := pub.PublishCustom("", tenantA, gcidA, map[string]any{}); err == nil {
		t.Fatal("want error for empty topic")
	}
	if _, err := pub.PublishCustom("chora.delivery.assessment.created.v1", "", gcidA, nil); err == nil {
		t.Fatal("want error for empty tenant_id with nil payload")
	}
}
