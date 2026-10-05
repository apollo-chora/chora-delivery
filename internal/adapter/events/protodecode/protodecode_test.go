// protodecode_test.go — coverage for the JSON-fallback path + envelope-
// attribute hydration. The binary path is exercised once chora-contracts
// codegen lands GradingOeBatchCompleted; for now the binary decoder is a
// sentinel that routes to JSON, which this file verifies.
package protodecode_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protodecode"
)

func TestDecodePayloadMap_JSONFallback(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"submission_id": "sub-1",
		"oe_batch_id":   "batch-1",
		"graded": []map[string]any{
			{"test_set_question_id": "oe-1", "points_earned": 40.0, "points_possible": 50.0},
		},
	})
	out, err := protodecode.DecodePayloadMap("chora.delivery.grading.oe_batch_completed.v1", payload)
	if err != nil {
		t.Fatalf("DecodePayloadMap: %v", err)
	}
	if got := out["submission_id"]; got != "sub-1" {
		t.Errorf("submission_id: want sub-1, got %v", got)
	}
}

func TestDecodePayloadMap_EmptyReturnsSentinel(t *testing.T) {
	_, err := protodecode.DecodePayloadMap("chora.delivery.grading.oe_batch_completed.v1", nil)
	if !errors.Is(err, protodecode.ErrEmptyPayload) {
		t.Fatalf("want ErrEmptyPayload, got %v", err)
	}
}

func TestDecodePayloadMap_GarbageReturnsError(t *testing.T) {
	_, err := protodecode.DecodePayloadMap("chora.delivery.grading.oe_batch_completed.v1", []byte("not-json-not-proto"))
	if err == nil {
		t.Fatalf("want error on garbage payload, got nil")
	}
}

func TestDecodePayloadMapWithAttrs_HydratesEnvelope(t *testing.T) {
	// Payload omits envelope fields — the JSON-shape publisher places them in
	// Pub/Sub attributes only.
	payload := []byte(`{"submission_id":"sub-1"}`)
	attrs := map[string]string{
		"event_id":    "evt-1",
		"tenant_id":   "tnt-1",
		"gcid":        "gcid-1",
		"traceparent": "00-trace-01",
	}
	out, err := protodecode.DecodePayloadMapWithAttrs(
		"chora.delivery.grading.oe_batch_completed.v1", payload, attrs,
	)
	if err != nil {
		t.Fatalf("DecodePayloadMapWithAttrs: %v", err)
	}
	if got := out["event_id"]; got != "evt-1" {
		t.Errorf("event_id from attrs: want evt-1, got %v", got)
	}
	if got := out["tenant_id"]; got != "tnt-1" {
		t.Errorf("tenant_id from attrs: want tnt-1, got %v", got)
	}
	if got := out["traceparent"]; got != "00-trace-01" {
		t.Errorf("traceparent from attrs: want 00-trace-01, got %v", got)
	}
	// Payload value preserved.
	if got := out["submission_id"]; got != "sub-1" {
		t.Errorf("submission_id (payload): want sub-1, got %v", got)
	}
}

func TestDecodePayloadMapWithAttrs_PayloadWinsOverAttrs(t *testing.T) {
	payload := []byte(`{"tenant_id":"payload-tnt"}`)
	attrs := map[string]string{"tenant_id": "attr-tnt"}
	out, err := protodecode.DecodePayloadMapWithAttrs(
		"chora.delivery.grading.oe_batch_completed.v1", payload, attrs,
	)
	if err != nil {
		t.Fatalf("DecodePayloadMapWithAttrs: %v", err)
	}
	if got := out["tenant_id"]; got != "payload-tnt" {
		t.Errorf("tenant_id: want payload-tnt (payload wins), got %v", got)
	}
}

func TestDecodePayloadMap_UnknownTopicJSONOK(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"foo": "bar"})
	out, err := protodecode.DecodePayloadMap("chora.unknown.topic.v1", payload)
	if err != nil {
		t.Fatalf("unknown topic: want OK (JSON fallback), got %v", err)
	}
	if got := out["foo"]; got != "bar" {
		t.Errorf("payload: want bar, got %v", got)
	}
}
