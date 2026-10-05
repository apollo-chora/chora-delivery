// Package outbox — TransactionalOutboxPublisher implementation.
//
// TransactionalOutboxPublisher satisfies events.Publisher by:
//
//  1. Delegating to an inner publisher (typically events.InMemoryPublisher)
//     for envelope minting + payload shaping. This preserves all existing
//     idempotency-key derivation, IMDA tagging, and topic-routing logic.
//  2. Writing the resulting PublishedEvent to outbox_events via the Store
//     port instead of (or in addition to) publishing directly to Pub/Sub.
//
// The Dispatcher (see dispatcher.go) drains the outbox to Cloud Pub/Sub on
// a separate goroutine. This decouples event emission from Pub/Sub
// availability — a crash between domain state-mutation and publish no
// longer loses events.
//
// Per `feedback_d6_resilience_first_class` B.6.2.a — producer-side durable
// emission for chora-delivery's per-event canonical topic stream.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
	"github.com/apollo-chora/chora-delivery/internal/domain/application"
)

// schemaVersion is the major version of the on-wire payload schema. Matches
// the v{N} suffix on the canonical topic taxonomy.
const schemaVersion = 1

// PublisherConfig wires the TransactionalOutboxPublisher.
type PublisherConfig struct {
	// Inner is the publisher whose PublishedEvent shape we tee into the
	// outbox. Required. Typically events.InMemoryPublisher (which mints
	// envelopes + payloads with the canonical IMDA tagging + idempotency-key
	// derivation per .claude/rules/ddd-enforcement.md).
	Inner events.Publisher

	// Store is the outbox table backend. Required.
	Store Store

	// SourceProject is the project the service runs in (e.g.
	// chora-489812). Defaults to "chora-489812".
	SourceProject string

	// SourceService is the publisher's service name. Defaults to
	// "chora-delivery".
	SourceService string

	// Now is injectable for tests; defaults to time.Now().UTC.
	Now func() time.Time
}

// TransactionalOutboxPublisher satisfies events.Publisher by enqueueing the
// event into outbox_events instead of publishing directly to Pub/Sub.
type TransactionalOutboxPublisher struct {
	cfg PublisherConfig
}

