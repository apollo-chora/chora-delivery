// course_encoders.go — binary-protobuf encoder for the CJ#2
// `chora.delivery.course.released.v1` topic per
// chora-contracts/proto/events-flat/delivery/course/released.proto.
//
// Schema field map:
//
//	1  Envelope envelope
//	2  string   course_id
//	3  string   title
//	4  string   author_gcid
//	5  int64    price_sgd_cents
//	6  bool     sf_eligible
//	7  Timestamp scheduled_open_at
//	8  repeated string instructor_gcids
//	9  repeated string test_set_ids
//	10 Timestamp released_at
//
// Producer: services/chora-delivery/internal/adapter/http/course_cj2_handler.go
// emits this on /release at AWAITING_REVIEW → PUBLISHED state-flip.
//
// Per CJ#2 directive row at `docs/m13/e2e-fe-coord-directive-2026-05-16.md` §3.
package protomarshal

import "fmt"

// encodeCourseReleased emits the canonical binary wire bytes for
// chora.delivery.course.released.v1 (Schema Registry validation passes).
func encodeCourseReleased(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 512)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "course_id", 2)
	out = encodeStringField(out, payload, "title", 3)
	out = encodeStringField(out, payload, "author_gcid", 4)

	// 5 — int64 price_sgd_cents (proto3 int64 = varint). Encode when non-zero
	// per proto3 default-omit semantics. The Schema Registry int64 wire type
	// is varint (not fixed64 / zigzag) per .proto definition.
	if v, ok := asInt64(payload["price_sgd_cents"]); ok && v != 0 {
		out = appendVarint(out, 5, uint64(v))
	}

	// 6 — bool sf_eligible (varint 0/1; omit when false per proto3 default).
	if v, ok := payload["sf_eligible"].(bool); ok && v {
		out = appendVarint(out, 6, 1)
	}

	// 7 — Timestamp scheduled_open_at (optional; omit when zero/nil).
	if err := encodeTimestampInto(&out, payload, "scheduled_open_at", 7); err != nil {
		return nil, fmt.Errorf("scheduled_open_at: %w", err)
	}

	// 8 — repeated string instructor_gcids (one length-delimited entry per item).
	for _, s := range asStringSlice(payload["instructor_gcids"]) {
		if s == "" {
			continue
		}
		out = appendString(out, 8, s)
	}

	// 9 — repeated string test_set_ids.
	for _, s := range asStringSlice(payload["test_set_ids"]) {
		if s == "" {
			continue
		}
		out = appendString(out, 9, s)
	}

	// 10 — Timestamp released_at.
	if err := encodeTimestampInto(&out, payload, "released_at", 10); err != nil {
		return nil, fmt.Errorf("released_at: %w", err)
	}

	return out, nil
}

// encodeCoursePublished emits the canonical binary wire bytes for
// chora.delivery.course.published.v1 — fired when a Course's visibility flips
// into `public`. Schema at events-flat/delivery/course/published.proto.
//
//	1  Envelope        envelope
//	2  string          course_id
//	3  string          title
//	4  string          instructor_gcid
//	5  Timestamp       published_at
//	6  repeated string imda_dimensions (producer payload key: chora_imda_dimensions)
//
// Producer: services/chora-delivery/internal/adapter/events/publisher.go
// InMemoryPublisher.PublishCoursePublished. Contract landed by the 2026-07-01
// event-fabric audit (Class D — the topic was previously unprovisioned so the
// publish silently JSON-fell-back + NOT_FOUND-dead-lettered).
func encodeCoursePublished(env Envelope, payload map[string]any) ([]byte, error) {
	out := make([]byte, 0, 256)
	envBz, err := encodeEnvelope(env, payload)
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	out = appendLengthDelimited(out, 1, envBz)
	if payload == nil {
		return out, nil
	}
	out = encodeStringField(out, payload, "course_id", 2)
	out = encodeStringField(out, payload, "title", 3)
	out = encodeStringField(out, payload, "instructor_gcid", 4)

	// 5 — Timestamp published_at.
	if err := encodeTimestampInto(&out, payload, "published_at", 5); err != nil {
		return nil, fmt.Errorf("published_at: %w", err)
	}

	// 6 — repeated string imda_dimensions. The producer payload key is the
	// plural `chora_imda_dimensions` (the singular envelope field 14 carries at
	// most one value, so this event's D1+D2 pair rides on the message).
	for _, s := range asStringSlice(payload["chora_imda_dimensions"]) {
		if s == "" {
			continue
		}
		out = appendString(out, 6, s)
	}

	return out, nil
}

// asStringSlice coerces a map[string]any value into []string.
//
// Accepts:
//   - []string                — direct
//   - []any                   — element-wise asserted to string
//
// Returns nil for any other shape (including missing/nil).
func asStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// asInt64 is defined in protomarshal.go (shared helper).
