package training_admin

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// CertificateProgramService manages the lifecycle of certificate programs
// and program enrollments.
type CertificateProgramService struct {
	programRepo CertificateProgramRepository
	enrollRepo  ProgramEnrollmentRepository
	events      EventPublisher
}

// NewCertificateProgramService constructs a CertificateProgramService with the
// required repository and event publisher dependencies.
func NewCertificateProgramService(
	programRepo CertificateProgramRepository,
	enrollRepo ProgramEnrollmentRepository,
	events EventPublisher,
) *CertificateProgramService {
	return &CertificateProgramService{
		programRepo: programRepo,
		enrollRepo:  enrollRepo,
		events:      events,
	}
}

// ---------------------------------------------------------------------------
// Program CRUD
// ---------------------------------------------------------------------------

// CreateProgram validates and persists a new CertificateProgram. The program
// is assigned a UUIDv7 ID, status=draft, and total_paths derived from PathIDs.
func (s *CertificateProgramService) CreateProgram(ctx context.Context, p *CertificateProgram) (*CertificateProgram, error) {
	if p.Title == "" {
		return nil, fmt.Errorf("title is required: %w", ErrValidationFailed)
	}
	if len(p.PathIDs) == 0 {
		return nil, fmt.Errorf("path_ids must not be empty: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	p.ID = uuid.Must(uuid.NewV7())
	p.Status = ProgramStatusDraft
	p.TotalPaths = len(p.PathIDs)
	p.CreatedAt = now
	p.UpdatedAt = now

	if err := s.programRepo.Create(ctx, p); err != nil {
		return nil, err
	}

	return p, nil
}

// GetProgram retrieves a certificate program by ID within a tenant.
func (s *CertificateProgramService) GetProgram(ctx context.Context, id, tenantID uuid.UUID) (*CertificateProgram, error) {
	p, err := s.programRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrNotFound
	}
	return p, nil
}

// ListPrograms returns certificate programs for a tenant with cursor-based pagination.
func (s *CertificateProgramService) ListPrograms(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]CertificateProgram, error) {
	return s.programRepo.List(ctx, tenantID, cursor, limit)
}

// UpdateProgram applies changes to an existing certificate program. Archived
// programs cannot be modified. If PathIDs changed, TotalPaths is recalculated.
func (s *CertificateProgramService) UpdateProgram(ctx context.Context, p *CertificateProgram) (*CertificateProgram, error) {
	existing, err := s.programRepo.GetByID(ctx, p.ID, p.TenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrNotFound
	}

	if existing.Status == ProgramStatusArchived {
		return nil, fmt.Errorf("archived programs cannot be modified: %w", ErrSessionNotModifiable)
	}

	existing.Title = p.Title
	existing.Description = p.Description
	existing.PathIDs = p.PathIDs
	existing.TotalPaths = len(p.PathIDs)
	existing.UpdatedAt = time.Now().UTC()

	if err := s.programRepo.Update(ctx, existing); err != nil {
		return nil, err
	}

	return existing, nil
}

// ---------------------------------------------------------------------------
// Enrollment
// ---------------------------------------------------------------------------

// EnrollInProgram enrols a learner in an active certificate program. The
// program must exist and be active, and the learner must not already be
// enrolled.
func (s *CertificateProgramService) EnrollInProgram(ctx context.Context, e *ProgramEnrollment) (*ProgramEnrollment, error) {
	program, err := s.programRepo.GetByID(ctx, e.ProgramID, e.TenantID)
	if err != nil {
		return nil, err
	}
	if program == nil {
		return nil, ErrNotFound
	}

	if program.Status != ProgramStatusActive {
		return nil, fmt.Errorf("program is not active: %w", ErrValidationFailed)
	}

	// Check for duplicate enrollment by listing existing enrollments and
	// comparing GCIDs.
	enrollments, err := s.enrollRepo.ListByProgram(ctx, e.ProgramID, e.TenantID, nil, 1000)
	if err != nil {
		return nil, fmt.Errorf("checking existing enrollments: %w", err)
	}
	for _, existing := range enrollments {
		if existing.GCID == e.GCID {
			return nil, fmt.Errorf("learner already enrolled: %w", ErrAlreadyEnrolled)
		}
	}

	now := time.Now().UTC()
	e.ID = uuid.Must(uuid.NewV7())
	e.Status = EnrollmentStatusEnrolled
	e.ProgressPct = 0
	e.TotalPaths = program.TotalPaths
	e.EnrolledAt = now

	if err := s.enrollRepo.Create(ctx, e); err != nil {
		return nil, err
	}

	return e, nil
}