// NewTransactionalPublisher constructs a TransactionalOutboxPublisher. Panics
// when Inner or Store is nil.
func NewTransactionalPublisher(cfg PublisherConfig) *TransactionalOutboxPublisher {
	if cfg.Inner == nil {
		panic("outbox: NewTransactionalPublisher: Inner publisher required")
	}
	if cfg.Store == nil {
		panic("outbox: NewTransactionalPublisher: Store required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.SourceProject == "" {
		cfg.SourceProject = "chora-489812"
	}
	if cfg.SourceService == "" {
		cfg.SourceService = "chora-delivery"
	}
	return &TransactionalOutboxPublisher{cfg: cfg}
}

// PublishCourseCreated forwards to Inner then tees the PublishedEvent into
// the outbox under aggregate_type=course.
func (p *TransactionalOutboxPublisher) PublishCourseCreated(in events.CourseCreated) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishCourseCreated(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "course", in.CourseID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishLiveQuizScoreAwarded forwards to Inner then tees into the outbox
// (ADR-168 durable score path).
func (p *TransactionalOutboxPublisher) PublishLiveQuizScoreAwarded(in events.LiveQuizScoreAwarded) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishLiveQuizScoreAwarded(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "live_quiz_session", in.SessionID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishLiveQuizSessionStarted forwards to Inner then tees into the outbox
// (ADR-168 durable session lifecycle).
func (p *TransactionalOutboxPublisher) PublishLiveQuizSessionStarted(in events.LiveQuizSessionStarted) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishLiveQuizSessionStarted(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "live_quiz_session", in.SessionID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishLiveQuizSessionEnded forwards to Inner then tees into the outbox
// (ADR-168 durable session lifecycle).
func (p *TransactionalOutboxPublisher) PublishLiveQuizSessionEnded(in events.LiveQuizSessionEnded) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishLiveQuizSessionEnded(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "live_quiz_session", in.SessionID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishCoursePublished forwards + tees.
func (p *TransactionalOutboxPublisher) PublishCoursePublished(in events.CoursePublished) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishCoursePublished(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "course", in.CourseID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishEnrollmentCreated forwards + tees.
func (p *TransactionalOutboxPublisher) PublishEnrollmentCreated(in events.EnrollmentCreated) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishEnrollmentCreated(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "enrollment", in.EnrollmentID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishEnrollmentCancelled forwards + tees.
func (p *TransactionalOutboxPublisher) PublishEnrollmentCancelled(in events.EnrollmentCancelled) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishEnrollmentCancelled(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "enrollment", in.EnrollmentID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishEnrollmentCompleted forwards + tees.
func (p *TransactionalOutboxPublisher) PublishEnrollmentCompleted(in events.EnrollmentCompleted) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishEnrollmentCompleted(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "enrollment", in.EnrollmentID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishBookingConfirmed forwards + tees.
func (p *TransactionalOutboxPublisher) PublishBookingConfirmed(in events.BookingConfirmed) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishBookingConfirmed(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "booking", in.BookingID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishCertificationIssued forwards + tees.
func (p *TransactionalOutboxPublisher) PublishCertificationIssued(in events.CertificationIssued) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishCertificationIssued(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "certification", in.CertificationID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishApplicationStateChanged forwards + tees.
func (p *TransactionalOutboxPublisher) PublishApplicationStateChanged(app *application.Application, traceparent string) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishApplicationStateChanged(app, traceparent)
	if err != nil {
		return ev, err
	}
	if app == nil {
		return ev, errors.New("outbox: nil application")
	}
	if err := p.teeRow(ev, "application", app.ID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishTestSetCreated forwards + tees.
func (p *TransactionalOutboxPublisher) PublishTestSetCreated(in events.TestSetCreated) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishTestSetCreated(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "test_set", in.TestSetID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishTestSetQuestionAdded forwards + tees.
func (p *TransactionalOutboxPublisher) PublishTestSetQuestionAdded(in events.TestSetQuestionAdded) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishTestSetQuestionAdded(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "test_set", in.TestSetID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishTestSetQuestionUpdated forwards + tees.
func (p *TransactionalOutboxPublisher) PublishTestSetQuestionUpdated(in events.TestSetQuestionUpdated) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishTestSetQuestionUpdated(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "test_set", in.TestSetID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishTestSetQuestionRemoved forwards + tees.
func (p *TransactionalOutboxPublisher) PublishTestSetQuestionRemoved(in events.TestSetQuestionRemoved) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishTestSetQuestionRemoved(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "test_set", in.TestSetID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishTestSetPublished forwards + tees.
func (p *TransactionalOutboxPublisher) PublishTestSetPublished(in events.TestSetPublished) (events.PublishedEvent, error) {
	ev, err := p.cfg.Inner.PublishTestSetPublished(in)
	if err != nil {
		return ev, err
	}
	if err := p.teeRow(ev, "test_set", in.TestSetID); err != nil {
		return ev, err
	}
	return ev, nil
}

// PublishCustom is the Lane B (ADR-155) generic publish path for the 9
// assessment + submission + grading topics. Forwards to the inner
// publisher's PublishCustom (added in assessment_publisher.go) and tees
// the resulting PublishedEvent into the outbox under an aggregate-type
// derived from the topic.
func (p *TransactionalOutboxPublisher) PublishCustom(topic, tenantID, gcid string, payload map[string]any) (events.PublishedEvent, error) {
	type publishCustomer interface {
		PublishCustom(topic, tenantID, gcid string, payload map[string]any) (events.PublishedEvent, error)
	}
	pc, ok := p.cfg.Inner.(publishCustomer)
	if !ok {
		return events.PublishedEvent{}, fmt.Errorf("outbox: inner publisher does not implement PublishCustom")
	}
	ev, err := pc.PublishCustom(topic, tenantID, gcid, payload)
	if err != nil {
		return ev, err
	}
	aggregateType, aggregateID := assessmentAggregateOf(topic, payload)
	if err := p.teeRow(ev, aggregateType, aggregateID); err != nil {
		return ev, err
	}
	return ev, nil
}

// assessmentAggregateOf returns (aggregate_type, aggregate_id) for a topic.
func assessmentAggregateOf(topic string, payload map[string]any) (string, string) {
	switch {
	case strings.HasPrefix(topic, "chora.delivery.assessment."):
		id, _ := payload["assessment_id"].(string)
		return "assessment", id
	case strings.HasPrefix(topic, "chora.delivery.submission."):
		id, _ := payload["submission_id"].(string)
		return "submission", id
	case strings.HasPrefix(topic, "chora.delivery.grading."):
		id, _ := payload["oe_batch_id"].(string)
		return "grading", id
	case strings.HasPrefix(topic, "chora.delivery.exam_result."):
		// W4 Exam BC outcome event (ADR-190 D1) — aggregate keyed by the
		// durable ExamResult row id.
		id, _ := payload["result_id"].(string)
		return "exam_result", id
	case strings.HasPrefix(topic, "chora.delivery.course."):
		// CHO-1612 — course.content_composed.v1 (+ future course PublishCustom
		// topics) tee under the course aggregate keyed by course_id.
		id, _ := payload["course_id"].(string)
		return "course", id
	}
	return "unknown", ""
}

// teeRow persists the PublishedEvent into outbox_events. Errors here surface
// to the HTTP handler so the caller can decide whether to roll back the
// state mutation.
//
// Wire format: producer-side encoding emits canonical binary protobuf for
// topics whose binary Schema Registry schema is BINARY-encoded.
// JSON-marshalled payloads fail validation at publish time with "Invalid
// binary proto message" and dead-letter forever. JSON fallback retained for
// the 4 topics without an attached Schema Registry schema (course.updated /
// enrollment.created / enrollment.cancelled / application.under_review). Per
// the gap surfaced 2026-05-16 (course.published gained a binary schema +
// encoder in the 2026-07-01 event-fabric repair).
func (p *TransactionalOutboxPublisher) teeRow(ev events.PublishedEvent, aggregateType, aggregateID string) error {
	row, err := buildOutboxRow(ev, aggregateType, aggregateID, p.envSourceProject(ev), p.envSourceService(ev), p.cfg.Now())
	if err != nil {
		return err
	}
	// The events.Publisher port does not thread a request-scoped context;
	// outbox writes use context.Background() (no I/O timeouts at this seam).
	return p.cfg.Store.Insert(context.Background(), row)
}

// buildOutboxRow shapes the outbox_events Row for a minted PublishedEvent. It is
// the single row-builder shared by the after-commit tee (teeRow) and the
// transactional bulk-enrol tee (bulk_tx_tee.go), so the persisted column shape
// never drifts between the two write paths. Producer-side encoding emits binary
// protobuf for Schema-Registry-attached topics, JSON fallback otherwise.
func buildOutboxRow(ev events.PublishedEvent, aggregateType, aggregateID, sourceProject, sourceService string, publishedAt time.Time) (Row, error) {
	payloadBytes, err := encodeOutboxPayload(ev.Topic, ev.Envelope, ev.Payload)
	if err != nil {
		return Row{}, fmt.Errorf("outbox: marshal payload: %w", err)
	}
	envelope := map[string]string{
		"event_id":        ev.Envelope.EventID,
		"idempotency_key": ev.Envelope.IdempotencyKey,
		"tenant_id":       ev.Envelope.TenantID,
		"gcid":            ev.Envelope.GCID,
		"occurred_at":     ev.Envelope.OccurredAt.UTC().Format(time.RFC3339Nano),
		"published_at":    publishedAt.Format(time.RFC3339Nano),
		"traceparent":     ev.Envelope.Traceparent,
		"tracestate":      ev.Envelope.Tracestate,
		"source_project":  sourceProject,
		"source_service":  sourceService,
		"schema_version":  strconv.Itoa(schemaVersion),
		"correlation_id":  ev.Envelope.CorrelationID,
		"causation_id":    ev.Envelope.CausationID,
	}
	return Row{
		ID:             ev.Envelope.EventID,
		TenantID:       ev.Envelope.TenantID,
		GCID:           ev.Envelope.GCID,
		AggregateType:  aggregateType,
		AggregateID:    aggregateID,
		EventType:      deriveEventType(ev.Topic),
		Topic:          ev.Topic,
		Payload:        payloadBytes,
		Envelope:       envelope,
		IdempotencyKey: ev.Envelope.IdempotencyKey,
		OccurredAt:     ev.Envelope.OccurredAt.UTC(),
	}, nil
}

func (p *TransactionalOutboxPublisher) envSourceProject(ev events.PublishedEvent) string {
	if ev.Envelope.SourceProject != "" {
		return ev.Envelope.SourceProject
	}
	return p.cfg.SourceProject
}

func (p *TransactionalOutboxPublisher) envSourceService(ev events.PublishedEvent) string {
	if ev.Envelope.SourceService != "" {
		return ev.Envelope.SourceService
	}
	return p.cfg.SourceService
}

// deriveEventType pulls the trailing event-type segment off a canonical
// topic name. Example: "chora.delivery.booking.confirmed.v1" -> "booking.confirmed".
// Falls back to the full topic when the format is unexpected.
func deriveEventType(topic string) string {
	segs := strings.Split(topic, ".")
	if len(segs) < 4 {
		return topic
	}
	last := segs[len(segs)-1]
	if len(last) >= 2 && last[0] == 'v' {
		segs = segs[:len(segs)-1]
	}
	if len(segs) < 3 {
		return topic
	}
	return strings.Join(segs[2:], ".")
}

// Compile-time port assertion.
var _ events.Publisher = (*TransactionalOutboxPublisher)(nil)

// -----------------------------------------------------------------------------
// Payload encoding — binary protobuf for Schema-Registry-attached topics, JSON
// fallback for topics that don't yet have a binary encoder.
//
// JSON fallback exists for the 5 chora-delivery topics that emit but don't
// (yet) have a Pub/Sub Schema Registry schema attached: course.updated /
// course.published / enrollment.created / enrollment.cancelled /
// application.under_review. Each unknown topic logs a one-time WARN so its
// missing encoder is visible in production. New topics MUST add a case in
// protomarshal.MarshalPayload.
// -----------------------------------------------------------------------------

var (
	warnedUnknownTopicsMu sync.Mutex
	warnedUnknownTopics   = map[string]bool{}
)

// toProtoEnvelope projects the local events.EventEnvelope onto the encoder's
// flat envelope type. Keeps protomarshal import-cycle-free.
func toProtoEnvelope(env events.EventEnvelope) protomarshal.Envelope {
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

func encodeOutboxPayload(topic string, env events.EventEnvelope, payload map[string]any) ([]byte, error) {
	bz, err := protomarshal.MarshalPayload(topic, toProtoEnvelope(env), payload)
	if err == nil {
		return bz, nil
	}
	if !protomarshal.IsUnsupportedTopic(err) {
		// Real encoding error (e.g. type mismatch) — fail loud per
		// feedback_no_stubs_real_wiring.
		return nil, err
	}

	// Topic has no protobuf encoder yet. Log a one-shot WARN and fall back
	// to JSON. These 5 topics chora-delivery emits do NOT have a registered
	// Schema Registry schema, so the JSON payload publishes successfully —
	// follow-on cleanup tracks adding schemas + encoders for each.
	warnedUnknownTopicsMu.Lock()
	if !warnedUnknownTopics[topic] {
		warnedUnknownTopics[topic] = true
		log.Printf("WARN outbox: topic %q has no binary protobuf encoder — payload will JSON-marshal. If a Schema Registry schema is later attached to this topic, add a case to internal/adapter/events/protomarshal/MarshalPayload.", topic)
	}
	warnedUnknownTopicsMu.Unlock()

	bz, mErr := json.Marshal(payload)
	if mErr != nil {
		return nil, fmt.Errorf("outbox: json fallback marshal: %w", mErr)
	}
	return bz, nil
}
