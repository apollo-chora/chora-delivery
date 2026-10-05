package classroom

import (
	"context"

	"github.com/google/uuid"
)

// ProfileService manages class engagement profiles for training sessions.
type ProfileService struct {
	profiles ClassProfileRepository
}

// NewProfileService creates a ProfileService with the given repository.
func NewProfileService(profiles ClassProfileRepository) *ProfileService {
	return &ProfileService{profiles: profiles}
}

// GetProfile retrieves the class engagement profile for a training session.
// Returns ErrProfileNotFound if no profile exists.
func (s *ProfileService) GetProfile(ctx context.Context, sessionID, tenantID uuid.UUID) (*ClassProfile, error) {
	profile, err := s.profiles.GetBySessionID(ctx, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, ErrProfileNotFound
	}
	return profile, nil
}
