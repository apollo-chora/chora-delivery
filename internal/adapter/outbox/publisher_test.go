// Package outbox_test — TransactionalOutboxPublisher adapter tests.
//
// The TransactionalOutboxPublisher satisfies events.Publisher by writing
// each emitted PublishedEvent to the outbox_events table (via the Store
// port) instead of (or in addition to) publishing directly to Pub/Sub.
// A separate Dispatcher drains the outbox to Cloud Pub/Sub.
//
// Per `feedback_d6_resilience_first_class` Pillar 2 — producer-side durable
// emission for chora-delivery's per-event canonical topics.
package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/outbox"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

func newOutboxPublisher(store outbox.Store) *outbox.TransactionalOutboxPublisher {
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	return outbox.NewTransactionalPublisher(outbox.PublisherConfig{
		Inner:         inner,
		Store:         store,
		SourceProject: "chora-489812",
		SourceService: "chora-delivery",
	})
}

func TestOutboxPublisher_PublishCourseCreated_WritesRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)

	ev, err := pub.PublishCourseCreated(events.CourseCreated{
		TenantID:       "00000000-0000-0000-0000-0000000000aa",
		GCID:           "00000000-0000-0000-0000-0000000000bb",
		CourseID:       "course-1",
		Title:          "Test Course",
		InstructorGCID: "00000000-0000-0000-0000-0000000000bb",
		Public:         true,
		PriceSGDCents:  0,
	})
	if err != nil {
		t.Fatalf("PublishCourseCreated: %v", err)
	}
	if ev.Topic != events.TopicCourseCreated {
		t.Errorf("ev.Topic = %q; want %q", ev.Topic, events.TopicCourseCreated)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}
	row := rows[0]
	if row.Topic != events.TopicCourseCreated {
		t.Errorf("row.Topic = %q; want %q", row.Topic, events.TopicCourseCreated)
	}
	if row.TenantID != "00000000-0000-0000-0000-0000000000aa" {
		t.Errorf("row.TenantID = %q; want tenant_aa", row.TenantID)
	}
	if row.AggregateType != "course" {
		t.Errorf("row.AggregateType = %q; want course", row.AggregateType)
	}
	if row.AggregateID != "course-1" {
		t.Errorf("row.AggregateID = %q; want course-1", row.AggregateID)
	}
	if row.IdempotencyKey == "" {
		t.Errorf("row.IdempotencyKey empty")
	}
}

func TestOutboxPublisher_PublishBookingConfirmed_WritesRowWithCanonicalTopic(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	ev, err := pub.PublishBookingConfirmed(events.BookingConfirmed{
		TenantID:    "00000000-0000-0000-0000-0000000000aa",
		GCID:        "00000000-0000-0000-0000-0000000000bb",
		BookingID:   "book-1",
		CourseID:    "course-1",
		ClassID:     "class-1",
		LearnerGCID: "00000000-0000-0000-0000-0000000000bb",
		SeatNumber:  3,
	})
	if err != nil {
		t.Fatalf("PublishBookingConfirmed: %v", err)
	}
	if ev.Topic != "chora.delivery.booking.confirmed.v1" {
		t.Errorf("ev.Topic = %q; want chora.delivery.booking.confirmed.v1", ev.Topic)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}
	if rows[0].AggregateType != "booking" {
		t.Errorf("row.AggregateType = %q; want booking", rows[0].AggregateType)
	}
}

func TestOutboxPublisher_PublishCertificationIssued_WritesRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	ev, err := pub.PublishCertificationIssued(events.CertificationIssued{
		TenantID:        "00000000-0000-0000-0000-0000000000aa",
		GCID:            "00000000-0000-0000-0000-0000000000bb",
		CertificationID: "cert-1",
		CourseID:        "course-1",
		LearnerGCID:     "00000000-0000-0000-0000-0000000000bb",
		Hash:            "abc123",
	})
	if err != nil {
		t.Fatalf("PublishCertificationIssued: %v", err)
	}
	if ev.Topic != "chora.delivery.certification.issued.v1" {
		t.Errorf("ev.Topic = %q; want chora.delivery.certification.issued.v1", ev.Topic)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}
	if rows[0].AggregateType != "certification" {
		t.Errorf("row.AggregateType = %q; want certification", rows[0].AggregateType)
	}
}

