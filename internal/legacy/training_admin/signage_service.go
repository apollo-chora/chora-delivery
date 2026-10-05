package training_admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SignageService manages digital signage device registration, content delivery,
// and heartbeat monitoring.
type SignageService struct {
	deviceRepo SignageDeviceRepository
	events     EventPublisher
}

// NewSignageService creates a SignageService with the given repository and
// event publisher.
func NewSignageService(
	deviceRepo SignageDeviceRepository,
	events EventPublisher,
) *SignageService {
	return &SignageService{
		deviceRepo: deviceRepo,
		events:     events,
	}
}

// ---------------------------------------------------------------------------
// Device CRUD
// ---------------------------------------------------------------------------

// RegisterDevice creates a new signage device with a unique device token.
func (s *SignageService) RegisterDevice(ctx context.Context, device *SignageDevice) (*SignageDevice, error) {
	if device.DeviceName == "" {
		return nil, fmt.Errorf("device_name is required: %w", ErrValidationFailed)
	}
	if !device.DisplayMode.IsValid() {
		return nil, fmt.Errorf("invalid display_mode %q: %w", device.DisplayMode, ErrValidationFailed)
	}

	now := time.Now().UTC()
	device.ID = uuid.Must(uuid.NewV7())
	device.DeviceToken = generateDeviceToken()
	device.Status = DeviceStatusOffline
	device.CreatedAt = now
	device.UpdatedAt = now

	if err := s.deviceRepo.Create(ctx, device); err != nil {
		return nil, err
	}

	return device, nil
}

// GetDevice retrieves a signage device by ID within a tenant.
func (s *SignageService) GetDevice(ctx context.Context, id, tenantID uuid.UUID) (*SignageDevice, error) {
	device, err := s.deviceRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return nil, ErrDeviceNotFound
	}
	return device, nil
}

// GetDeviceByToken retrieves a signage device by its device token.
func (s *SignageService) GetDeviceByToken(ctx context.Context, token string) (*SignageDevice, error) {
	device, err := s.deviceRepo.GetByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return nil, ErrDeviceNotFound
	}
	return device, nil
}

// ListDevices returns signage devices for a tenant with cursor-based pagination.
func (s *SignageService) ListDevices(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SignageDevice, error) {
	return s.deviceRepo.List(ctx, tenantID, cursor, limit)
}

// UpdateDevice applies mutable field changes to an existing signage device.
func (s *SignageService) UpdateDevice(ctx context.Context, device *SignageDevice) (*SignageDevice, error) {
	existing, err := s.deviceRepo.GetByID(ctx, device.ID, device.TenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrDeviceNotFound
	}

	existing.DeviceName = device.DeviceName
	existing.VenueID = device.VenueID
	existing.RoomID = device.RoomID
	existing.DisplayMode = device.DisplayMode
	existing.Config = device.Config
	if device.Status != "" {
		existing.Status = device.Status
	}
	existing.UpdatedAt = time.Now().UTC()

	if err := s.deviceRepo.Update(ctx, existing); err != nil {
		return nil, err
	}

	return existing, nil
}

// DeleteDevice soft-deletes a signage device.
func (s *SignageService) DeleteDevice(ctx context.Context, id, tenantID uuid.UUID) error {
	existing, err := s.deviceRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrDeviceNotFound
	}

	return s.deviceRepo.Delete(ctx, id, tenantID)
}

// ---------------------------------------------------------------------------
// Heartbeat & Monitoring
// ---------------------------------------------------------------------------

// RecordHeartbeat updates the device's last_heartbeat_at and sets status to online.
func (s *SignageService) RecordHeartbeat(ctx context.Context, token string) (*SignageDevice, error) {
	device, err := s.deviceRepo.GetByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return nil, ErrDeviceNotFound
	}

	now := time.Now().UTC()
	device.LastHeartbeatAt = &now
	device.Status = DeviceStatusOnline
	device.UpdatedAt = now

	if err := s.deviceRepo.Update(ctx, device); err != nil {
		return nil, fmt.Errorf("record heartbeat: %w", err)
	}

	return device, nil
}

// MarkStaleDevicesOffline marks devices with no heartbeat in the given duration
// as offline. Returns the number of devices marked offline.
func (s *SignageService) MarkStaleDevicesOffline(ctx context.Context, tenantID uuid.UUID, staleDuration time.Duration) (int, error) {
	devices, err := s.deviceRepo.ListStaleDevices(ctx, tenantID, staleDuration)
	if err != nil {
		return 0, fmt.Errorf("list stale devices: %w", err)
	}

	count := 0
	now := time.Now().UTC()
	for i := range devices {
		devices[i].Status = DeviceStatusOffline
		devices[i].UpdatedAt = now
		if err := s.deviceRepo.Update(ctx, &devices[i]); err != nil {
			return count, fmt.Errorf("mark device %s offline: %w", devices[i].ID, err)
		}
		count++
	}

	return count, nil
}

// ---------------------------------------------------------------------------
// Content Aggregation
// ---------------------------------------------------------------------------

// GetContent returns aggregated content for a signage device based on its
// display mode. This is a simplified implementation that returns placeholder data.
func (s *SignageService) GetContent(ctx context.Context, token string) (*SignageContent, error) {
	device, err := s.deviceRepo.GetByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return nil, ErrDeviceNotFound
	}

	content := &SignageContent{
		DeviceToken: device.DeviceToken,
		DisplayMode: device.DisplayMode,
		GeneratedAt: time.Now().UTC(),
	}

	// Content is aggregated based on display_mode. In production, this would
	// query other services. For now, return empty arrays.
	switch device.DisplayMode {
	case DisplayModeLeaderboard:
		content.Leaderboard = []LeaderboardEntry{}
	case DisplayModeTimetable:
		content.Timetable = []TimetableEntry{}
	case DisplayModeAnnouncements:
		content.Announcements = []AnnouncementEntry{}
	case DisplayModeMixed:
		content.Leaderboard = []LeaderboardEntry{}
		content.Timetable = []TimetableEntry{}
		content.Announcements = []AnnouncementEntry{}
	}

	return content, nil
}

// GetDisplayURL returns the display content URL for a device based on its config.
func (s *SignageService) GetDisplayURL(ctx context.Context, token string) (string, DisplayMode, error) {
	device, err := s.deviceRepo.GetByToken(ctx, token)
	if err != nil {
		return "", "", err
	}
	if device == nil {
		return "", "", ErrDeviceNotFound
	}

	url := fmt.Sprintf("/api/v1/training/signage/devices/%s/content", device.DeviceToken)
	return url, device.DisplayMode, nil
}

// generateDeviceToken creates a cryptographically random 32-character hex token.
func generateDeviceToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to UUID-based token if crypto/rand fails.
		return uuid.Must(uuid.NewV7()).String()
	}
	return hex.EncodeToString(b)
}
