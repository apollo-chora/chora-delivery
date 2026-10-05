package training_admin

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TrainingSessionService manages the TrainingSession lifecycle, attendance
// recording, and scheduling.
type TrainingSessionService struct {
	sessions  TrainingSessionRepository
	schedules ScheduleRepository
	attend    AttendanceRepository
	events    EventPublisher
}

// NewTrainingSessionService creates a TrainingSessionService with the given
// repositories and event publisher.
func NewTrainingSessionService(
	sessions TrainingSessionRepository,
	schedules ScheduleRepository,
	attend AttendanceRepository,
	events EventPublisher,
) *TrainingSessionService {
	return &TrainingSessionService{
		sessions:  sessions,
		schedules: schedules,
		attend:    attend,
		events:    events,
	}
}

// ---------------------------------------------------------------------------
// Session CRUD
// ---------------------------------------------------------------------------

// CreateSession validates and persists a new TrainingSession with UUIDv7 ID,
// draft status, and enrollment_open=false. Publishes a session.created event.
func (s *TrainingSessionService) CreateSession(ctx context.Context, session *TrainingSession) (*TrainingSession, error) {
	// Validate required fields.
	if session.Title == "" {
		return nil, fmt.Errorf("title must not be empty: %w", ErrValidationFailed)
	}
	if !session.DeliveryMode.IsValid() {
		return nil, fmt.Errorf("invalid delivery mode %q: %w", session.DeliveryMode, ErrValidationFailed)
	}
	if session.MaxCapacity <= 0 {
		return nil, fmt.Errorf("max_capacity must be greater than 0: %w", ErrValidationFailed)
	}

	now := time.Now().UTC()
	session.ID = uuid.Must(uuid.NewV7())
	session.Status = SessionStatusDraft
	session.EnrollmentOpen = false
	session.EnrolledCount = 0
	session.CreatedAt = now
	session.UpdatedAt = now

	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventSessionCreated,
		session.TenantID,
		&session.CreatedByGCID,
		session.ID,
		AggregateTrainingSession,
		map[string]interface{}{
			"title":         session.Title,
			"delivery_mode": string(session.DeliveryMode),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
		return nil, fmt.Errorf("publish session.created event: %w", err)
	}

	return session, nil
}

// ListSessions returns training sessions for a tenant with cursor-based
// pagination.
func (s *TrainingSessionService) ListSessions(ctx context.Context, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]TrainingSession, error) {
	return s.sessions.List(ctx, tenantID, cursor, limit)
}

// GetSession retrieves a training session by ID and tenant. Returns
// ErrSessionNotFound if the session does not exist.
func (s *TrainingSessionService) GetSession(ctx context.Context, id, tenantID uuid.UUID) (*TrainingSession, error) {
	session, err := s.sessions.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

// UpdateSession applies mutable field changes to an existing session. The
// session must be in draft or scheduled status; otherwise ErrSessionNotModifiable
// is returned.
func (s *TrainingSessionService) UpdateSession(ctx context.Context, session *TrainingSession) (*TrainingSession, error) {
	existing, err := s.sessions.GetByID(ctx, session.ID, session.TenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrSessionNotFound
	}

	if existing.Status != SessionStatusDraft && existing.Status != SessionStatusScheduled {
		return nil, ErrSessionNotModifiable
	}

	// Apply mutable fields to the existing entity.
	existing.Title = session.Title
	existing.Description = session.Description
	existing.DeliveryMode = session.DeliveryMode
	existing.MaxCapacity = session.MaxCapacity
	existing.UpdatedAt = time.Now().UTC()

	if err := s.sessions.Update(ctx, existing); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventSessionUpdated,
		existing.TenantID,
		nil,
		existing.ID,
		AggregateTrainingSession,
		map[string]interface{}{
			"title":  existing.Title,
			"status": string(existing.Status),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
		return nil, fmt.Errorf("publish session.updated event: %w", err)
	}

	return existing, nil
}

// DeleteSession soft-deletes a training session. Only draft sessions may be
// deleted; otherwise ErrSessionNotDeletable is returned.
func (s *TrainingSessionService) DeleteSession(ctx context.Context, id, tenantID uuid.UUID) error {
	existing, err := s.sessions.GetByID(ctx, id, tenantID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrSessionNotFound
	}

	if existing.Status != SessionStatusDraft {
		return ErrSessionNotDeletable
	}

	return s.sessions.Delete(ctx, id, tenantID)
}

// ---------------------------------------------------------------------------
// State transitions
// ---------------------------------------------------------------------------

// OpenEnrollment transitions a draft session to scheduled and sets
// enrollment_open=true. Publishes a session.updated event.
func (s *TrainingSessionService) OpenEnrollment(ctx context.Context, id, tenantID uuid.UUID) (*TrainingSession, error) {
	existing, err := s.sessions.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrSessionNotFound
	}

	if existing.Status != SessionStatusDraft {
		return nil, ErrInvalidStateTransition
	}

	existing.Status = SessionStatusScheduled
	existing.EnrollmentOpen = true
	existing.UpdatedAt = time.Now().UTC()

	if err := s.sessions.Update(ctx, existing); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventSessionUpdated,
		existing.TenantID,
		nil,
		existing.ID,
		AggregateTrainingSession,
		map[string]interface{}{
			"status":          string(existing.Status),
			"enrollment_open": true,
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
		return nil, fmt.Errorf("publish enrollment opened event: %w", err)
	}

	return existing, nil
}

