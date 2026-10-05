// cloud_publisher.go — outbox-backed Publisher decorator for chora-delivery.
//
// Wraps an inner Publisher (typically InMemoryPublisher for envelope
// minting + payload shaping) and tees every PublishedEvent into the
// chora_delivery outbox_events table via chora-go-common/outbox.Recorder.
// A separate Relay process drains pending rows to Cloud Pub/Sub.
//
// Why a decorator (vs writing a new full-featured CloudPublisher)?
//
//   - The inner publisher already encapsulates idempotency-key derivation,
//     IMDA payload-shape conventions, and PublishApplicationStateChanged
//     state-machine routing. Re-implementing all that risks behavioural drift.
//   - Atomic-publish guarantees come from the outbox table, not from the
//     in-memory recorder. The decorator pattern keeps the inner publisher
//     contract pristine for in-process subscribers (e.g. attendance
//     reconcilers that wire to InMemoryPublisher.History()).
//
// Resilience properties (per `feedback_resilience_priority`):
//   - Atomic enqueue inside the same Postgres transaction the surrounding
//     domain mutation already opened (Tx propagation via PublishWithTx).
//   - Dead-pod resume: the Relay claim contract picks up pending rows on
//     replacement-pod boot.
//   - DLQ: ultimate publish failure -> status=deadlettered with reason.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/google/uuid"

	cgcenvelope "github.com/apollo-chora/chora-common/envelope"
	cgcoutbox "github.com/apollo-chora/chora-common/outbox"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

// CloudPublisher decorates an inner Publisher and writes every emitted
// event to the outbox.
type CloudPublisher struct {
	inner Publisher
	rec   cgcoutbox.Recorder
}

// NewCloudPublisher returns a CloudPublisher wrapping `inner` and writing
// every published event to `rec`.
func NewCloudPublisher(inner Publisher, rec cgcoutbox.Recorder) *CloudPublisher {
	return &CloudPublisher{inner: inner, rec: rec}
}

// teeOutbox writes the PublishedEvent to the outbox. Returns the original
// event + a wrapped error so callers can preserve ev when outbox writes
// fail (the inner publisher already recorded the event in-memory).
func (p *CloudPublisher) teeOutbox(aggregateType, aggregateID string, ev PublishedEvent) (PublishedEvent, error) {
	if err := validateTopic(ev.Topic); err != nil {
		return ev, fmt.Errorf("events.CloudPublisher: %w", err)
	}
	// Producer-side encoding: emit canonical binary protobuf for topics whose
	// binary Schema Registry schema is BINARY-encoded. JSON-marshalled
	// payloads fail validation at publish time with "Invalid binary proto
	// message" and dead-letter forever. Per the gap surfaced 2026-05-16.
	body, err := encodeCloudPublisherPayload(ev.Topic, ev.Envelope, ev.Payload)
	if err != nil {
		return ev, fmt.Errorf("events.CloudPublisher: marshal payload: %w", err)
	}
	commonEnv := cgcenvelope.Envelope{
		EventID:        ev.Envelope.EventID,
		IdempotencyKey: ev.Envelope.IdempotencyKey,
		TenantID:       ev.Envelope.TenantID,
		GCID:           ev.Envelope.GCID,
		OccurredAt:     ev.Envelope.OccurredAt,
		PublishedAt:    ev.Envelope.PublishedAt,
		Traceparent:    ev.Envelope.Traceparent,
		Tracestate:     ev.Envelope.Tracestate,
		SourceProject:  ev.Envelope.SourceProject,
		SourceService:  ev.Envelope.SourceService,
		SchemaVersion:  ev.Envelope.SchemaVersion,
		CorrelationID:  ev.Envelope.CorrelationID,
		CausationID:    ev.Envelope.CausationID,
	}
	if err := cgcenvelope.Validate(commonEnv); err != nil {
		return ev, fmt.Errorf("events.CloudPublisher: envelope: %w", err)
	}
	row := &cgcoutbox.Row{
		ID:            newRowID(),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     deriveEventType(ev.Topic),
		Topic:         ev.Topic,
		Payload:       body,
		Envelope:      commonEnv,
		OccurredAt:    ev.Envelope.OccurredAt,
		Status:        cgcoutbox.StatusPending,
	}
	if err := p.rec.Record(context.Background(), nil, row); err != nil {
		return ev, fmt.Errorf("events.CloudPublisher: outbox: %w", err)
	}
	return ev, nil
}