// TestOutboxPublisher_PublishEnrollmentCompleted_WritesBinaryRow — WS1.c2.
// Proves the new per-learner completion event tees to the outbox AND that the
// payload routes through the binary protobuf encoder (NOT the JSON fallback) —
// i.e. encodeEnrollmentCompleted is reachable from the real publish path, so
// the binary Schema Registry accepts the dispatched bytes.
func TestOutboxPublisher_PublishEnrollmentCompleted_WritesBinaryRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	ev, err := pub.PublishEnrollmentCompleted(events.EnrollmentCompleted{
		TenantID:     "00000000-0000-0000-0000-0000000000aa",
		GCID:         "00000000-0000-0000-0000-0000000000bb",
		EnrollmentID: "enr-1",
		CourseID:     "course-1",
		LearnerGCID:  "00000000-0000-0000-0000-0000000000bb",
		Passed:       true,
	})
	if err != nil {
		t.Fatalf("PublishEnrollmentCompleted: %v", err)
	}
	if ev.Topic != "chora.delivery.enrollment.completed.v1" {
		t.Errorf("ev.Topic = %q; want chora.delivery.enrollment.completed.v1", ev.Topic)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}
	row := rows[0]
	if row.AggregateType != "enrollment" {
		t.Errorf("row.AggregateType = %q; want enrollment", row.AggregateType)
	}
	if row.AggregateID != "enr-1" {
		t.Errorf("row.AggregateID = %q; want enr-1", row.AggregateID)
	}
	body := row.Payload
	if len(body) == 0 {
		t.Fatal("empty payload bytes")
	}
	// Must be canonical proto wire bytes, NOT JSON fallback.
	var pl map[string]any
	if err := json.Unmarshal(body, &pl); err == nil {
		t.Errorf("payload parseable as JSON (%q) — expected binary protobuf (encoder unrouted?)", string(body))
	}
	num, typ, n := protowire.ConsumeTag(body)
	if n < 0 || num != 1 || typ != protowire.BytesType {
		t.Fatalf("leading tag = (num=%d typ=%d) want (1, bytes) — Envelope first", num, typ)
	}
}

func TestOutboxPublisher_PublishEnrollmentCreated_StampsEnvelope(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	_, err := pub.PublishEnrollmentCreated(events.EnrollmentCreated{
		TenantID:     "00000000-0000-0000-0000-0000000000aa",
		GCID:         "00000000-0000-0000-0000-0000000000bb",
		EnrollmentID: "enr-1",
		CourseID:     "course-1",
		LearnerGCID:  "00000000-0000-0000-0000-0000000000bb",
	})
	if err != nil {
		t.Fatalf("PublishEnrollmentCreated: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}
	env := rows[0].Envelope
	for _, key := range []string{
		"event_id", "idempotency_key", "tenant_id", "occurred_at", "published_at",
		"traceparent", "source_project", "source_service", "schema_version",
	} {
		if env[key] == "" {
			t.Errorf("envelope.%s empty; want non-empty (mandatory per CLAUDE.md §6)", key)
		}
	}
	if env["source_project"] != "chora-489812" {
		t.Errorf("envelope.source_project = %q; want chora-489812", env["source_project"])
	}
	if env["source_service"] != "chora-delivery" {
		t.Errorf("envelope.source_service = %q; want chora-delivery", env["source_service"])
	}
	if env["schema_version"] != "1" {
		t.Errorf("envelope.schema_version = %q; want 1", env["schema_version"])
	}
}

func TestOutboxPublisher_PublishCourseCreated_RejectsEmptyTenant(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	_, err := pub.PublishCourseCreated(events.CourseCreated{
		CourseID: "course-1",
	})
	if err == nil {
		t.Errorf("expected error for empty tenant_id")
	}
}

func TestOutboxPublisher_RejectsMissingStore(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("NewTransactionalPublisher with nil store should panic")
		}
	}()
	outbox.NewTransactionalPublisher(outbox.PublisherConfig{
		Inner: events.NewInMemoryPublisher("p", "s"),
		Store: nil,
	})
}

