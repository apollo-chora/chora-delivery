// cloud_helpers_internal_test.go — in-package unit tests for the unexported
// CloudPublisher helpers: teeOutbox error branches, validateTopic,
// deriveEventType, newRowID and encodeCloudPublisherPayload. The exported
// PublishX surface is exercised externally in cloud_publisher_exhaust_test.go.
package events

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	cgcoutbox "github.com/apollo-chora/chora-common/outbox"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// inmemRecorder is an in-package cgcoutbox.Recorder double; set err to force
// Record failures (the external stubRecorder only ever succeeds).
type inmemRecorder struct {
	rows []*cgcoutbox.Row
	err  error
}

func (r *inmemRecorder) Record(_ context.Context, _ cgcoutbox.Tx, row *cgcoutbox.Row) error {
	if r.err != nil {
		return r.err
	}
	r.rows = append(r.rows, row)
	return nil
}
func (r *inmemRecorder) Claim(_ context.Context, _ int) ([]*cgcoutbox.Row, error) { return nil, nil }
func (r *inmemRecorder) MarkPublished(_ context.Context, _ []string) error        { return nil }
func (r *inmemRecorder) MarkFailed(_ context.Context, _ string, _ string, _ bool) error {
	return nil
}

// validEnvelopeForTest returns an EventEnvelope satisfying
// cgcenvelope.Validate's mandatory-field checks.
func validEnvelopeForTest() EventEnvelope {
	now := time.Now().UTC()
	return EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: "idem-1",
		TenantID:       "tenant-1",
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-01",
		SourceProject:  "chora-489812",
		SourceService:  "chora-delivery",
		SchemaVersion:  1,
	}
}

// -----------------------------------------------------------------------------
// teeOutbox — the four wrapped-error branches
// -----------------------------------------------------------------------------

func TestTeeOutbox_InvalidTopic(t *testing.T) {
	rec := &inmemRecorder{}
	p := &CloudPublisher{inner: NewInMemoryPublisher("p", "s"), rec: rec}
	_, err := p.teeOutbox("course", "c1", PublishedEvent{Topic: "not a topic"})
	if err == nil || !strings.Contains(err.Error(), "events.CloudPublisher") {
		t.Fatalf("want wrapped topic error, got %v", err)
	}
	if len(rec.rows) != 0 {
		t.Fatalf("no row must be written on invalid topic; got %d", len(rec.rows))
	}
}

func TestTeeOutbox_MarshalPayloadError(t *testing.T) {
	// A topic WITH a binary encoder + a payload whose required string field is
	// the wrong type -> a REAL encoder failure (not ErrUnsupportedTopic) must
	// fail loud instead of silently falling back to JSON.
	rec := &inmemRecorder{}
	p := &CloudPublisher{inner: NewInMemoryPublisher("p", "s"), rec: rec}
	_, err := p.teeOutbox("live_quiz_session", "s1", PublishedEvent{
		Topic:    TopicLiveQuizSessionStarted,
		Envelope: validEnvelopeForTest(),
		Payload:  map[string]interface{}{"session_id": 123}, // requireString fails
	})
	if err == nil || !strings.Contains(err.Error(), "marshal payload") {
		t.Fatalf("want marshal-payload error, got %v", err)
	}
	if len(rec.rows) != 0 {
		t.Fatalf("no row must be written on encode failure; got %d", len(rec.rows))
	}
}

func TestTeeOutbox_EnvelopeValidationError(t *testing.T) {
	// TopicEnrollmentCancelled has no binary encoder -> JSON fallback succeeds;
	// the empty envelope then fails cgcenvelope.Validate.
	rec := &inmemRecorder{}
	p := &CloudPublisher{inner: NewInMemoryPublisher("p", "s"), rec: rec}
	_, err := p.teeOutbox("enrollment", "e1", PublishedEvent{
		Topic:    TopicEnrollmentCancelled,
		Envelope: EventEnvelope{}, // missing event_id / tenant_id / traceparent ...
		Payload:  map[string]interface{}{"enrollment_id": "e1"},
	})
	if err == nil || !strings.Contains(err.Error(), "envelope") {
		t.Fatalf("want envelope-validation error, got %v", err)
	}
	if len(rec.rows) != 0 {
		t.Fatalf("no row must be written on envelope failure; got %d", len(rec.rows))
	}
}

func TestTeeOutbox_RecorderError(t *testing.T) {
	rec := &inmemRecorder{err: errors.New("pg down")}
	p := &CloudPublisher{inner: NewInMemoryPublisher("p", "s"), rec: rec}
	_, err := p.teeOutbox("enrollment", "e1", PublishedEvent{
		Topic:    TopicEnrollmentCancelled,
		Envelope: validEnvelopeForTest(),
		Payload:  map[string]interface{}{"enrollment_id": "e1"},
	})
	if err == nil || !strings.Contains(err.Error(), "outbox") {
		t.Fatalf("want outbox-recorder error, got %v", err)
	}
}

