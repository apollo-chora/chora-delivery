// submission_graded_delivery_type_test — SubmissionGraded.delivery_type,
// additive field 14 within v1 (CHO-2224, §10.6 capstone criterion 1).
//
// The encoder must read delivery_type off the payload map and wire-encode it at
// field 14 (string, length-delimited), because it is chora-consumption's ONLY
// mode-bearing signal: without it a graduate assessment and a short-course grade
// both project as kind='assessment' and the unified transcript can demonstrate
// 2 KINDS but never 2 MODES.
//
// ⚠⚠ The omit-when-empty case is a SAFETY property, not a nicety. The topic has
// a Pub/Sub schema ATTACHED, so a populated field 14 is rejected 400 AT PUBLISH
// (never reaching a DLQ) until the matching revision is committed. Omitting the
// empty value is exactly what lets this code land before that revision.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/delivery/v1"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
)

// TestMarshalSubmissionGraded_DeliveryType asserts delivery_type wire-encodes at
// contract field number 14 and round-trips through the generated struct.
func TestMarshalSubmissionGraded_DeliveryType(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"submission_id": "sub-1",
		"assessment_id": "ass-1",
		"learner_gcid":  "gcid-learner",
		"delivery_type": "graduate",
		"graded_at":     env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.submission.graded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if !seen[14] {
		t.Errorf("SubmissionGraded: missing delivery_type field 14 (seen: %v)", seen)
	}
	var m deliveryv1.SubmissionGraded
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen SubmissionGraded: %v", err)
	}
	if m.GetDeliveryType() != "graduate" {
		t.Errorf("delivery_type (14) = %q; want %q", m.GetDeliveryType(), "graduate")
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalSubmissionGraded_DeliveryTypeShort proves the field is not pinned
// to one value: 'short' is the OTHER half of the §10.6 ">=2 modes" proof, so an
// encoder that hardcoded 'graduate' would satisfy the test above and still fail
// the capstone.
func TestMarshalSubmissionGraded_DeliveryTypeShort(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"submission_id": "sub-2",
		"assessment_id": "ass-2",
		"learner_gcid":  "gcid-learner",
		"delivery_type": "short",
		"graded_at":     env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.submission.graded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	var m deliveryv1.SubmissionGraded
	if err := proto.Unmarshal(bz, &m); err != nil {
		t.Fatalf("Unmarshal into gen SubmissionGraded: %v", err)
	}
	if m.GetDeliveryType() != "short" {
		t.Errorf("delivery_type (14) = %q; want %q", m.GetDeliveryType(), "short")
	}
}

// TestMarshalSubmissionGraded_OmitsEmptyDeliveryType — the pre-revision publish
// safety property. A freestanding assessment (offering_id NULL, legal per
// migration 0033) resolves NO delivery_type, and an unresolved value must leave
// field 14 entirely ABSENT from the wire so the message still validates against
// a registry revision that does not yet know field 14.
func TestMarshalSubmissionGraded_OmitsEmptyDeliveryType(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"submission_id": "sub-1",
		"assessment_id": "ass-1",
		"learner_gcid":  "gcid-learner",
		"delivery_type": "",
		"graded_at":     env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.submission.graded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if seen[14] {
		t.Error("field 14 delivery_type must be omitted when empty: a populated field 14 is rejected 400 AT PUBLISH until the schema revision is committed")
	}
}

// TestMarshalSubmissionGraded_OmitsAbsentDeliveryType — same safety property for
// a payload that carries no delivery_type key at all (every producer that has
// not been taught to resolve it).
func TestMarshalSubmissionGraded_OmitsAbsentDeliveryType(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"submission_id": "sub-1",
		"assessment_id": "ass-1",
		"learner_gcid":  "gcid-learner",
		"graded_at":     env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.submission.graded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if seen[14] {
		t.Error("field 14 delivery_type must be absent when the payload carries no delivery_type key")
	}
}
