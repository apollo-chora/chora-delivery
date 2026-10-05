// Additional publisher tests for S4.3 emitters: course.published,
// enrollment.cancelled, booking.confirmed, certification.issued.
//
// CHO-2247: the course.updated emitter + its tests were DELETED — the topic has
// never existed (describe -> NOT_FOUND) and the lane emitted 0 events in its
// lifetime, so these tests were green against an event that could never leave
// the process.
//
// All envelopes per .claude/rules/ddd-enforcement.md "Event envelope mandatory
// fields"; topic taxonomy chora.{domain}.{aggregate}.{event_type}.v{N}.
package events_test

import (
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
)

func TestPublisher_PublishCoursePublished(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishCoursePublished(events.CoursePublished{
		TenantID:       tenantA,
		GCID:           gcidA,
		CourseID:       "01970000-0000-7000-c000-000000000001",
		Title:          "CSM Prep",
		InstructorGCID: gcidA,
	})
	if err != nil {
		t.Fatalf("PublishCoursePublished: %v", err)
	}
	if got.Topic != "chora.delivery.course.published.v1" {
		t.Fatalf("topic: got %q", got.Topic)
	}
	if got.Envelope.EventID == "" {
		t.Fatalf("event_id required")
	}
	// Course published is the IMDA D1 + D2 (transparency) signal — chora-sharing
	// subscribes for discovery feed.
	if got.Envelope.IdempotencyKey == "" {
		t.Fatalf("idempotency_key required")
	}
}

func TestPublisher_PublishEnrollmentCancelled(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishEnrollmentCancelled(events.EnrollmentCancelled{
		TenantID:     tenantA,
		GCID:         gcidA,
		EnrollmentID: "01970000-0000-7000-a000-000000000001",
		CourseID:     "01970000-0000-7000-c000-000000000001",
		LearnerGCID:  gcidA,
		Reason:       "user-initiated",
	})
	if err != nil {
		t.Fatalf("PublishEnrollmentCancelled: %v", err)
	}
	if got.Topic != "chora.delivery.enrollment.cancelled.v1" {
		t.Fatalf("topic: got %q", got.Topic)
	}
	// Idempotency key on (course_id, gcid, "cancelled") so a redelivered
	// cancel does not generate a duplicate downstream effect.
	if !strings.Contains(got.Envelope.IdempotencyKey, "cancelled") {
		t.Fatalf("expected cancelled in idempotency_key, got %q", got.Envelope.IdempotencyKey)
	}
}

