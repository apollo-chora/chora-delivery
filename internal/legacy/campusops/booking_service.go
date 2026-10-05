package campusops

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// BookingService manages FacilityBooking lifecycle.
type BookingService struct {
	bookings BookingRepository
	events   EventPublisher
}

// NewBookingService creates a BookingService with the given repository and event publisher.
func NewBookingService(bookings BookingRepository, events EventPublisher) *BookingService {
	return &BookingService{bookings: bookings, events: events}
}

// CreateBooking validates and persists a new FacilityBooking with UUIDv7 ID
// and confirmed status. Publishes a booking.confirmed event.
func (s *BookingService) CreateBooking(ctx context.Context, booking *FacilityBooking) (*FacilityBooking, error) {
	if booking.Title == "" {
		return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
	}
	if !booking.EndsAt.After(booking.StartsAt) {
		return nil, fmt.Errorf("ends_at must be after starts_at: %w", ErrBookingDateRange)
	}

	// Check for overlapping bookings in the same room.
	overlap, err := s.bookings.HasOverlap(ctx, booking.RoomID, booking.TenantID, booking.StartsAt, booking.EndsAt)
	if err != nil {
		return nil, err
	}
	if overlap {
		return nil, ErrBookingConflict
	}

	now := time.Now().UTC()
	booking.ID = uuid.Must(uuid.NewV7())
	booking.Status = BookingStatusConfirmed
	booking.CreatedAt = now
	booking.UpdatedAt = now

	if err := s.bookings.Create(ctx, booking); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventBookingConfirmed,
		booking.TenantID,
		&booking.BookedByGCID,
		booking.ID,
		AggregateFacilityBooking,
		map[string]interface{}{
			"room_id": booking.RoomID.String(),
			"title":   booking.Title,
		},
	)
	if err := s.events.Publish(ctx, TopicCampusEvents, evt); err != nil {
		return nil, fmt.Errorf("publish booking.confirmed event: %w", err)
	}

	return booking, nil
}

// ListBookings returns bookings with optional filters and cursor-based pagination.
func (s *BookingService) ListBookings(ctx context.Context, tenantID uuid.UUID, roomID *uuid.UUID, from, to *string, cursor *uuid.UUID, limit int) ([]FacilityBooking, error) {
	return s.bookings.List(ctx, tenantID, roomID, from, to, cursor, limit)
}

// GetBooking retrieves a booking by ID and tenant.
func (s *BookingService) GetBooking(ctx context.Context, id, tenantID uuid.UUID) (*FacilityBooking, error) {
	booking, err := s.bookings.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if booking == nil {
		return nil, ErrBookingNotFound
	}
	return booking, nil
}

// DeleteBooking soft-deletes (cancels) a booking and publishes a booking.cancelled event.
func (s *BookingService) DeleteBooking(ctx context.Context, id, tenantID uuid.UUID) error {
	existing, err := s.bookings.GetByID(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrBookingNotFound
	}
	if err := s.bookings.Delete(ctx, id, tenantID); err != nil {
		return err
	}

	evt := NewDomainEvent(
		EventBookingCancelled,
		tenantID,
		&existing.BookedByGCID,
		existing.ID,
		AggregateFacilityBooking,
		map[string]interface{}{
			"room_id": existing.RoomID.String(),
			"title":   existing.Title,
		},
	)
	_ = s.events.Publish(ctx, TopicCampusEvents, evt)

	return nil
}

// GetRoomAvailability returns bookings for a room on a specific date.
func (s *BookingService) GetRoomAvailability(ctx context.Context, roomID, tenantID uuid.UUID, date string) ([]FacilityBooking, error) {
	return s.bookings.ListByRoomAndDate(ctx, roomID, tenantID, date)
}

// CheckConflict returns true if the given time range conflicts with existing bookings.
func (s *BookingService) CheckConflict(ctx context.Context, roomID, tenantID uuid.UUID, startsAt, endsAt interface{}) (bool, error) {
	return s.bookings.HasOverlap(ctx, roomID, tenantID, startsAt, endsAt)
}