// PublishCourseCreated forwards + tees.
func (p *CloudPublisher) PublishCourseCreated(in CourseCreated) (PublishedEvent, error) {
	ev, err := p.inner.PublishCourseCreated(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("course", in.CourseID, ev)
}

// PublishLiveQuizScoreAwarded forwards to inner then tees into the outbox
// (ADR-168 durable score path).
func (p *CloudPublisher) PublishLiveQuizScoreAwarded(in LiveQuizScoreAwarded) (PublishedEvent, error) {
	ev, err := p.inner.PublishLiveQuizScoreAwarded(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("live_quiz_session", in.SessionID, ev)
}

// PublishLiveQuizSessionStarted forwards to inner then tees into the outbox
// (ADR-168 durable session lifecycle).
func (p *CloudPublisher) PublishLiveQuizSessionStarted(in LiveQuizSessionStarted) (PublishedEvent, error) {
	ev, err := p.inner.PublishLiveQuizSessionStarted(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("live_quiz_session", in.SessionID, ev)
}

// PublishLiveQuizSessionEnded forwards to inner then tees into the outbox
// (ADR-168 durable session lifecycle).
func (p *CloudPublisher) PublishLiveQuizSessionEnded(in LiveQuizSessionEnded) (PublishedEvent, error) {
	ev, err := p.inner.PublishLiveQuizSessionEnded(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("live_quiz_session", in.SessionID, ev)
}

// PublishCoursePublished forwards + tees.
func (p *CloudPublisher) PublishCoursePublished(in CoursePublished) (PublishedEvent, error) {
	ev, err := p.inner.PublishCoursePublished(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("course", in.CourseID, ev)
}

// PublishEnrollmentCreated forwards + tees.
func (p *CloudPublisher) PublishEnrollmentCreated(in EnrollmentCreated) (PublishedEvent, error) {
	ev, err := p.inner.PublishEnrollmentCreated(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("enrollment", in.EnrollmentID, ev)
}

// PublishEnrollmentCancelled forwards + tees.
func (p *CloudPublisher) PublishEnrollmentCancelled(in EnrollmentCancelled) (PublishedEvent, error) {
	ev, err := p.inner.PublishEnrollmentCancelled(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("enrollment", in.EnrollmentID, ev)
}

// PublishEnrollmentCompleted forwards + tees.
func (p *CloudPublisher) PublishEnrollmentCompleted(in EnrollmentCompleted) (PublishedEvent, error) {
	ev, err := p.inner.PublishEnrollmentCompleted(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("enrollment", in.EnrollmentID, ev)
}

// PublishBookingConfirmed forwards + tees.
func (p *CloudPublisher) PublishBookingConfirmed(in BookingConfirmed) (PublishedEvent, error) {
	ev, err := p.inner.PublishBookingConfirmed(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("booking", in.BookingID, ev)
}

// PublishCertificationIssued forwards + tees.
func (p *CloudPublisher) PublishCertificationIssued(in CertificationIssued) (PublishedEvent, error) {
	ev, err := p.inner.PublishCertificationIssued(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("certification", in.CertificationID, ev)
}

// PublishApplicationStateChanged forwards + tees.
func (p *CloudPublisher) PublishApplicationStateChanged(app *application.Application, traceparent string) (PublishedEvent, error) {
	ev, err := p.inner.PublishApplicationStateChanged(app, traceparent)
	if err != nil {
		return ev, err
	}
	if app == nil {
		return ev, errors.New("events.CloudPublisher: nil application")
	}
	return p.teeOutbox("application", app.ID, ev)
}

// PublishTestSetCreated forwards + tees.
func (p *CloudPublisher) PublishTestSetCreated(in TestSetCreated) (PublishedEvent, error) {
	ev, err := p.inner.PublishTestSetCreated(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("test_set", in.TestSetID, ev)
}

// PublishTestSetQuestionAdded forwards + tees.
func (p *CloudPublisher) PublishTestSetQuestionAdded(in TestSetQuestionAdded) (PublishedEvent, error) {
	ev, err := p.inner.PublishTestSetQuestionAdded(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("test_set", in.TestSetID, ev)
}

// PublishTestSetQuestionUpdated forwards + tees.
func (p *CloudPublisher) PublishTestSetQuestionUpdated(in TestSetQuestionUpdated) (PublishedEvent, error) {
	ev, err := p.inner.PublishTestSetQuestionUpdated(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("test_set", in.TestSetID, ev)
}

// PublishTestSetQuestionRemoved forwards + tees.
func (p *CloudPublisher) PublishTestSetQuestionRemoved(in TestSetQuestionRemoved) (PublishedEvent, error) {
	ev, err := p.inner.PublishTestSetQuestionRemoved(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("test_set", in.TestSetID, ev)
}

// PublishTestSetPublished forwards + tees.
func (p *CloudPublisher) PublishTestSetPublished(in TestSetPublished) (PublishedEvent, error) {
	ev, err := p.inner.PublishTestSetPublished(in)
	if err != nil {
		return ev, err
	}
	return p.teeOutbox("test_set", in.TestSetID, ev)
}

// PublishCustom is the Lane B (ADR-155) generic publish path. Forwards to the
// inner publisher's PublishCustom + tees through the outbox recorder.
func (p *CloudPublisher) PublishCustom(topic, tenantID, gcid string, payload map[string]any) (PublishedEvent, error) {
	type publishCustomer interface {
		PublishCustom(topic, tenantID, gcid string, payload map[string]any) (PublishedEvent, error)
	}
	pc, ok := p.inner.(publishCustomer)
	if !ok {
		return PublishedEvent{}, errors.New("cloud_publisher: inner does not implement PublishCustom")
	}
	ev, err := pc.PublishCustom(topic, tenantID, gcid, payload)
	if err != nil {
		return ev, err
	}
	aggregateType, aggregateID := "assessment", ""
	switch {
	case strings.HasPrefix(topic, "chora.delivery.submission."):
		aggregateType = "submission"
		if v, ok := payload["submission_id"].(string); ok {
			aggregateID = v
		}
	case strings.HasPrefix(topic, "chora.delivery.grading."):
		aggregateType = "grading"
		if v, ok := payload["oe_batch_id"].(string); ok {
			aggregateID = v
		}
	default:
		if v, ok := payload["assessment_id"].(string); ok {
			aggregateID = v
		}
	}
	return p.teeOutbox(aggregateType, aggregateID, ev)
}

// validateTopic enforces chora.{domain}.{aggregate}.{event_type}.v{N} shape
// and locks the second segment to "delivery" (or "governance" for IMDA
// evidence emitted from delivery — chora-delivery is the primary owner of
// course-published evidence per ADR-141).
func validateTopic(topic string) error {
	t := strings.TrimSpace(topic)
	if t == "" {
		return errors.New("topic required")
	}
	parts := strings.Split(t, ".")
	if len(parts) < 5 {
		return errors.New("topic must follow chora.{domain}.{aggregate}.{event_type}.v{N}")
	}
	if parts[0] != "chora" {
		return errors.New("topic must start with 'chora.'")
	}
	if parts[1] != "delivery" && parts[1] != "governance" {
		return fmt.Errorf("topic domain must be 'delivery' or 'governance'; got %q", parts[1])
	}
	last := parts[len(parts)-1]
	if !strings.HasPrefix(last, "v") || len(last) < 2 {
		return errors.New("topic must end with v{N} version suffix")
	}
	for _, ch := range last[1:] {
		if ch < '0' || ch > '9' {
			return errors.New("topic version suffix must be numeric")
		}
	}
	return nil
}

func deriveEventType(topic string) string {
	parts := strings.SplitN(topic, ".", 3)
	if len(parts) < 3 {
		return topic
	}
	return parts[2]
}

func newRowID() string {
	return uuid.Must(uuid.NewV7()).String()
}

// Compile-time check.
var _ Publisher = (*CloudPublisher)(nil)

// -----------------------------------------------------------------------------
// Payload encoding — binary protobuf for Schema-Registry-attached topics, JSON
// fallback for topics that don't yet have a binary encoder. New topics MUST
// add a case in internal/adapter/events/protomarshal/MarshalPayload.
// -----------------------------------------------------------------------------

var (
	cloudWarnedUnknownTopicsMu sync.Mutex
	cloudWarnedUnknownTopics   = map[string]bool{}
)

// toProtoEnvelope projects the local events.EventEnvelope onto the encoder's
// flat envelope type. Keeps protomarshal import-cycle-free.
func toProtoEnvelope(env EventEnvelope) protomarshal.Envelope {
	return protomarshal.Envelope{
		EventID:        env.EventID,
		IdempotencyKey: env.IdempotencyKey,
		TenantID:       env.TenantID,
		GCID:           env.GCID,
		OccurredAt:     env.OccurredAt,
		PublishedAt:    env.PublishedAt,
		Traceparent:    env.Traceparent,
		Tracestate:     env.Tracestate,
		SourceProject:  env.SourceProject,
		SourceService:  env.SourceService,
		SchemaVersion:  env.SchemaVersion,
		CorrelationID:  env.CorrelationID,
		CausationID:    env.CausationID,
	}
}

func encodeCloudPublisherPayload(topic string, env EventEnvelope, payload map[string]interface{}) ([]byte, error) {
	bz, err := protomarshal.MarshalPayload(topic, toProtoEnvelope(env), payload)
	if err == nil {
		return bz, nil
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		// Real encoding error (e.g. type mismatch) — fail loud per
		// feedback_no_stubs_real_wiring. JSON fallback would mask the bug.
		return nil, err
	}

	// Topic not yet wired for binary protobuf. Log a one-shot WARN and fall
	// back to JSON so the pre-existing path doesn't regress for the 5 topics
	// chora-delivery emits without a registered Schema Registry schema
	// (course.updated / course.published / enrollment.created /
	// enrollment.cancelled / application.under_review). These rows continue
	// to publish successfully to topics without an attached schema.
	cloudWarnedUnknownTopicsMu.Lock()
	if !cloudWarnedUnknownTopics[topic] {
		cloudWarnedUnknownTopics[topic] = true
		log.Printf("WARN events.CloudPublisher: topic %q has no binary protobuf encoder — payload will JSON-marshal. If a Schema Registry schema is later attached to this topic, add a case to internal/adapter/events/protomarshal/MarshalPayload.", topic)
	}
	cloudWarnedUnknownTopicsMu.Unlock()

	bz, jErr := json.Marshal(payload)
	if jErr != nil {
		return nil, fmt.Errorf("events.CloudPublisher: json fallback marshal: %w", jErr)
	}
	return bz, nil
}