// TestPublisher_PublishEnrollmentCompleted_EnvelopeShape — WS1.c2 producer for
// chora.delivery.enrollment.completed.v1 (ADR-200 LearnerProfile read-model +
// ADR-203 verified-EXP tier S). Mirrors the EnrollmentCreated envelope-shape
// contract: all mandatory envelope fields + the per-learner completion payload.
func TestPublisher_PublishEnrollmentCompleted_EnvelopeShape(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	traceparent := "00-0123456789abcdef0123456789abcdef-fedcba9876543210-01"
	completedAt := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)

	got, err := pub.PublishEnrollmentCompleted(events.EnrollmentCompleted{
		TenantID:     tenantA,
		GCID:         gcidA,
		EnrollmentID: "01970000-0000-7000-a000-000000000001",
		CourseID:     "01970000-0000-7000-c000-000000000001",
		LearnerGCID:  gcidA,
		Passed:       true,
		CompletedAt:  completedAt,
		Traceparent:  traceparent,
	})
	if err != nil {
		t.Fatalf("PublishEnrollmentCompleted: %v", err)
	}

	if got.Topic != "chora.delivery.enrollment.completed.v1" {
		t.Fatalf("topic: got %q want chora.delivery.enrollment.completed.v1", got.Topic)
	}
	env := got.Envelope
	if env.EventID == "" {
		t.Fatalf("event_id required")
	}
	if env.IdempotencyKey == "" {
		t.Fatalf("idempotency_key required")
	}
	if env.TenantID != tenantA {
		t.Fatalf("tenant_id: got %q want %q", env.TenantID, tenantA)
	}
	if env.GCID != gcidA {
		t.Fatalf("gcid: got %q want %q", env.GCID, gcidA)
	}
	if env.OccurredAt.IsZero() || env.PublishedAt.IsZero() {
		t.Fatalf("occurred_at + published_at required")
	}
	if env.Traceparent != traceparent {
		t.Fatalf("traceparent: got %q want %q", env.Traceparent, traceparent)
	}
	if env.SourceProject != "chora-489812" || env.SourceService != "chora-delivery" {
		t.Fatalf("source project/service: got %q/%q", env.SourceProject, env.SourceService)
	}
	if env.SchemaVersion != 1 {
		t.Fatalf("schema_version: got %d want 1", env.SchemaVersion)
	}

	// Payload carries the per-learner completion fact: enrollment_id,
	// learner_gcid, course_id, passed (bool), completed_at + the canonical
	// IMDA D1 accountability tag (ADR-141, mirrors enrollment.created).
	if got.Payload["enrollment_id"] != "01970000-0000-7000-a000-000000000001" {
		t.Fatalf("payload enrollment_id: got %v", got.Payload["enrollment_id"])
	}
	if got.Payload["learner_gcid"] != gcidA {
		t.Fatalf("payload learner_gcid: got %v", got.Payload["learner_gcid"])
	}
	if got.Payload["course_id"] != "01970000-0000-7000-c000-000000000001" {
		t.Fatalf("payload course_id: got %v", got.Payload["course_id"])
	}
	if passed, ok := got.Payload["passed"].(bool); !ok || !passed {
		t.Fatalf("payload passed: got %v (want true)", got.Payload["passed"])
	}
	if got.Payload["completed_at"] == nil {
		t.Fatalf("payload completed_at required")
	}
	if got.Payload["chora_imda_dimension"] != "accountability" {
		t.Fatalf("payload chora_imda_dimension: got %v want accountability", got.Payload["chora_imda_dimension"])
	}

	// Idempotency key MUST be deterministic on (course_id, learner_gcid) so a
	// redelivered completion of the SAME enrollment dedupes at the bus even
	// when the in-memory enrollment_id differs. Suffixed ":completed" to keep
	// it distinct from the created (no suffix) + cancelled (:cancelled) keys.
	if !strings.Contains(env.IdempotencyKey, "completed") {
		t.Fatalf("expected 'completed' in idempotency_key, got %q", env.IdempotencyKey)
	}
	got2, _ := pub.PublishEnrollmentCompleted(events.EnrollmentCompleted{
		TenantID:     tenantA,
		GCID:         gcidA,
		EnrollmentID: "01970000-0000-7000-a000-000000000099", // different id, same logical fact
		CourseID:     "01970000-0000-7000-c000-000000000001",
		LearnerGCID:  gcidA,
		Passed:       false,
		Traceparent:  traceparent,
	})
	if got2.Envelope.IdempotencyKey != env.IdempotencyKey {
		t.Fatalf("idempotency_key must be deterministic on (course_id, learner_gcid); got %q vs %q",
			got2.Envelope.IdempotencyKey, env.IdempotencyKey)
	}
}

// TestPublisher_PublishEnrollmentCompleted_MintsTraceparentIfEmpty — the
// publisher mints a fresh W3C traceparent when the caller omits one (Cloud
// Trace propagation across Pub/Sub is mandatory).
func TestPublisher_PublishEnrollmentCompleted_MintsTraceparentIfEmpty(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishEnrollmentCompleted(events.EnrollmentCompleted{
		TenantID:     tenantA,
		GCID:         gcidA,
		EnrollmentID: "01970000-0000-7000-a000-000000000001",
		CourseID:     "01970000-0000-7000-c000-000000000001",
		LearnerGCID:  gcidA,
		Passed:       true,
	})
	if err != nil {
		t.Fatalf("PublishEnrollmentCompleted: %v", err)
	}
	if got.Envelope.Traceparent == "" {
		t.Fatalf("publisher must mint traceparent if missing")
	}
	// completed_at defaults to publish time when the caller omits it.
	if got.Payload["completed_at"] == nil {
		t.Fatalf("completed_at must default to publish time when omitted")
	}
}

// TestPublisher_PublishEnrollmentCompleted_RejectsMissingTenant — fail loud
// (cannot RLS-scope nor envelope-stamp without a tenant).
func TestPublisher_PublishEnrollmentCompleted_RejectsMissingTenant(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	if _, err := pub.PublishEnrollmentCompleted(events.EnrollmentCompleted{
		EnrollmentID: "e1",
		CourseID:     "c1",
		LearnerGCID:  gcidA,
	}); err == nil {
		t.Fatalf("expected error for missing tenant_id")
	}
}

