// protodecode_topup_test.go — remaining public-API branch coverage: the
// one-shot unknown-topic WARN short-circuit, JSON "null" payload hydration
// edge, attribute hydration into blank-string keys, and non-string values
// never being overwritten by attributes.
package protodecode_test

import (
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protodecode"
)

func TestDecodePayloadMap_UnknownTopicWarnOnce(t *testing.T) {
	topic := "chora.delivery.never.seen.v1"
	payload := []byte(`{"k":"v"}`)
	// First call logs the one-shot WARN and JSON-decodes.
	out, err := protodecode.DecodePayloadMap(topic, payload)
	if err != nil {
		t.Fatalf("first decode: %v", err)
	}
	if out["k"] != "v" {
		t.Fatalf("payload k = %v", out["k"])
	}
	// Second call exercises the warnedUnknown map short-circuit.
	out2, err := protodecode.DecodePayloadMap(topic, payload)
	if err != nil {
		t.Fatalf("second decode: %v", err)
	}
	if out2["k"] != "v" {
		t.Fatalf("second payload k = %v", out2["k"])
	}
}

func TestDecodePayloadMap_NullPayloadYieldsEmptyMap(t *testing.T) {
	out, err := protodecode.DecodePayloadMap("chora.unknown.topic.v1", []byte("null"))
	if err != nil {
		t.Fatalf("null payload must decode to an empty map, got %v", err)
	}
	if out == nil || len(out) != 0 {
		t.Fatalf("expected non-nil empty map, got %v", out)
	}
}

func TestDecodePayloadMapWithAttrs_FillsBlankStringKey(t *testing.T) {
	// Payload carries tenant_id as an explicit empty string — the attribute
	// value must fill it (hydrateFromAttrs treats ""-as-present as missing).
	out, err := protodecode.DecodePayloadMapWithAttrs(
		"chora.delivery.grading.oe_batch_completed.v1",
		[]byte(`{"tenant_id":""}`),
		map[string]string{"tenant_id": "attr-tnt"},
	)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := out["tenant_id"]; got != "attr-tnt" {
		t.Fatalf("tenant_id = %q, want attr-tnt (blank key must hydrate)", got)
	}
}

func TestDecodePayloadMapWithAttrs_NonStringValueNotOverwritten(t *testing.T) {
	// schema_version is a JSON number in the payload — an attribute string
	// must NEVER clobber a non-string value.
	out, err := protodecode.DecodePayloadMapWithAttrs(
		"chora.delivery.grading.oe_batch_completed.v1",
		[]byte(`{"schema_version":1}`),
		map[string]string{"schema_version": "2"},
	)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := out["schema_version"]; got != float64(1) {
		t.Fatalf("schema_version = %v (%T), want float64(1)", got, got)
	}
}
