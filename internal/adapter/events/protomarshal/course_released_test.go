// course_released_test.go — wire-format test for the CJ#2 course
// `chora.delivery.course.released.v1` encoder.
//
// Schema: chora-contracts/proto/events-flat/delivery/course/released.proto
//
//	1 envelope, 2 course_id, 3 title, 4 author_gcid,
//	5 price_sgd_cents (int64 varint), 6 sf_eligible (bool varint),
//	7 scheduled_open_at (Timestamp), 8 repeated string instructor_gcids,
//	9 repeated string test_set_ids, 10 released_at (Timestamp)
package protomarshal_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
)

// TestMarshalCourseReleased_RoundTripsBinaryProto asserts the encoder for
// chora.delivery.course.released.v1 emits well-formed wire bytes.
func TestMarshalCourseReleased_RoundTripsBinaryProto(t *testing.T) {
	env := fixedEnvelope()
	releasedAt := env.OccurredAt
	payload := map[string]any{
		"course_id":         "019e2f93-d586-71b5-8c3d-e2b0d0d50100",
		"title":             "Algorithmic Thinking 101",
		"author_gcid":       "019e2f93-d586-71b5-8c3d-e2b0d0d50101",
		"price_sgd_cents":   int64(99900),
		"sf_eligible":       true,
		"scheduled_open_at": releasedAt.Add(24 * time.Hour),
		"instructor_gcids": []string{
			"019e2f93-d586-71b5-8c3d-e2b0d0d50101",
			"019e2f93-d586-71b5-8c3d-e2b0d0d50102",
		},
		"test_set_ids": []string{
			"019e2f93-d586-71b5-8c3d-e2b0d0d50208",
		},
		"released_at": releasedAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.released.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty bytes")
	}
	seen := walkWireTags(t, bz)
	for _, want := range []protowire.Number{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		if !seen[want] {
			t.Errorf("CourseReleased: missing field %d (seen: %v)", want, seen)
		}
	}
	requireEnvelopeFields(t, envelopeBytes(t, bz))
}

// TestMarshalCourseReleased_ZeroPriceOmitted asserts that proto3-default
// zero price_sgd_cents is correctly omitted from the wire bytes.
func TestMarshalCourseReleased_ZeroPriceOmitted(t *testing.T) {
	env := fixedEnvelope()
	payload := map[string]any{
		"course_id":        "course-free",
		"title":            "Free Course",
		"author_gcid":      "gcid-1",
		"price_sgd_cents":  int64(0), // proto3 default — should be omitted
		"sf_eligible":      false,    // proto3 default — should be omitted
		"instructor_gcids": []string{"gcid-1"},
		"test_set_ids":     []string{"ts-1"},
		"released_at":      env.OccurredAt,
	}
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.released.v1", env, payload)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if seen[5] {
		t.Errorf("CourseReleased: expected price_sgd_cents (field 5) omitted when zero")
	}
	if seen[6] {
		t.Errorf("CourseReleased: expected sf_eligible (field 6) omitted when false")
	}
	for _, want := range []protowire.Number{1, 2, 3, 4, 8, 9, 10} {
		if !seen[want] {
			t.Errorf("CourseReleased: missing required field %d (seen: %v)", want, seen)
		}
	}
}

// TestMarshalCourseReleased_NoPayload yields envelope-only.
func TestMarshalCourseReleased_NoPayload(t *testing.T) {
	env := fixedEnvelope()
	bz, err := protomarshal.MarshalPayload("chora.delivery.course.released.v1", env, nil)
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	seen := walkWireTags(t, bz)
	if !seen[1] {
		t.Errorf("envelope-only: missing envelope field 1")
	}
	for _, fwd := range []protowire.Number{2, 3, 4, 5} {
		if seen[fwd] {
			t.Errorf("envelope-only: unexpected field %d present", fwd)
		}
	}
}
