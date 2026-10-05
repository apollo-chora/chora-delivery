package campusops

import (
	"context"

	"github.com/google/uuid"
)

// AcademicTermRepository defines the data access interface for AcademicTerm entities.
type AcademicTermRepository interface {
	// Create persists a new academic term.
	Create(ctx context.Context, term *AcademicTerm) error

	// GetByID retrieves an academic term by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*AcademicTerm, error)

	// List returns academic terms for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]AcademicTerm, error)

	// Update saves changes to an existing academic term.
	Update(ctx context.Context, term *AcademicTerm) error

	// Delete soft-deletes an academic term.
	Delete(ctx context.Context, id, tenantID uuid.UUID) error

	// GetCurrent returns the currently active term for a tenant based on date range.
	GetCurrent(ctx context.Context, tenantID uuid.UUID) (*AcademicTerm, error)

	// HasOverlap checks whether a term overlaps with existing terms for the tenant.
	HasOverlap(ctx context.Context, tenantID uuid.UUID, startsAt, endsAt interface{}, excludeID *uuid.UUID) (bool, error)
}

// VenueRepository defines the data access interface for Venue entities.
type VenueRepository interface {
	// Create persists a new venue.
	Create(ctx context.Context, venue *Venue) error

	// GetByID retrieves a venue by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*Venue, error)

	// List returns venues for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Venue, error)

	// Update saves changes to an existing venue.
	Update(ctx context.Context, venue *Venue) error

	// Delete soft-deletes a venue.
	Delete(ctx context.Context, id, tenantID uuid.UUID) error
}

// RoomRepository defines the data access interface for Room entities.
type RoomRepository interface {
	// Create persists a new room.
	Create(ctx context.Context, room *Room) error

	// GetByID retrieves a room by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*Room, error)

	// ListByVenue returns rooms for a venue with cursor-based pagination.
	ListByVenue(ctx context.Context, venueID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Room, error)

	// Update saves changes to an existing room.
	Update(ctx context.Context, room *Room) error

	// Delete soft-deletes a room.
	Delete(ctx context.Context, id, tenantID uuid.UUID) error
}

// ClassSectionRepository defines the data access interface for ClassSection entities.
type ClassSectionRepository interface {
	// Create persists a new class section.
	Create(ctx context.Context, section *ClassSection) error

	// GetByID retrieves a class section by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*ClassSection, error)

	// List returns class sections for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]ClassSection, error)

	// ListByTerm returns class sections for a specific term.
	ListByTerm(ctx context.Context, termID, tenantID uuid.UUID) ([]ClassSection, error)

	// Update saves changes to an existing class section.
	Update(ctx context.Context, section *ClassSection) error

	// Delete soft-deletes a class section.
	Delete(ctx context.Context, id, tenantID uuid.UUID) error
}

// TimeSlotRepository defines the data access interface for SectionTimeSlot entities.
type TimeSlotRepository interface {
	// Create persists a new time slot entry.
	Create(ctx context.Context, slot *SectionTimeSlot) error

	// GetByID retrieves a time slot by ID.
	GetByID(ctx context.Context, id uuid.UUID) (*SectionTimeSlot, error)

	// ListBySection returns all time slots for a class section.
	ListBySection(ctx context.Context, sectionID uuid.UUID) ([]SectionTimeSlot, error)

	// Update saves changes to an existing time slot.
	Update(ctx context.Context, slot *SectionTimeSlot) error

	// Delete soft-deletes a time slot.
	Delete(ctx context.Context, id uuid.UUID) error
}

// AttendanceRepository defines the data access interface for Attendance records.
type AttendanceRepository interface {
	// Create persists a batch of attendance records (append-only).
	Create(ctx context.Context, records []Attendance) error

	// ListBySection returns attendance records for a class section with cursor-based pagination.
	ListBySection(ctx context.Context, sectionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Attendance, error)
}

