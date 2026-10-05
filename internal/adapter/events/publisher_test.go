// Package events_test exercises the in-memory Pub/Sub publisher adapter.
//
// Per .claude/rules/ddd-enforcement.md "Event envelope mandatory fields":
// every emitted event MUST carry event_id (UUIDv7), idempotency_key,
// tenant_id, gcid, occurred_at, published_at, traceparent, tracestate,
// source_project, source_service, schema_version.
//
// The MVP uses an in-memory publisher; M12+ swaps to a Cloud Pub/Sub adapter.
package events_test

import (
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
)

const (
	tenantA = "01970000-0000-7000-8000-000000000001"
	gcidA   = "01970000-0000-7000-9000-000000000001"
)

func TestPublisher_PublishEnrollmentCreated_EnvelopeShape(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	traceparent := "00-0123456789abcdef0123456789abcdef-fedcba9876543210-01"

	got, err := pub.PublishEnrollmentCreated(events.EnrollmentCreated{
		TenantID:     tenantA,
		GCID:         gcidA,
		EnrollmentID: "01970000-0000-7000-a000-000000000001",
		CourseID:     "01970000-0000-7000-c000-000000000001",
		LearnerGCID:  gcidA,
		Traceparent:  traceparent,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Topic taxonomy: chora.{domain}.{aggregate}.{event_type}.v{N}
	wantTopic := "chora.delivery.enrollment.created.v1"
	if got.Topic != wantTopic {
		t.Fatalf("topic: got %q want %q", got.Topic, wantTopic)
	}

	// Mandatory envelope fields per ddd-enforcement.md.
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
	if env.OccurredAt.IsZero() {
		t.Fatalf("occurred_at required")
	}
	if env.PublishedAt.IsZero() {
		t.Fatalf("published_at required")
	}
	if env.Traceparent != traceparent {
		t.Fatalf("traceparent: got %q want %q", env.Traceparent, traceparent)
	}
	if env.SourceProject != "chora-489812" {
		t.Fatalf("source_project: got %q", env.SourceProject)
	}
	if env.SourceService != "chora-delivery" {
		t.Fatalf("source_service: got %q", env.SourceService)
	}
	if env.SchemaVersion != 1 {
		t.Fatalf("schema_version: got %d want 1", env.SchemaVersion)
	}

	// Idempotency key MUST be deterministic on (course_id, gcid) so
	// duplicate enrollments dedupe at the bus.
	got2, _ := pub.PublishEnrollmentCreated(events.EnrollmentCreated{
		TenantID:     tenantA,
		GCID:         gcidA,
		EnrollmentID: "01970000-0000-7000-a000-000000000099", // different ID, same logical fact
		CourseID:     "01970000-0000-7000-c000-000000000001",
		LearnerGCID:  gcidA,
		Traceparent:  traceparent,
	})
	if got2.Envelope.IdempotencyKey != env.IdempotencyKey {
		t.Fatalf("idempotency_key must be deterministic on (course_id, gcid); got %q vs %q",
			got2.Envelope.IdempotencyKey, env.IdempotencyKey)
	}
}

func TestPublisher_PublishEnrollmentCreated_MintsTraceparentIfEmpty(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishEnrollmentCreated(events.EnrollmentCreated{
		TenantID:     tenantA,
		GCID:         gcidA,
		EnrollmentID: "01970000-0000-7000-a000-000000000001",
		CourseID:     "01970000-0000-7000-c000-000000000001",
		LearnerGCID:  gcidA,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got.Envelope.Traceparent == "" {
		t.Fatalf("publisher must mint traceparent if missing")
	}
}

func TestPublisher_PublishCourseCreated_EnvelopeShape(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	got, err := pub.PublishCourseCreated(events.CourseCreated{
		TenantID:       tenantA,
		GCID:           gcidA,
		CourseID:       "01970000-0000-7000-c000-000000000001",
		Title:          "CSPO Fundamentals",
		InstructorGCID: gcidA,
		Public:         true,
		PriceSGDCents:  20000,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got.Topic != "chora.delivery.course.created.v1" {
		t.Fatalf("topic: got %q", got.Topic)
	}
	if got.Envelope.EventID == "" {
		t.Fatalf("event_id required")
	}
	if got.Envelope.IdempotencyKey != "01970000-0000-7000-c000-000000000001" {
		t.Fatalf("course-created idempotency_key should equal course_id; got %q", got.Envelope.IdempotencyKey)
	}
}

func TestPublisher_RecordsHistory(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	_, _ = pub.PublishCourseCreated(events.CourseCreated{
		TenantID:       tenantA,
		GCID:           gcidA,
		CourseID:       "c1",
		Title:          "x",
		InstructorGCID: gcidA,
	})
	_, _ = pub.PublishEnrollmentCreated(events.EnrollmentCreated{
		TenantID:     tenantA,
		GCID:         gcidA,
		EnrollmentID: "e1",
		CourseID:     "c1",
		LearnerGCID:  gcidA,
	})
	hist := pub.History()
	if len(hist) != 2 {
		t.Fatalf("expected 2 events in history, got %d", len(hist))
	}
}

func TestPublisher_RejectsMissingTenant(t *testing.T) {
	t.Parallel()
	pub := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	_, err := pub.PublishEnrollmentCreated(events.EnrollmentCreated{
		EnrollmentID: "e1",
		CourseID:     "c1",
		LearnerGCID:  gcidA,
	})
	if err == nil {
		t.Fatalf("expected error for missing tenant_id")
	}
}
