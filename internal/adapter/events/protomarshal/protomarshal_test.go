// Package protomarshal_test verifies binary protobuf wire-format encoding for
// chora-delivery's outbox event payloads. RED tests written BEFORE the encoder
// lands per CLAUDE.md §development-execution + feedback_strict_tdd.
//
// Gap: outbox writer was persisting JSON-marshalled payload bytes that the binary
// Pub/Sub Schema Registry (BINARY encoding) rejects at publish time with
// "Invalid binary proto message". Fix is producer-side: marshal to canonical
// proto wire bytes before the outbox row is written. Dispatcher passes bytes
// through unchanged.
//
// Mirrors the canonical fix landed at
// services/chora-consumption/internal/adapter/events/protomarshal/.
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
)

// fixedEnvelope returns an envelope with deterministic values for byte-level
// assertions.
func fixedEnvelope() protomarshal.Envelope {
	t := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	return protomarshal.Envelope{
		EventID:        "01971a90-0000-7000-8000-000000000001",
		IdempotencyKey: "idemp-1",
		TenantID:       "tenant-1",
		GCID:           "gcid-applicant",
		OccurredAt:     t,
		PublishedAt:    t.Add(time.Millisecond),
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:     "",
		SourceProject:  "chora-489812",
		SourceService:  "chora-delivery",
		SchemaVersion:  1,
	}
}

