// exam_result_encoders.go — binary-protobuf encoder for the W4 Exam BC
// outcome event `chora.delivery.exam_result.released.v1` per
// chora-contracts/proto/events-flat/delivery/exam_result/released.proto.
//
// Schema field map:
//
//	1  Envelope           envelope
//	2  string             result_id
//	3  string             exam_id
//	4  string             exam_form_id
//	5  string             candidate_ref
//	6  string             tenant_id
//	7  ExamResultOutcome  outcome (enum varint: PASS=1, FAIL=2)
//	8  int32              raw_score
//	9  int32              max_score
//	10 int32              cut_score
//	11 Timestamp          scored_at
//
// Producer: services/chora-delivery/internal/adapter/events/exam_result_publisher.go
// (ExamResultPublisher.PublishExamResultReleased) tees this into the outbox at
// ExamForm.Grade finalize (ADR-190 D1 outcome-spine seam). No consumer yet.
package protomarshal

import (
	"fmt"
	"strings"
)

// encodeExamResultReleased emits the canonical binary wire bytes for
// chora.delivery.exam_result.released.v1 (Schema Registry validation passes).
func encodeExamResultReleased(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}

	out = encodeStringField(out, payload, "result_id", 2)
	out = encodeStringField(out, payload, "exam_id", 3)
	out = encodeStringField(out, payload, "exam_form_id", 4)
	out = encodeStringField(out, payload, "candidate_ref", 5)
	out = encodeStringField(out, payload, "tenant_id", 6)

	// 7 — ExamResultOutcome enum (varint). Accept the domain-native string
	// ("PASS"/"FAIL") or a pre-mapped enum int; omit the proto3-default 0
	// (never a real verdict — the domain only emits PASS/FAIL).
	if v, ok := examResultOutcomeEnum(payload["outcome"]); ok && v != 0 {
		out = appendVarint(out, 7, uint64(uint32(v)))
	}

	// 8/9/10 — int32 scores. proto3 default-omit at 0 is lossless (the decoder
	// reads an absent int32 back as 0 — a scored-0 raw is fully disambiguated by
	// max_score + cut_score + outcome).
	if v, ok := asInt32(payload["raw_score"]); ok && v != 0 {
		out = appendVarint(out, 8, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["max_score"]); ok && v != 0 {
		out = appendVarint(out, 9, uint64(uint32(v)))
	}
	if v, ok := asInt32(payload["cut_score"]); ok && v != 0 {
		out = appendVarint(out, 10, uint64(uint32(v)))
	}

	// 11 — Timestamp scored_at (accepts time.Time, *time.Time, or RFC3339Nano).
	if err := encodeTimestampInto(&out, payload, "scored_at", 11); err != nil {
		return nil, fmt.Errorf("scored_at: %w", err)
	}

	return out, nil
}

// examResultOutcomeEnum maps a payload outcome value onto the ExamResultOutcome
// enum wire value. Accepts:
//   - "PASS" / "FAIL" (case-insensitive; also the SCREAMING enum spelling) —
//     the domain-native exam.Outcome form the producer sends
//   - an int form (1=PASS, 2=FAIL) already mapped by a caller
//
// Returns (0,false) for anything else so the encoder omits the field.
func examResultOutcomeEnum(v any) (int32, bool) {
	if s, ok := v.(string); ok {
		switch strings.ToUpper(strings.TrimSpace(s)) {
		case "PASS", "EXAM_RESULT_OUTCOME_PASS":
			return 1, true
		case "FAIL", "EXAM_RESULT_OUTCOME_FAIL":
			return 2, true
		default:
			return 0, false
		}
	}
	if n, ok := asInt32(v); ok {
		return n, true
	}
	return 0, false
}