func TestPublisher_PublishBookingConfirmed(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishBookingConfirmed(events.BookingConfirmed{
		TenantID:    tenantA,
		GCID:        gcidA,
		BookingID:   "01970000-0000-7000-b000-000000000001",
		CourseID:    "01970000-0000-7000-c000-000000000001",
		ClassID:     "01970000-0000-7000-d000-000000000001",
		LearnerGCID: gcidA,
		SeatNumber:  3,
	})
	if err != nil {
		t.Fatalf("PublishBookingConfirmed: %v", err)
	}
	if got.Topic != "chora.delivery.booking.confirmed.v1" {
		t.Fatalf("topic: got %q", got.Topic)
	}
	if got.Envelope.EventID == "" {
		t.Fatalf("event_id required")
	}
}

// IMDA evidence — enrollment.created carries D1 (accountability) tag.
// Per ADR-141 canonical labels.
func TestPublisher_EnrollmentCreated_CarriesIMDAAccountability(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, _ := pub.PublishEnrollmentCreated(events.EnrollmentCreated{
		TenantID:     tenantA,
		GCID:         gcidA,
		EnrollmentID: "01970000-0000-7000-a000-000000000001",
		CourseID:     "01970000-0000-7000-c000-000000000001",
		LearnerGCID:  gcidA,
	})
	dim, ok := got.Payload["chora_imda_dimension"]
	if !ok {
		t.Fatalf("payload missing chora_imda_dimension")
	}
	if dim != "accountability" {
		t.Fatalf("expected 'accountability' (ADR-141 canonical D1), got %v", dim)
	}
}

// IMDA evidence — course.published carries D1+D2 tags.
func TestPublisher_CoursePublished_CarriesIMDADimensions(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, _ := pub.PublishCoursePublished(events.CoursePublished{
		TenantID: tenantA,
		GCID:     gcidA,
		CourseID: "01970000-0000-7000-c000-000000000001",
		Title:    "x",
	})
	dimsRaw, ok := got.Payload["chora_imda_dimensions"]
	if !ok {
		t.Fatalf("payload missing chora_imda_dimensions")
	}
	dims, ok := dimsRaw.([]string)
	if !ok {
		t.Fatalf("expected []string, got %T", dimsRaw)
	}
	want := map[string]bool{"accountability": true, "transparency": true}
	for _, d := range dims {
		if !want[d] {
			t.Fatalf("unexpected IMDA dimension %q", d)
		}
		delete(want, d)
	}
	if len(want) > 0 {
		t.Fatalf("missing IMDA dimensions: %+v", want)
	}
}

func TestPublisher_PublishCertificationIssued(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishCertificationIssued(events.CertificationIssued{
		TenantID:        tenantA,
		GCID:            gcidA,
		CertificationID: "01970000-0000-7000-e000-000000000001",
		CourseID:        "01970000-0000-7000-c000-000000000001",
		LearnerGCID:     gcidA,
		Hash:            "deadbeef",
	})
	if err != nil {
		t.Fatalf("PublishCertificationIssued: %v", err)
	}
	if got.Topic != "chora.delivery.certification.issued.v1" {
		t.Fatalf("topic: got %q", got.Topic)
	}
	// Cert hash should appear somewhere in idempotency_key so re-issuance
	// dedupes if the same accomplishments are passed.
	if !strings.Contains(got.Envelope.IdempotencyKey, "deadbeef") {
		t.Fatalf("expected hash in idempotency_key, got %q", got.Envelope.IdempotencyKey)
	}
}

// Tests that all extra publishers reject missing tenant.
func TestPublisher_AllExtras_RejectsMissingTenant(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	if _, err := pub.PublishCoursePublished(events.CoursePublished{}); err == nil {
		t.Fatalf("CoursePublished: expected error")
	}
	if _, err := pub.PublishEnrollmentCancelled(events.EnrollmentCancelled{}); err == nil {
		t.Fatalf("EnrollmentCancelled: expected error")
	}
	if _, err := pub.PublishBookingConfirmed(events.BookingConfirmed{}); err == nil {
		t.Fatalf("BookingConfirmed: expected error")
	}
	if _, err := pub.PublishCertificationIssued(events.CertificationIssued{}); err == nil {
		t.Fatalf("CertificationIssued: expected error")
	}
}
