package wbl

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// CapstoneService manages capstone projects and submissions.
type CapstoneService struct {
	capstones   CapstoneRepository
	submissions SubmissionRepository
	events      EventPublisher
}

// NewCapstoneService creates a CapstoneService with the given
// repositories and event publisher.
func NewCapstoneService(
	capstones CapstoneRepository,
	submissions SubmissionRepository,
	events EventPublisher,
) *CapstoneService {
	return &CapstoneService{
		capstones:   capstones,
		submissions: submissions,
		events:      events,
	}
}

// ---------------------------------------------------------------------------
// Capstone CRUD
// ---------------------------------------------------------------------------

// CreateCapstone validates and persists a new CapstoneProject with UUIDv7 ID
// and draft status.
func (s *CapstoneService) CreateCapstone(ctx context.Context, capstone *CapstoneProject) (*CapstoneProject, error) {
	if capstone.Title == "" {
		return nil, fmt.Errorf("title is required: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	capstone.ID = uuid.Must(uuid.NewV7())
	capstone.Status = CapstoneStatusDraft
	capstone.CreatedAt = now
	capstone.UpdatedAt = now

	if err := s.capstones.Create(ctx, capstone); err != nil {
		return nil, err
	}

	return capstone, nil
}

// GetCapstone retrieves a capstone project by ID within a tenant.
func (s *CapstoneService) GetCapstone(ctx context.Context, id, tenantID uuid.UUID) (*CapstoneProject, error) {
	capstone, err := s.capstones.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if capstone == nil {
		return nil, ErrCapstoneNotFound
	}
	return capstone, nil
}

// ListCapstones returns capstone projects for a tenant with cursor-based pagination.
func (s *CapstoneService) ListCapstones(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]CapstoneProject, error) {
	return s.capstones.List(ctx, tenantID, cursor, limit)
}

// ---------------------------------------------------------------------------
// Submissions
// ---------------------------------------------------------------------------

// SubmitCapstone validates and persists a CapstoneSubmission. The capstone must
// be in draft or active status. Publishes a capstone.submitted event.
func (s *CapstoneService) SubmitCapstone(ctx context.Context, submission *CapstoneSubmission) (*CapstoneSubmission, error) {
	if submission.SubmissionURL == "" {
		return nil, fmt.Errorf("submission_url is required: %w", ErrValidationFailed)
	}

	capstone, err := s.capstones.GetByID(ctx, submission.CapstoneID, submission.TenantID)
	if err != nil {
		return nil, fmt.Errorf("lookup capstone: %w", err)
	}
	if capstone == nil {
		return nil, ErrCapstoneNotFound
	}

	if capstone.Status != CapstoneStatusDraft && capstone.Status != CapstoneStatusActive {
		return nil, fmt.Errorf("capstone is not in a submittable state: %w", ErrCapstoneNotSubmittable)
	}

	now := time.Now().UTC()
	submission.ID = uuid.Must(uuid.NewV7())
	submission.SubmittedAt = now

	if err := s.submissions.Create(ctx, submission); err != nil {
		return nil, err
	}

	// Transition capstone to active if still draft.
	if capstone.Status == CapstoneStatusDraft {
		capstone.Status = CapstoneStatusActive
		capstone.UpdatedAt = now
		if err := s.capstones.Update(ctx, capstone); err != nil {
			return nil, fmt.Errorf("update capstone status: %w", err)
		}
	}

	evt := NewDomainEvent(
		EventCapstoneSubmitted,
		submission.TenantID,
		&submission.SubmitterGCID,
		submission.CapstoneID,
		AggregateCapstoneProject,
		map[string]interface{}{
			"submission_id":  submission.ID.String(),
			"capstone_id":    submission.CapstoneID.String(),
			"submitter_gcid": submission.SubmitterGCID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish capstone.submitted: %w", err)
	}

	return submission, nil
}
