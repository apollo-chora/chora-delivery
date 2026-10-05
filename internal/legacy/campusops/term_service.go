package campusops

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TermService manages AcademicTerm CRUD operations.
type TermService struct {
	terms  AcademicTermRepository
	events EventPublisher
}

// NewTermService creates a TermService with the given repository and event publisher.
func NewTermService(terms AcademicTermRepository, events EventPublisher) *TermService {
	return &TermService{terms: terms, events: events}
}

// CreateTerm validates and persists a new AcademicTerm with UUIDv7 ID.
func (s *TermService) CreateTerm(ctx context.Context, term *AcademicTerm) (*AcademicTerm, error) {
	if term.Name == "" {
		return nil, fmt.Errorf("name must not be empty: %w", ErrValidationFailed)
	}
	if !term.TermType.IsValid() {
		return nil, fmt.Errorf("invalid term_type %q: %w", term.TermType, ErrValidationFailed)
	}
	if !term.EndsAt.After(term.StartsAt) {
		return nil, fmt.Errorf("ends_at must be after starts_at: %w", ErrTermDateRange)
	}

	now := time.Now().UTC()
	term.ID = uuid.Must(uuid.NewV7())
	term.IsActive = true
	term.CreatedAt = now
	term.UpdatedAt = now

	if err := s.terms.Create(ctx, term); err != nil {
		return nil, err
	}
	return term, nil
}

// GetTerm retrieves an academic term by ID and tenant.
func (s *TermService) GetTerm(ctx context.Context, id, tenantID uuid.UUID) (*AcademicTerm, error) {
	term, err := s.terms.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if term == nil {
		return nil, ErrTermNotFound
	}
	return term, nil
}

// ListTerms returns academic terms with cursor-based pagination.
func (s *TermService) ListTerms(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]AcademicTerm, error) {
	return s.terms.List(ctx, tenantID, cursor, limit)
}

// UpdateTerm applies mutable field changes to an existing academic term.
func (s *TermService) UpdateTerm(ctx context.Context, term *AcademicTerm) (*AcademicTerm, error) {
	existing, err := s.terms.GetByID(ctx, term.ID, term.TenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrTermNotFound
	}

	if term.Name == "" {
		return nil, fmt.Errorf("name must not be empty: %w", ErrValidationFailed)
	}
	if !term.TermType.IsValid() {
		return nil, fmt.Errorf("invalid term_type %q: %w", term.TermType, ErrValidationFailed)
	}
	if !term.EndsAt.After(term.StartsAt) {
		return nil, fmt.Errorf("ends_at must be after starts_at: %w", ErrTermDateRange)
	}

	existing.Name = term.Name
	existing.TermType = term.TermType
	existing.StartsAt = term.StartsAt
	existing.EndsAt = term.EndsAt
	existing.EnrollmentWindowStart = term.EnrollmentWindowStart
	existing.EnrollmentWindowEnd = term.EnrollmentWindowEnd
	existing.IsActive = term.IsActive
	existing.UpdatedAt = time.Now().UTC()

	if err := s.terms.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// DeleteTerm soft-deletes an academic term.
func (s *TermService) DeleteTerm(ctx context.Context, id, tenantID uuid.UUID) error {
	existing, err := s.terms.GetByID(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrTermNotFound
	}
	return s.terms.Delete(ctx, id, tenantID)
}

// GetCurrentTerm returns the currently active term based on date range.
func (s *TermService) GetCurrentTerm(ctx context.Context, tenantID uuid.UUID) (*AcademicTerm, error) {
	term, err := s.terms.GetCurrent(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if term == nil {
		return nil, ErrTermNotFound
	}
	return term, nil
}
