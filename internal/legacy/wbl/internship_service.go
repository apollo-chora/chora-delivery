package wbl

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// InternshipService manages internship postings, applications, and partner queries.
type InternshipService struct {
	internships  InternshipRepository
	applications ApplicationRepository
	partners     PartnerRepository
	events       EventPublisher
}

// NewInternshipService creates an InternshipService with the given
// repositories and event publisher.
func NewInternshipService(
	internships InternshipRepository,
	applications ApplicationRepository,
	partners PartnerRepository,
	events EventPublisher,
) *InternshipService {
	return &InternshipService{
		internships:  internships,
		applications: applications,
		partners:     partners,
		events:       events,
	}
}

// ---------------------------------------------------------------------------
// Internship CRUD
// ---------------------------------------------------------------------------

// CreateInternship validates and persists a new Internship with UUIDv7 ID,
// draft status, and filled_positions=0. Publishes no event (event on apply).
func (s *InternshipService) CreateInternship(ctx context.Context, internship *Internship) (*Internship, error) {
	if internship.Title == "" {
		return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
	}
	if internship.PartnerID == uuid.Nil {
		return nil, fmt.Errorf("partner_id is required: %w", ErrValidationFailed)
	}
	if internship.MaxPositions <= 0 {
		return nil, fmt.Errorf("max_positions must be greater than 0: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	internship.ID = uuid.Must(uuid.NewV7())
	internship.Status = InternshipStatusDraft
	internship.FilledPositions = 0
	internship.CreatedAt = now
	internship.UpdatedAt = now

	if err := s.internships.Create(ctx, internship); err != nil {
		return nil, err
	}

	return internship, nil
}

// GetInternship retrieves an internship by ID and tenant. Returns
// ErrInternshipNotFound if the internship does not exist.
func (s *InternshipService) GetInternship(ctx context.Context, id, tenantID uuid.UUID) (*Internship, error) {
	internship, err := s.internships.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if internship == nil {
		return nil, ErrInternshipNotFound
	}
	return internship, nil
}

// ListInternships returns internships for a tenant with cursor-based pagination.
func (s *InternshipService) ListInternships(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Internship, error) {
	return s.internships.List(ctx, tenantID, cursor, limit)
}

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

// ApplyForInternship validates and creates a new InternshipApplication.
// The internship must be open and not full. Duplicate applications are rejected.
// Publishes an application.submitted event.
func (s *InternshipService) ApplyForInternship(ctx context.Context, app *InternshipApplication) (*InternshipApplication, error) {
	if app.InternshipID == uuid.Nil {
		return nil, fmt.Errorf("internship_id is required: %w", ErrValidationFailed)
	}

	internship, err := s.internships.GetByID(ctx, app.InternshipID, app.TenantID)
	if err != nil {
		return nil, fmt.Errorf("lookup internship: %w", err)
	}
	if internship == nil {
		return nil, ErrInternshipNotFound
	}

	if internship.Status != InternshipStatusOpen {
		return nil, fmt.Errorf("internship is not open: %w", ErrInternshipNotOpen)
	}

	if internship.FilledPositions >= internship.MaxPositions {
		return nil, fmt.Errorf("internship has no open positions: %w", ErrInternshipFull)
	}

	// Check for duplicate application.
	existing, err := s.applications.ListByApplicant(ctx, app.InternshipID, app.TenantID, app.ApplicantGCID)
	if err != nil {
		return nil, fmt.Errorf("check existing applications: %w", err)
	}
	if len(existing) > 0 {
		return nil, fmt.Errorf("already applied to this internship: %w", ErrDuplicateApplication)
	}

	now := time.Now().UTC()
	app.ID = uuid.Must(uuid.NewV7())
	app.Status = ApplicationStatusSubmitted
	app.SubmittedAt = now
	app.CreatedAt = now
	app.UpdatedAt = now

	if err := s.applications.Create(ctx, app); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventApplicationSubmitted,
		app.TenantID,
		&app.ApplicantGCID,
		app.InternshipID,
		AggregateInternship,
		map[string]interface{}{
			"application_id": app.ID.String(),
			"internship_id":  app.InternshipID.String(),
			"applicant_gcid": app.ApplicantGCID.String(),
		},
	)
	if err := s.events.Publish(ctx, TopicWBLEvents, evt); err != nil {
		return nil, fmt.Errorf("publish application.submitted: %w", err)
	}

	return app, nil
}

// ListApplications returns applications for an internship with cursor-based pagination.
func (s *InternshipService) ListApplications(ctx context.Context, internshipID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]InternshipApplication, error) {
	return s.applications.ListByInternship(ctx, internshipID, tenantID, cursor, limit)
}

// DecideApplication approves or rejects an internship application. The application
// must be in submitted or under_review status. Rejections require a reason.
func (s *InternshipService) DecideApplication(ctx context.Context, id, tenantID uuid.UUID, approved bool, reviewerGCID uuid.UUID, reason *string) (*InternshipApplication, error) {
	app, err := s.applications.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if app == nil {
		return nil, ErrNotFound
	}

	if app.Status != ApplicationStatusSubmitted && app.Status != ApplicationStatusUnderReview {
		return nil, fmt.Errorf("application %s is in status %s: %w", id, app.Status, ErrApplicationNotReviewable)
	}

	now := time.Now().UTC()
	app.ReviewedByGCID = &reviewerGCID
	app.ReviewedAt = &now
	app.UpdatedAt = now

	if approved {
		app.Status = ApplicationStatusApproved
	} else {
		if reason == nil {
			return nil, fmt.Errorf("rejection reason is required: %w", ErrValidationFailed)
		}
		app.Status = ApplicationStatusRejected
		app.RejectionReason = reason
	}

	if err := s.applications.Update(ctx, app); err != nil {
		return nil, fmt.Errorf("update application: %w", err)
	}

	return app, nil
}

// ---------------------------------------------------------------------------
// Partners
// ---------------------------------------------------------------------------

// ListPartners returns industry partners for a tenant with cursor-based pagination.
func (s *InternshipService) ListPartners(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]IndustryPartner, error) {
	return s.partners.List(ctx, tenantID, cursor, limit)
}
