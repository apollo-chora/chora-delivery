package training_admin

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TrainingSessionRepository defines the data access interface for TrainingSession entities.
type TrainingSessionRepository interface {
	// Create persists a new training session.
	Create(ctx context.Context, session *TrainingSession) error

	// GetByID retrieves a training session by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*TrainingSession, error)

	// List returns training sessions for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]TrainingSession, error)

	// Update saves changes to an existing training session.
	Update(ctx context.Context, session *TrainingSession) error

	// Delete soft-deletes a training session.
	Delete(ctx context.Context, id, tenantID uuid.UUID) error
}

// AttendanceRepository defines the data access interface for Attendance records.
type AttendanceRepository interface {
	// Create persists a batch of attendance records (append-only).
	Create(ctx context.Context, records []Attendance) error

	// ListBySession returns attendance records for a training session on a given date.
	ListBySession(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Attendance, error)

	// GetSummary returns attendance statistics for a training session.
	GetSummary(ctx context.Context, sessionID, tenantID uuid.UUID) (*AttendanceSummary, error)
}

// ScheduleRepository defines the data access interface for Schedule entities.
type ScheduleRepository interface {
	// Create persists a new schedule entry.
	Create(ctx context.Context, schedule *Schedule) error

	// ListBySession returns all schedules for a training session.
	ListBySession(ctx context.Context, sessionID uuid.UUID) ([]Schedule, error)

	// Delete removes a schedule entry by ID.
	Delete(ctx context.Context, id uuid.UUID) error
}

// ApplicationRepository defines the data access interface for TrainingApplication entities.
type ApplicationRepository interface {
	// Create persists a new training application.
	Create(ctx context.Context, application *TrainingApplication) error

	// GetByID retrieves a training application by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*TrainingApplication, error)

	// List returns training applications for a session with cursor-based pagination.
	List(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]TrainingApplication, error)

	// ListByStatus returns all applications in the given status for a tenant.
	ListByStatus(ctx context.Context, tenantID uuid.UUID, status ApplicationStatus) ([]TrainingApplication, error)

	// Update saves changes to an existing training application.
	Update(ctx context.Context, application *TrainingApplication) error
}

// TraineeRequestRepository defines the data access interface for TraineeRequest entities.
type TraineeRequestRepository interface {
	// Create persists a new trainee request.
	Create(ctx context.Context, request *TraineeRequest) error

	// GetByID retrieves a trainee request by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*TraineeRequest, error)

	// List returns trainee requests for a session with cursor-based pagination.
	List(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]TraineeRequest, error)

	// ListByStatus returns all trainee requests in the given status for a tenant.
	ListByStatus(ctx context.Context, tenantID uuid.UUID, status RequestStatus) ([]TraineeRequest, error)

	// Update saves changes to an existing trainee request.
	Update(ctx context.Context, request *TraineeRequest) error
}

// CertificateProgramRepository defines the data access interface for CertificateProgram entities.
type CertificateProgramRepository interface {
	// Create persists a new certificate program.
	Create(ctx context.Context, program *CertificateProgram) error

	// GetByID retrieves a certificate program by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*CertificateProgram, error)

	// List returns certificate programs for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]CertificateProgram, error)

	// Update saves changes to an existing certificate program.
	Update(ctx context.Context, program *CertificateProgram) error

	// Delete soft-deletes a certificate program.
	Delete(ctx context.Context, id, tenantID uuid.UUID) error
}

// ProgramEnrollmentRepository defines the data access interface for ProgramEnrollment entities.
type ProgramEnrollmentRepository interface {
	// Create persists a new program enrollment.
	Create(ctx context.Context, enrollment *ProgramEnrollment) error

	// GetByID retrieves a program enrollment by ID within a tenant.
	// Returns nil if not found.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*ProgramEnrollment, error)

	// ListByProgram returns enrollments for a certificate program with cursor-based pagination.
	ListByProgram(ctx context.Context, programID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]ProgramEnrollment, error)

	// Update saves changes to an existing program enrollment.
	Update(ctx context.Context, enrollment *ProgramEnrollment) error
}

// LearnerResultRepository defines the data access interface for LearnerResult entities.
type LearnerResultRepository interface {
	// Create persists a new learner result.
	Create(ctx context.Context, result *LearnerResult) error

	// GetByID retrieves a learner result by ID within a tenant.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*LearnerResult, error)

	// ListByLearner returns results for a learner with cursor-based pagination.
	ListByLearner(ctx context.Context, learnerID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]LearnerResult, error)

	// ListBySession returns results for a session with cursor-based pagination.
	ListBySession(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]LearnerResult, error)

	// ListUnpublishedBySession returns unpublished results for a session.
	ListUnpublishedBySession(ctx context.Context, sessionID, tenantID uuid.UUID) ([]LearnerResult, error)

	// Update saves changes to an existing learner result.
	Update(ctx context.Context, result *LearnerResult) error
}

// AttendanceRecordRepository defines the data access interface for AttendanceRecord entities.
type AttendanceRecordRepository interface {
	// ListByLearner returns aggregated attendance records for a learner.
	ListByLearner(ctx context.Context, learnerID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]AttendanceRecord, error)
}

// CertificateRequestRepository defines the data access interface for CertificateRequest entities.
type CertificateRequestRepository interface {
	// Create persists a new certificate request.
	Create(ctx context.Context, request *CertificateRequest) error

	// GetByID retrieves a certificate request by ID within a tenant.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*CertificateRequest, error)

	// List returns certificate requests for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]CertificateRequest, error)

	// Update saves changes to an existing certificate request.
	Update(ctx context.Context, request *CertificateRequest) error
}

// StudentAppealRepository defines the data access interface for StudentAppeal entities.
type StudentAppealRepository interface {
	// Create persists a new student appeal.
	Create(ctx context.Context, appeal *StudentAppeal) error

	// GetByID retrieves a student appeal by ID within a tenant.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*StudentAppeal, error)

	// List returns student appeals for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]StudentAppeal, error)

	// Update saves changes to an existing student appeal.
	Update(ctx context.Context, appeal *StudentAppeal) error
}

// SignageDeviceRepository defines the data access interface for SignageDevice entities.
type SignageDeviceRepository interface {
	// Create persists a new signage device.
	Create(ctx context.Context, device *SignageDevice) error

	// GetByID retrieves a signage device by ID within a tenant.
	GetByID(ctx context.Context, id, tenantID uuid.UUID) (*SignageDevice, error)

	// GetByToken retrieves a signage device by device token.
	GetByToken(ctx context.Context, token string) (*SignageDevice, error)

	// List returns signage devices for a tenant with cursor-based pagination.
	List(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]SignageDevice, error)

	// Update saves changes to an existing signage device.
	Update(ctx context.Context, device *SignageDevice) error

	// Delete soft-deletes a signage device.
	Delete(ctx context.Context, id, tenantID uuid.UUID) error

	// ListStaleDevices returns devices with no heartbeat in the given duration.
	ListStaleDevices(ctx context.Context, tenantID uuid.UUID, staleDuration time.Duration) ([]SignageDevice, error)
}

// EventPublisher abstracts the event bus (Cloud Pub/Sub in production,
// in-memory or emulator in tests/local dev).
type EventPublisher interface {
	// Publish sends a domain event to the specified topic.
	Publish(ctx context.Context, topic string, event interface{}) error

	// Close releases resources held by the publisher.
	Close() error
}