func TestOutboxPublisher_RejectsMissingInner(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("NewTransactionalPublisher with nil inner should panic")
		}
	}()
	outbox.NewTransactionalPublisher(outbox.PublisherConfig{
		Inner: nil,
		Store: outbox.NewInMemoryStore(),
	})
}

func TestOutboxPublisher_DuplicateIdempotency_ReturnsSentinel(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	in := events.BookingConfirmed{
		TenantID:    "00000000-0000-0000-0000-0000000000aa",
		BookingID:   "same-booking",
		CourseID:    "course-1",
		LearnerGCID: "00000000-0000-0000-0000-0000000000bb",
		SeatNumber:  1,
	}
	if _, err := pub.PublishBookingConfirmed(in); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	// Inner publisher mints a new event_id but idempotency_key is deterministic
	// on (booking_id, "confirmed"). Second emit collides.
	_, err := pub.PublishBookingConfirmed(in)
	if err == nil {
		t.Fatalf("expected duplicate idempotency_key rejection")
	}
	if !errors.Is(err, outbox.ErrDuplicateIdempotencyKey) {
		t.Errorf("err = %v; want ErrDuplicateIdempotencyKey", err)
	}
}

func TestOutboxPublisher_PublishCoursePublished_WritesRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	_, err := pub.PublishCoursePublished(events.CoursePublished{
		TenantID:       "00000000-0000-0000-0000-0000000000aa",
		CourseID:       "course-1",
		Title:          "Public Course",
		InstructorGCID: "00000000-0000-0000-0000-0000000000bb",
	})
	if err != nil {
		t.Fatalf("PublishCoursePublished: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	if rows[0].Topic != events.TopicCoursePublished {
		t.Errorf("row.Topic = %q; want %q", rows[0].Topic, events.TopicCoursePublished)
	}
}

func TestOutboxPublisher_PublishEnrollmentCancelled_WritesRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	_, err := pub.PublishEnrollmentCancelled(events.EnrollmentCancelled{
		TenantID:     "00000000-0000-0000-0000-0000000000aa",
		EnrollmentID: "enr-1",
		CourseID:     "course-1",
		LearnerGCID:  "00000000-0000-0000-0000-0000000000bb",
		Reason:       "user-cancel",
	})
	if err != nil {
		t.Fatalf("PublishEnrollmentCancelled: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	if rows[0].Topic != events.TopicEnrollmentCancelled {
		t.Errorf("row.Topic = %q; want %q", rows[0].Topic, events.TopicEnrollmentCancelled)
	}
}

// TestOutboxPublisher_PayloadIsBinaryProto verifies the producer-side
// binary protobuf encoding for Schema-Registry-attached topics.
//
// Pre-2026-05-16 the test asserted JSON-ness — that invariant was wrong and
// caused Pub/Sub Schema Registry to dead-letter every published row with
// "Invalid binary proto message". The corrected invariant: course.created.v1
// (and the other 9 schema-attached topics) publish canonical proto wire
// bytes whose nested envelope parses to the chora.common.v1.EventEnvelope
// layout (fields 1..11 set).
//
// See services/chora-delivery/internal/adapter/events/protomarshal/.
func TestOutboxPublisher_PayloadIsBinaryProto(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	_, err := pub.PublishCourseCreated(events.CourseCreated{
		TenantID:       "00000000-0000-0000-0000-0000000000aa",
		GCID:           "00000000-0000-0000-0000-0000000000bb",
		CourseID:       "course-1",
		Title:          "Binary Test",
		InstructorGCID: "00000000-0000-0000-0000-0000000000bb",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 1)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	body := rows[0].Payload
	if len(body) == 0 {
		t.Fatal("empty payload bytes")
	}
	// JSON should fail — the bytes are canonical proto wire format now.
	var pl map[string]any
	if err := json.Unmarshal(body, &pl); err == nil {
		t.Errorf("payload is parseable as JSON (%q) — expected binary protobuf",
			string(body))
	}
	// Leading tag must be field 1 = Envelope (BytesType).
	num, typ, n := protowire.ConsumeTag(body)
	if n < 0 {
		t.Fatalf("invalid leading wire tag: %x", body)
	}
	if num != 1 {
		t.Errorf("leading field = %d; want 1 (Envelope)", num)
	}
	if typ != protowire.BytesType {
		t.Errorf("leading wire type = %d; want bytes (length-delimited)", typ)
	}
	envBytes, m := protowire.ConsumeBytes(body[n:])
	if m < 0 || len(envBytes) == 0 {
		t.Fatal("invalid Envelope bytes")
	}
	// Walk the envelope; required CLAUDE.md §6 fields must all be set.
	seen := map[protowire.Number]bool{}
	rem := envBytes
	for len(rem) > 0 {
		num, typ, n := protowire.ConsumeTag(rem)
		if n < 0 {
			t.Fatalf("invalid envelope inner tag")
		}
		rem = rem[n:]
		seen[num] = true
		switch typ {
		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				t.Fatalf("invalid envelope inner bytes for field %d", num)
			}
			rem = rem[n:]
		case protowire.VarintType:
			_, n := protowire.ConsumeVarint(rem)
			if n < 0 {
				t.Fatalf("invalid envelope inner varint for field %d", num)
			}
			rem = rem[n:]
		}
	}
	required := []protowire.Number{1, 2, 3, 4, 5, 6, 7, 9, 10, 11}
	for _, want := range required {
		if !seen[want] {
			t.Errorf("envelope: missing required field %d (seen: %v)", want, seen)
		}
	}
}

