// Package events is the Pub/Sub publisher adapter for chora-delivery.
//
// Per the Phyllis MVP brief (docs/m13/phyllis-mvp-2026-05-08.md §6) and
// .claude/rules/ddd-enforcement.md "Event envelope mandatory fields":
//
//   - Topic taxonomy: chora.{domain}.{aggregate}.{event_type}.v{N}
//   - Mandatory envelope fields: event_id (UUIDv7), idempotency_key,
//     tenant_id, gcid, occurred_at, published_at, traceparent, tracestate,
//     source_project, source_service, schema_version
//   - W3C trace context propagation across Pub/Sub
//
// MVP ships with an in-memory publisher. M12+ swaps to a Cloud Pub/Sub
// adapter against the chora-489812 platform host project per Tier 2 D8.
//
// Hexagonal: this package is an OUTBOUND adapter — depends on domain
// errors/UUIDv7 generator only; the domain never imports this package.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/application"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

// Topic constants — single source of truth for delivery event topics.
const (
	TopicCourseCreated        = "chora.delivery.course.created.v1"
	TopicCoursePublished      = "chora.delivery.course.published.v1"
	TopicEnrollmentCreated    = "chora.delivery.enrollment.created.v1"
	TopicEnrollmentCancelled  = "chora.delivery.enrollment.cancelled.v1"
	TopicEnrollmentCompleted  = "chora.delivery.enrollment.completed.v1"
	TopicBookingConfirmed     = "chora.delivery.booking.confirmed.v1"
	TopicCertificationIssued  = "chora.delivery.certification.issued.v1"
	TopicApplicationSubmitted = "chora.delivery.application.submitted.v1"

	// TestSet topic constants — per chora-contracts/openapi/delivery-test-sets.yaml
	// + chora-contracts/proto/events/delivery/test_set.proto.
	TopicTestSetCreated         = "chora.delivery.test_set.created.v1"
	TopicTestSetQuestionAdded   = "chora.delivery.test_set.question_added.v1"
	TopicTestSetQuestionUpdated = "chora.delivery.test_set.question_updated.v1"
	TopicTestSetQuestionRemoved = "chora.delivery.test_set.question_removed.v1"
	TopicTestSetPublished       = "chora.delivery.test_set.published.v1"

	// TopicLiveQuizScoreAwarded — ADR-168 durable score event. Emitted when a
	// learner's live-quiz answer is graded; chora-sharing rolls it into the
	// Three-Currency leaderboard. The per-vote realtime hot path does NOT use
	// this topic (that rides the Redis backplane); this is the durable path.
	TopicLiveQuizScoreAwarded = "chora.delivery.live_quiz_session.score_awarded.v1"

	// TopicLiveQuizSessionStarted / TopicLiveQuizSessionEnded — ADR-168 durable
	// session-lifecycle events. Emitted on the ARMED→LIVE and LIVE→CLOSED
	// transitions for cross-domain consumers (observability dashboards,
	// notifications). The per-vote realtime path rides the Redis backplane.
	TopicLiveQuizSessionStarted = "chora.delivery.live_quiz_session.started.v1"
	TopicLiveQuizSessionEnded   = "chora.delivery.live_quiz_session.ended.v1"

	schemaVersion int32 = 1
)