func TestTeeOutbox_SuccessWritesRow(t *testing.T) {
	rec := &inmemRecorder{}
	p := &CloudPublisher{inner: NewInMemoryPublisher("p", "s"), rec: rec}
	ev, err := p.teeOutbox("enrollment", "e1", PublishedEvent{
		Topic:    TopicEnrollmentCancelled,
		Envelope: validEnvelopeForTest(),
		Payload:  map[string]interface{}{"enrollment_id": "e1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Topic != TopicEnrollmentCancelled {
		t.Fatalf("teeOutbox must return the original event; got %q", ev.Topic)
	}
	if len(rec.rows) != 1 {
		t.Fatalf("expected 1 outbox row; got %d", len(rec.rows))
	}
	row := rec.rows[0]
	if row.AggregateType != "enrollment" || row.AggregateID != "e1" {
		t.Fatalf("row aggregate = %s/%s, want enrollment/e1", row.AggregateType, row.AggregateID)
	}
	if row.EventType != "enrollment.cancelled.v1" {
		t.Fatalf("row event_type = %q, want enrollment.cancelled.v1", row.EventType)
	}
}

// -----------------------------------------------------------------------------
// validateTopic — every rejection branch + valid delivery/governance shapes
// -----------------------------------------------------------------------------

func TestValidateTopic_AllShapeBranches(t *testing.T) {
	cases := []struct {
		name    string
		topic   string
		wantErr bool
	}{
		{"valid delivery", "chora.delivery.course.created.v1", false},
		{"valid governance", "chora.governance.certification.issued.v1", false},
		{"whitespace trimmed", "  chora.delivery.course.created.v1  ", false},
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"too few parts", "chora.delivery.course", true},
		{"not chora prefix", "foo.delivery.course.created.v1", true},
		{"wrong domain", "chora.analytics.course.created.v1", true},
		{"missing version suffix", "chora.delivery.course.created", true},
		{"bare v suffix", "chora.delivery.course.created.v", true},
		{"non numeric version", "chora.delivery.course.created.vx", true},
		{"non numeric version tail", "chora.delivery.course.created.v1x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTopic(tc.topic)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q", tc.topic)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.topic, err)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// deriveEventType + newRowID
// -----------------------------------------------------------------------------

func TestDeriveEventType(t *testing.T) {
	cases := []struct{ topic, want string }{
		{"chora.delivery.course.created.v1", "course.created.v1"},
		{"a.b.c.d", "c.d"},
		{"a.b", "a.b"}, // fewer than 3 parts -> verbatim
	}
	for _, tc := range cases {
		if got := deriveEventType(tc.topic); got != tc.want {
			t.Errorf("deriveEventType(%q) = %q, want %q", tc.topic, got, tc.want)
		}
	}
}

func TestNewRowID(t *testing.T) {
	id := newRowID()
	if len(id) != 36 {
		t.Fatalf("expected 36-char UUID, got %q (len %d)", id, len(id))
	}
	// The uuid.NewV7 error fallback (uuid.NewString) is not triggerable:
	// uuid.NewV7 only errors on clock/entropy catastrophes, which cannot be
	// induced from a test without injecting into google/uuid.
}

// -----------------------------------------------------------------------------
// encodeCloudPublisherPayload — binary path, JSON fallback (incl. one-shot
// WARN short-circuit), real-encoder failure, JSON-fallback failure
// -----------------------------------------------------------------------------

func TestEncodeCloudPublisherPayload_AllPaths(t *testing.T) {
	inner := NewInMemoryPublisher("chora-489812", "chora-delivery")

	// 1) Real binary protobuf path — envelope-first wire layout.
	ev, err := inner.PublishLiveQuizSessionStarted(LiveQuizSessionStarted{TenantID: "t", SessionID: "s"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	bz, err := encodeCloudPublisherPayload(ev.Topic, ev.Envelope, ev.Payload)
	if err != nil {
		t.Fatalf("binary encode: %v", err)
	}
	if len(bz) == 0 {
		t.Fatal("empty binary payload")
	}
	num, typ, n := protowire.ConsumeTag(bz)
	if n < 0 || num != 1 || typ != protowire.BytesType {
		t.Errorf("expected envelope-first binary layout, got num=%d typ=%d", num, typ)
	}

	// 2) Unsupported topic -> JSON fallback. Call TWICE so the second call
	// hits the cloudWarnedUnknownTopics short-circuit (one-shot WARN).
	ev2, err := inner.PublishEnrollmentCancelled(EnrollmentCancelled{TenantID: "t", CourseID: "c", LearnerGCID: "g"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	bz2, err := encodeCloudPublisherPayload(ev2.Topic, ev2.Envelope, ev2.Payload)
	if err != nil {
		t.Fatalf("json fallback: %v", err)
	}
	if len(bz2) == 0 || bz2[0] != '{' {
		t.Errorf("expected JSON fallback payload, got %q", bz2)
	}
	if _, err := encodeCloudPublisherPayload(ev2.Topic, ev2.Envelope, ev2.Payload); err != nil {
		t.Fatalf("second json fallback: %v", err)
	}

	// 3) Real encoder failure (registered topic, wrong-typed required field)
	// must fail loud rather than JSON-falling-back.
	if _, err := encodeCloudPublisherPayload(ev.Topic, ev.Envelope, map[string]interface{}{"session_id": 123}); err == nil {
		t.Fatal("expected encoder error for wrong-typed required field")
	}

	// 4) JSON fallback itself fails (payload not JSON-serialisable).
	if _, err := encodeCloudPublisherPayload(ev2.Topic, ev2.Envelope, map[string]interface{}{"fn": func() {}}); err == nil {
		t.Fatal("expected json-marshal error for unsupported value type")
	} else if !strings.Contains(err.Error(), "json fallback marshal") {
		t.Fatalf("want json fallback marshal error, got %v", err)
	}
}
