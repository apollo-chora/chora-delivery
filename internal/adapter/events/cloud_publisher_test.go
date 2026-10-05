package events_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	cgcoutbox "github.com/apollo-chora/chora-common/outbox"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
)

type stubRecorder struct {
	rows []*cgcoutbox.Row
}

func (s *stubRecorder) Record(_ context.Context, _ cgcoutbox.Tx, row *cgcoutbox.Row) error {
	s.rows = append(s.rows, row)
	return nil
}
func (s *stubRecorder) Claim(_ context.Context, _ int) ([]*cgcoutbox.Row, error) { return nil, nil }
func (s *stubRecorder) MarkPublished(_ context.Context, _ []string) error        { return nil }
func (s *stubRecorder) MarkFailed(_ context.Context, _ string, _ string, _ bool) error {
	return nil
}

func TestCloudPublisher_PublishCourseCreated_TeesToOutbox(t *testing.T) {
	rec := &stubRecorder{}
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	pub := events.NewCloudPublisher(inner, rec)

	ev, err := pub.PublishCourseCreated(events.CourseCreated{
		TenantID:       "22222222-2222-7222-8222-222222222222",
		GCID:           "00000000-0000-7000-8000-000000001002",
		CourseID:       "33333333-3333-7333-8333-333333333333",
		Title:          "CSPO Fundamentals",
		InstructorGCID: "00000000-0000-7000-8000-000000001002",
		Public:         true,
		PriceSGDCents:  0,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if ev.Topic != events.TopicCourseCreated {
		t.Errorf("ev.Topic: got %q want %q", ev.Topic, events.TopicCourseCreated)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("expected 1 outbox row; got %d", len(rec.rows))
	}
	row := rec.rows[0]
	if row.Topic != events.TopicCourseCreated {
		t.Errorf("row.Topic: got %q want %q", row.Topic, events.TopicCourseCreated)
	}
	if row.AggregateType != "course" {
		t.Errorf("aggregate_type: got %q want course", row.AggregateType)
	}
	if row.AggregateID != "33333333-3333-7333-8333-333333333333" {
		t.Errorf("aggregate_id: got %q want CSPO course id", row.AggregateID)
	}
	if row.Status != cgcoutbox.StatusPending {
		t.Errorf("status: got %q want pending", row.Status)
	}
	if row.Envelope.Traceparent == "" {
		t.Errorf("traceparent missing on outbox envelope")
	}
}

// TestCloudPublisher_PublishEnrollmentCompleted_TeesBinaryToOutbox — WS1.c2.
// The per-learner completion event tees to the outbox; the row payload routes
// through the binary protobuf encoder (encodeEnrollmentCompleted), NOT the JSON
// fallback — proving the new topic is reachable end-to-end via the real publish
// path so the Pub/Sub Schema Registry (BINARY) accepts the dispatched bytes.
func TestCloudPublisher_PublishEnrollmentCompleted_TeesBinaryToOutbox(t *testing.T) {
	rec := &stubRecorder{}
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	pub := events.NewCloudPublisher(inner, rec)

	ev, err := pub.PublishEnrollmentCompleted(events.EnrollmentCompleted{
		TenantID:     "22222222-2222-7222-8222-222222222222",
		GCID:         "00000000-0000-7000-8000-000000001999",
		EnrollmentID: "01970000-0000-7000-8000-00000000eeee",
		CourseID:     "33333333-3333-7333-8333-333333333333",
		LearnerGCID:  "00000000-0000-7000-8000-000000001999",
		Passed:       true,
		Traceparent:  "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if ev.Topic != events.TopicEnrollmentCompleted {
		t.Errorf("topic: got %q want %q", ev.Topic, events.TopicEnrollmentCompleted)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("expected 1 outbox row; got %d", len(rec.rows))
	}
	row := rec.rows[0]
	if row.AggregateType != "enrollment" {
		t.Errorf("aggregate_type: got %q want enrollment", row.AggregateType)
	}
	if row.AggregateID != "01970000-0000-7000-8000-00000000eeee" {
		t.Errorf("aggregate_id: got %q want enrollment id", row.AggregateID)
	}
	// Binary protobuf, not JSON fallback — the new encoder must be wired.
	if len(row.Payload) == 0 {
		t.Fatal("empty payload bytes")
	}
	num, typ, n := protowire.ConsumeTag(row.Payload)
	if n < 0 || num != 1 || typ != protowire.BytesType {
		t.Errorf("leading tag = (num=%d typ=%d) want (1, bytes) — Envelope first (JSON fallback?)", num, typ)
	}
}

func TestCloudPublisher_PublishEnrollmentCreated_TeesToOutbox(t *testing.T) {
	rec := &stubRecorder{}
	inner := events.NewInMemoryPublisher("chora-489812", "chora-delivery")
	pub := events.NewCloudPublisher(inner, rec)

	ev, err := pub.PublishEnrollmentCreated(events.EnrollmentCreated{
		TenantID:     "22222222-2222-7222-8222-222222222222",
		GCID:         "00000000-0000-7000-8000-000000001999",
		EnrollmentID: "01970000-0000-7000-8000-00000000eeee",
		CourseID:     "33333333-3333-7333-8333-333333333333",
		LearnerGCID:  "00000000-0000-7000-8000-000000001999",
		Traceparent:  "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if ev.Topic != events.TopicEnrollmentCreated {
		t.Errorf("topic: got %q want %q", ev.Topic, events.TopicEnrollmentCreated)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("expected 1 outbox row; got %d", len(rec.rows))
	}
	if rec.rows[0].AggregateType != "enrollment" {
		t.Errorf("aggregate_type: got %q want enrollment", rec.rows[0].AggregateType)
	}
}