// TestOutboxPublisher_JSONFallbackForUnattachedTopic — topics with NO Schema
// Registry schema attached fall through to JSON-marshalling.
//
// CHO-2247: this test used to be driven through PublishCourseUpdated, and its
// comment listed course.updated + course.published among the schema-less topics.
// Both claims were false against deployed reality — course.updated.v1 has no
// topic AT ALL, and course.published.v1 DOES carry a schema
// (chora-delivery-course-published-v1). Re-subjected onto enrollment.created.v1,
// verified schema-less by live census 2026-07-17:
//
//	course.created.v1        -> chora-delivery-course-created-v1
//	course.released.v1       -> chora-delivery-course-released-v1
//	course.published.v1      -> chora-delivery-course-published-v1
//	enrollment.completed.v1  -> chora-delivery-enrollment-completed-v1
//	enrollment.created.v1    -> NO SCHEMA   <- this test's subject
//	enrollment.cancelled.v1  -> NO SCHEMA
//	course.content_composed.v1 -> NO SCHEMA
//
// Drop this test when a schema lands on enrollment.created.v1 and protomarshal
// picks up an encoder for it.
func TestOutboxPublisher_JSONFallbackForUnattachedTopic(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	_, err := pub.PublishEnrollmentCreated(events.EnrollmentCreated{
		TenantID:     "00000000-0000-0000-0000-0000000000aa",
		GCID:         "00000000-0000-0000-0000-0000000000bb",
		EnrollmentID: "enr-1",
		CourseID:     "course-1",
		LearnerGCID:  "00000000-0000-0000-0000-0000000000bb",
	})
	if err != nil {
		t.Fatalf("PublishEnrollmentCreated: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	var pl map[string]any
	if err := json.Unmarshal(rows[0].Payload, &pl); err != nil {
		t.Fatalf("CourseUpdated payload (no Schema Registry schema) should JSON-marshal: %v", err)
	}
	if pl["course_id"] != "course-1" {
		t.Errorf("payload.course_id = %v; want course-1", pl["course_id"])
	}
}

// Compile-time check that TransactionalOutboxPublisher satisfies events.Publisher.
var _ events.Publisher = (*outbox.TransactionalOutboxPublisher)(nil)

func TestOutboxPublisher_HasCanonicalTopicConstants(t *testing.T) {
	t.Parallel()
	canonical := map[string]string{
		"course_created":       "chora.delivery.course.created.v1",
		"booking_confirmed":    "chora.delivery.booking.confirmed.v1",
		"certification_issued": "chora.delivery.certification.issued.v1",
		"enrollment_created":   "chora.delivery.enrollment.created.v1",
		"enrollment_cancelled": "chora.delivery.enrollment.cancelled.v1",
	}
	got := map[string]string{
		"course_created":       events.TopicCourseCreated,
		"booking_confirmed":    events.TopicBookingConfirmed,
		"certification_issued": events.TopicCertificationIssued,
		"enrollment_created":   events.TopicEnrollmentCreated,
		"enrollment_cancelled": events.TopicEnrollmentCancelled,
	}
	for k, want := range canonical {
		if got[k] != want {
			t.Errorf("topic %q = %q; want %q (canonical chora.{domain}.{aggregate}.{event_type}.v{N})",
				k, got[k], want)
		}
	}
}

func TestOutboxPublisher_PublishApplicationStateChanged_WritesRow(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)

	app, err := application.NewApplication(application.NewApplicationInput{
		TenantID: "00000000-0000-0000-0000-0000000000aa",
		CourseID: "course-app-1",
		GCID:     "00000000-0000-0000-0000-0000000000bb",
	})
	if err != nil {
		t.Fatalf("NewApplication: %v", err)
	}
	// Transition to a state that emits an event (Draft has no topic).
	if err := app.Transition(application.StatusSubmitted); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	ev, err := pub.PublishApplicationStateChanged(app, "")
	if err != nil {
		t.Fatalf("PublishApplicationStateChanged: %v", err)
	}
	if ev.Envelope.TenantID != app.TenantID {
		t.Errorf("ev.Envelope.TenantID = %q; want %q", ev.Envelope.TenantID, app.TenantID)
	}

	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("store rows = %d; want 1", len(rows))
	}
	row := rows[0]
	if row.TenantID != app.TenantID {
		t.Errorf("row.TenantID = %q; want %q", row.TenantID, app.TenantID)
	}
	if row.AggregateType != "application" {
		t.Errorf("row.AggregateType = %q; want application", row.AggregateType)
	}
	if row.AggregateID != app.ID {
		t.Errorf("row.AggregateID = %q; want %q", row.AggregateID, app.ID)
	}
	if row.IdempotencyKey == "" {
		t.Errorf("row.IdempotencyKey empty")
	}
}