// walkWireTags walks a wire-format byte stream, returning the set of field
// numbers seen at the top level. Asserts each TLV is well-formed; failure
// is reported via t.Fatalf at the offending offset.
func walkWireTags(t *testing.T, bz []byte) map[protowire.Number]bool {
	t.Helper()
	seen := map[protowire.Number]bool{}
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		rem = rem[n:]
		seen[num] = true
		switch typ {
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				t.Fatalf("invalid length-delimited value for field %d", num)
			}
			rem = rem[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(rem)
			if n < 0 {
				t.Fatalf("invalid varint value for field %d", num)
			}
			rem = rem[n:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}
	return seen
}

// envelopeBytes returns the field-1 envelope submessage bytes from a
// top-level wire stream. Fatals when missing or malformed.
func envelopeBytes(t *testing.T, bz []byte) []byte {
	t.Helper()
	num, typ, n := protowire.ConsumeTag(bz)
	if num != 1 || typ != protowire.BytesType || n < 0 {
		t.Fatalf("expected envelope tag (1, bytes); got num=%d typ=%d", num, typ)
	}
	env, m := protowire.ConsumeBytes(bz[n:])
	if m < 0 || len(env) == 0 {
		t.Fatal("invalid or empty envelope bytes")
	}
	return env
}

// requireEnvelopeFields enforces the chora.common.v1.EventEnvelope (inlined)
// invariants the Schema Registry checks on every events-flat schema: every
// producer-set field 1..11 is present.
func requireEnvelopeFields(t *testing.T, envBz []byte) {
	t.Helper()
	seen := walkWireTags(t, envBz)
	required := []protowire.Number{1, 2, 3, 4, 5, 6, 7, 9, 10, 11}
	for _, want := range required {
		if !seen[want] {
			t.Fatalf("envelope: missing required field %d (seen: %v)", want, seen)
		}
	}
}

// TestMarshalCourseCreated_RoundTripsBinaryProto asserts the encoder for
// chora.delivery.course.created.v1 emits wire-format protobuf bytes whose
// nested envelope parses and whose tag stream is well-formed.
//
// Schema: chora-contracts/proto/events-flat/delivery/course/created.proto
//
//	1 envelope, 2 course_id, 3 title, 4 instructor_gcid, 5 atom_path_id,
//	6 modality (enum), 7 starts_at, 8 ends_at, 9 capacity, 10 created_at
func TestMarshalCourseCreated_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"course_id":       "course-1",
		"title":           "CSPO Fundamentals",
		"instructor_gcid": "gcid-instructor",
		"public":          true,
		"price_sgd_cents": 99900,
		"created_at":      env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4} {
		if !seen[want] {
			t.Errorf("CourseCreated: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalBookingConfirmed_RoundTripsBinaryProto verifies BookingConfirmed.
//
// Schema: chora-contracts/proto/events-flat/delivery/booking/confirmed.proto
//
//	1 envelope, 2 booking_id, 3 course_id, 4 learner_gcid,
//	5 status (enum), 6 seat_number, 7 confirmed_at
func TestMarshalBookingConfirmed_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"booking_id":   "booking-1",
		"course_id":    "course-1",
		"class_id":     "class-1",
		"learner_gcid": "gcid-learner",
		"seat_number":  int32(7),
		"confirmed_at": env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.booking.confirmed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 6, 7} {
		if !seen[want] {
			t.Errorf("BookingConfirmed: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalCertIssued_RoundTripsBinaryProto verifies CertIssued.
//
// Schema: chora-contracts/proto/events-flat/delivery/certification/issued.proto
//
//	1 envelope, 2 cert_id, 3 learner_gcid, 4 course_id,
//	5 cert_type (enum), 6 issued_at, 7 expires_at
func TestMarshalCertIssued_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"certification_id": "cert-1",
		"course_id":        "course-1",
		"learner_gcid":     "gcid-learner",
		"hash":             "deadbeef",
		"issued_at":        env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.certification.issued.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	// envelope, cert_id, learner_gcid, course_id, issued_at MUST be present.
	for _, want := range []protowire.Number{1, 2, 3, 4, 6} {
		if !seen[want] {
			t.Errorf("CertIssued: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalApplicationSubmitted_RoundTripsBinaryProto verifies the
// schema-bound chora.delivery.application.submitted.v1 topic.
//
// Schema: chora-contracts/proto/events-flat/delivery/application/submitted.proto
//
//	1 envelope, 2 application_id, 3 applicant_gcid, 4 course_id,
//	5 myinfo_retrieved_at, 6 supporting_docs_uri, 7 submitted_at
func TestMarshalApplicationSubmitted_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id":       "app-1",
		"course_id":            "course-1",
		"learner_gcid":         "gcid-applicant",
		"status":               "submitted",
		"created_at":           env.OccurredAt.Format(time.RFC3339Nano),
		"updated_at":           env.OccurredAt.Format(time.RFC3339Nano),
		"chora_imda_dimension": "accountability",
		"singpass_session_id":  "sgp-sess-1",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.submitted.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4} {
		if !seen[want] {
			t.Errorf("ApplicationSubmitted: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalApplicationOfferMade_RoundTripsBinaryProto.
//
// Schema: events-flat/delivery/application/offer_made.proto
//
//	1 envelope, 2 application_id, 3 applicant_gcid, 4 course_id,
//	5 reviewer_gcid, 6 offer_expires_at, 7 price_micros, 8 currency, 9 notes,
//	10 offered_at
func TestMarshalApplicationOfferMade_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id":   "app-1",
		"course_id":        "course-1",
		"learner_gcid":     "gcid-applicant",
		"status":           "offer_made",
		"offer_expires_at": env.OccurredAt.Add(48 * time.Hour).Format(time.RFC3339Nano),
		"created_at":       env.OccurredAt.Format(time.RFC3339Nano),
		"updated_at":       env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.offer_made.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4} {
		if !seen[want] {
			t.Errorf("ApplicationOfferMade: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalApplicationAccepted_RoundTripsBinaryProto.
//
// Schema: events-flat/delivery/application/accepted.proto
//
//	1 envelope, 2 application_id, 3 applicant_gcid, 4 course_id, 5 accepted_at
func TestMarshalApplicationAccepted_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id": "app-1",
		"course_id":      "course-1",
		"learner_gcid":   "gcid-applicant",
		"status":         "accepted",
		"updated_at":     env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.accepted.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4} {
		if !seen[want] {
			t.Errorf("ApplicationAccepted: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalApplicationWithdrawn_RoundTripsBinaryProto.
//
// Schema: events-flat/delivery/application/withdrawn.proto
//
//	1 envelope, 2 application_id, 3 applicant_gcid, 4 course_id,
//	5 prior_status (enum), 6 reason, 7 withdrawn_at
func TestMarshalApplicationWithdrawn_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id":   "app-1",
		"course_id":        "course-1",
		"learner_gcid":     "gcid-applicant",
		"status":           "withdrawn",
		"withdrawn_reason": "personal",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.withdrawn.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 6} {
		if !seen[want] {
			t.Errorf("ApplicationWithdrawn: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalApplicationRejected_RoundTripsBinaryProto.
//
// Schema: events-flat/delivery/application/rejected.proto
//
//	1 envelope, 2 application_id, 3 applicant_gcid, 4 course_id,
//	5 reviewer_gcid, 6 reason, 7 rejected_at
func TestMarshalApplicationRejected_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id":  "app-1",
		"course_id":       "course-1",
		"learner_gcid":    "gcid-applicant",
		"status":          "rejected",
		"rejected_reason": "below cut-off",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.rejected.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 6} {
		if !seen[want] {
			t.Errorf("ApplicationRejected: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalApplicationPaid_RoundTripsBinaryProto.
//
// Schema: events-flat/delivery/application/paid.proto
//
//	1 envelope, 2 application_id, 3 applicant_gcid, 4 course_id,
//	5 stripe_payment_intent_id, 6 total_micros, 7 currency,
//	8 funding_lines (repeated), 9 paid_at
func TestMarshalApplicationPaid_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id":           "app-1",
		"course_id":                "course-1",
		"learner_gcid":             "gcid-applicant",
		"status":                   "paid",
		"stripe_payment_intent_id": "pi_3OabcXYZ123",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.paid.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5} {
		if !seen[want] {
			t.Errorf("ApplicationPaid: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalApplicationEnrolled_RoundTripsBinaryProto.
//
// Schema: events-flat/delivery/application/enrolled.proto
//
//	1 envelope, 2 application_id, 3 applicant_gcid, 4 course_id,
//	5 enrollment_id, 6 enrolled_at
func TestMarshalApplicationEnrolled_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id": "app-1",
		"course_id":      "course-1",
		"learner_gcid":   "gcid-applicant",
		"status":         "enrolled",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.enrolled.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4} {
		if !seen[want] {
			t.Errorf("ApplicationEnrolled: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshal_UnknownTopic_FailsLoud asserts unsupported topics return a
// typed error so the dispatcher dead-letters rather than persisting bytes
// the Schema Registry will reject.
func TestMarshal_UnknownTopic_FailsLoud(t *testing.T) {
	env := fixedEnvelope()
	_, err := protomarshal.MarshalPayload("chora.delivery.some.unwired.v1", env, nil)
	if err == nil {
		t.Fatal("expected ErrUnsupportedTopic; got nil")
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		t.Fatalf("expected IsUnsupportedTopic(true); got: %v", err)
	}
}

// TestMarshalPayload_RoutesAllAssessmentLifecycleTopics asserts the OUTER
// dispatcher (MarshalPayload) routes EVERY assessment/submission/grading
// lifecycle topic through to the binary encoder in MarshalAssessmentPayload.
//
// Regression guard for §4 of HANDOFF_CHO-1638: the inner MarshalAssessmentPayload
// gained encoders for all 15 topics, but the outer MarshalPayload routing list
// omitted assessment.opened / grading_started / graded / archived. Those 4 fell
// through to ErrUnsupportedTopic → JSON fallback → REJECTED by the BINARY Schema
// Registry schema attached to the topic → silent dead-letter. A topic that has a
// binary encoder MUST be reachable from MarshalPayload, else the JSON fallback
// path it lands in publishes bytes the schema rejects.
func TestMarshalPayload_RoutesAllAssessmentLifecycleTopics(t *testing.T) {
	env := fixedEnvelope()
	topics := []string{
		"chora.delivery.assessment.created.v1",
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
	}
	for _, topic := range topics {
		bz, err := protomarshal.MarshalPayload(topic, env, map[string]any{"assessment_id": "a-1"})
		if err != nil {
			t.Errorf("topic %q: MarshalPayload returned error (unrouted → would JSON-marshal + dead-letter): %v", topic, err)
			continue
		}
		if len(bz) == 0 {
			t.Errorf("topic %q: produced zero bytes", topic)
			continue
		}
		// The nested envelope (field 1) must parse + carry the required fields,
		// proving these went through the binary encoder, not JSON.
		requireEnvelopeFields(t, envelopeBytes(t, bz))
	}
}

// TestMarshal_UnattachedTopic_FailsLoud — chora-delivery has 4 currently-
// emitted topics with NO Schema Registry schema (course.updated /
// enrollment.created / enrollment.cancelled / application.under_review). The
// encoder DOES NOT pretend to support them — it returns ErrUnsupportedTopic so
// the caller logs+falls back to JSON.
//
// (course.published GAINED a binary encoder + Schema Registry schema in the
// 2026-07-01 event-fabric repair — it is no longer in this list.)
//
// When schemas land for these topics, add cases + drop the JSON fallback.
func TestMarshal_UnattachedTopic_FailsLoud(t *testing.T) {
	env := fixedEnvelope()
	for _, topic := range []string{
		"chora.delivery.course.updated.v1",
		"chora.delivery.enrollment.created.v1",
		"chora.delivery.enrollment.cancelled.v1",
		"chora.delivery.application.under_review.v1",
	} {
		_, err := protomarshal.MarshalPayload(topic, env, map[string]any{"course_id": "c-1"})
		if err == nil {
			t.Errorf("topic %q: expected ErrUnsupportedTopic; got nil", topic)
			continue
		}
		if !protomarshal.IsUnsupportedTopic(err) {
			t.Errorf("topic %q: expected IsUnsupportedTopic(true); got: %v", topic, err)
		}
	}
}

// TestMarshal_NilPayloadProducesEmptyButValidMessage asserts a nil payload
// still produces a wire-valid message containing just the envelope.
func TestMarshal_NilPayloadProducesEmptyButValidMessage(t *testing.T) {
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, nil)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("nil payload should still produce envelope bytes")
	}
	num, _, n := protowire.ConsumeTag(bz)
	if n < 0 {
		t.Fatal("invalid leading tag")
	}
	if num != 1 {
		t.Fatalf("expected leading field=1 (envelope); got %d", num)
	}
}

// TestMarshal_RejectsInvalidPayloadType — wrong Go type on a numeric slot
// must fail loud rather than silently coerce.
func TestMarshal_RejectsInvalidPayloadType(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"seat_number":  "not-a-number", // schema demands int32
		"booking_id":   "b-1",
		"course_id":    "c-1",
		"learner_gcid": "g-1",
	}
	_, err := protomarshal.MarshalPayload("chora.delivery.booking.confirmed.v1", env, payload)
	if err == nil {
		t.Fatal("expected type error for seat_number=string")
	}
}

// TestMarshal_StringFieldRejectsNonString — schema-string slot must reject
// non-string Go types (no silent coercion → garbage bytes).
func TestMarshal_StringFieldRejectsNonString(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"course_id": 12345, // schema demands string
		"title":     "OK",
	}
	_, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, payload)
	if err == nil {
		t.Fatal("expected typed error for course_id=int")
	}
}

// TestMarshal_TimestampStringIsoFormat — application payloads pass timestamps
// as RFC3339Nano strings (matches CanonicalEventPayload). The encoder must
// parse them into the nested google.protobuf.Timestamp shape (seconds+nanos).
func TestMarshal_TimestampStringIsoFormat(t *testing.T) {
	env := fixedEnvelope()
	isoTs := env.OccurredAt.Format(time.RFC3339Nano)
	payload := map[string]any{
		"application_id": "app-1",
		"course_id":      "course-1",
		"learner_gcid":   "gcid-applicant",
		"status":         "accepted",
		"updated_at":     isoTs,
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.accepted.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	// field 5 = accepted_at — the encoder maps `updated_at` (when status is
	// accepted) onto the schema's accepted_at slot.
	if !seen[5] {
		t.Errorf("ApplicationAccepted: missing accepted_at field 5 (seen: %v)", seen)
	}
}

// TestEnvelopePtr_TimePointerAccepted ensures asTime accepts *time.Time.
func TestEnvelopePtr_TimePointerAccepted(t *testing.T) {
	env := fixedEnvelope()
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"course_id":  "c-1",
		"title":      "T",
		"created_at": &now,
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload with *time.Time: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
}

// TestEnvelope_CarriesCorrelationAndCausation — application_publisher.go
// stamps CorrelationID = application_id. The encoder MUST emit field 12 +
// 13 in the nested envelope.
func TestEnvelope_CarriesCorrelationAndCausation(t *testing.T) {
	env := fixedEnvelope()
	env.CorrelationID = "app-1-correlation"
	env.CausationID = "evt-cause-001"
	payload := map[string]any{
		"application_id": "app-1",
		"course_id":      "course-1",
		"learner_gcid":   "gcid-applicant",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.submitted.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	envBz := envelopeBytes(t, bz)
	seen := walkWireTags(t, envBz)
	if !seen[12] {
		t.Errorf("envelope missing correlation_id field 12 (seen: %v)", seen)
	}
	if !seen[13] {
		t.Errorf("envelope missing causation_id field 13 (seen: %v)", seen)
	}
}

// TestEnvelope_CarriesIMDADimension — chora-delivery sets
// chora_imda_dimension on application + enrollment payloads. Encoder must
// emit it as envelope field 14.
func TestEnvelope_CarriesIMDADimension(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id":       "app-1",
		"course_id":            "course-1",
		"learner_gcid":         "gcid-applicant",
		"chora_imda_dimension": "transparency",
		"imda_lifecycle_stage": "runtime",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.paid.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	envBz := envelopeBytes(t, bz)
	seen := walkWireTags(t, envBz)
	if !seen[14] {
		t.Errorf("envelope missing chora_imda_dimension field 14 (seen: %v)", seen)
	}
	if !seen[15] {
		t.Errorf("envelope missing imda_lifecycle_stage field 15 (seen: %v)", seen)
	}
}

// TestMarshalApplicationPaid_FullPayload exercises every typed slot the
// paid schema carries (stripe id, total_micros int64, currency, paid_at).
func TestMarshalApplicationPaid_FullPayload(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id":           "app-1",
		"course_id":                "course-1",
		"learner_gcid":             "gcid-applicant",
		"status":                   "paid",
		"stripe_payment_intent_id": "pi_3OabcXYZ",
		"total_micros":             int64(99_900_000),
		"currency":                 "SGD",
		"updated_at":               env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.paid.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6, 7, 9} {
		if !seen[want] {
			t.Errorf("ApplicationPaid full: missing field %d (seen: %v)", want, seen)
		}
	}
}

// TestMarshalApplicationEnrolled_WithEnrollmentID exercises the
// enrollment_id slot.
func TestMarshalApplicationEnrolled_WithEnrollmentID(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id": "app-1",
		"course_id":      "course-1",
		"learner_gcid":   "gcid-applicant",
		"status":         "enrolled",
		"enrollment_id":  "enr-1",
		"updated_at":     env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.enrolled.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if !seen[5] {
		t.Errorf("ApplicationEnrolled: missing enrollment_id field 5 (seen: %v)", seen)
	}
	if !seen[6] {
		t.Errorf("ApplicationEnrolled: missing enrolled_at field 6 (seen: %v)", seen)
	}
}

// TestMarshal_NilPayload_AcrossAllSchemas — nil payload must produce a
// valid envelope-only message for every schema-bound topic.
func TestMarshal_NilPayload_AcrossAllSchemas(t *testing.T) {
	env := fixedEnvelope()
	for _, topic := range []string{
		"chora.delivery.course.created.v1",
		"chora.delivery.booking.confirmed.v1",
		"chora.delivery.certification.issued.v1",
		"chora.delivery.application.submitted.v1",
		"chora.delivery.application.offer_made.v1",
		"chora.delivery.application.accepted.v1",
		"chora.delivery.application.withdrawn.v1",
		"chora.delivery.application.rejected.v1",
		"chora.delivery.application.paid.v1",
		"chora.delivery.application.enrolled.v1",
	} {
		bz, err := protomarshal.MarshalPayload(topic, env, nil)
		if err != nil {
			t.Errorf("topic %q: nil payload: %v", topic, err)
			continue
		}
		if len(bz) == 0 {
			t.Errorf("topic %q: nil payload produced empty bytes", topic)
		}
		num, _, n := protowire.ConsumeTag(bz)
		if n < 0 || num != 1 {
			t.Errorf("topic %q: leading tag = %d (want envelope=1)", topic, num)
		}
	}
}

// TestMarshal_RejectsBadString_PerSchema — every schema-bound topic must
// fail loud on a wrong-typed string slot (no silent garbage).
func TestMarshal_RejectsBadString_PerSchema(t *testing.T) {
	env := fixedEnvelope()
	cases := []struct {
		topic string
		key   string
		bad   any
	}{
		{"chora.delivery.application.submitted.v1", "application_id", 42},
		{"chora.delivery.application.offer_made.v1", "course_id", true},
		{"chora.delivery.application.accepted.v1", "learner_gcid", 3.14},
		{"chora.delivery.application.withdrawn.v1", "application_id", []string{"x"}},
		{"chora.delivery.application.rejected.v1", "course_id", map[string]any{}},
		{"chora.delivery.application.paid.v1", "learner_gcid", 99},
		{"chora.delivery.application.enrolled.v1", "application_id", false},
	}
	for _, tc := range cases {
		payload := map[string]any{tc.key: tc.bad}
		if _, err := protomarshal.MarshalPayload(tc.topic, env, payload); err == nil {
			t.Errorf("topic %q key %q bad type %T: expected error", tc.topic, tc.key, tc.bad)
		}
	}
}

// TestMarshal_RejectsBadTimestamp_PerSchema — wrong type on a Timestamp
// slot must fail loud.
func TestMarshal_RejectsBadTimestamp_PerSchema(t *testing.T) {
	env := fixedEnvelope()
	cases := []struct {
		topic string
		key   string
	}{
		{"chora.delivery.application.submitted.v1", "updated_at"},
		{"chora.delivery.application.offer_made.v1", "offer_expires_at"},
		{"chora.delivery.application.accepted.v1", "updated_at"},
		{"chora.delivery.application.withdrawn.v1", "updated_at"},
		{"chora.delivery.application.rejected.v1", "updated_at"},
		{"chora.delivery.application.paid.v1", "updated_at"},
		{"chora.delivery.application.enrolled.v1", "updated_at"},
		{"chora.delivery.course.created.v1", "created_at"},
		{"chora.delivery.booking.confirmed.v1", "confirmed_at"},
		{"chora.delivery.certification.issued.v1", "issued_at"},
	}
	for _, tc := range cases {
		payload := map[string]any{
			"application_id": "app-1",
			"course_id":      "course-1",
			"learner_gcid":   "gcid-applicant",
			"booking_id":     "b-1",
			"title":          "t",
			tc.key:           42, // wrong type for a Timestamp slot
		}
		if _, err := protomarshal.MarshalPayload(tc.topic, env, payload); err == nil {
			t.Errorf("topic %q key %q bad timestamp: expected error", tc.topic, tc.key)
		}
	}
}

// TestMarshal_AcceptsRFC3339WithoutNanos — producer formatting variations.
func TestMarshal_AcceptsRFC3339WithoutNanos(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"course_id":  "c-1",
		"title":      "T",
		"created_at": "2026-05-16T12:00:00Z",
	}
	if _, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, payload); err != nil {
		t.Fatalf("MarshalPayload RFC3339: %v", err)
	}
}

// TestMarshal_AcceptsUnparseableTimestampString_Skips — a malformed string
// is treated as absent (not present + not coercible). This matches
// chora-consumption's behaviour where mis-formatted timestamps degrade
// gracefully rather than crash the producer.
//
// Rationale: the envelope.OccurredAt / PublishedAt fields already capture
// the canonical event time; payload-level timestamp slots are secondary.
func TestMarshal_AcceptsUnparseableTimestampString_FailsLoud(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"course_id":  "c-1",
		"title":      "T",
		"created_at": "not-a-timestamp",
	}
	// We chose fail-loud here since silent-skip would mask a real producer
	// bug. The encoder rejects unparseable Timestamp strings.
	if _, err := protomarshal.MarshalPayload("chora.delivery.course.created.v1", env, payload); err == nil {
		t.Fatal("expected error for unparseable created_at; got nil")
	}
}

// TestMarshal_PaidAcceptsFloatTotalMicros covers the asInt64 float path
// (JSON-decoded payloads commonly arrive with numeric values as float64).
func TestMarshal_PaidAcceptsFloatTotalMicros(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"application_id":           "app-1",
		"course_id":                "course-1",
		"learner_gcid":             "gcid-applicant",
		"stripe_payment_intent_id": "pi_X",
		"total_micros":             float64(123_456),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.application.paid.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if !seen[6] {
		t.Errorf("ApplicationPaid: missing total_micros field 6 (float64 in)")
	}
}

// -----------------------------------------------------------------------------
// TestSet — 5 topics (created / question_added / question_updated /
// question_removed / published). Schemas at
// chora-contracts/proto/events-flat/delivery/test_set/*.
// -----------------------------------------------------------------------------

// walkWireTagsAllTypes is walkWireTags extended to also consume Fixed64 +
// Fixed32 wire types (used by proto `double` and `float`). Kept local to
// the test_set encoder tests since the upstream walkWireTags only sees
// BytesType + VarintType for the application/course/booking/cert encoders.
func walkWireTagsAllTypes(t *testing.T, bz []byte) map[protowire.Number]bool {
	t.Helper()
	seen := map[protowire.Number]bool{}
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		rem = rem[n:]
		seen[num] = true
		switch typ {
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				t.Fatalf("invalid length-delimited value for field %d", num)
			}
			rem = rem[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(rem)
			if n < 0 {
				t.Fatalf("invalid varint value for field %d", num)
			}
			rem = rem[n:]
		case protowire.Fixed64Type:
			_, n := protowire.ConsumeFixed64(rem)
			if n < 0 {
				t.Fatalf("invalid fixed64 value for field %d", num)
			}
			rem = rem[n:]
		case protowire.Fixed32Type:
			_, n := protowire.ConsumeFixed32(rem)
			if n < 0 {
				t.Fatalf("invalid fixed32 value for field %d", num)
			}
			rem = rem[n:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}
	return seen
}

// TestMarshalTestSetCreated_RoundTripsBinaryProto verifies test_set.created.
//
//	1 envelope, 2 test_set_id, 3 tenant_id, 4 author_gcid, 5 title, 6 created_at
func TestMarshalTestSetCreated_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"test_set_id": "ts-1",
		"tenant_id":   "tenant-1",
		"author_gcid": "gcid-author",
		"title":       "Phyllis Math Test Set",
		"created_at":  env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.test_set.created.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTagsAllTypes(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6} {
		if !seen[want] {
			t.Errorf("TestSetCreated: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalTestSetQuestionAdded_RoundTripsBinaryProto verifies
// test_set.question_added.
//
//	1 envelope, 2 test_set_id, 3 test_set_question_id, 4 question_atom_id,
//	5 question_type, 6 display_order, 7 points (double), 8 added_at
func TestMarshalTestSetQuestionAdded_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"test_set_id":          "ts-1",
		"test_set_question_id": "tsq-1",
		"question_atom_id":     "qatom-1",
		"question_type":        "mcq",
		"display_order":        int32(1),
		"points":               float64(2.0),
		"added_at":             env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.test_set.question_added.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTagsAllTypes(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6, 7, 8} {
		if !seen[want] {
			t.Errorf("TestSetQuestionAdded: missing field %d (seen: %v)", want, seen)
		}
	}
}

// TestMarshalTestSetQuestionUpdated_RoundTripsBinaryProto verifies
// test_set.question_updated.
//
//	1 envelope, 2 test_set_id, 3 test_set_question_id, 4 display_order,
//	5 points (double), 6 updated_at
func TestMarshalTestSetQuestionUpdated_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"test_set_id":          "ts-1",
		"test_set_question_id": "tsq-1",
		"display_order":        int32(3),
		"points":               float64(5.0),
		"updated_at":           env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.test_set.question_updated.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTagsAllTypes(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6} {
		if !seen[want] {
			t.Errorf("TestSetQuestionUpdated: missing field %d (seen: %v)", want, seen)
		}
	}
}

// TestMarshalTestSetQuestionRemoved_RoundTripsBinaryProto verifies
// test_set.question_removed.
//
//	1 envelope, 2 test_set_id, 3 test_set_question_id, 4 removed_at
func TestMarshalTestSetQuestionRemoved_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"test_set_id":          "ts-1",
		"test_set_question_id": "tsq-1",
		"removed_at":           env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.test_set.question_removed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTagsAllTypes(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4} {
		if !seen[want] {
			t.Errorf("TestSetQuestionRemoved: missing field %d (seen: %v)", want, seen)
		}
	}
}

// -----------------------------------------------------------------------------
// Grading — chora.delivery.grading.failed.v1 (E2E-BE-8)
//
// Producer: grading_inbox.go subscriber when batch_outcome != "ok" + (forward-
// looking) any grading-job-level terminal failure. Schema at
// chora-contracts/proto/events-flat/delivery/grading/failed.proto.
// -----------------------------------------------------------------------------

// TestMarshalGradingFailed_RoundTripsBinaryProto verifies the encoder for the
// terminal grading-failure event emits well-formed binary protobuf with the
// envelope at field 1 and the load-bearing payload fields populated.
//
// Schema fields:
//
//	1 envelope, 2 grading_job_id, 3 submission_id, 4 assessment_id,
//	5 learner_gcid, 6 failure_category (enum varint), 7 failure_message,
//	8 status (enum varint), 9 attempt_count, 10 oe_batch_id, 11 failed_at
func TestMarshalGradingFailed_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"grading_job_id":       "01985e7f-0000-7gid-0000-000000000001",
		"submission_id":        "01985e7f-1111-7sub-0000-000000000001",
		"assessment_id":        "01985e7f-2222-7ass-0000-000000000001",
		"learner_gcid":         "00000000-0000-7000-8000-000000002999",
		"failure_category":     int32(1), // VERTEX_AI_5XX
		"failure_message":      "vertex 5xx after 3 retries",
		"status":               int32(8), // GRADING_JOB_STATUS_FAILED
		"attempt_count":        int32(3),
		"oe_batch_id":          "01985e7f-3333-7bat-0000-000000000001",
		"failed_at":            env.OccurredAt.Format(time.RFC3339Nano),
		"chora_imda_dimension": "accountability",
		"imda_lifecycle_stage": "runtime",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.grading.failed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTagsAllTypes(t, bz)
	// envelope(1), grading_job_id(2), submission_id(3), assessment_id(4),
	// learner_gcid(5), failure_category(6), failure_message(7), status(8),
	// attempt_count(9), oe_batch_id(10), failed_at(11) ALL expected.
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11} {
		if !seen[want] {
			t.Errorf("GradingFailed: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalGradingFailed_MinimalPayload covers the grading-job-level
// (non-OE-batch-tied) failure where oe_batch_id is absent and failure_category
// is INTERNAL_ERROR.
func TestMarshalGradingFailed_MinimalPayload(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"submission_id":    "01985e7f-1111-7sub-0000-000000000001",
		"failure_category": int32(6), // INTERNAL_ERROR
		"failure_message":  "snapshot missing",
		"status":           int32(8),
		"failed_at":        env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.grading.failed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTagsAllTypes(t, bz)
	for _, want := range []protowire.Number{1, 3, 6, 7, 8, 11} {
		if !seen[want] {
			t.Errorf("GradingFailed minimal: missing field %d (seen: %v)", want, seen)
		}
	}
	// oe_batch_id absent → must NOT appear on the wire (canonical encoder omits
	// zero/empty values to keep the bytes minimal + Schema-Registry-clean).
	if seen[10] {
		t.Errorf("GradingFailed minimal: oe_batch_id field 10 should be absent")
	}
}

// TestMarshalGradingFailed_NilPayloadProducesEnvelopeOnly — nil payload still
// produces a wire-valid message containing just the envelope (cheap pre-flight
// sanity).
func TestMarshalGradingFailed_NilPayloadProducesEnvelopeOnly(t *testing.T) {
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload("chora.delivery.grading.failed.v1", env, nil)
	if err != nil {
		t.Fatalf("MarshalPayload nil payload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("nil payload produced empty bytes")
	}
	num, _, n := protowire.ConsumeTag(bz)
	if n < 0 || num != 1 {
		t.Errorf("GradingFailed nil: leading tag = %d (want envelope=1)", num)
	}
}

// TestMarshalGradingFailed_RejectsBadString — a non-string failure_message
// must fail loud (no silent garbage bytes).
func TestMarshalGradingFailed_RejectsBadString(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"submission_id":   "sub-1",
		"failure_message": 12345, // wrong type
	}
	if _, err := protomarshal.MarshalPayload("chora.delivery.grading.failed.v1", env, payload); err == nil {
		t.Fatal("expected typed error for failure_message=int; got nil")
	}
}

// TestMarshalTestSetPublished_RoundTripsBinaryProto verifies
// test_set.published.
//
//	1 envelope, 2 test_set_id, 3 tenant_id, 4 author_gcid,
//	5 question_count, 6 total_points (double), 7 published_at
func TestMarshalTestSetPublished_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"test_set_id":    "ts-1",
		"tenant_id":      "tenant-1",
		"author_gcid":    "gcid-author",
		"question_count": int32(3),
		"total_points":   float64(15.0),
		"published_at":   env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.test_set.published.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTagsAllTypes(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6, 7} {
		if !seen[want] {
			t.Errorf("TestSetPublished: missing field %d (seen: %v)", want, seen)
		}
	}
}

// -----------------------------------------------------------------------------
// LiveQuizSession events (ADR-168 §2.4) — schema events-flat/delivery/
// live_quiz_session/{started,score_awarded,ended}.proto.
// -----------------------------------------------------------------------------

// TestMarshalLiveQuizScoreAwarded_RoundTripsBinaryProto — score_awarded.v1:
//
//	1 envelope, 2 session_id, 3 live_quiz_id, 4 question_id,
//	5 awarded_points, 6 cumulative_score, 7 correct, 8 answer_millis
func TestMarshalLiveQuizScoreAwarded_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"session_id":       "sess-1",
		"live_quiz_id":     "lq-1",
		"question_id":      "q1",
		"awarded_points":   950,
		"cumulative_score": 950,
		"correct":          true,
		"answer_millis":    int64(1200),
		// redundant body key carried by the producer; encoder ignores it.
		"learner_gcid": "gcid-learner",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.live_quiz_session.score_awarded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6, 7, 8} {
		if !seen[want] {
			t.Errorf("LiveQuizScoreAwarded: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// collectWireStrings returns every length-delimited (BytesType) value carried
// at top-level field `field`, decoded as a string. Used to verify a repeated
// string field (each element is its own field-N entry on the wire).
func collectWireStrings(t *testing.T, bz []byte, field protowire.Number) []string {
	t.Helper()
	var out []string
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		rem = rem[n:]
		switch typ {
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				t.Fatalf("invalid bytes for field %d", num)
			}
			if num == field {
				out = append(out, string(v))
			}
			rem = rem[m:]
		case protowire.VarintType:
			_, m := protowire.ConsumeVarint(rem)
			if m < 0 {
				t.Fatalf("invalid varint for field %d", num)
			}
			rem = rem[m:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}
	return out
}

// TestMarshalLiveQuizScoreAwarded_AtomIDAndTopicTags — CR2-C3 atoms-as-
// questions → mastery seam. The additive fields 9 (atom_id, string) + 10
// (topic_tags, repeated string) must wire-encode correctly so the
// Schema Registry validates the binary against score_awarded.proto (NOT
// INVALID_BINARY_PROTO_MESSAGE) and chora-consumption can decode them.
func TestMarshalLiveQuizScoreAwarded_AtomIDAndTopicTags(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"session_id":       "sess-1",
		"live_quiz_id":     "lq-1",
		"question_id":      "q1",
		"awarded_points":   950,
		"cumulative_score": 950,
		"correct":          true,
		"answer_millis":    int64(1200),
		"atom_id":          "atom-uuid-9",
		"topic_tags":       []string{"scrum", "agile"},
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.live_quiz_session.score_awarded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{9, 10} {
		if !seen[want] {
			t.Errorf("missing field %d (seen: %v)", want, seen)
		}
	}
	if got := collectWireStrings(t, bz, 9); len(got) != 1 || got[0] != "atom-uuid-9" {
		t.Errorf("field 9 atom_id = %v; want [atom-uuid-9]", got)
	}
	// Repeated string: each tag is its own field-10 entry, in order.
	if got := collectWireStrings(t, bz, 10); len(got) != 2 || got[0] != "scrum" || got[1] != "agile" {
		t.Errorf("field 10 topic_tags = %v; want [scrum agile]", got)
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// Empty atom_id / empty topic_tags are proto3 defaults — omitted on the wire
// (ad-hoc question, or a linked atom with no tags). Keeps ad-hoc score_awarded
// byte-identical to the pre-CR2 shape.
func TestMarshalLiveQuizScoreAwarded_OmitsEmptyAtomAndTags(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"session_id":   "sess-1",
		"live_quiz_id": "lq-1",
		"question_id":  "q1",
		"correct":      true,
		"atom_id":      "",
		"topic_tags":   []string{},
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.live_quiz_session.score_awarded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if seen[9] {
		t.Error("field 9 atom_id must be omitted when empty (proto3 default)")
	}
	if seen[10] {
		t.Error("field 10 topic_tags must be omitted when empty")
	}
}

// TestMarshalLiveQuizSessionStarted_RoundTripsBinaryProto — started.v1:
//
//	1 envelope, 2 session_id, 3 live_quiz_id, 4 instructor_gcid, 5 started_at
func TestMarshalLiveQuizSessionStarted_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"session_id":      "sess-1",
		"live_quiz_id":    "lq-1",
		"instructor_gcid": "gcid-instructor",
		"started_at":      env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.live_quiz_session.started.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5} {
		if !seen[want] {
			t.Errorf("LiveQuizSessionStarted: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// -----------------------------------------------------------------------------
// EnrollmentCompleted — chora.delivery.enrollment.completed.v1 (WS1.c2, ADR-200)
// Schema — events-flat/delivery/enrollment/completed.proto:
//
//	1 envelope, 2 enrollment_id, 3 learner_gcid, 4 course_id,
//	5 passed (bool), 6 completed_at (Timestamp)
// -----------------------------------------------------------------------------

// TestMarshalEnrollmentCompleted_RoundTripsBinaryProto asserts the encoder for
// the per-learner course-completion fact emits wire-format protobuf whose
// nested envelope parses and whose load-bearing payload fields wire-encode at
// the contract field numbers (so the Schema Registry validates the
// binary against completed.proto, NOT INVALID_BINARY_PROTO_MESSAGE).
func TestMarshalEnrollmentCompleted_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"enrollment_id":        "enr-1",
		"learner_gcid":         "gcid-learner",
		"course_id":            "course-1",
		"passed":               true,
		"completed_at":         env.OccurredAt.Format(time.RFC3339Nano),
		"chora_imda_dimension": "accountability",
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.enrollment.completed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	// envelope(1), enrollment_id(2), learner_gcid(3), course_id(4),
	// passed(5, bool→varint), completed_at(6) ALL expected.
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6} {
		if !seen[want] {
			t.Errorf("EnrollmentCompleted: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
	// learner_gcid is a TOP-LEVEL payload field here (NOT the envelope gcid) —
	// confirm it wire-encodes at field 3 with the supplied value.
	if got := collectWireStrings(t, bz, 3); len(got) != 1 || got[0] != "gcid-learner" {
		t.Errorf("field 3 learner_gcid = %v; want [gcid-learner]", got)
	}
}

// TestMarshalEnrollmentCompleted_OmitsFalsePassed — proto3 omits a false bool
// on the wire; an enrollment completed WITHOUT passing must not emit field 5
// (decodes back to passed=false, the canonical default).
func TestMarshalEnrollmentCompleted_OmitsFalsePassed(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"enrollment_id": "enr-1",
		"learner_gcid":  "gcid-learner",
		"course_id":     "course-1",
		"passed":        false,
		"completed_at":  env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.enrollment.completed.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if seen[5] {
		t.Error("field 5 passed must be omitted when false (proto3 default)")
	}
	// The completion fact is still valid — id/course/completed_at present.
	for _, want := range []protowire.Number{1, 2, 3, 4, 6} {
		if !seen[want] {
			t.Errorf("EnrollmentCompleted(false): missing field %d (seen: %v)", want, seen)
		}
	}
}

// TestMarshalEnrollmentCompleted_RejectsBadString — a non-string enrollment_id
// must fail loud (no silent garbage bytes that the Schema Registry rejects).
func TestMarshalEnrollmentCompleted_RejectsBadString(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"enrollment_id": 12345, // schema demands string
		"course_id":     "c-1",
		"learner_gcid":  "g-1",
	}
	if _, err := protomarshal.MarshalPayload("chora.delivery.enrollment.completed.v1", env, payload); err == nil {
		t.Fatal("expected typed error for enrollment_id=int; got nil")
	}
}

// TestMarshalEnrollmentCompleted_NilPayloadProducesEnvelopeOnly — nil payload
// still yields a wire-valid envelope-only message.
func TestMarshalEnrollmentCompleted_NilPayloadProducesEnvelopeOnly(t *testing.T) {
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload("chora.delivery.enrollment.completed.v1", env, nil)
	if err != nil {
		t.Fatalf("MarshalPayload nil payload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("nil payload produced empty bytes")
	}
	num, _, n := protowire.ConsumeTag(bz)
	if n < 0 || num != 1 {
		t.Errorf("EnrollmentCompleted nil: leading tag = %d (want envelope=1)", num)
	}
}

// -----------------------------------------------------------------------------
// SubmissionGraded.hint_count — additive field 12 within v1 (WS1.c2, ADR-203
// first-attempt-mastery EXP bonus). The encoder must read hint_count from the
// payload map and wire-encode it at field 12 (int32 varint).
// -----------------------------------------------------------------------------

// TestMarshalSubmissionGraded_HintCount asserts hint_count wire-encodes at the
// contract field number 12 when present + non-zero.
func TestMarshalSubmissionGraded_HintCount(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"submission_id": "sub-1",
		"assessment_id": "ass-1",
		"learner_gcid":  "gcid-learner",
		"hint_count":    int32(3),
		"graded_at":     env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.submission.graded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if !seen[12] {
		t.Errorf("SubmissionGraded: missing hint_count field 12 (seen: %v)", seen)
	}
	// Confirm the value round-trips as varint 3 at field 12.
	if got := collectWireVarint(t, bz, 12); len(got) != 1 || got[0] != 3 {
		t.Errorf("field 12 hint_count = %v; want [3]", got)
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalSubmissionGraded_OmitsZeroHintCount — proto3 omits a zero int32 on
// the wire. hint_count=0 ("no hints used" → ADR-203 bonus eligible) decodes
// back to 0 from an absent field, so the encoder MUST omit it.
func TestMarshalSubmissionGraded_OmitsZeroHintCount(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"submission_id": "sub-1",
		"assessment_id": "ass-1",
		"learner_gcid":  "gcid-learner",
		"hint_count":    int32(0),
		"graded_at":     env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.submission.graded.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if seen[12] {
		t.Error("field 12 hint_count must be omitted when zero (proto3 default)")
	}
}

// collectWireVarint returns every varint value carried at top-level field
// `field`. Used to assert a scalar int field (e.g. hint_count) round-trips.
func collectWireVarint(t *testing.T, bz []byte, field protowire.Number) []uint64 {
	t.Helper()
	var out []uint64
	rem := bz
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid tag at offset %d", len(bz)-len(rem))
		}
		rem = rem[n:]
		switch typ {
		case protowire.BytesType:
			_, m := protowire.ConsumeBytes(rem)
			if m < 0 {
				t.Fatalf("invalid bytes for field %d", num)
			}
			rem = rem[m:]
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(rem)
			if m < 0 {
				t.Fatalf("invalid varint for field %d", num)
			}
			if num == field {
				out = append(out, v)
			}
			rem = rem[m:]
		default:
			t.Fatalf("unexpected wire type %d for field %d", typ, num)
		}
	}
	return out
}

// TestMarshalLiveQuizSessionEnded_RoundTripsBinaryProto — ended.v1:
//
//	1 envelope, 2 session_id, 3 live_quiz_id, 4 total_responses, 5 ended_at
func TestMarshalLiveQuizSessionEnded_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"session_id":      "sess-1",
		"live_quiz_id":    "lq-1",
		"total_responses": 42,
		"ended_at":        env.OccurredAt.Format(time.RFC3339Nano),
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.live_quiz_session.ended.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5} {
		if !seen[want] {
			t.Errorf("LiveQuizSessionEnded: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}
