package classroom

import (
	"time"

	"github.com/google/uuid"
)

// DomainEvent is the envelope for all events published by the classroom service.
// Mirrors the chora-core DomainEvent structure.
type DomainEvent struct {
	EventID       uuid.UUID              `json:"event_id"`
	EventType     string                 `json:"event_type"`
	Timestamp     time.Time              `json:"timestamp"`
	TenantID      uuid.UUID              `json:"tenant_id"`
	GCID          *uuid.UUID             `json:"gcid,omitempty"`
	AggregateID   uuid.UUID              `json:"aggregate_id"`
	AggregateType string                 `json:"aggregate_type"`
	Payload       map[string]interface{} `json:"payload"`
	CorrelationID *uuid.UUID             `json:"correlation_id,omitempty"`
	CausationID   *uuid.UUID             `json:"causation_id,omitempty"`
}

// TopicClassroomEvents is the Cloud Pub/Sub topic for all classroom domain events.
//
// M12.3.E (2026-05-12): migrated from "chora.classroom.events" to canonical
// chora.{domain}.{aggregate}.{event_type}.v{N} form. Classroom is hosted
// under chora-delivery (5-core consolidation per M12.2).
const TopicClassroomEvents = "chora.delivery.classroom.events.v1"

// Aggregate type constants.
const (
	AggregateLiveQuizSession  = "LiveQuizSession"
	AggregateLivePollSession  = "LivePollSession"
	AggregateJamBoard         = "JamBoard"
	AggregateClassroomSession = "ClassroomSession"
)

// Classroom domain event type constants.
const (
	EventQuizStarted             = "classroom.quiz.started"
	EventQuizEnded               = "classroom.quiz.ended"
	EventQuizAnswerSubmitted     = "classroom.quiz.answer.submitted"
	EventQuizPaused              = "classroom.quiz.paused"
	EventQuizResumed             = "classroom.quiz.resumed"
	EventQuizEliminationExecuted = "classroom.quiz.elimination.executed"
	EventPollCreated             = "classroom.poll.created"
	EventPollVoteCast            = "classroom.poll.vote.cast"
	EventJamBoardEntryAdded      = "classroom.jamboard.entry.added"
	EventSessionStarted          = "classroom.session.started"
	EventSessionEnded            = "classroom.session.ended"
	EventDiscussionPosted        = "classroom.discussion.posted"
)

// NewDomainEvent creates a new DomainEvent with a generated UUIDv7 event ID
// and the current timestamp.
func NewDomainEvent(
	eventType string,
	tenantID uuid.UUID,
	gcid *uuid.UUID,
	aggregateID uuid.UUID,
	aggregateType string,
	payload map[string]interface{},
) DomainEvent {
	return DomainEvent{
		EventID:       uuid.Must(uuid.NewV7()),
		EventType:     eventType,
		Timestamp:     time.Now().UTC(),
		TenantID:      tenantID,
		GCID:          gcid,
		AggregateID:   aggregateID,
		AggregateType: aggregateType,
		Payload:       payload,
	}
}