// EventEnvelope mirrors chora.common.v1.EventEnvelope (proto/common/envelope.proto).
//
// All mandatory fields per .claude/rules/ddd-enforcement.md are present.
// JSON tags use snake_case to match the Protobuf wire format and the
// AsyncAPI documentation in chora-contracts/asyncapi/.
type EventEnvelope struct {
	EventID        string    `json:"event_id"`
	IdempotencyKey string    `json:"idempotency_key"`
	TenantID       string    `json:"tenant_id"`
	GCID           string    `json:"gcid,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	PublishedAt    time.Time `json:"published_at"`
	Traceparent    string    `json:"traceparent"`
	Tracestate     string    `json:"tracestate,omitempty"`
	SourceProject  string    `json:"source_project"`
	SourceService  string    `json:"source_service"`
	SchemaVersion  int32     `json:"schema_version"`
	CorrelationID  string    `json:"correlation_id,omitempty"`
	CausationID    string    `json:"causation_id,omitempty"`
}

// PublishedEvent is the in-memory record of a published event — used by
// tests and (post-MVP) cross-service smoke tests.
type PublishedEvent struct {
	Topic    string
	Envelope EventEnvelope
	Payload  map[string]interface{}
}

// CourseCreated is the input shape for PublishCourseCreated.
type CourseCreated struct {
	TenantID       string
	GCID           string // typically the instructor GCID
	CourseID       string
	Title          string
	InstructorGCID string
	Public         bool
	PriceSGDCents  int32
	Traceparent    string
	Tracestate     string
}

// EnrollmentCreated is the input shape for PublishEnrollmentCreated.
type EnrollmentCreated struct {
	TenantID     string
	GCID         string // typically the learner GCID
	EnrollmentID string
	CourseID     string
	LearnerGCID  string
	Traceparent  string
	Tracestate   string
}

// CoursePublished is the input shape for PublishCoursePublished —
// emitted when a course's visibility flips INTO `public`. chora-sharing
// subscribes for the discovery feed; chora-observability captures D1 + D2.
type CoursePublished struct {
	TenantID       string
	GCID           string
	CourseID       string
	Title          string
	InstructorGCID string
	Traceparent    string
	Tracestate     string
}

// EnrollmentCancelled is the input shape for PublishEnrollmentCancelled.
type EnrollmentCancelled struct {
	TenantID     string
	GCID         string
	EnrollmentID string
	CourseID     string
	LearnerGCID  string
	Reason       string
	Traceparent  string
	Tracestate   string
}

// EnrollmentCompleted is the input shape for PublishEnrollmentCompleted —
// emitted when a learner's enrollment in a Course/Class reaches the COMPLETED
// terminal state. The per-learner counterpart to the class-lifecycle
// course.completed event (DISTINCT from certification.issued — not every
// completion issues a credential — and from learning_path.completed — a
// self-paced atom path). Consumed by the Familiar Global LearnerProfile
// read-model (ADR-200) + familiar verified-EXP (ADR-203, tier S).
// Topic: chora.delivery.enrollment.completed.v1.
type EnrollmentCompleted struct {
	TenantID     string
	GCID         string // the learner GCID (the subject of the completion)
	EnrollmentID string
	CourseID     string
	LearnerGCID  string
	Passed       bool      // met the passing requirements vs completed-without-passing
	CompletedAt  time.Time // defaults to publish time when zero
	Traceparent  string
	Tracestate   string
}

// BookingConfirmed mirrors chora.delivery.v1.BookingConfirmed.
type BookingConfirmed struct {
	TenantID    string
	GCID        string
	BookingID   string
	CourseID    string
	ClassID     string
	LearnerGCID string
	SeatNumber  int32
	Traceparent string
	Tracestate  string
}

// CertificationIssued mirrors chora.delivery.v1.CertificationIssued.
type CertificationIssued struct {
	TenantID        string
	GCID            string
	CertificationID string
	CourseID        string
	LearnerGCID     string
	Hash            string
	Traceparent     string
	Tracestate      string
}

// TestSetCreated is the input shape for PublishTestSetCreated.
type TestSetCreated struct {
	TenantID    string
	GCID        string // typically the author GCID
	TestSetID   string
	AuthorGCID  string
	Title       string
	Traceparent string
	Tracestate  string
}

// TestSetQuestionAdded is the input shape for PublishTestSetQuestionAdded.
type TestSetQuestionAdded struct {
	TenantID          string
	GCID              string
	TestSetID         string
	TestSetQuestionID string
	QuestionAtomID    string
	QuestionType      string
	DisplayOrder      int32
	Points            float64
	Traceparent       string
	Tracestate        string
}

// TestSetQuestionUpdated is the input shape for PublishTestSetQuestionUpdated.
type TestSetQuestionUpdated struct {
	TenantID          string
	GCID              string
	TestSetID         string
	TestSetQuestionID string
	DisplayOrder      int32
	Points            float64
	Traceparent       string
	Tracestate        string
}

// TestSetQuestionRemoved is the input shape for PublishTestSetQuestionRemoved.
type TestSetQuestionRemoved struct {
	TenantID          string
	GCID              string
	TestSetID         string
	TestSetQuestionID string
	Traceparent       string
	Tracestate        string
}

// TestSetPublished is the input shape for PublishTestSetPublished.
type TestSetPublished struct {
	TenantID      string
	GCID          string
	TestSetID     string
	AuthorGCID    string
	QuestionCount int32
	TotalPoints   float64
	Traceparent   string
	Tracestate    string
}

// Publisher is the publisher contract — production swaps the in-memory
// implementation for a Cloud Pub/Sub one without changing callers.
type Publisher interface {
	PublishCourseCreated(CourseCreated) (PublishedEvent, error)
	PublishCoursePublished(CoursePublished) (PublishedEvent, error)
	PublishEnrollmentCreated(EnrollmentCreated) (PublishedEvent, error)
	PublishEnrollmentCancelled(EnrollmentCancelled) (PublishedEvent, error)
	PublishEnrollmentCompleted(EnrollmentCompleted) (PublishedEvent, error)
	PublishBookingConfirmed(BookingConfirmed) (PublishedEvent, error)
	PublishCertificationIssued(CertificationIssued) (PublishedEvent, error)
	// PublishApplicationStateChanged — S6.1, emits per-state event for the
	// Course Application aggregate. Routes per application.EventTopicFor.
	PublishApplicationStateChanged(app *application.Application, traceparent string) (PublishedEvent, error)

	// TestSet authoring lifecycle (delivery-test-sets.yaml Lane A).
	PublishTestSetCreated(TestSetCreated) (PublishedEvent, error)
	PublishTestSetQuestionAdded(TestSetQuestionAdded) (PublishedEvent, error)
	PublishTestSetQuestionUpdated(TestSetQuestionUpdated) (PublishedEvent, error)
	PublishTestSetQuestionRemoved(TestSetQuestionRemoved) (PublishedEvent, error)
	PublishTestSetPublished(TestSetPublished) (PublishedEvent, error)

	// PublishLiveQuizScoreAwarded — ADR-168 durable score event.
	PublishLiveQuizScoreAwarded(LiveQuizScoreAwarded) (PublishedEvent, error)

	// PublishLiveQuizSessionStarted / Ended — ADR-168 durable session lifecycle.
	PublishLiveQuizSessionStarted(LiveQuizSessionStarted) (PublishedEvent, error)
	PublishLiveQuizSessionEnded(LiveQuizSessionEnded) (PublishedEvent, error)
}

// LiveQuizSessionStarted is the durable ARMED→LIVE lifecycle event (ADR-168).
// GCID is the instructor who opened the room.
type LiveQuizSessionStarted struct {
	TenantID       string
	GCID           string // instructor GCID (the actor)
	SessionID      string
	LiveQuizID     string
	InstructorGCID string
	StartedAt      time.Time
	Traceparent    string
	Tracestate     string
}

// LiveQuizSessionEnded is the durable LIVE→CLOSED lifecycle event (ADR-168).
type LiveQuizSessionEnded struct {
	TenantID       string
	GCID           string // instructor GCID (the actor)
	SessionID      string
	LiveQuizID     string
	TotalResponses int
	EndedAt        time.Time
	Traceparent    string
	Tracestate     string
}

// LiveQuizScoreAwarded is the durable per-answer scoring event (ADR-168).
type LiveQuizScoreAwarded struct {
	TenantID        string
	GCID            string // learner GCID (also the leaderboard member)
	SessionID       string
	LiveQuizID      string
	QuestionID      string
	AwardedPoints   int
	CumulativeScore int
	Correct         bool
	AnswerMillis    int64
	// CR2-C3: when the answered question linked a LearningAtom, these flow to
	// chora-consumption's derived-weakness subscriber (empty/nil for ad-hoc).
	AtomID      string
	TopicTags   []string
	Traceparent string
	Tracestate  string
}

// InMemoryPublisher is the MVP publisher — captures all published events
// in memory for assertion + later replay during demo.
//
// Source-project + source-service are set at construction time per
// .claude/rules/ddd-enforcement.md "no inline config" rule (these come
// from env vars in the wiring layer).
type InMemoryPublisher struct {
	sourceProject string
	sourceService string

	mu      sync.Mutex
	history []PublishedEvent
}

// NewInMemoryPublisher returns a publisher pinned to a (project, service).
//
// The two strings come from env vars at the wiring layer — never inline
// per feedback_no_inline_config.
func NewInMemoryPublisher(sourceProject, sourceService string) *InMemoryPublisher {
	return &InMemoryPublisher{
		sourceProject: sourceProject,
		sourceService: sourceService,
	}
}

// PublishCourseCreated emits chora.delivery.course.created.v1.
func (p *InMemoryPublisher) PublishCourseCreated(in CourseCreated) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: in.CourseID, // course_id is the natural dedup key
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"course_id":       in.CourseID,
		"title":           in.Title,
		"instructor_gcid": in.InstructorGCID,
		"public":          in.Public,
		"price_sgd_cents": in.PriceSGDCents,
		"created_at":      now.Format(time.RFC3339Nano),
	}
	ev := PublishedEvent{Topic: TopicCourseCreated, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishEnrollmentCreated emits chora.delivery.enrollment.created.v1.
//
// Idempotency key is deterministic on (course_id, gcid) so subscribers
// dedupe replays at the bus — duplicate enrolment POSTs surface the same
// idempotency_key even when the in-memory enrolment ID differs.
func (p *InMemoryPublisher) PublishLiveQuizScoreAwarded(in LiveQuizScoreAwarded) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return PublishedEvent{}, errors.New("session_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("score:%s:%s:%s", in.SessionID, in.QuestionID, in.GCID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"session_id":           in.SessionID,
		"live_quiz_id":         in.LiveQuizID,
		"question_id":          in.QuestionID,
		"awarded_points":       in.AwardedPoints,
		"cumulative_score":     in.CumulativeScore,
		"correct":              in.Correct,
		"answer_millis":        in.AnswerMillis,
		"learner_gcid":         in.GCID,
		"chora_imda_dimension": "accountability", // D1 — ADR-141 canonical
		"atom_id":              in.AtomID,        // CR2-C3 (empty for ad-hoc Qs)
		"topic_tags":           in.TopicTags,     // CR2-C3 (nil for ad-hoc Qs)
	}
	ev := PublishedEvent{Topic: TopicLiveQuizScoreAwarded, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishLiveQuizSessionStarted emits chora.delivery.live_quiz_session.started.v1.
func (p *InMemoryPublisher) PublishLiveQuizSessionStarted(in LiveQuizSessionStarted) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return PublishedEvent{}, errors.New("session_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("session_started:%s", in.SessionID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"session_id":      in.SessionID,
		"live_quiz_id":    in.LiveQuizID,
		"instructor_gcid": in.InstructorGCID,
		"started_at":      in.StartedAt.UTC(),
	}
	ev := PublishedEvent{Topic: TopicLiveQuizSessionStarted, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishLiveQuizSessionEnded emits chora.delivery.live_quiz_session.ended.v1.
func (p *InMemoryPublisher) PublishLiveQuizSessionEnded(in LiveQuizSessionEnded) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return PublishedEvent{}, errors.New("session_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("session_ended:%s", in.SessionID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"session_id":      in.SessionID,
		"live_quiz_id":    in.LiveQuizID,
		"total_responses": in.TotalResponses,
		"ended_at":        in.EndedAt.UTC(),
	}
	ev := PublishedEvent{Topic: TopicLiveQuizSessionEnded, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

func (p *InMemoryPublisher) PublishEnrollmentCreated(in EnrollmentCreated) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("enrollment:%s:%s", in.CourseID, in.LearnerGCID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"enrollment_id":        in.EnrollmentID,
		"course_id":            in.CourseID,
		"learner_gcid":         in.LearnerGCID,
		"enrolled_at":          now.Format(time.RFC3339Nano),
		"chora_imda_dimension": "accountability", // D1 — ADR-141 canonical
	}
	ev := PublishedEvent{Topic: TopicEnrollmentCreated, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishCoursePublished emits chora.delivery.course.published.v1 — fired
// when visibility flips into VisibilityPublic. chora-sharing subscribes
// for the discovery feed; chora-observability captures the D1+D2 IMDA
// evidence row (transparency).
func (p *InMemoryPublisher) PublishCoursePublished(in CoursePublished) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("course:%s:published", in.CourseID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"course_id":             in.CourseID,
		"title":                 in.Title,
		"instructor_gcid":       in.InstructorGCID,
		"published_at":          now.Format(time.RFC3339Nano),
		"chora_imda_dimensions": []string{"accountability", "transparency"}, // D1 + D2
	}
	ev := PublishedEvent{Topic: TopicCoursePublished, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishEnrollmentCancelled emits chora.delivery.enrollment.cancelled.v1.
func (p *InMemoryPublisher) PublishEnrollmentCancelled(in EnrollmentCancelled) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("enrollment:%s:%s:cancelled", in.CourseID, in.LearnerGCID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"enrollment_id": in.EnrollmentID,
		"course_id":     in.CourseID,
		"learner_gcid":  in.LearnerGCID,
		"reason":        in.Reason,
		"cancelled_at":  now.Format(time.RFC3339Nano),
	}
	ev := PublishedEvent{Topic: TopicEnrollmentCancelled, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishEnrollmentCompleted emits chora.delivery.enrollment.completed.v1.
//
// Idempotency key is deterministic on (course_id, learner_gcid) with a
// ":completed" suffix — consistent with the created (no suffix) + cancelled
// (":cancelled") sibling keys — so a redelivered completion of the same
// enrollment dedupes at the bus even when the in-memory enrollment_id differs.
//
// completed_at defaults to publish time when the caller leaves it zero. The
// payload carries the canonical IMDA D1 accountability tag (ADR-141), mirroring
// enrollment.created — this is a per-learner course-completion evidence fact.
func (p *InMemoryPublisher) PublishEnrollmentCompleted(in EnrollmentCompleted) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	completedAt := in.CompletedAt
	if completedAt.IsZero() {
		completedAt = now
	}
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("enrollment:%s:%s:completed", in.CourseID, in.LearnerGCID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"enrollment_id":        in.EnrollmentID,
		"course_id":            in.CourseID,
		"learner_gcid":         in.LearnerGCID,
		"passed":               in.Passed,
		"completed_at":         completedAt.Format(time.RFC3339Nano),
		"chora_imda_dimension": "accountability", // D1 — ADR-141 canonical
	}
	ev := PublishedEvent{Topic: TopicEnrollmentCompleted, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishBookingConfirmed emits chora.delivery.booking.confirmed.v1.
func (p *InMemoryPublisher) PublishBookingConfirmed(in BookingConfirmed) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("booking:%s:confirmed", in.BookingID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"booking_id":   in.BookingID,
		"course_id":    in.CourseID,
		"class_id":     in.ClassID,
		"learner_gcid": in.LearnerGCID,
		"seat_number":  in.SeatNumber,
		"confirmed_at": now.Format(time.RFC3339Nano),
	}
	ev := PublishedEvent{Topic: TopicBookingConfirmed, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishCertificationIssued emits chora.delivery.certification.issued.v1.
func (p *InMemoryPublisher) PublishCertificationIssued(in CertificationIssued) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	hash := in.Hash
	if hash == "" {
		hash = "no-hash"
	}
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("cert:%s:%s:%s", in.CourseID, in.LearnerGCID, hash),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"certification_id": in.CertificationID,
		"course_id":        in.CourseID,
		"learner_gcid":     in.LearnerGCID,
		"hash":             in.Hash,
		"issued_at":        now.Format(time.RFC3339Nano),
	}
	ev := PublishedEvent{Topic: TopicCertificationIssued, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishTestSetCreated emits chora.delivery.test_set.created.v1.
//
// Idempotency key = test_set_id (the natural dedup key — re-create POST on
// the same TestSet replays the same event_id at the bus).
func (p *InMemoryPublisher) PublishTestSetCreated(in TestSetCreated) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("test_set:%s:created", in.TestSetID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"test_set_id": in.TestSetID,
		"tenant_id":   in.TenantID,
		"author_gcid": in.AuthorGCID,
		"title":       in.Title,
		"created_at":  now.Format(time.RFC3339Nano),
	}
	ev := PublishedEvent{Topic: TopicTestSetCreated, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishTestSetQuestionAdded emits chora.delivery.test_set.question_added.v1.
func (p *InMemoryPublisher) PublishTestSetQuestionAdded(in TestSetQuestionAdded) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("test_set:%s:question_added:%s", in.TestSetID, in.TestSetQuestionID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"test_set_id":          in.TestSetID,
		"test_set_question_id": in.TestSetQuestionID,
		"question_atom_id":     in.QuestionAtomID,
		"question_type":        in.QuestionType,
		"display_order":        in.DisplayOrder,
		"points":               in.Points,
		"added_at":             now.Format(time.RFC3339Nano),
	}
	ev := PublishedEvent{Topic: TopicTestSetQuestionAdded, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishTestSetQuestionUpdated emits chora.delivery.test_set.question_updated.v1.
func (p *InMemoryPublisher) PublishTestSetQuestionUpdated(in TestSetQuestionUpdated) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("test_set:%s:question_updated:%s:%d", in.TestSetID, in.TestSetQuestionID, now.UnixMilli()),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"test_set_id":          in.TestSetID,
		"test_set_question_id": in.TestSetQuestionID,
		"display_order":        in.DisplayOrder,
		"points":               in.Points,
		"updated_at":           now.Format(time.RFC3339Nano),
	}
	ev := PublishedEvent{Topic: TopicTestSetQuestionUpdated, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishTestSetQuestionRemoved emits chora.delivery.test_set.question_removed.v1.
func (p *InMemoryPublisher) PublishTestSetQuestionRemoved(in TestSetQuestionRemoved) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("test_set:%s:question_removed:%s", in.TestSetID, in.TestSetQuestionID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"test_set_id":          in.TestSetID,
		"test_set_question_id": in.TestSetQuestionID,
		"removed_at":           now.Format(time.RFC3339Nano),
	}
	ev := PublishedEvent{Topic: TopicTestSetQuestionRemoved, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// PublishTestSetPublished emits chora.delivery.test_set.published.v1.
//
// Idempotency_key is deterministic on test_set_id so subscribers dedupe
// replays at the bus — duplicate publish-POSTs surface the same key.
func (p *InMemoryPublisher) PublishTestSetPublished(in TestSetPublished) (PublishedEvent, error) {
	if strings.TrimSpace(in.TenantID) == "" {
		return PublishedEvent{}, errors.New("tenant_id required")
	}
	now := time.Now().UTC()
	env := EventEnvelope{
		EventID:        domain.NewUUIDv7(),
		IdempotencyKey: fmt.Sprintf("test_set:%s:published", in.TestSetID),
		TenantID:       in.TenantID,
		GCID:           in.GCID,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    p.ensureTraceparent(in.Traceparent),
		Tracestate:     in.Tracestate,
		SourceProject:  p.sourceProject,
		SourceService:  p.sourceService,
		SchemaVersion:  schemaVersion,
	}
	payload := map[string]interface{}{
		"test_set_id":    in.TestSetID,
		"tenant_id":      in.TenantID,
		"author_gcid":    in.AuthorGCID,
		"question_count": in.QuestionCount,
		"total_points":   in.TotalPoints,
		"published_at":   now.Format(time.RFC3339Nano),
	}
	ev := PublishedEvent{Topic: TopicTestSetPublished, Envelope: env, Payload: payload}
	p.append(ev)
	return ev, nil
}

// History returns a copy of every event published so far.
func (p *InMemoryPublisher) History() []PublishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PublishedEvent, len(p.history))
	copy(out, p.history)
	return out
}

func (p *InMemoryPublisher) append(ev PublishedEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.history = append(p.history, ev)
}

// ensureTraceparent mints a fresh W3C traceparent if the caller did not
// provide one. Cloud Trace requires propagation across Pub/Sub per
// CLAUDE.md §6.
func (p *InMemoryPublisher) ensureTraceparent(in string) string {
	if strings.TrimSpace(in) != "" {
		return in
	}
	traceID := randHex(16)
	spanID := randHex(8)
	return fmt.Sprintf("00-%s-%s-01", traceID, spanID)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback — derive from monotonic time so the field stays non-empty
		// under entropy starvation. Cloud Run / CI never hit this branch.
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> uint(8*(i%8)))
		}
	}
	return hex.EncodeToString(b)
}
