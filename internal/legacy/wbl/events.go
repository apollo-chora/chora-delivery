package wbl

import (
	"time"

	"github.com/google/uuid"
)

// DomainEvent is the envelope for all events published by the WBL service.
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

// TopicWBLEvents is the Cloud Pub/Sub topic for all WBL domain events.
//
// M12.3.E (2026-05-12): migrated from "chora.wbl.events" to canonical
// chora.{domain}.{aggregate}.{event_type}.v{N} form. WBL is hosted under
// chora-delivery (5-core consolidation per M12.2).
const TopicWBLEvents = "chora.delivery.wbl.events.v1"

// WBL domain event type constants.
const (
	EventApplicationSubmitted = "wbl.application.submitted"
	EventPlacementStarted     = "wbl.placement.started"
	EventPlacementCompleted   = "wbl.placement.completed"
	EventCapstoneSubmitted    = "wbl.capstone.submitted"
	EventSkillEndorsed        = "wbl.skill.endorsed"
	EventWorkLogAdded         = "wbl.worklog.added"
	EventWBLLogSubmitted      = "wbl.log.submitted"
	EventWBLLogApproved       = "wbl.log.approved"
	EventWBLLogRejected       = "wbl.log.rejected"
)

// Aggregate type constants for domain events.
const (
	AggregateInternship      = "Internship"
	AggregatePlacement       = "Placement"
	AggregateCapstoneProject = "CapstoneProject"
	AggregateWBLLog          = "WBLLog"
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
