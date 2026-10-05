// exam_result_canonical_test — golden guardrail for the W4 Exam BC outcome
// event encoder (chora.delivery.exam_result.released.v1). Each case marshals via
// MarshalPayload and decodes the bytes into the generated canonical struct
// deliveryv1.ExamResultReleased: a field-number or wire-type drift makes
// proto.Unmarshal fail (proto3 UTF-8 validation) or lands a value in the wrong
// field. This is the durable regression gate — every encoded event MUST
// round-trip through its registered schema shape (mirrors fabric_canonical_test).
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
)

const examResultReleasedTopic = "chora.delivery.exam_result.released.v1"

// TestExamResultReleased_CanonicalRoundTrip — PASS verdict, string outcome
// (the domain-native producer form). Asserts every field + the envelope
// round-trip through the generated schema shape.
func TestExamResultReleased_CanonicalRoundTrip(t *testing.T) {
	env := fixedEnvelope()
	scoredAt := time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC)
	payload := map[string]any{
		"result_id":     "019e2f93-d586-71b5-8c3d-e2b0d0d5f001",
		"exam_id":       "019e2f93-d586-71b5-8c3d-e2b0d0d5e777",
		"exam_form_id":  "019e2f93-d586-71b5-8c3d-e2b0d0d5a010",
		"candidate_ref": "019e2f93-d586-71b5-8c3d-e2b0d0d5c777",
		"tenant_id":     "tenant-1",
		"outcome":       "PASS",
		"raw_score":     int32(72),
		"max_score":     int32(100),
		"cut_score":     int32(60),
		"scored_at":     scoredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload(examResultReleasedTopic, env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.ExamResultReleased
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen ExamResultReleased: %v", err)
	}
	if m.GetResultId() != "019e2f93-d586-71b5-8c3d-e2b0d0d5f001" {
		t.Errorf("result_id (2) = %q", m.GetResultId())
	}
	if m.GetExamId() != "019e2f93-d586-71b5-8c3d-e2b0d0d5e777" {
		t.Errorf("exam_id (3) = %q", m.GetExamId())
	}
	if m.GetExamFormId() != "019e2f93-d586-71b5-8c3d-e2b0d0d5a010" {
		t.Errorf("exam_form_id (4) = %q", m.GetExamFormId())
	}
	if m.GetCandidateRef() != "019e2f93-d586-71b5-8c3d-e2b0d0d5c777" {
		t.Errorf("candidate_ref (5) = %q", m.GetCandidateRef())
	}
	if m.GetTenantId() != "tenant-1" {
		t.Errorf("tenant_id (6) = %q", m.GetTenantId())
	}
	if m.GetOutcome() != deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_PASS {
		t.Errorf("outcome (7) = %v want PASS", m.GetOutcome())
	}
	if m.GetRawScore() != 72 {
		t.Errorf("raw_score (8) = %d", m.GetRawScore())
	}
	if m.GetMaxScore() != 100 {
		t.Errorf("max_score (9) = %d", m.GetMaxScore())
	}
	if m.GetCutScore() != 60 {
		t.Errorf("cut_score (10) = %d", m.GetCutScore())
	}
	if m.GetScoredAt() == nil || !m.GetScoredAt().AsTime().Equal(scoredAt) {
		t.Errorf("scored_at (11) = %v want %v", m.GetScoredAt(), scoredAt)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() != "tenant-1" {
		t.Errorf("envelope (1) missing/empty tenant_id")
	}
	if m.GetEnvelope().GetEventId() != env.EventID {
		t.Errorf("envelope.event_id = %q want %q", m.GetEnvelope().GetEventId(), env.EventID)
	}
	if m.GetEnvelope().GetTraceparent() != env.Traceparent {
		t.Errorf("envelope.traceparent = %q want %q", m.GetEnvelope().GetTraceparent(), env.Traceparent)
	}
}

// TestExamResultReleased_FailOutcome_IntForm asserts the FAIL verdict and that
// the encoder also accepts the enum-int outcome form (defence-in-depth for a
// caller that pre-maps the enum). Zero raw_score is a legit FAIL and MUST still
// encode (it is the proto3 default but a scored 0 is real data — the encoder
// emits it because outcome is present; raw_score omission at 0 is acceptable
// since max_score + cut_score + outcome fully disambiguate the verdict).
func TestExamResultReleased_FailOutcome_IntForm(t *testing.T) {
	env := fixedEnvelope()
	scoredAt := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"result_id":     "res-fail-1",
		"exam_id":       "exam-1",
		"exam_form_id":  "form-1",
		"candidate_ref": "cand-1",
		"tenant_id":     "tenant-1",
		"outcome":       int32(2), // EXAM_RESULT_OUTCOME_FAIL
		"raw_score":     int32(41),
		"max_score":     int32(100),
		"cut_score":     int32(60),
		"scored_at":     scoredAt,
	}
	bz, err := protomarshal.MarshalPayload(examResultReleasedTopic, env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.ExamResultReleased
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.GetOutcome() != deliveryv1.ExamResultOutcome_EXAM_RESULT_OUTCOME_FAIL {
		t.Errorf("outcome (7) = %v want FAIL", m.GetOutcome())
	}
	if m.GetRawScore() != 41 {
		t.Errorf("raw_score (8) = %d", m.GetRawScore())
	}
	if m.GetCutScore() != 60 {
		t.Errorf("cut_score (10) = %d", m.GetCutScore())
	}
}

// TestExamResultReleased_NoPayload yields envelope-only (field 1 present, no
// domain fields) — proves the encoder never panics on a nil payload.
func TestExamResultReleased_NoPayload(t *testing.T) {
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload(examResultReleasedTopic, env, nil)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.ExamResultReleased
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.GetEnvelope() == nil || m.GetEnvelope().GetTenantId() != "tenant-1" {
		t.Errorf("envelope-only: missing envelope tenant_id")
	}
	if m.GetResultId() != "" {
		t.Errorf("envelope-only: unexpected result_id %q", m.GetResultId())
	}
}