// GetEnrollment retrieves a program enrollment by ID within a tenant.
func (s *CertificateProgramService) GetEnrollment(ctx context.Context, id, tenantID uuid.UUID) (*ProgramEnrollment, error) {
	e, err := s.enrollRepo.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, ErrNotFound
	}
	return e, nil
}

// ListEnrollments returns enrollments for a certificate program with
// cursor-based pagination.
func (s *CertificateProgramService) ListEnrollments(ctx context.Context, programID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]ProgramEnrollment, error) {
	return s.enrollRepo.ListByProgram(ctx, programID, tenantID, cursor, limit)
}

// ---------------------------------------------------------------------------
// Path completion
// ---------------------------------------------------------------------------

// MarkPathComplete records that a learner has completed a path within their
// enrolled certificate program. The operation is idempotent — marking an
// already-completed path is a no-op. When all paths are complete the
// enrollment status transitions to completed and a certificate is issued.
func (s *CertificateProgramService) MarkPathComplete(ctx context.Context, enrollmentID, tenantID, pathID uuid.UUID) (*ProgramEnrollment, error) {
	enrollment, err := s.enrollRepo.GetByID(ctx, enrollmentID, tenantID)
	if err != nil {
		return nil, err
	}
	if enrollment == nil {
		return nil, ErrNotFound
	}

	program, err := s.programRepo.GetByID(ctx, enrollment.ProgramID, tenantID)
	if err != nil {
		return nil, err
	}

	// Verify the path belongs to the program.
	pathInProgram := false
	for _, pid := range program.PathIDs {
		if pid == pathID {
			pathInProgram = true
			break
		}
	}
	if !pathInProgram {
		return nil, fmt.Errorf("path %s is not part of program %s: %w", pathID, program.ID, ErrPathNotInProgram)
	}

	// Idempotency: if path already completed, return as-is.
	for _, cpid := range enrollment.CompletedPathIDs {
		if cpid == pathID {
			return enrollment, nil
		}
	}

	// Add the path and recalculate progress.
	enrollment.CompletedPathIDs = append(enrollment.CompletedPathIDs, pathID)
	enrollment.ProgressPct = math.Round(float64(len(enrollment.CompletedPathIDs))/float64(enrollment.TotalPaths)*10000) / 100

	// Check for full completion.
	certificateIssued := false
	if len(enrollment.CompletedPathIDs) == enrollment.TotalPaths {
		now := time.Now().UTC()
		enrollment.Status = EnrollmentStatusCompleted
		enrollment.ProgressPct = 100
		enrollment.CompletedAt = &now
		enrollment.CertificateIssuedAt = &now
		certID := uuid.Must(uuid.NewV7())
		enrollment.CertificateID = &certID
		certificateIssued = true
	}

	if err := s.enrollRepo.Update(ctx, enrollment); err != nil {
		return nil, err
	}

	if certificateIssued {
		event := NewDomainEvent(
			EventCertificateIssued,
			enrollment.TenantID,
			&enrollment.GCID,
			enrollment.ID,
			AggregateCertificateProgram,
			map[string]interface{}{
				"program_id":     enrollment.ProgramID.String(),
				"enrollment_id":  enrollment.ID.String(),
				"certificate_id": enrollment.CertificateID.String(),
			},
		)
		_ = s.events.Publish(ctx, TopicTrainingEvents, event)
	}

	return enrollment, nil
}
