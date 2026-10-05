package campusops

import (
	"time"

	"github.com/google/uuid"
)

// DomainEvent is the envelope for all events published by the campusops service.
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

// TopicCampusEvents is the Cloud Pub/Sub topic for all campus domain events.
//
// M12.3.E (2026-05-12): migrated from "chora.campus.events" to the
// canonical chora.{domain}.{aggregate}.{event_type}.v{N} form. Campusops
// is hosted under chora-delivery (5-core domain consolidation per M12.2);
// the topic moves under the delivery domain segment.
const TopicCampusEvents = "chora.delivery.campus.events.v1"

// Campus domain event type constants.
const (
	EventAttendanceRecorded   = "campus.attendance.recorded"
	EventSectionCreated       = "campus.section.created"
	EventSectionCancelled     = "campus.section.cancelled"
	EventBookingConfirmed     = "campus.booking.confirmed"
	EventBookingCancelled     = "campus.booking.cancelled"
	EventEnrollmentConfirmed  = "campus.enrollment.confirmed"
	EventEnrollmentWaitlisted = "campus.enrollment.waitlisted"
	EventEnrollmentDropped    = "campus.enrollment.dropped"
	EventEnrollmentPromoted   = "campus.enrollment.promoted"
	EventTimetablePublished   = "campus.timetable.published"
)

// Aggregate type constants for domain events.
const (
	AggregateClassSection         = "ClassSection"
	AggregateFacilityBooking      = "FacilityBooking"
	AggregateSectionEnrollment    = "SectionEnrollment"
	AggregateTimetablePublication = "TimetablePublication"
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
