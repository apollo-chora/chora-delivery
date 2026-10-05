package training_admin

import (
	"time"

	"github.com/google/uuid"
)

// DomainEvent is the envelope for all events published by the training-admin service.
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

// TopicTrainingEvents is the Cloud Pub/Sub topic for all training domain events.
//
// M12.3.E (2026-05-12): migrated from "chora.training.events" to canonical
// chora.{domain}.{aggregate}.{event_type}.v{N} form. Training admin is
// hosted under chora-delivery (5-core consolidation per M12.2).
const TopicTrainingEvents = "chora.delivery.training.events.v1"

// Training domain event type constants.
const (
	EventSessionCreated       = "training.session.created"
	EventSessionUpdated       = "training.session.updated"
	EventSessionStarted       = "training.session.started"
	EventSessionCancelled     = "training.session.cancelled"
	EventSessionCompleted     = "training.session.completed"
	EventAttendanceRecorded   = "training.attendance.recorded"
	EventApplicationSubmitted = "training.application.submitted"
	EventApplicationDecided   = "training.application.decided"
	EventRequestSubmitted     = "training.request.submitted"
	EventRequestDecided       = "training.request.decided"
	EventApplicationTimedOut  = "training.application.timed_out"
	EventRequestTimedOut      = "training.request.timed_out"
	EventCertificateIssued    = "training.certificate.issued"
	EventResultPublished      = "training.result.published"
	EventCertificateRequested = "training.certificate.requested"
	EventAppealSubmitted      = "training.appeal.submitted"
	EventAppealResolved       = "training.appeal.resolved"
)

// Aggregate type constants for domain events.
const (
	AggregateTrainingSession     = "TrainingSession"
	AggregateTrainingApplication = "TrainingApplication"
	AggregateCertificateProgram  = "CertificateProgram"
	AggregateLearnerResult       = "LearnerResult"
	AggregateCertificateRequest  = "CertificateRequest"
	AggregateStudentAppeal       = "StudentAppeal"
	AggregateSignageDevice       = "SignageDevice"
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