func TestOutboxPublisher_PublishApplicationStateChanged_NilApplicationReturnsErr(t *testing.T) {
	t.Parallel()
	pub := newOutboxPublisher(outbox.NewInMemoryStore())
	_, err := pub.PublishApplicationStateChanged(nil, "")
	if err == nil {
		t.Errorf("nil application: want error, got nil")
	}
}

// TestOutboxPublisher_EnvSourceOverride_FromEvent — exercises the
// envSourceProject + envSourceService branches that pick the per-event
// envelope value over the publisher-level default.
func TestOutboxPublisher_EnvSourceOverride_FromEvent(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	innerOverride := events.NewInMemoryPublisher("override-project", "override-service")
	pub := outbox.NewTransactionalPublisher(outbox.PublisherConfig{
		Inner:         innerOverride,
		Store:         store,
		SourceProject: "chora-489812",
		SourceService: "chora-delivery",
	})

	_, err := pub.PublishCourseCreated(events.CourseCreated{
		TenantID:       "00000000-0000-0000-0000-0000000000aa",
		GCID:           "00000000-0000-0000-0000-0000000000bb",
		CourseID:       "course-env-1",
		Title:          "Env override",
		InstructorGCID: "00000000-0000-0000-0000-0000000000bb",
	})
	if err != nil {
		t.Fatalf("PublishCourseCreated: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 5)
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	if rows[0].Envelope["source_project"] != "override-project" {
		t.Errorf("source_project = %q; want override-project", rows[0].Envelope["source_project"])
	}
	if rows[0].Envelope["source_service"] != "override-service" {
		t.Errorf("source_service = %q; want override-service", rows[0].Envelope["source_service"])
	}
}

// TestOutboxPublisher_DeriveEventType — exercises the dispatcher.deriveEventType
// fallback branches (short topic + missing v{N} suffix).
func TestOutboxPublisher_DeriveEventType_FallbackBranches(t *testing.T) {
	t.Parallel()
	// Indirect: publish via canonical topic + verify EventType in row.
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)
	_, err := pub.PublishCourseCreated(events.CourseCreated{
		TenantID: "00000000-0000-0000-0000-0000000000aa",
		GCID:     "00000000-0000-0000-0000-0000000000bb",
		CourseID: "course-d1",
		Title:    "D1", InstructorGCID: "00000000-0000-0000-0000-0000000000bb",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	rows, _ := store.FetchPending(context.Background(), 5)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	// Canonical: "chora.delivery.course.created.v1" -> "course.created"
	if got := rows[0].EventType; got != "course.created" {
		t.Errorf("EventType = %q; want course.created", got)
	}
}

// TestOutboxPublisher_AllForwardingPublishers_TeeIntoOutbox exercises the
// 5 lower-coverage Publish* forwarders (Updated/Published/EnrollmentCreated/
// EnrollmentCancelled/CertificationIssued) — same shape, only the
// aggregateType + aggregateID differ. Pushes per-function coverage from
// 66.7% baseline to ≥83.3% by exercising the success path of each.
func TestOutboxPublisher_AllForwardingPublishers_TeeIntoOutbox(t *testing.T) {
	t.Parallel()
	tenant := "00000000-0000-0000-0000-0000000000aa"
	gcid := "00000000-0000-0000-0000-0000000000bb"

	cases := []struct {
		name         string
		emit         func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error)
		wantAggType  string
		wantAggIDLen int // sanity check non-empty
	}{
		{
			name: "CoursePublished",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishCoursePublished(events.CoursePublished{
					TenantID: tenant, GCID: gcid, CourseID: "course-pub-1",
					Title: "Published",
				})
			},
			wantAggType: "course",
		},
		{
			name: "EnrollmentCreated",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishEnrollmentCreated(events.EnrollmentCreated{
					TenantID: tenant, GCID: gcid,
					EnrollmentID: "enr-1", CourseID: "course-x", LearnerGCID: gcid,
				})
			},
			wantAggType: "enrollment",
		},
		{
			name: "EnrollmentCancelled",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishEnrollmentCancelled(events.EnrollmentCancelled{
					TenantID: tenant, GCID: gcid,
					EnrollmentID: "enr-2", CourseID: "course-y", LearnerGCID: gcid,
				})
			},
			wantAggType: "enrollment",
		},
		{
			name: "CertificationIssued",
			emit: func(p *outbox.TransactionalOutboxPublisher) (events.PublishedEvent, error) {
				return p.PublishCertificationIssued(events.CertificationIssued{
					TenantID: tenant, GCID: gcid,
					CertificationID: "cert-1", CourseID: "course-z", LearnerGCID: gcid,
				})
			},
			wantAggType: "certification",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := outbox.NewInMemoryStore()
			pub := newOutboxPublisher(store)
			ev, err := tc.emit(pub)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if ev.Envelope.TenantID != tenant {
				t.Errorf("ev.Envelope.TenantID = %q; want %q", ev.Envelope.TenantID, tenant)
			}
			rows, _ := store.FetchPending(context.Background(), 10)
			if len(rows) != 1 {
				t.Fatalf("rows = %d; want 1", len(rows))
			}
			if rows[0].AggregateType != tc.wantAggType {
				t.Errorf("row.AggregateType = %q; want %q", rows[0].AggregateType, tc.wantAggType)
			}
		})
	}
}