// StartSession transitions a scheduled session to in_progress. Publishes a
// session.started event.
func (s *TrainingSessionService) StartSession(ctx context.Context, id, tenantID uuid.UUID) (*TrainingSession, error) {
	existing, err := s.sessions.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrSessionNotFound
	}

	if existing.Status != SessionStatusScheduled {
		return nil, ErrInvalidStateTransition
	}

	existing.Status = SessionStatusInProgress
	existing.UpdatedAt = time.Now().UTC()

	if err := s.sessions.Update(ctx, existing); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventSessionStarted,
		existing.TenantID,
		nil,
		existing.ID,
		AggregateTrainingSession,
		map[string]interface{}{
			"status": string(existing.Status),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
		return nil, fmt.Errorf("publish session.started event: %w", err)
	}

	return existing, nil
}

// CompleteSession transitions an in_progress session to completed. Publishes a
// session.completed event.
func (s *TrainingSessionService) CompleteSession(ctx context.Context, id, tenantID uuid.UUID) (*TrainingSession, error) {
	existing, err := s.sessions.GetByID(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrSessionNotFound
	}

	if existing.Status != SessionStatusInProgress {
		return nil, ErrInvalidStateTransition
	}

	existing.Status = SessionStatusCompleted
	existing.UpdatedAt = time.Now().UTC()

	if err := s.sessions.Update(ctx, existing); err != nil {
		return nil, err
	}

	evt := NewDomainEvent(
		EventSessionCompleted,
		existing.TenantID,
		nil,
		existing.ID,
		AggregateTrainingSession,
		map[string]interface{}{
			"status": string(existing.Status),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
		return nil, fmt.Errorf("publish session.completed event: %w", err)
	}

	return existing, nil
}

// ---------------------------------------------------------------------------
// Attendance
// ---------------------------------------------------------------------------

// RecordAttendance validates and persists a batch of attendance records. Each
// record is assigned a UUIDv7 ID and recorded_at timestamp. Publishes an
// attendance.recorded event.
func (s *TrainingSessionService) RecordAttendance(ctx context.Context, records []Attendance) error {
	now := time.Now().UTC()
	for i := range records {
		if !records[i].Status.IsValid() {
			return fmt.Errorf("invalid attendance status %q at index %d: %w", records[i].Status, i, ErrValidationFailed)
		}
		records[i].ID = uuid.Must(uuid.NewV7())
		records[i].RecordedAt = now
	}

	if err := s.attend.Create(ctx, records); err != nil {
		return err
	}

	// Use the first record's tenant/session for the event envelope.
	first := records[0]
	evt := NewDomainEvent(
		EventAttendanceRecorded,
		first.TenantID,
		nil,
		first.TrainingSessionID,
		AggregateTrainingSession,
		map[string]interface{}{
			"count": len(records),
		},
	)
	if err := s.events.Publish(ctx, TopicTrainingEvents, evt); err != nil {
		return fmt.Errorf("publish attendance.recorded event: %w", err)
	}

	return nil
}

// ListAttendance returns attendance records for a training session with
// cursor-based pagination.
func (s *TrainingSessionService) ListAttendance(ctx context.Context, sessionID, tenantID uuid.UUID, cursor *uuid.UUID, limit int) ([]Attendance, error) {
	return s.attend.ListBySession(ctx, sessionID, tenantID, cursor, limit)
}

// GetAttendanceSummary returns attendance statistics for a training session.
func (s *TrainingSessionService) GetAttendanceSummary(ctx context.Context, sessionID, tenantID uuid.UUID) (*AttendanceSummary, error) {
	return s.attend.GetSummary(ctx, sessionID, tenantID)
}

// ---------------------------------------------------------------------------
// Scheduling
// ---------------------------------------------------------------------------

// AddSchedule validates and persists a new schedule entry for a training
// session. The end_time must be after start_time. The referenced session must
// exist.
func (s *TrainingSessionService) AddSchedule(ctx context.Context, schedule *Schedule) (*Schedule, error) {
	// Validate end_time > start_time (string comparison works for HH:MM format).
	if schedule.EndTime <= schedule.StartTime {
		return nil, fmt.Errorf("end_time must be after start_time: %w", ErrValidationFailed)
	}

	// Verify that the session exists.
	sess, err := s.sessions.GetByID(ctx, schedule.TrainingSessionID, uuid.Nil)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, ErrSessionNotFound
	}

	now := time.Now().UTC()
	schedule.ID = uuid.Must(uuid.NewV7())
	schedule.CreatedAt = now

	if err := s.schedules.Create(ctx, schedule); err != nil {
		return nil, err
	}

	return schedule, nil
}

// ListSchedules returns all schedule entries for a training session.
func (s *TrainingSessionService) ListSchedules(ctx context.Context, sessionID uuid.UUID) ([]Schedule, error) {
	return s.schedules.ListBySession(ctx, sessionID)
}

// DeleteSchedule removes a schedule entry by ID.
func (s *TrainingSessionService) DeleteSchedule(ctx context.Context, id uuid.UUID) error {
	return s.schedules.Delete(ctx, id)
}
