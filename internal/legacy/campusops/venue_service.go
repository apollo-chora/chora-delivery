package campusops

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// VenueService manages Venue and Room CRUD operations.
type VenueService struct {
	venues VenueRepository
	rooms  RoomRepository
	events EventPublisher
}

// NewVenueService creates a VenueService with the given repositories and event publisher.
func NewVenueService(venues VenueRepository, rooms RoomRepository, events EventPublisher) *VenueService {
	return &VenueService{venues: venues, rooms: rooms, events: events}
}

// ---------------------------------------------------------------------------
// Venue CRUD
// ---------------------------------------------------------------------------

// CreateVenue validates and persists a new Venue with UUIDv7 ID.
func (s *VenueService) CreateVenue(ctx context.Context, venue *Venue) (*Venue, error) {
	if venue.Name == "" {
		return nil, fmt.Errorf("name must not be empty: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	venue.ID = uuid.Must(uuid.NewV7())
	venue.IsActive = true
	venue.CreatedAt = now
	venue.UpdatedAt = now

	if err := s.venues.Create(ctx, venue); err != nil {
		return nil, err
	}
	return venue, nil
}

// GetVenue retrieves a venue by ID and tenant.
func (s *VenueService) GetVenue(ctx context.Context, id, tenantID uuid.UUID) (*Venue, error) {
	venue, err := s.venues.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if venue == nil {
		return nil, ErrVenueNotFound
	}
	return venue, nil
}

// ListVenues returns venues with cursor-based pagination.
func (s *VenueService) ListVenues(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Venue, error) {
	return s.venues.List(ctx, tenantID, cursor, limit)
}

// UpdateVenue applies mutable field changes to an existing venue.
func (s *VenueService) UpdateVenue(ctx context.Context, venue *Venue) (*Venue, error) {
	existing, err := s.venues.GetByID(ctx, venue.ID, venue.TenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrVenueNotFound
	}

	if venue.Name == "" {
		return nil, fmt.Errorf("name must not be empty: %w", ErrValidationFailed)
	}

	existing.Name = venue.Name
	existing.Address = venue.Address
	existing.Campus = venue.Campus
	existing.Timezone = venue.Timezone
	existing.IsActive = venue.IsActive
	existing.UpdatedAt = time.Now().UTC()

	if err := s.venues.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// ---------------------------------------------------------------------------
// Room CRUD
// ---------------------------------------------------------------------------

// CreateRoom validates and persists a new Room within a venue.
func (s *VenueService) CreateRoom(ctx context.Context, room *Room) (*Room, error) {
	if room.Name == "" {
		return nil, fmt.Errorf("name must not be empty: %w", ErrValidationFailed)
	}
	if room.RoomCode == "" {
		return nil, fmt.Errorf("room_code must not be empty: %w", ErrValidationFailed)
	}
	if !room.RoomType.IsValid() {
		return nil, fmt.Errorf("invalid room_type %q: %w", room.RoomType, ErrValidationFailed)
	}
	if room.Capacity <= 0 {
		return nil, fmt.Errorf("capacity must be greater than 0: %w", ErrValidationFailed)
	}

	// Verify venue exists.
	venue, err := s.venues.GetByID(ctx, room.VenueID, room.TenantID)
	if err != nil {
		return nil, err
	}
	if venue == nil {
		return nil, ErrVenueNotFound
	}

	now := time.Now().UTC()
	room.ID = uuid.Must(uuid.NewV7())
	room.IsActive = true
	room.CreatedAt = now
	room.UpdatedAt = now

	if err := s.rooms.Create(ctx, room); err != nil {
		return nil, err
	}
	return room, nil
}

// ListRooms returns rooms for a venue with cursor-based pagination.
func (s *VenueService) ListRooms(ctx context.Context, venueID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Room, error) {
	return s.rooms.ListByVenue(ctx, venueID, tenantID, cursor, limit)
}

// DeleteVenue soft-deletes a venue.
func (s *VenueService) DeleteVenue(ctx context.Context, id, tenantID uuid.UUID) error {
	existing, err := s.venues.GetByID(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrVenueNotFound
	}
	return s.venues.Delete(ctx, id, tenantID)
}

// GetRoom retrieves a room by ID and tenant.
func (s *VenueService) GetRoom(ctx context.Context, id, tenantID uuid.UUID) (*Room, error) {
	room, err := s.rooms.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if room == nil {
		return nil, ErrRoomNotFound
	}
	return room, nil
}

// UpdateRoom applies mutable field changes to an existing room.
func (s *VenueService) UpdateRoom(ctx context.Context, room *Room) (*Room, error) {
	existing, err := s.rooms.GetByID(ctx, room.ID, room.TenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrRoomNotFound
	}

	if room.Name == "" {
		return nil, fmt.Errorf("name must not be empty: %w", ErrValidationFailed)
	}
	if room.RoomCode == "" {
		return nil, fmt.Errorf("room_code must not be empty: %w", ErrValidationFailed)
	}
	if !room.RoomType.IsValid() {
		return nil, fmt.Errorf("invalid room_type %q: %w", room.RoomType, ErrValidationFailed)
	}
	if room.Capacity <= 0 {
		return nil, fmt.Errorf("capacity must be greater than 0: %w", ErrValidationFailed)
	}

	existing.Name = room.Name
	existing.RoomCode = room.RoomCode
	existing.Capacity = room.Capacity
	existing.RoomType = room.RoomType
	existing.Floor = room.Floor
	existing.Building = room.Building
	existing.Amenities = room.Amenities
	existing.IsActive = room.IsActive
	existing.UpdatedAt = time.Now().UTC()

	if err := s.rooms.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// DeleteRoom soft-deletes a room.
func (s *VenueService) DeleteRoom(ctx context.Context, id, tenantID uuid.UUID) error {
	existing, err := s.rooms.GetByID(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrRoomNotFound
	}
	return s.rooms.Delete(ctx, id, tenantID)
}