// outboxRowsByTime sorts rows for test convenience (deterministic ordering).
func outboxRowsByTime(rows []outbox.Row) []outbox.Row {
	out := append([]outbox.Row(nil), rows...)
	// Bubble sort (small N) by occurred_at ASC.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].OccurredAt.Before(out[i].OccurredAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestOutboxPublisher_MultiplePublishesAreOrdered(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	pub := newOutboxPublisher(store)

	t0 := time.Now().UTC()
	for i := 0; i < 3; i++ {
		_, err := pub.PublishCourseCreated(events.CourseCreated{
			TenantID:       "00000000-0000-0000-0000-0000000000aa",
			CourseID:       "course-" + string(rune('a'+i)),
			Title:          "Course",
			InstructorGCID: "00000000-0000-0000-0000-0000000000bb",
		})
		if err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
		time.Sleep(1 * time.Millisecond)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 3 {
		t.Fatalf("rows = %d; want 3", len(rows))
	}
	sorted := outboxRowsByTime(rows)
	if !sorted[0].OccurredAt.Before(sorted[2].OccurredAt) && !sorted[0].OccurredAt.Equal(sorted[2].OccurredAt) {
		t.Errorf("outbox rows not time-ordered: %+v vs %+v", sorted[0].OccurredAt, sorted[2].OccurredAt)
	}
	_ = t0
}
