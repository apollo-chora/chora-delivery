package wbl

import (
	"context"

	"github.com/google/uuid"
)

// InternshipRepository defines the data access interface for Internship entities.
type InternshipRepository interface {
	// Create persists a new internship posting.
	Create(ctx context.Context, internship *Internship) error

	// GetByID retrieves an internship by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*Internship, error)

	// List returns internships for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Internship, error)

	// Update saves changes to an existing internship.
	Update(ctx context.Context, internship *Internship) error
}

// ApplicationRepository defines the data access interface for InternshipApplication entities.
type ApplicationRepository interface {
	// Create persists a new internship application.
	Create(ctx context.Context, application *InternshipApplication) error

	// GetByID retrieves an application by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*InternshipApplication, error)

	// ListByInternship returns applications for an internship with cursor-based pagination.
	ListByInternship(ctx context.Context, internshipID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]InternshipApplication, error)

	// ListByApplicant returns applications by a specific applicant for an internship.
	ListByApplicant(ctx context.Context, internshipID, tenantID, applicantGCID uuid.UUID) ([]InternshipApplication, error)

	// Update saves changes to an existing application.
	Update(ctx context.Context, application *InternshipApplication) error
}

// PlacementRepository defines the data access interface for Placement entities.
type PlacementRepository interface {
	// Create persists a new placement.
	Create(ctx context.Context, placement *Placement) error

	// GetByID retrieves a placement by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*Placement, error)

	// List returns placements for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Placement, error)

	// Update saves changes to an existing placement.
	Update(ctx context.Context, placement *Placement) error
}

// WorkLogRepository defines the data access interface for WorkLogEntry entities.
type WorkLogRepository interface {
	// Create persists a new work log entry.
	Create(ctx context.Context, entry *WorkLogEntry) error

	// ListByPlacement returns work log entries for a placement with cursor-based pagination.
	ListByPlacement(ctx context.Context, placementID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]WorkLogEntry, error)
}

// EndorsementRepository defines the data access interface for SkillEndorsement entities.
type EndorsementRepository interface {
	// Create persists a new skill endorsement.
	Create(ctx context.Context, endorsement *SkillEndorsement) error

	// ListByPlacement returns endorsements for a placement.
	ListByPlacement(ctx context.Context, placementID, tenantID uuid.UUID) ([]SkillEndorsement, error)
}

// CapstoneRepository defines the data access interface for CapstoneProject entities.
type CapstoneRepository interface {
	// Create persists a new capstone project.
	Create(ctx context.Context, capstone *CapstoneProject) error

	// GetByID retrieves a capstone project by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*CapstoneProject, error)

	// List returns capstone projects for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]CapstoneProject, error)

	// Update saves changes to an existing capstone project.
	Update(ctx context.Context, capstone *CapstoneProject) error
}

// SubmissionRepository defines the data access interface for CapstoneSubmission entities.
type SubmissionRepository interface {
	// Create persists a new capstone submission.
	Create(ctx context.Context, submission *CapstoneSubmission) error

	// ListByCapstone returns submissions for a capstone project.
	ListByCapstone(ctx context.Context, capstoneID, tenantID uuid.UUID) ([]CapstoneSubmission, error)
}

// PartnerRepository defines the data access interface for IndustryPartner entities.
type PartnerRepository interface {
	// Create persists a new industry partner.
	Create(ctx context.Context, partner *IndustryPartner) error

	// GetByID retrieves a partner by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*IndustryPartner, error)

	// List returns partners for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]IndustryPartner, error)
}

// WBLLogRepository defines the data access interface for WBLLog entities.
type WBLLogRepository interface {
	// Create persists a new WBL log entry.
	Create(ctx context.Context, log *WBLLog) error

	// GetByID retrieves a WBL log by ID and tenant. Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*WBLLog, error)

	// List returns WBL logs for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]WBLLog, error)

	// Update saves changes to an existing WBL log entry.
	Update(ctx context.Context, log *WBLLog) error
}

// EventPublisher abstracts the event bus (Cloud Pub/Sub in production,
// in-memory or emulator in tests/local dev).
type EventPublisher interface {
	// Publish sends a domain event to the specified topic.
	Publish(ctx context.Context, topic string, event interface{}) error

	// Close releases resources held by the publisher.
	Close() error
}