// BookingRepository defines the data access interface for FacilityBooking entities.
type BookingRepository interface {
	// Create persists a new facility booking.
	Create(ctx context.Context, booking *FacilityBooking) error

	// GetByID retrieves a booking by ID within a tenant.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*FacilityBooking, error)

	// List returns bookings with optional filters and cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, roomID *uuid.UUID, from, to *string, cursor *uuid.UUID, limit int) ([]FacilityBooking, error)

	// ListByRoomAndDate returns bookings for a specific room on a given date.
	ListByRoomAndDate(ctx context.Context, roomID, tenantID uuid.UUID, date string) ([]FacilityBooking, error)

	// HasOverlap checks whether a booking conflicts with existing bookings for the same room.
	HasOverlap(ctx context.Context, roomID, tenantID uuid.UUID, startsAt, endsAt interface{}) (bool, error)

	// Delete soft-deletes a booking.
	Delete(ctx context.Context, id, tenantID uuid.UUID) error
}

// EnrollmentRepository defines the data access interface for SectionEnrollment entities.
type EnrollmentRepository interface {
	// Create persists a new enrollment.
	Create(ctx context.Context, enrollment *SectionEnrollment) error

	// GetByLearnerAndSection retrieves an enrollment by learner GCID and section.
	GetByLearnerAndSection(ctx context.Context, learnerGCID, sectionID, tenantID uuid.UUID) (*SectionEnrollment, error)

	// ListBySection returns enrollments for a section with cursor-based pagination.
	ListBySection(ctx context.Context, sectionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SectionEnrollment, error)

	// ListWaitlisted returns waitlisted enrollments for a section ordered by position.
	ListWaitlisted(ctx context.Context, sectionID, tenantID uuid.UUID) ([]SectionEnrollment, error)

	// ListBySectionAndStatus returns enrollments for a section filtered by status.
	ListBySectionAndStatus(ctx context.Context, sectionID, tenantID uuid.UUID, status EnrollmentStatus) ([]SectionEnrollment, error)

	// ListByLearnerAndTerm returns enrollments for a learner in sections belonging to a term.
	ListByLearnerAndTerm(ctx context.Context, learnerGCID, termID, tenantID uuid.UUID) ([]SectionEnrollment, error)

	// Update saves changes to an existing enrollment.
	Update(ctx context.Context, enrollment *SectionEnrollment) error

	// CountEnrolled returns the count of enrolled (not waitlisted/dropped) learners in a section.
	CountEnrolled(ctx context.Context, sectionID, tenantID uuid.UUID) (int, error)

	// NextWaitlistPosition returns the next available waitlist position for a section.
	NextWaitlistPosition(ctx context.Context, sectionID, tenantID uuid.UUID) (int, error)
}

// TimetableRepository defines the data access interface for TimetablePublication entities.
type TimetableRepository interface {
	// Create persists a new timetable publication.
	Create(ctx context.Context, pub *TimetablePublication) error

	// GetLatest returns the latest published timetable for a term.
	GetLatest(ctx context.Context, termID, tenantID uuid.UUID) (*TimetablePublication, error)

	// ListByTerm returns all timetable publications for a term.
	ListByTerm(ctx context.Context, termID, tenantID uuid.UUID) ([]TimetablePublication, error)

	// GetMaxVersion returns the highest version number for a term's timetable.
	GetMaxVersion(ctx context.Context, termID, tenantID uuid.UUID) (int, error)

	// SupersedeAll marks all existing published timetables for a term as superseded.
	SupersedeAll(ctx context.Context, termID, tenantID uuid.UUID) error
}

// EventPublisher abstracts the event bus (Cloud Pub/Sub in production,
// in-memory or emulator in tests/local dev).
type EventPublisher interface {
	// Publish sends a domain event to the specified topic.
	Publish(ctx context.Context, topic string, event interface{}) error

	// Close releases resources held by the publisher.
	Close() error
}
